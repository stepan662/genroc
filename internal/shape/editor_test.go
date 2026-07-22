package shape

import (
	"encoding/json"
	"strings"
	"testing"

	"genroc/internal/schema"
)

// relax(target) and decode the result to a map for structural assertions.
func relaxed(t *testing.T, target schema.Schema) map[string]any {
	t.Helper()
	b, err := RelaxedSchema(target)
	if err != nil {
		t.Fatalf("RelaxedSchema: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// anyOfBranches returns the two branches of a relaxed node: its typed literal and the
// expression-string escape hatch (identified by type:"string").
func anyOfBranches(t *testing.T, node map[string]any) (lit, str map[string]any) {
	t.Helper()
	arr, ok := node["anyOf"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("expected anyOf with 2 branches, got %v", node)
	}
	for _, b := range arr {
		bm := b.(map[string]any)
		if bm["type"] == "string" {
			str = bm
		} else {
			lit = bm
		}
	}
	if lit == nil || str == nil {
		t.Fatalf("expected a literal branch and a string branch, got %v", arr)
	}
	return lit, str
}

// A string leaf collapses: a literal string and an expression string coincide, so it stays a
// plain string node (no redundant anyOf) — but it still gains the "may be an expression" hint.
func TestRelaxedSchema_StringLeafCollapsesAndIsAnnotated(t *testing.T) {
	m := relaxed(t, schema.Type("string"))
	if m["type"] != "string" {
		t.Errorf("string leaf should stay a plain string node, got %v", m)
	}
	if _, ok := m["anyOf"]; ok {
		t.Errorf("string leaf must not gain an anyOf, got %v", m)
	}
	if _, ok := m["description"]; !ok {
		t.Errorf("string leaf should be annotated as expression-capable, got %v", m)
	}
}

// object<string> (fetch headers' target) becomes "an object whose values are strings, or the
// whole thing an expression" — and, per the reported gap, the VALUES carry the expression hint.
func TestRelaxedSchema_ObjectOfStrings_ValuesAnnotated(t *testing.T) {
	lit, str := anyOfBranches(t, relaxed(t, schema.Map(schema.Type("string"))))
	if lit["type"] != "object" {
		t.Fatalf("literal branch should be an object, got %v", lit)
	}
	if _, ok := str["description"]; !ok {
		t.Errorf("the whole-value expression branch should be annotated, got %v", str)
	}
	ap, ok := lit["additionalProperties"].(map[string]any)
	if !ok || ap["type"] != "string" {
		t.Fatalf("values should collapse to plain strings, got %v", lit["additionalProperties"])
	}
	if _, ok := ap["description"]; !ok {
		t.Errorf("header VALUES must be annotated as expression-capable (the reported gap), got %v", ap)
	}
}

// A typed non-string leaf (integer) gains the expression branch; a sibling string leaf
// collapses (but is annotated); and structural keywords (required) survive on the literal.
func TestRelaxedSchema_TypedPropertiesRelaxRecursively(t *testing.T) {
	target := schema.Object().
		WithProperty("count", schema.Type("integer"), true).
		WithProperty("name", schema.Type("string"), true)
	lit, _ := anyOfBranches(t, relaxed(t, target))

	props, ok := lit["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties on the literal branch, got %v", lit)
	}

	// count:integer → anyOf:[{integer}, {string expression}]
	countLit, _ := anyOfBranches(t, props["count"].(map[string]any))
	if countLit["type"] != "integer" {
		t.Errorf("count literal should stay integer, got %v", countLit)
	}
	// name:string → collapsed plain string, still annotated
	name := props["name"].(map[string]any)
	if name["type"] != "string" {
		t.Errorf("name should collapse to a plain string, got %v", name)
	}
	if _, ok := name["anyOf"]; ok {
		t.Errorf("string property must not gain an anyOf, got %v", name)
	}

	// required carries through onto the literal branch.
	req, ok := lit["required"].([]any)
	if !ok || len(req) != 2 {
		t.Errorf("required should carry through, got %v", lit["required"])
	}
}

// Array elements are relaxed too: array<number> accepts a number or an expression per element,
// or an expression for the whole array.
func TestRelaxedSchema_ArrayItemsRelax(t *testing.T) {
	lit, _ := anyOfBranches(t, relaxed(t, schema.Array(schema.Type("number"))))
	if lit["type"] != "array" {
		t.Fatalf("literal branch should be an array, got %v", lit)
	}
	itemLit, _ := anyOfBranches(t, lit["items"].(map[string]any))
	if itemLit["type"] != "number" {
		t.Errorf("array item literal should stay number, got %v", itemLit)
	}
}

// GenericValueSchema — the ModelShape def every free Shape slot resolves to: the 6-way Value
// union that recurses via a $ref for object values.
func TestGenericValueSchema(t *testing.T) {
	b, err := GenericValueSchema()
	if err != nil {
		t.Fatalf("GenericValueSchema: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	arr, ok := m["anyOf"].([]any)
	if !ok || len(arr) != 6 {
		t.Fatalf("generic Value should be anyOf with 6 branches, got %v", m["anyOf"])
	}
	if !strings.Contains(string(b), `"$ref":"#/$defs/ModelShape"`) {
		t.Errorf("object branch must recurse via #/$defs/ModelShape; got %s", b)
	}
}
