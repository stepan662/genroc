package engine

import (
	"context"
	"fmt"

	"genroc/internal/model"
	"genroc/internal/schema"
)

// collectChildOutputs runs when a parent is in WaitStateCollecting: it reads all child
// instances of the task and returns their merged output as the task's action result
// (self.result) — a keyed map for child_map, an ordered array for child_list. Exported to
// outputs.<id> only if the task projects it via `output`.
//
// Collecting is valid only when every child completed — a failed/cancelled child makes the
// parent failing/cancelling, which exits advance() before this phase. The guard below
// enforces that rather than silently merging nil outputs.
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

// buildMapChildOutput returns each sibling's output keyed by its child key, validated
// against the declared result_schema (if any) and resolved from the object store when
// externalized.
func (e *Engine) buildMapChildOutput(siblings []*model.ProcessInstance) (any, error) {
	result := make(map[string]any, len(siblings))
	for _, child := range siblings {
		key, _ := child.ContextData["_spawn_child_key"].(string)
		output, err := e.resolveAndValidateChildOutput(child)
		if err != nil {
			return nil, err
		}
		result[key] = output
	}
	return result, nil
}

// buildListChildOutput returns the children's outputs as an array in input order.
// Siblings come back unordered, so each is placed at its recorded _spawn_index —
// guaranteeing result order matches the `over` array regardless of scan order. Each is
// validated against the declared result_schema and resolved from the object store if
// externalized.
func (e *Engine) buildListChildOutput(siblings []*model.ProcessInstance) (any, error) {
	result := make([]any, len(siblings))
	for _, child := range siblings {
		idx, ok := spawnIndex(child)
		if !ok || idx < 0 || idx >= len(siblings) {
			return nil, fmt.Errorf("child process %q has an invalid _spawn_index", child.ID)
		}
		output, err := e.resolveAndValidateChildOutput(child)
		if err != nil {
			return nil, err
		}
		result[idx] = output
	}
	return result, nil
}

// resolveAndValidateChildOutput reads a completed child's projected output, resolving it
// from the object store if externalized and validating it against the child's stored
// (already-normalized) result_schema when declared. Shared by the map and list collectors.
func (e *Engine) resolveAndValidateChildOutput(child *model.ProcessInstance) (any, error) {
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
	return output, nil
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

// validateChildOutput parses the child's stored (already-normalized) result_schema and
// validates the child output against it, returning the normalized output (undeclared keys
// dropped, defaults filled).
func validateChildOutput(schemaRaw string, output any) (any, error) {
	raw, err := schema.Parse([]byte(schemaRaw))
	if err != nil {
		return nil, fmt.Errorf("schema validation error: %w", err)
	}
	// The stored schema was normalized before the spawn marshaled it, so it can be
	// used directly without a re-normalize pass per collected child.
	return raw.AssumeNormalized().Validate(output)
}
