package validation

import (
	"genroc/internal/schema"
	"genroc/internal/shape"
)

// InferRecursiveOutput infers the type of a single self-referential output map.
// In ctx, both outputs.<id> and self.previous resolve via $ref to selfDef (the
// recursive placeholder in ctx's $defs). It is the one-member case of the
// demand-driven solver used for whole processes; ctx's defs handle is mutated
// in place so the running estimate is observed through those $refs, and selfDef
// ends up holding the inferred type, which is returned.
func InferRecursiveOutput(exprs map[string]string, ctx schema.Schema, selfDef string) (schema.Schema, error) {
	defs := ctx.DefsHandle()
	if defs.IsZero() {
		defs = schema.NewDefs()
		ctx = ctx.WithDefs(defs)
	}
	node := make(map[string]any, len(exprs))
	for k, v := range exprs {
		node[k] = v
	}
	solver := schema.NewSolver(defs)
	solver.Declare(selfDef, func() (schema.Schema, error) {
		shp := shape.Shape{Raw: node, Name: "output"}
		return shp.Check(ctx)
	})
	if err := solver.Solve(); err != nil {
		return schema.Schema{}, err
	}
	out, _ := defs.Get(selfDef)
	return out, nil
}
