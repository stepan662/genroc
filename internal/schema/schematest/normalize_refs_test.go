package schematest

import "testing"

func TestNormalize_items(t *testing.T) {
	assertJSON(t, normalize(t, `{
		"type": "array",
		"items": {"$ref": "#/$defs/Item"},
		"$defs": {
			"Item":   {"type": "string"},
			"Unused": {"type": "integer"}
		}
	}`), `{
		"$defs": {"Item": {"type": "string"}},
		"items": {"$ref": "#/$defs/Item"},
		"type": "array"
	}`)
}

func TestParse_rejectPrefixItems(t *testing.T) {
	assertParseErr(t,
		`{"type":"array","prefixItems":[{"type":"integer"},{"type":"string"}]}`,
		`unsupported schema keyword "prefixItems"`,
	)
}

func TestNormalize_oneOf(t *testing.T) {
	assertJSON(t, normalize(t, `{
		"oneOf": [
			{"$ref": "#/$defs/A"},
			{"$ref": "#/$defs/B"}
		],
		"$defs": {
			"A":      {"type": "string"},
			"B":      {"type": "integer"},
			"Unused": {"type": "boolean"}
		}
	}`), `{
		"$defs": {
			"A": {"type": "string"},
			"B": {"type": "integer"}
		},
		"oneOf": [
			{"$ref": "#/$defs/A"},
			{"$ref": "#/$defs/B"}
		]
	}`)
}

func TestNormalize_anyOf(t *testing.T) {
	assertJSON(t, normalize(t, `{
		"anyOf": [{"$ref": "#/$defs/A"}, {"type": "null"}],
		"$defs": {
			"A":      {"type": "string"},
			"Unused": {"type": "boolean"}
		}
	}`), `{
		"$defs": {"A": {"type": "string"}},
		"anyOf": [{"$ref": "#/$defs/A"}, {"type": "null"}]
	}`)
}

func TestParse_rejectAllOf(t *testing.T) {
	assertParseErr(t,
		`{"allOf":[{"type":"integer"},{"minimum":0}]}`,
		`unsupported schema keyword "allOf"`,
	)
}

func TestParse_rejectNot(t *testing.T) {
	assertParseErr(t,
		`{"not":{"type":"string"}}`,
		`unsupported schema keyword "not"`,
	)
}

func TestParse_additionalPropertiesTypedFormOnly(t *testing.T) {
	// The typed (schema) form is accepted...
	mustParse(t, `{"type":"object","additionalProperties":{"type":"string"}}`)
	// ...but the boolean form is rejected — genroc never accepts untyped extras (true),
	// and "closed" is expressed by absence, not an explicit false.
	assertParseErr(t,
		`{"type":"object","additionalProperties":true}`,
		`additionalProperties must be a schema object; the boolean form is not supported`,
	)
	assertParseErr(t,
		`{"type":"object","additionalProperties":false}`,
		`additionalProperties must be a schema object; the boolean form is not supported`,
	)
}

func TestParse_rejectIfThenElse(t *testing.T) {
	assertParseErr(t,
		`{"if":{"type":"string"},"then":{"type":"integer"}}`,
		"", // map iteration order is non-deterministic; just check error is non-nil
	)
}

func TestNormalize_rejectExternalRef(t *testing.T) {
	assertErr(t,
		`{"$ref": "https://example.com/schema"}`,
		`unsupported $ref "https://example.com/schema": must be "#/$defs/<name>" or "#<anchor>"`,
	)
}

func TestNormalize_rejectRelativePointer(t *testing.T) {
	assertErr(t,
		`{"$ref": "#/properties/foo"}`,
		`unsupported $ref "#/properties/foo": must be "#/$defs/<name>" or "#<anchor>"`,
	)
}

func TestNormalize_rejectUnknownAnchor(t *testing.T) {
	assertErr(t,
		`{"$ref": "#no-such-anchor", "$defs": {}}`,
		`unresolved $ref "#no-such-anchor": anchor "no-such-anchor" is not defined in the root resource`,
	)
}

func TestNormalize_rejectShortPathWithoutID(t *testing.T) {
	// "#/$defs/Item" must match a root-level definition exactly.
	// Without a $id boundary, short-name suffix matching is not applied.
	assertErr(t,
		`{
			"properties": {"x": {"$ref": "#/$defs/Item"}},
			"$defs": {
				"Order": {
					"$defs": {
						"Item": {"type": "string"}
					}
				}
			}
		}`,
		`unresolved $ref "#/$defs/Item": no matching definition`,
	)
}

func TestNormalize_shortPathWithIDScope(t *testing.T) {
	// Inside a $id sub-resource "#/$defs/Item" resolves relative to that resource.
	out := normalize(t, `{
		"properties": {"order": {"$ref": "#/$defs/Order"}},
		"$defs": {
			"Order": {
				"$id": "urn:order",
				"type": "object",
				"properties": {"item": {"$ref": "#/$defs/Item"}},
				"$defs": {
					"Item": {"type": "string"}
				}
			}
		}
	}`)
	assertJSON(t, out, `{
		"$defs": {
			"Item":  {"type": "string"},
			"Order": {
				"type": "object",
				"properties": {"item": {"$ref": "#/$defs/Item"}}
			}
		},
		"properties": {"order": {"$ref": "#/$defs/Order"}}
	}`)
}
