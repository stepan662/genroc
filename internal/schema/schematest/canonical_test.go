package schematest

import (
	"encoding/json"
	"testing"

	"genroc/internal/schema"
)

// canonJSON canonicalizes s and returns its JSON, asserting idempotence.
func canonJSON(t *testing.T, s schema.Schema) string {
	t.Helper()
	got := s.Canonicalize()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal canonical: %v", err)
	}
	// Idempotence: canonicalizing again must not change the JSON.
	b2, _ := json.Marshal(got.Canonicalize())
	if string(b) != string(b2) {
		t.Fatalf("Canonicalize not idempotent:\n  once: %s\n twice: %s", b, b2)
	}
	return string(b)
}

// ── shape builders shared by the canonical/join tests ──────────────────────────

func prim(types ...string) schema.Schema { return schema.Type(types...) }

func oneOf(vs ...schema.Schema) schema.Schema { return schema.OneOf(vs...) }

func anyOf(vs ...schema.Schema) schema.Schema { return schema.AnyOf(vs...) }

// allOf has no public builder — it is never accepted from user JSON and exists
// only as normalization's internal bundling vehicle — so the tests assemble it
// through Load, which tolerates the internal field.
func allOf(vs ...schema.Schema) schema.Schema {
	ms := make([]any, len(vs))
	for i, v := range vs {
		ms[i] = v.AsMap()
	}
	return schema.Load(map[string]any{"allOf": ms})
}

func obj(req []string, props map[string]schema.Schema) schema.Schema {
	inReq := make(map[string]bool, len(req))
	for _, r := range req {
		inReq[r] = true
	}
	s := schema.Object()
	for name, v := range props {
		s = s.WithProperty(name, v, inReq[name])
	}
	return s
}

// objP builds {type:object, properties:{name: v}, required:[name]}.
func objP(name string, v schema.Schema) schema.Schema {
	return obj([]string{name}, map[string]schema.Schema{name: v})
}

func TestCanonicalize_EqualTypesProduceEqualJSON(t *testing.T) {
	tests := []struct {
		name string
		a, b schema.Schema
	}{
		{"oneOf order-insensitive",
			oneOf(prim("integer"), prim("string")),
			oneOf(prim("string"), prim("integer"))},
		{"oneOf dedup",
			oneOf(prim("integer"), prim("integer"), prim("string")),
			oneOf(prim("string"), prim("integer"))},
		{"nullable simple: oneOf spelling == type-array spelling",
			oneOf(prim("string"), prim("null")),
			prim("string", "null")},
		{"type-array order-insensitive",
			prim("string", "null"),
			prim("null", "string")},
		{"singleton union collapses to the bare type",
			oneOf(prim("integer")),
			prim("integer")},
		{"nested unions flatten",
			oneOf(oneOf(prim("integer"), prim("string")), prim("boolean")),
			prim("boolean", "integer", "string")},
		{"property values are canonicalized",
			objP("x", oneOf(prim("integer"), prim("string"))),
			objP("x", prim("string", "integer"))},
		{"required is sorted and deduped",
			// Built via Load: the builders dedup required themselves, and this case
			// must present a genuinely duplicated, unsorted list to Canonicalize.
			schema.Load(map[string]any{"type": "object", "required": []any{"b", "a", "b"}}),
			schema.Load(map[string]any{"type": "object", "required": []any{"a", "b"}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ja := canonJSON(t, tt.a)
			jb := canonJSON(t, tt.b)
			if ja != jb {
				t.Errorf("canonical forms differ:\n  a: %s\n  b: %s", ja, jb)
			}
		})
	}
}

func TestCanonicalize_DistinctTypesStayDistinct(t *testing.T) {
	a := canonJSON(t, prim("integer"))
	b := canonJSON(t, prim("string"))
	if a == b {
		t.Errorf("integer and string canonicalized identically: %s", a)
	}
	// A nullable object stays a union (object is not a simple type), with the
	// null variant preserved and the object canonicalized.
	nullableObj := oneOf(
		objP("x", oneOf(prim("integer"), prim("integer"))),
		prim("null"),
	)
	got := canonJSON(t, nullableObj)
	// Order of oneOf variants is canonical (sorted by JSON); compare as canonical.
	wantCanon := canonJSON(t, oneOf(
		prim("null"),
		objP("x", prim("integer")),
	))
	if got != wantCanon {
		t.Errorf("nullable object canonical mismatch:\n  got:  %s\n  want: %s", got, wantCanon)
	}
}
