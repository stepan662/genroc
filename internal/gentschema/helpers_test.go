package gentschema_test

import (
	"encoding/json"
	"testing"

	"gent/internal/gentschema"
	"gent/internal/model"
)

func runGenerate(t *testing.T, defJSON string) gentschema.SchemaFile {
	t.Helper()
	var def model.ProcessDefinition
	if err := json.Unmarshal([]byte(defJSON), &def); err != nil {
		t.Fatalf("unmarshal definition: %v", err)
	}
	// Extract version from raw JSON; model no longer carries it.
	var raw map[string]any
	json.Unmarshal([]byte(defJSON), &raw)
	version := 0
	if v, ok := raw["version"].(float64); ok {
		version = int(v)
	}
	out, err := gentschema.Generate(&def, version)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return out
}

func runGenerateErr(t *testing.T, defJSON string) error {
	t.Helper()
	var def model.ProcessDefinition
	if err := json.Unmarshal([]byte(defJSON), &def); err != nil {
		t.Fatalf("unmarshal definition: %v", err)
	}
	var raw map[string]any
	json.Unmarshal([]byte(defJSON), &raw)
	version := 0
	if v, ok := raw["version"].(float64); ok {
		version = int(v)
	}
	_, err := gentschema.Generate(&def, version)
	return err
}

func defKeys(out gentschema.SchemaFile) []string {
	keys := make([]string, 0, len(out.Defs))
	for k := range out.Defs {
		keys = append(keys, k)
	}
	return keys
}

func assertJSON(t *testing.T, got any, wantJSON string) {
	t.Helper()
	// Round-trip through map[string]any so both sides use alphabetical key order.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	var gotParsed, wantParsed any
	if err := json.Unmarshal(raw, &gotParsed); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal([]byte(wantJSON), &wantParsed); err != nil {
		t.Fatalf("wantJSON is not valid JSON: %v\n%s", err, wantJSON)
	}
	ga, _ := json.MarshalIndent(gotParsed, "", "  ")
	gb, _ := json.MarshalIndent(wantParsed, "", "  ")
	if string(ga) != string(gb) {
		t.Errorf("schema mismatch:\n got:  %s\n want: %s", ga, gb)
	}
}
