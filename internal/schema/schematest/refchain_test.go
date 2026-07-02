package schematest

import (
	"reflect"
	"strings"
	"testing"

	"genroc/internal/schema"
)

// A named schema that is a bare root $ref to its own nested definition sharing
// the container's name (e.g. an input_schema of {$ref:#/$defs/input} whose def
// pool spells the definition "input" — the playground shape). This is not a
// real collision: the entry is a pure alias of its target, so the target must
// bind directly under the name — one definition, no input → input_1 chain —
// and never resolve to the entry itself (root-first resolution turned it into
// a self-loop and dropped the real definition as unused).
func TestFlattenNamedBareRefEntrySharingDefName(t *testing.T) {
	in := mustParse(t, `{
		"$ref": "#/$defs/input",
		"$defs": {"input": {
			"type": "object",
			"properties": {"blob": {"type": "string", "default": "12"}, "sleep": {"type": "integer"}},
			"required": ["sleep"]
		}}
	}`)
	defs, err := schema.FlattenNamed(map[string]schema.Schema{"input": in})
	if err != nil {
		t.Fatalf("FlattenNamed: %v", err)
	}
	if defs.Len() != 1 {
		t.Fatalf("expected the alias to collapse into one definition, got %d: %v", defs.Len(), defs.Names())
	}
	bound, ok := defs.Get("input")
	if !ok {
		t.Fatalf("expected the definition to be bound as %q, have %v", "input", defs.Names())
	}
	if bound.HasRef() {
		t.Errorf("definition %q is still an alias (%v); expected the object schema directly", "input", bound)
	}

	sc := schema.Ref("input").WithDefs(defs)
	if _, err := sc.Property("blob"); err != nil {
		t.Errorf("Property(blob) through the flattened def: %v", err)
	}
	// Validation resolves the chain too: the default fills, required enforces.
	got, err := sc.Validate(map[string]any{"sleep": float64(1)})
	if err != nil {
		t.Fatalf("Validate through the ref chain: %v", err)
	}
	want := map[string]any{"blob": "12", "sleep": float64(1)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalized mismatch\ngot:  %v\nwant: %v", got, want)
	}
	if _, err := sc.Validate(map[string]any{}); err == nil {
		t.Errorf("Validate: expected missing-required error through the ref chain, got none")
	}
}

// The same definition arriving through two schemas (each carrying its own baked
// copy) is not a conflict either: the content-equal copies share one entry
// instead of piling up User, User_1, ….
func TestFlattenNamedSharesContentEqualDefs(t *testing.T) {
	const user = `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`
	a := mustParse(t, `{"type":"object","properties":{"u":{"$ref":"#/$defs/User"}},"$defs":{"User":`+user+`}}`)
	b := mustParse(t, `{"type":"object","properties":{"w":{"$ref":"#/$defs/User"}},"$defs":{"User":`+user+`}}`)
	defs, err := schema.FlattenNamed(map[string]schema.Schema{"a_output": a, "b_output": b})
	if err != nil {
		t.Fatalf("FlattenNamed: %v", err)
	}
	if !defs.Has("User") || defs.Has("User_1") {
		t.Errorf("expected one shared User definition, got %v", defs.Names())
	}
	if defs.Len() != 3 { // a_output, b_output, User
		t.Errorf("expected 3 definitions, got %d: %v", defs.Len(), defs.Names())
	}
	// Both schemas' refs land on the shared copy.
	for _, entry := range []struct{ name, prop string }{{"a_output", "u"}, {"b_output", "w"}} {
		sc := schema.Ref(entry.name).WithDefs(defs)
		if _, err := sc.Infer(entry.prop + ".name"); err != nil {
			t.Errorf("%s.%s.name through the shared def: %v", entry.name, entry.prop, err)
		}
	}
}

// Merging the same self-recursive definition twice (the result schema is merged
// once per inference pass) must reuse the renamed copy from the first merge:
// plain textual comparison sees the fresh copy's self-ref spelling the original
// name and the merged copy's spelling the renamed one, and mints a new
// input_2, input_3, … duplicate on every pass.
func TestMergeIntoReusesRenamedRecursiveDef(t *testing.T) {
	baked := func() schema.Schema {
		return mustParse(t, `{
			"$ref": "#/$defs/input",
			"$defs": {"input": {
				"type": "object",
				"properties": {"recursive": {"$ref": "#/$defs/input"}}
			}}
		}`)
	}
	defs := schema.NewDefs()
	defs.Set("input", mustParse(t, `{"type":"string"}`)) // the name is taken (e.g. the generated input schema)

	first, err := baked().MergeInto(defs)
	if err != nil {
		t.Fatalf("first MergeInto: %v", err)
	}
	second, err := baked().MergeInto(defs)
	if err != nil {
		t.Fatalf("second MergeInto: %v", err)
	}
	if defs.Len() != 2 { // input (taken) + one renamed recursive copy
		t.Errorf("expected the second merge to reuse the renamed copy, got defs %v", defs.Names())
	}
	if !first.Equal(second) {
		t.Errorf("merged schemas diverged:\nfirst:  %v\nsecond: %v", first, second)
	}
}

// A definition may be a bare alias for another definition (collision renames
// produce these); every deref-based operation must follow the chain to the
// concrete schema instead of stopping after one hop.
func TestValidateAndNavigateThroughRefAlias(t *testing.T) {
	sc := mustParse(t, `{
		"$ref": "#/$defs/A",
		"$defs": {"A": {"$ref": "#/$defs/B"}, "B": {"type": "string", "minLength": 2}}
	}`)
	if _, err := sc.Validate("ok"); err != nil {
		t.Errorf("Validate through alias: %v", err)
	}
	if _, err := sc.Validate("x"); err == nil {
		t.Errorf("Validate: expected minLength error through alias, got none")
	}
	resolved, err := sc.Resolve()
	if err != nil {
		t.Fatalf("Resolve through alias: %v", err)
	}
	if !resolved.Type().Contains("string") {
		t.Errorf("Resolve: expected the concrete string schema, got type %v", resolved.Type())
	}
}

// A pure $ref cycle can never bottom out at a schema; operations must fail with
// an error rather than hang.
func TestPureRefCycleErrors(t *testing.T) {
	sc := mustParse(t, `{
		"$ref": "#/$defs/A",
		"$defs": {"A": {"$ref": "#/$defs/B"}, "B": {"$ref": "#/$defs/A"}}
	}`)
	_, err := sc.Validate("x")
	if err == nil {
		t.Fatalf("Validate: expected circular-ref error, got none")
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Errorf("error %q does not mention the circular ref", err.Error())
	}
}
