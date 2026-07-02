package schematest

import (
	"testing"

	"genroc/internal/schema"
)

func TestJoin(t *testing.T) {
	tests := []struct {
		name string
		a, b schema.Schema
		want schema.Schema
	}{
		{"identical scalars", prim("integer"), prim("integer"), prim("integer")},
		{"distinct scalars union", prim("integer"), prim("string"), prim("integer", "string")},
		{"scalar with null", prim("integer"), prim("null"), prim("integer", "null")},
		{"nullable absorbs", prim("integer", "null"), prim("integer"), prim("integer", "null")},
		{
			"objects merge per key (widening field)",
			objP("x", prim("integer")),
			objP("x", prim("string")),
			objP("x", prim("integer", "string")),
		},
		{
			"object key on one side becomes nullable and optional",
			obj([]string{"x", "y"}, map[string]schema.Schema{"x": prim("integer"), "y": prim("integer")}),
			objP("x", prim("integer")),
			obj([]string{"x"}, map[string]schema.Schema{"x": prim("integer"), "y": prim("integer", "null")}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.a.Join(tt.b)
			if !got.Equal(tt.want) {
				t.Errorf("Join mismatch:\n  got:  %s\n  want: %s", canonJSON(t, got), canonJSON(t, tt.want))
			}
			// Join is commutative (up to canonical form).
			if !tt.b.Join(tt.a).Equal(tt.want) {
				t.Errorf("Join not commutative for %s", tt.name)
			}
			// Idempotent: joining the result with an input doesn't grow it.
			if !got.Join(tt.a).Equal(got) {
				t.Errorf("Join(got, a) != got for %s", tt.name)
			}
		})
	}
}

func TestEqual_OrderAndSpellingInsensitive(t *testing.T) {
	if !oneOf(prim("integer"), prim("string")).Equal(prim("string", "integer")) {
		t.Error("oneOf and type-array spellings should be Equal")
	}
	if prim("integer").Equal(prim("string")) {
		t.Error("distinct scalars should not be Equal")
	}
	// allOf of objects is order-insensitive and its members are canonicalized.
	a := allOf(objP("x", oneOf(prim("integer"), prim("integer"))), objP("y", prim("string")))
	b := allOf(objP("y", prim("string")), objP("x", prim("integer")))
	if !a.Equal(b) {
		t.Error("allOf of objects should be Equal regardless of order / inner spelling")
	}
}

func TestJoin_ObjectsInsideUnions(t *testing.T) {
	objA := objP("a", prim("integer"))
	objB := objP("b", prim("string"))
	objC := objP("c", prim("boolean"))

	// Joining a union of objects with another object yields the flattened union.
	got := oneOf(objA, objB).Join(objC)
	want := oneOf(objA, objB, objC)
	if !got.Equal(want) {
		t.Errorf("join union+object:\n  got:  %s\n  want: %s", canonJSON(t, got), canonJSON(t, want))
	}

	// Joining two single objects with the same key widens that key (no union of objects).
	got2 := objP("v", prim("integer")).Join(objP("v", prim("string")))
	want2 := objP("v", prim("integer", "string"))
	if !got2.Equal(want2) {
		t.Errorf("join same-key objects:\n  got:  %s\n  want: %s", canonJSON(t, got2), canonJSON(t, want2))
	}
}
