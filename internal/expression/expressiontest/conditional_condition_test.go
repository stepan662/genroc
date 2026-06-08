package expressiontest

import "testing"

// nullableErrorSchema — error is a nullable object with string fields step/message/code,
// mirroring the error context shape produced by the gent engine.
var nullableErrorSchema = mustSchema(`{
	"properties": {
		"error": {
			"oneOf": [
				{
					"type": "object",
					"properties": {
						"step":    {"type": "string"},
						"message": {"type": "string"},
						"code":    {"type": "string"}
					},
					"required": ["step", "message", "code"]
				},
				{"type": "null"}
			]
		}
	},
	"required": ["error"]
}`)

// Unknown identifiers used only in the conditional condition must be rejected.
// Prior to the fix, inferConditional never called inferNode on n.Cond, so
// identifiers like "null" or "non_existant" that appear only in the condition
// were silently ignored.

func TestConditionalCondition_RejectsUnknownIdentifier(t *testing.T) {
	inferErr(t, "non_existant != nil ? 0 : 1", integerXSchema, `"non_existant"`)
}

func TestConditionalCondition_RejectsNullIdentifier(t *testing.T) {
	// "null" is not a nil literal in expr-lang — it's treated as an identifier.
	// The correct null literal is "nil".
	inferErr(t, "error != null ? error.code : 'hi'", nullableErrorSchema, `"null"`)
}

// Guard on "error" propagates through member access — accessing error.code in the
// safe branch of error != nil infers as non-null string.
func TestConditionalCondition_NilGuardPropagatesThrough_MemberAccess(t *testing.T) {
	assertSchema(t,
		infer(t, "error != nil ? error.code : 'hi'", nullableErrorSchema),
		`{"type": "string"}`,
	)
}
