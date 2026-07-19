package validation

import (
	"fmt"
	"slices"

	"genroc/internal/model"
	"genroc/internal/schema"
	"genroc/internal/template"
)

// inferOutputs infers the type of every output-map task's output and writes it
// to defs (as <id>_output). Resolution is demand-driven: each task's inference
// pulls the outputs it reads through their $refs, so the schema solver orders
// the work by exact dependency, detects self- and mutually-recursive output
// maps on contact, and resolves each cycle with a joint fixpoint (members
// seeded null, re-inferred, joined until stable). There is no separately
// maintained dependency graph to keep in sync with what inference actually
// reads. See docs/recursive-type-inference.md.
func inferOutputs(tasks []*model.Task, taskSchemas map[string]TaskSchemas, processInput, configSchema schema.Schema,
	defs schema.Defs, required, optional map[string][]string, mustErr, mayErr map[string]bool) error {

	solver := schema.NewSolver(defs)
	declared := false
	for _, s := range tasks {
		if !s.Output.Present() {
			continue
		}
		id := s.ID
		base := contextSchema(required[id], optional[id], taskSchemas, processInput, configSchema, mustErr[id], mayErr[id])
		// The task loops iff it is its own predecessor: computeContextSets then
		// lists its own output among its available (optional) outputs.
		loops := slices.Contains(optional[id], id) || slices.Contains(required[id], id)
		resultType, typed, err := actionResultType(s, defs)
		if err != nil {
			return fmt.Errorf("task %q: %w", id, err)
		}
		// An untyped result (fetch/external with no result_schema) cannot be exported:
		// give a clear error up front rather than an opaque navigation failure later.
		if !typed {
			refs, rerr := shapeRefsSelfResult(s.Output.Raw)
			if rerr != nil {
				return fmt.Errorf("task %q output: %w", id, rerr)
			}
			if refs {
				return fmt.Errorf("task %q: output references self.result, but the action has no result_schema — add a result_schema to type the response", id)
			}
		}
		ctx := outputMapContext(base, resultType, typed, id, loops).WithDefs(defs)
		node := s.Output.Raw
		label := fmt.Sprintf("task %q output", id)
		solver.Declare(id+"_output", func() (schema.Schema, error) {
			return inferShape(node, ctx, label)
		})
		declared = true
	}
	if !declared {
		return nil
	}
	return solver.Solve()
}

// shapeRefsSelfResult reports whether any expression in an output shape (a string leaf
// or a nested object of them) reads self.result.
func shapeRefsSelfResult(node any) (bool, error) {
	switch n := node.(type) {
	case string:
		t, err := template.Get(n)
		if err != nil {
			return false, err
		}
		return t.RootRefs().SelfResult, nil
	case map[string]any:
		for _, v := range n {
			ref, err := shapeRefsSelfResult(v)
			if err != nil {
				return false, err
			}
			if ref {
				return true, nil
			}
		}
	}
	return false, nil
}
