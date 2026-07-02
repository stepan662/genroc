package validation

import (
	"fmt"
	"slices"

	"genroc/internal/model"
	"genroc/internal/schema"
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
		resultType, err := actionResultType(s, defs)
		if err != nil {
			return fmt.Errorf("task %q: %w", id, err)
		}
		ctx := outputMapContext(base, resultType, id, loops).WithDefs(defs)
		node := s.Output.Raw
		solver.Declare(id+"_output", func() (schema.Schema, error) {
			return inferShape(node, ctx, "output")
		})
		declared = true
	}
	if !declared {
		return nil
	}
	return solver.Solve()
}
