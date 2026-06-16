package expression

import (
	"fmt"

	"gent/internal/schema"
)

// maxRecursivePasses bounds the fixpoint iteration. Fixed-shape accumulators
// (counters, sums, appends) converge in 1–2 passes; the cap guards against a
// genuinely diverging type (e.g. one that nests deeper every iteration), which
// is reported as an error rather than looped forever.
const maxRecursivePasses = 16

// InferRecursiveObject infers the type of the object built from exprs (a map of
// output-field name → expression) when that object may reference its own
// previous value via outputs.<selfID> in ctx.
//
// It runs a bounded fixpoint over the type lattice. The self-output is seeded as
// null — its genuine value before the first iteration — so a `?? default` base
// case resolves it; the inferred result is then fed back as nullable each pass,
// joined with the running type and canonicalized, until it stabilizes. A
// recursive expression with no base case (e.g. `outputs.x.n + 1` with no `??`)
// surfaces as an inference error from the null seed; a type that never
// stabilizes hits the pass cap and errors.
func InferRecursiveObject(exprs map[string]string, ctx *schema.SchemaNode, selfID string) (*schema.SchemaNode, error) {
	var prev *schema.SchemaNode
	for pass := 0; pass < maxRecursivePasses; pass++ {
		seed := nullNode()
		if prev != nil {
			seed = schema.WithNull(prev)
		}
		cur, err := inferObjectWithSelf(exprs, ctx, selfID, seed)
		if err != nil {
			return nil, err
		}
		cur = schema.Canonicalize(cur)
		if prev == nil {
			prev = cur
			continue
		}
		joined := schema.Join(prev, cur)
		if schema.Equal(joined, prev) {
			return prev, nil
		}
		prev = joined
	}
	return nil, fmt.Errorf("recursive output type did not stabilize after %d passes; declare its type explicitly", maxRecursivePasses)
}

// inferObjectWithSelf infers {name: type} for each expression, with the context's
// outputs.<selfID> bound to seed (the current estimate of the recursive value).
func inferObjectWithSelf(exprs map[string]string, ctx *schema.SchemaNode, selfID string, seed *schema.SchemaNode) (*schema.SchemaNode, error) {
	ctxN := withOutputProp(ctx, selfID, seed)
	props := make(map[string]*schema.SchemaNode, len(exprs))
	required := make([]string, 0, len(exprs))
	for name, expr := range exprs {
		inferred, err := InferType(expr, schema.FromNode(ctxN))
		if err != nil {
			return nil, fmt.Errorf("output %q: %w", name, err)
		}
		props[name] = inferred.Node()
		required = append(required, name)
	}
	return &schema.SchemaNode{Type: schema.SchemaType{"object"}, Properties: props, Required: required}, nil
}

// withOutputProp returns a shallow copy of ctx with outputs.<id> replaced by seed,
// leaving the original (and its sibling outputs) untouched.
func withOutputProp(ctx *schema.SchemaNode, id string, seed *schema.SchemaNode) *schema.SchemaNode {
	outProps := map[string]*schema.SchemaNode{}
	var origReq []string
	if outs := ctx.Properties["outputs"]; outs != nil {
		for k, v := range outs.Properties {
			outProps[k] = v
		}
		origReq = outs.Required
	}
	outProps[id] = seed

	// Preserve the required (always-available) sibling outputs so they stay
	// non-nullable; the self output is intentionally left non-required, since its
	// previous value is null on the first iteration.
	var req []string
	for _, k := range origReq {
		if k != id {
			req = append(req, k)
		}
	}
	newOutputs := &schema.SchemaNode{Type: schema.SchemaType{"object"}, Properties: outProps}
	if len(req) > 0 {
		newOutputs.Required = req
	}

	newProps := make(map[string]*schema.SchemaNode, len(ctx.Properties))
	for k, v := range ctx.Properties {
		newProps[k] = v
	}
	newProps["outputs"] = newOutputs

	n := *ctx
	n.Properties = newProps
	return &n
}

func nullNode() *schema.SchemaNode {
	return &schema.SchemaNode{Type: schema.SchemaType{"null"}}
}
