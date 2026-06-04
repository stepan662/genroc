package schematest

import "testing"

// patternProperties, propertyNames, and contains are not in the supported keyword set.
// Schemas containing them must be rejected at parse time.

func TestParse_rejectPatternProperties(t *testing.T) {
	assertParseErr(t,
		`{"type":"object","patternProperties":{"^num_":{"type":"number"}}}`,
		`unsupported schema keyword "patternProperties"`,
	)
}

func TestParse_rejectPropertyNames(t *testing.T) {
	assertParseErr(t,
		`{"type":"object","propertyNames":{"type":"string"}}`,
		`unsupported schema keyword "propertyNames"`,
	)
}

func TestParse_rejectContains(t *testing.T) {
	assertParseErr(t,
		`{"type":"array","contains":{"type":"string"}}`,
		`unsupported schema keyword "contains"`,
	)
}
