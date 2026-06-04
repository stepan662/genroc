package schematest

import "testing"

func TestIsSubset_equivalent(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{
			"single-element allOf is transparent",
			`{"type":"integer"}`,
			`{"allOf":[{"type":"integer"}]}`,
			true,
		},
		{
			"anyOf and oneOf are equivalent for disjoint variants",
			`{"anyOf":[{"type":"string"},{"type":"integer"}]}`,
			`{"oneOf":[{"type":"string"},{"type":"integer"}]}`,
			true,
		},
		{
			"identical schemas are equivalent",
			`{"type":"string","minLength":1,"maxLength":10}`,
			`{"type":"string","minLength":1,"maxLength":10}`,
			true,
		},
		{
			"number and integer are not equivalent — number is wider",
			`{"type":"number"}`,
			`{"type":"integer"}`,
			false,
		},
		{
			"enum supersets are not equivalent",
			`{"enum":[1,2,3]}`,
			`{"enum":[1,2]}`,
			false,
		},
		{
			"anyOf with integer vs number are not equivalent — integer⊆number but not vice versa",
			`{"anyOf":[{"type":"string"},{"type":"integer"}]}`,
			`{"anyOf":[{"type":"string"},{"type":"number"}]}`,
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertEquivalent(t, tc.a, tc.b, tc.want)
		})
	}
}

func TestIsSubset_equivalent_recursive(t *testing.T) {
	// A: Node.next → Node (direct self-reference)
	directList := `{
		"$defs": {
			"Node": {
				"type": "object",
				"properties": {
					"value": {"type": "integer"},
					"next":  {"$ref": "#/$defs/Node"}
				},
				"required": ["value"]
			}
		},
		"$ref": "#/$defs/Node"
	}`

	// B: same linked-list shape but the $defs key is renamed to ListNode.
	renamedList := `{
		"$defs": {
			"ListNode": {
				"type": "object",
				"properties": {
					"value": {"type": "integer"},
					"next":  {"$ref": "#/$defs/ListNode"}
				},
				"required": ["value"]
			}
		},
		"$ref": "#/$defs/ListNode"
	}`

	// C: same linked-list shape but recursion goes through an extra Wrapper hop:
	//    Node.next → Wrapper, Wrapper.next → Node.
	wrappedList := `{
		"$defs": {
			"Node": {
				"type": "object",
				"properties": {
					"value": {"type": "integer"},
					"next":  {"$ref": "#/$defs/Wrapper"}
				},
				"required": ["value"]
			},
			"Wrapper": {
				"type": "object",
				"properties": {
					"value": {"type": "integer"},
					"next":  {"$ref": "#/$defs/Node"}
				},
				"required": ["value"]
			}
		},
		"$ref": "#/$defs/Node"
	}`

	// D: same shape as A but value is number — strictly wider, so not equivalent.
	numberList := `{
		"$defs": {
			"Node": {
				"type": "object",
				"properties": {
					"value": {"type": "number"},
					"next":  {"$ref": "#/$defs/Node"}
				},
				"required": ["value"]
			}
		},
		"$ref": "#/$defs/Node"
	}`

	t.Run("same structure different def name", func(t *testing.T) {
		assertEquivalent(t, directList, renamedList, true)
	})
	t.Run("direct recursion vs one-level-deeper recursion (Node→Wrapper→Node)", func(t *testing.T) {
		assertEquivalent(t, directList, wrappedList, true)
	})
	t.Run("integer value vs number value — not equivalent", func(t *testing.T) {
		assertEquivalent(t, directList, numberList, false)
	})
}
