package model

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func TestShape_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"single expression", `"$: self.result"`, false},
		{"plain string literal", `"hello"`, false},
		{"nested object", `{"data": {"flag": "$: self.result.charged"}}`, false},
		{"empty object", `{}`, false},
		// The widened grammar accepts every JSON value structurally; type-checking is
		// deferred to inference, so none of these are rejected at unmarshal.
		{"bare number", `5`, false},
		{"bare bool", `true`, false},
		{"bare null", `null`, false},
		{"array", `["$: a", "$: b"]`, false},
		{"mixed array", `[1, true, "$: a", null]`, false},
		{"nested number", `{"n": 5}`, false},
		{"nested array", `{"tags": ["$: a"]}`, false},
		{"null leaf", `{"x": null}`, false},
		{"deeply nested", `[{"a": [1, "$: b"]}]`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s Shape
			err := json.Unmarshal([]byte(c.in), &s)
			if c.wantErr != (err != nil) {
				t.Fatalf("Unmarshal(%s): err=%v, wantErr=%v", c.in, err, c.wantErr)
			}
		})
	}
}

func TestShape_RoundTrip(t *testing.T) {
	in := `{"data":{"flag":"$: self.result.charged"},"id":"$: self.result.id"}`
	var s Shape
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	// Compare structurally (object key order is not stable).
	var a, b any
	json.Unmarshal([]byte(in), &a)
	json.Unmarshal(out, &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("round-trip mismatch:\n in:  %s\n out: %s", in, out)
	}
}

func TestShape_StringsAndPresent(t *testing.T) {
	var nilShape *Shape
	if nilShape.Present() {
		t.Error("nil shape should not be Present")
	}
	if nilShape.Strings() != nil {
		t.Error("nil shape Strings should be nil")
	}

	var s Shape
	if err := json.Unmarshal([]byte(`{"a":"$: x","b":{"c":"$: y"}}`), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Present() {
		t.Error("shape should be Present")
	}
	got := s.Strings()
	sort.Strings(got)
	want := []string{"$: x", "$: y"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Strings: got %v want %v", got, want)
	}

	// Strings must descend into arrays (scalars/null contribute no strings), or
	// output-dependency and secret-taint analysis would silently miss array leaves.
	var arr Shape
	if err := json.Unmarshal([]byte(`{"a":"$: x","b":["$: y",5,"$: z",null],"c":[{"d":"$: w"}]}`), &arr); err != nil {
		t.Fatal(err)
	}
	gotArr := arr.Strings()
	sort.Strings(gotArr)
	wantArr := []string{"$: w", "$: x", "$: y", "$: z"}
	if !reflect.DeepEqual(gotArr, wantArr) {
		t.Errorf("Strings over arrays: got %v want %v", gotArr, wantArr)
	}
}
