package engine

import (
	"context"
	"fmt"

	"genroc/internal/model"
	"genroc/internal/schema"
)

// collectChildOutputs is called when a parent instance is in WaitStateCollecting.
// It reads all child instances of the task and returns their merged output as the
// task's action result (self.result) — a keyed map for child_map, or an ordered
// array for child_list. It is exported to outputs.<id> only if the task projects
// it via `output`.
//
// Collecting is only valid when every child of the batch completed — a failed
// or cancelled child makes the parent failing/cancelling, which exits advance()
// before the collect phase. The guard below enforces this rather than silently
// merging nil outputs if that invariant is ever broken.
func (e *Engine) collectChildOutputs(ctx context.Context, inst *model.ProcessInstance, task *model.Task) (any, error) {
	siblings, err := e.db.ChildrenForTask(ctx, inst.ID, task.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range siblings {
		if c.Status != model.StatusCompleted {
			return nil, fmt.Errorf("child %q is %s; outputs can only be collected when all children completed", c.ID, c.Status)
		}
	}
	if task.Action.Type == model.ActionTypeChildList {
		return e.buildListChildOutput(siblings)
	}
	return e.buildMapChildOutput(siblings)
}

// buildMapChildOutput returns a map of each sibling's output keyed by its
// child key (validated against the declared result_schema, if any), resolving each
// from the object store if externalized.
func (e *Engine) buildMapChildOutput(siblings []*model.ProcessInstance) (any, error) {
	result := make(map[string]any, len(siblings))
	for _, child := range siblings {
		key, _ := child.ContextData["_spawn_child_key"].(string)
		output, err := e.resolveValue(child, child.ContextData["output"])
		if err != nil {
			return nil, err
		}
		if schemaRaw, _ := child.ContextData["_spawn_result_schema"].(string); schemaRaw != "" {
			normalized, err := validateChildOutput(schemaRaw, output)
			if err != nil {
				return nil, fmt.Errorf("child process %q (%s) output validation: %v", child.ID, child.ProcessName, err)
			}
			output = normalized
		}
		result[key] = output
	}
	return result, nil
}

// buildListChildOutput returns the children's outputs as an array in input order.
// Siblings come back from the DB unordered, so each output is placed at the child's
// recorded _spawn_index — guaranteeing the result order matches the `over` array
// order regardless of completion or scan order. Each output is validated against the
// declared result_schema and resolved from the object store if externalized.
func (e *Engine) buildListChildOutput(siblings []*model.ProcessInstance) (any, error) {
	result := make([]any, len(siblings))
	for _, child := range siblings {
		idx, ok := spawnIndex(child)
		if !ok || idx < 0 || idx >= len(siblings) {
			return nil, fmt.Errorf("child process %q has an invalid _spawn_index", child.ID)
		}
		output, err := e.resolveValue(child, child.ContextData["output"])
		if err != nil {
			return nil, err
		}
		if schemaRaw, _ := child.ContextData["_spawn_result_schema"].(string); schemaRaw != "" {
			normalized, err := validateChildOutput(schemaRaw, output)
			if err != nil {
				return nil, fmt.Errorf("child process %q (%s) output validation: %v", child.ID, child.ProcessName, err)
			}
			output = normalized
		}
		result[idx] = output
	}
	return result, nil
}

// spawnIndex reads a child's _spawn_index. It round-trips through JSON (engine_state),
// so it may come back as any numeric kind; a missing/foreign value reports !ok.
func spawnIndex(child *model.ProcessInstance) (int, bool) {
	switch v := child.ContextData["_spawn_index"].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

// validateChildOutput parses the child's stored (already-normalized) result_schema
// and validates the child output against it, returning the normalized output
// (undeclared keys dropped, defaults filled).
func validateChildOutput(schemaRaw string, output any) (any, error) {
	raw, err := schema.Parse([]byte(schemaRaw))
	if err != nil {
		return nil, fmt.Errorf("schema validation error: %w", err)
	}
	// The stored schema was normalized before the spawn marshaled it, so it can be
	// used directly without a re-normalize pass per collected child.
	return raw.AssumeNormalized().Validate(output)
}
