package schematest

import (
	"encoding/json"
	"strings"
	"testing"

	"genroc/internal/schema"
)

// description is now an accepted keyword — authors can document schema fields — and it
// survives parse + normalize into the stored/served schema.
func TestDescription_ParsesAndSurvivesNormalize(t *testing.T) {
	src := `{
		"type": "object",
		"description": "a user record",
		"properties": {
			"name": {"type": "string", "description": "the user's display name"}
		},
		"required": ["name"]
	}`
	raw, err := schema.Parse([]byte(src))
	if err != nil {
		t.Fatalf("description should be an accepted keyword: %v", err)
	}
	s, err := raw.Normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if s.Description() != "a user record" {
		t.Errorf("root description lost through normalize, got %q", s.Description())
	}
	if got := s.Properties()["name"].Description(); got != "the user's display name" {
		t.Errorf("property description lost through normalize, got %q", got)
	}
	// It also lands in the serialized schema.
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), "the user's display name") {
		t.Errorf("description missing from serialized schema: %s", b)
	}
}

// description has no type meaning: two schemas that differ only in wording are equal, and
// canonicalization drops it — so the recursive-inference fixpoint is unaffected.
func TestDescription_IgnoredByTypeIdentity(t *testing.T) {
	a := schema.Object().WithProperty("n", schema.Type("integer"), true).WithDescription("first wording")
	b := schema.Object().WithProperty("n", schema.Type("integer"), true).WithDescription("totally different wording")

	if !a.Equal(b) {
		t.Errorf("schemas differing only in description should be Equal")
	}
	cb, _ := json.Marshal(a.Canonicalize())
	if strings.Contains(string(cb), "first wording") {
		t.Errorf("canonicalize should strip description, got %s", cb)
	}
}

// description does not affect subset checking in either direction.
func TestDescription_IgnoredBySubset(t *testing.T) {
	described := schema.Type("string").WithDescription("documented")
	plain := schema.Type("string")
	if !described.IsSubset(plain) || !plain.IsSubset(described) {
		t.Errorf("description must not affect IsSubset")
	}
}

// WithDescription / Description round-trip, leaving the type untouched.
func TestDescription_Builder(t *testing.T) {
	s := schema.Type("number").WithDescription("a count")
	if s.Description() != "a count" {
		t.Errorf("WithDescription/Description round-trip failed, got %q", s.Description())
	}
	if !s.IsType("number") {
		t.Errorf("WithDescription must not change the type")
	}
}
