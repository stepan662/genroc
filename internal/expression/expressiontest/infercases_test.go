package expressiontest

import (
	"encoding/json"
	"testing"

	"genroc/internal/schema"
)

// Helpers shared by infer_lambda_test.go and infer_literal_test.go. They exist so
// each individual case can stay a one-line test body; anything reused by the wider
// package lives in helpers_test.go instead.

// assertSecretCase asserts that ReferencesSecret(expr) matches want. The two
// failure directions are reported differently on purpose: a missing taint is a
// value that reaches the logs in the clear, while a spurious one only costs log
// verbosity.
func assertSecretCase(t *testing.T, c schema.Schema, expr string, want bool) {
	t.Helper()
	got, err := c.ReferencesSecret(expr)
	if err != nil {
		t.Fatalf("ReferencesSecret(%q): %v", expr, err)
	}
	if got == want {
		return
	}
	if want {
		t.Errorf("ReferencesSecret(%q) = false, want true (secret leak!)", expr)
	} else {
		t.Errorf("ReferencesSecret(%q) = true, want false (over-redaction)", expr)
	}
}

// inferredJSON marshals the schema inferred for expr. Generated schemas are
// compared by bytes elsewhere (the recursive-inference fixpoint, the checked-in
// spec files), so the JSON encoding is the thing determinism is about.
func inferredJSON(t *testing.T, expr string, c schema.Schema) string {
	t.Helper()
	b, err := json.Marshal(infer(t, expr, c))
	if err != nil {
		t.Fatalf("marshal %q: %v", expr, err)
	}
	return string(b)
}

// assertDeterministic infers expr twice and requires byte-identical results — a
// result that depended on Go map iteration order would make generated schemas
// churn between runs and break the fixpoint's equality test.
func assertDeterministic(t *testing.T, expr string, c schema.Schema) {
	t.Helper()
	first := inferredJSON(t, expr, c)
	if second := inferredJSON(t, expr, c); second != first {
		t.Errorf("Infer(%q) is not deterministic:\n  first:  %s\n  second: %s", expr, first, second)
	}
}

// assertDescribesOwnEmptyArray checks that the schema inferred for expr accepts
// the empty array it can produce and still exposes an element type (child_list's
// `over` reads Items() directly, which a union does not answer).
func assertDescribesOwnEmptyArray(t *testing.T, c schema.Schema, expr string) {
	t.Helper()
	s, err := c.Infer(expr)
	if err != nil {
		t.Fatalf("Infer(%q): %v", expr, err)
	}
	if _, err := s.Validate([]any{}); err != nil {
		t.Errorf("Infer(%q) rejects the empty array it describes: %v", expr, err)
	}
	if !s.HasItems() {
		t.Errorf("Infer(%q) lost its element type; child_list over reads Items() directly", expr)
	}
}
