package schematest

import (
	"encoding/json"
	"strings"
	"testing"

	"genroc/internal/schema"
)

func relaxToMap(t *testing.T, s schema.Schema) map[string]any {
	t.Helper()
	b, err := json.Marshal(s.Relaxed(""))
	if err != nil {
		t.Fatalf("marshal relaxed: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// A pure string is left unchanged — a literal string and an expression string coincide.
func TestRelaxed_StringUnchanged(t *testing.T) {
	m := relaxToMap(t, schema.Type("string"))
	if m["type"] != "string" {
		t.Errorf("string should relax to itself, got %v", m)
	}
}

// A non-string leaf becomes `node | string`.
func TestRelaxed_ScalarGainsStringAlternative(t *testing.T) {
	m := relaxToMap(t, schema.Type("number"))
	arr, ok := m["anyOf"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("number should relax to anyOf[number, string], got %v", m)
	}
}

// A multi-type that already includes string is not double-wrapped.
func TestRelaxed_MultiTypeWithStringNotWrapped(t *testing.T) {
	m := relaxToMap(t, schema.Type("number", "string"))
	if _, ok := m["anyOf"]; ok {
		t.Errorf("a type already admitting string should not be wrapped, got %v", m)
	}
}

// An enum keeps expressions allowed: it is wrapped so a string (expression) is accepted
// alongside the enum members, even when the enum is over strings.
func TestRelaxed_EnumStaysExpressible(t *testing.T) {
	s := schema.Load(map[string]any{"type": "string", "enum": []any{"a", "b"}})
	m := relaxToMap(t, s)
	arr, ok := m["anyOf"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("an enum should be wrapped as anyOf[enum, string], got %v", m)
	}
}

// The walk is universal: it descends through a union AND the object structure inside it,
// rather than only handling a top-level object/array/string. A deeply-nested integer leaf
// still ends up expressible.
func TestRelaxed_RecursesThroughUnionAndObject(t *testing.T) {
	inner := schema.Object().WithProperty("n", schema.Type("integer"), true)
	s := schema.OneOf(inner, schema.Type("boolean"))

	b, err := json.Marshal(s.Relaxed(""))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The integer leaf n, three levels deep (wrap → oneOf variant wrap → object property),
	// must have gained a string alternative.
	if !strings.Contains(string(b), `"n":{"anyOf":[{"type":"integer"},{"type":"string"}]}`) {
		t.Errorf("nested integer leaf should have been relaxed to integer|string, got %s", b)
	}
	// The boolean variant likewise gains a string alternative.
	if !strings.Contains(string(b), `{"anyOf":[{"type":"boolean"},{"type":"string"}]}`) {
		t.Errorf("boolean variant should have been relaxed to boolean|string, got %s", b)
	}
}

// Nested objects inside arrays inside objects are all relaxed — the descent bottoms out at
// the deepest leaf regardless of how many object/array layers wrap it.
func TestRelaxed_NestedObjectInArray(t *testing.T) {
	// object{ rows: array< object{ n: integer } > }
	row := schema.Object().WithProperty("n", schema.Type("integer"), true)
	s := schema.Object().WithProperty("rows", schema.Array(row), true)

	b, err := json.Marshal(s.Relaxed(""))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"n":{"anyOf":[{"type":"integer"},{"type":"string"}]}`) {
		t.Errorf("integer leaf nested object→array→object should be relaxed, got %s", b)
	}
}

// Relax descends into $defs and through a $ref target, hoisting root $defs onto the wrapper
// so the ref still resolves — the branch used by any future ref-bearing bounded target.
func TestRelaxed_RefTargetRecursesDefsAndStaysResolvable(t *testing.T) {
	src := `{"type":"object","properties":{"x":{"$ref":"#/$defs/foo"}},"$defs":{"foo":{"type":"integer"}}}`
	raw, err := schema.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	s, err := raw.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s.Relaxed(""))
	if err != nil {
		t.Fatal(err)
	}
	str := string(b)
	// The referenced def is itself relaxed (integer → integer|string) …
	if !strings.Contains(str, `"foo":{"anyOf":[{"type":"integer"},{"type":"string"}]}`) {
		t.Errorf("$defs target should be relaxed, got %s", str)
	}
	// … and the $ref (plus its hoisted $defs) is preserved so it still resolves.
	if !strings.Contains(str, `"$ref":"#/$defs/foo"`) || !strings.Contains(str, `"$defs"`) {
		t.Errorf("the $ref and hoisted $defs should be preserved, got %s", str)
	}
}

// A null leaf is not a string, so it gains the string (expression) alternative.
func TestRelaxed_NullGainsStringAlternative(t *testing.T) {
	m := relaxToMap(t, schema.Type("null"))
	if _, ok := m["anyOf"]; !ok {
		t.Errorf("null should relax to anyOf[null, string], got %v", m)
	}
}

// stringNote labels every string position (the reported gap: header VALUES) — a string leaf
// and the string alternative added to a non-string node both carry it, and it is set on the
// node itself so no JSON post-pass is needed.
func TestRelaxed_StringNoteAnnotatesEveryStringPosition(t *testing.T) {
	s := schema.Map(schema.Type("string")) // object<string>, like fetch headers
	m := map[string]any{}
	b, _ := json.Marshal(s.Relaxed("EXPR-HINT"))
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	// The whole-value string alternative and the nested map VALUES both carry the note.
	if got := strings.Count(string(b), "EXPR-HINT"); got != 2 {
		t.Errorf("expected the note on both string positions (whole-value + values), got %d in %s", got, b)
	}
}

// An author's own description on a string field is never clobbered by the note.
func TestRelaxed_NoteDoesNotClobberAuthorDescription(t *testing.T) {
	s := schema.Type("string").WithDescription("the auth token")
	b, _ := json.Marshal(s.Relaxed("EXPR-HINT"))
	if strings.Contains(string(b), "EXPR-HINT") || !strings.Contains(string(b), "the auth token") {
		t.Errorf("author description should win over the note, got %s", b)
	}
}
