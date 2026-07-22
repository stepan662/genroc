package shape

import (
	"encoding/json"

	"genroc/internal/schema"
)

// exprLeafDesc annotates every string position in a relaxed schema: a string leaf accepts a
// literal, or — because a Shape leaf is an expression — a $: expression / ${ } template.
const exprLeafDesc = "A literal string, or a $: expression / ${ } template evaluated against the context."

// modelShapeRef is the recursive self-reference every free (unconstrained) Shape position
// resolves to: the generic Value def. The spec builder's InterceptDefName keeps this type's
// generated def name as ModelShape, and the OpenAPI builder rewrites #/$defs/ModelShape to
// #/components/schemas/ModelShape.
func modelShapeRef() map[string]any { return map[string]any{"$ref": "#/$defs/ModelShape"} }

// GenericValueSchema generates the ModelShape def — the generic Value grammar
// (string | number | boolean | null | array | object), recursive, with the string branch
// doubling as the $:/${ } expression escape hatch at every level. Every free Shape slot
// (body, output, input) resolves here via $ref, so this one generated def covers them all;
// RelaxedSchema handles the bounded slots (headers). Together they are the single source for
// all Shape editor schemas.
//
// anyOf, not oneOf: the string branch overlaps a nested object's string leaves and
// number/integer overlap, which oneOf's exactly-one rule would spuriously reject.
//
// Array items stay permissive ({}), NOT $ref ModelShape. openapi-typescript (which generates
// the test client from openapi.json) emits a $ref as an indexed access; an *array* of that
// indexed self-reference is an eager cycle tsc rejects (TS2502), whereas the object
// index-signature self-reference is lazy. So arrays are accepted but not recursed into — the
// same permissive-for-reflection compromise schema.Schema makes. Do not change items to
// $ref ModelShape: it breaks the client typecheck.
func GenericValueSchema() ([]byte, error) {
	return json.Marshal(map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string", "description": "A $: typed expression, a ${ } interpolation template, or a literal string."},
			map[string]any{"type": "number"},
			map[string]any{"type": "boolean"},
			map[string]any{"type": "null"},
			map[string]any{"type": "array", "description": "Literal array; each element is recursively a Shape.", "items": map[string]any{}},
			map[string]any{"type": "object", "description": "Literal object; each value is recursively a Shape.", "additionalProperties": modelShapeRef()},
		},
	})
}

// RelaxedSchema generates the editor JSON Schema for a Shape whose value must conform to
// target. schema.Relaxed does all the work — every node becomes "the literal value, or a
// string" (the expression escape hatch), recursively and universally over the schema, and
// every string position is labelled with exprLeafDesc so the editor explains that any string
// may be an expression.
//
// A bounded target like fetch headers' object<string> becomes "an object whose values are
// strings (each a string or an expression), or the whole thing an expression"; a typed leaf
// like count:integer becomes "an integer or an expression".
func RelaxedSchema(target schema.Schema) ([]byte, error) {
	return json.Marshal(target.Relaxed(exprLeafDesc))
}
