package schematest

import "testing"

func TestIsSubset_arrays(t *testing.T) {
	cases := []struct {
		name  string
		sub   string
		super string
		want  bool
	}{
		{
			"integer items ⊆ number items",
			`{"type":"array","items":{"type":"integer"}}`,
			`{"type":"array","items":{"type":"number"}}`,
			true,
		},
		{
			"number items ⊄ integer items",
			`{"type":"array","items":{"type":"number"}}`,
			`{"type":"array","items":{"type":"integer"}}`,
			false,
		},
		{
			"sub unconstrained items, super constrains",
			`{"type":"array"}`,
			`{"type":"array","items":{"type":"integer"}}`,
			false,
		},
		{
			"sub constrains items, super unconstrained",
			`{"type":"array","items":{"type":"integer"}}`,
			`{"type":"array"}`,
			true,
		},
		{
			"both unconstrained arrays",
			`{"type":"array"}`,
			`{"type":"array"}`,
			true,
		},
		{
			"same item type",
			`{"type":"array","items":{"type":"string"}}`,
			`{"type":"array","items":{"type":"string"}}`,
			true,
		},
		{
			"object items with compatible properties",
			`{"type":"array","items":{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}}`,
			`{"type":"array","items":{"type":"object","properties":{"id":{"type":"number"}},"required":["id"]}}`,
			true,
		},
		{
			// A literal `[]` infers to array/maxItems:0; it holds no element that could
			// violate the super's item type, so it is a subset of any array<T>.
			"provably-empty array ⊆ typed array",
			`{"type":"array","maxItems":0}`,
			`{"type":"array","items":{"type":"string"}}`,
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSubset(t, tc.sub, tc.super, tc.want)
		})
	}
}
