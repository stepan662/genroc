package schematest

import (
	"testing"

	"genroc/internal/schema"
)

// mustAssumed parses a JSON schema and wraps it as-is (subset tests supply
// already-flat fixtures; some deliberately exercise pre-normalized shapes).
func mustAssumed(t *testing.T, src string) schema.Schema {
	t.Helper()
	raw, err := schema.Parse([]byte(src))
	if err != nil {
		t.Fatalf("invalid schema: %v", err)
	}
	return raw.AssumeNormalized()
}

func assertSubset(t *testing.T, subJSON, superJSON string, want bool) {
	t.Helper()
	got := mustAssumed(t, subJSON).IsSubset(mustAssumed(t, superJSON))
	if got != want {
		t.Errorf("IsSubset(%s, %s) = %v, want %v", subJSON, superJSON, got, want)
	}
}

// assertEquivalent checks that a ⊆ b and b ⊆ a, proving semantic equivalence.
func assertEquivalent(t *testing.T, aJSON, bJSON string, want bool) {
	t.Helper()
	a, b := mustAssumed(t, aJSON), mustAssumed(t, bJSON)
	aSubB := a.IsSubset(b)
	bSubA := b.IsSubset(a)
	got := aSubB && bSubA
	if got != want {
		t.Errorf("equivalent(%s, %s): a⊆b=%v b⊆a=%v, want equivalent=%v", aJSON, bJSON, aSubB, bSubA, want)
	}
}
