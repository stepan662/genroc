package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xeipuuv/gojsonschema"

	"gent/internal/model"
)

// collectChildOutputs is called when a parent instance is in WaitStateCollecting.
// It reads all child instances of the step and merges their outputs into
// inst.ContextData. On success, inst.ContextData["outputs"][step.ID] holds the
// merged result.
//
// Collecting is only valid when every child of the batch completed — a failed
// or cancelled child makes the parent failing/cancelling, which exits advance()
// before the collect phase. The guard below enforces this rather than silently
// merging nil outputs if that invariant is ever broken.
func (e *Engine) collectChildOutputs(ctx context.Context, inst *model.ProcessInstance, step *model.Step) error {
	siblings, err := e.db.ChildrenForStep(ctx, inst.ID, step.ID)
	if err != nil {
		return err
	}
	for _, c := range siblings {
		if c.Status != model.StatusCompleted {
			return fmt.Errorf("child %q is %s; outputs can only be collected when all children completed", c.ID, c.Status)
		}
	}
	if inst.ContextData["outputs"] == nil {
		inst.ContextData["outputs"] = map[string]any{}
	}
	var mergeErr string
	switch step.Call.Type {
	case model.CallTypeChild:
		mergeErr = buildSingleChildOutput(inst.ContextData, step.ID, siblings)
	default:
		mergeErr = buildParallelChildOutput(inst.ContextData, step.ID, siblings)
	}
	if mergeErr != "" {
		return fmt.Errorf("%s", mergeErr)
	}
	return nil
}

// buildSingleChildOutput writes the single child's output directly to
// parentCtx["outputs"][stepID]. Returns a non-empty error message on validation failure.
func buildSingleChildOutput(parentCtx map[string]any, stepID string, siblings []*model.ProcessInstance) string {
	if len(siblings) == 0 {
		return ""
	}
	child := siblings[0]
	output := child.ContextData["output"]
	if schemaRaw, _ := child.ContextData["_spawn_output_schema"].(string); schemaRaw != "" {
		var schema map[string]any
		json.Unmarshal([]byte(schemaRaw), &schema) //nolint:errcheck
		if err := validateChildOutput(schema, output); err != nil {
			return fmt.Sprintf("child process %q (%s) output validation: %v", child.ID, child.ProcessName, err)
		}
	}
	parentCtx["outputs"].(map[string]any)[stepID] = output
	return ""
}

// buildParallelChildOutput writes each sibling's output to parentCtx["outputs"][stepID][key].
// Returns a non-empty error message on the first validation failure.
func buildParallelChildOutput(parentCtx map[string]any, stepID string, siblings []*model.ProcessInstance) string {
	result := make(map[string]any, len(siblings))
	for _, child := range siblings {
		key, _ := child.ContextData["_spawn_child_key"].(string)
		output := child.ContextData["output"]
		if schemaRaw, _ := child.ContextData["_spawn_output_schema"].(string); schemaRaw != "" {
			var schema map[string]any
			json.Unmarshal([]byte(schemaRaw), &schema) //nolint:errcheck
			if err := validateChildOutput(schema, output); err != nil {
				return fmt.Sprintf("child process %q (%s) output validation: %v", child.ID, child.ProcessName, err)
			}
		}
		result[key] = output
	}
	parentCtx["outputs"].(map[string]any)[stepID] = result
	return ""
}

func validateChildOutput(schema map[string]any, output any) error {
	result, err := gojsonschema.Validate(
		gojsonschema.NewGoLoader(schema),
		gojsonschema.NewGoLoader(output),
	)
	if err != nil {
		return fmt.Errorf("schema validation error: %w", err)
	}
	if !result.Valid() {
		msgs := make([]string, len(result.Errors()))
		for i, e := range result.Errors() {
			msgs[i] = e.String()
		}
		return fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	return nil
}
