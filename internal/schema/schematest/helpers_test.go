package schematest

import (
	"encoding/json"
	"testing"

	"genroc/internal/schema"

	"github.com/xeipuuv/gojsonschema"
)

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// normalize parses a JSON schema and normalizes it, failing the test on error.
func normalize(t *testing.T, in string) schema.Schema {
	t.Helper()
	raw, err := schema.Parse([]byte(in))
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	out, err := raw.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return out
}

func assertParseErr(t *testing.T, in string, wantMsg string) {
	t.Helper()
	_, err := schema.Parse([]byte(in))
	if err == nil {
		t.Fatalf("expected parse error %q, got nil", wantMsg)
	}
	if wantMsg != "" && err.Error() != wantMsg {
		t.Errorf("error message mismatch\ngot:  %q\nwant: %q", err.Error(), wantMsg)
	}
}

func assertErr(t *testing.T, in string, wantMsg string) {
	t.Helper()
	raw, err := schema.Parse([]byte(in))
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	_, err = raw.Normalize()
	if err == nil {
		t.Fatalf("expected error %q, got nil", wantMsg)
	}
	if err.Error() != wantMsg {
		t.Errorf("error message mismatch\ngot:  %q\nwant: %q", err.Error(), wantMsg)
	}
}

func assertJSON(t *testing.T, got any, want string) {
	t.Helper()
	// Round-trip got through map[string]any so key order matches want (both sort alphabetically).
	gotRaw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	var gotParsed, wantParsed any
	if err := json.Unmarshal(gotRaw, &gotParsed); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantParsed); err != nil {
		t.Fatalf("invalid expected JSON: %v", err)
	}
	gotBytes, _ := json.MarshalIndent(gotParsed, "", "  ")
	wantBytes, _ := json.MarshalIndent(wantParsed, "", "  ")
	if string(gotBytes) != string(wantBytes) {
		t.Errorf("output mismatch\ngot:\n%s\n\nwant:\n%s", gotBytes, wantBytes)
	}
}

func assertSemanticEquivalence(t *testing.T, src string, valid []any, invalid []any) {
	t.Helper()

	original, err := schema.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	normalized, err := original.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	origSchema, err := gojsonschema.NewSchema(gojsonschema.NewGoLoader(original))
	if err != nil {
		t.Fatalf("original schema is not a valid JSON Schema: %v", err)
	}
	normSchema, err := gojsonschema.NewSchema(gojsonschema.NewGoLoader(normalized))
	if err != nil {
		t.Fatalf("normalized schema is not a valid JSON Schema: %v", err)
	}

	check := func(data any, wantValid bool) {
		t.Helper()
		dl := gojsonschema.NewGoLoader(data)

		origRes, err := origSchema.Validate(dl)
		if err != nil {
			t.Fatalf("validate against original: %v", err)
		}
		normRes, err := normSchema.Validate(dl)
		if err != nil {
			t.Fatalf("validate against normalized: %v", err)
		}

		if origRes.Valid() != wantValid {
			t.Errorf("original schema: expected valid=%v for %#v (errors: %v)", wantValid, data, origRes.Errors())
		}
		if normRes.Valid() != origRes.Valid() {
			t.Errorf("normalization changed validity for %#v: original=%v normalized=%v\n  original errors:   %v\n  normalized errors: %v",
				data, origRes.Valid(), normRes.Valid(), origRes.Errors(), normRes.Errors())
		}
	}

	for _, d := range valid {
		check(d, true)
	}
	for _, d := range invalid {
		check(d, false)
	}
}
