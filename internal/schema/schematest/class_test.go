package schematest

import (
	"reflect"
	"testing"
)

// These tests exercise the unified path-aware Schema API purely from JSON — no
// process object involved — which is the whole point: the class is testable in
// isolation.

const nestedSchema = `{
	"type":"object",
	"properties":{
		"input":{"$ref":"#/$defs/Input"},
		"outputs":{
			"type":"object",
			"properties":{
				"charge":{"$ref":"#/$defs/Charge"}
			},
			"required":["charge"]
		}
	},
	"required":["input","outputs"],
	"$defs":{
		"Input":{
			"type":"object",
			"properties":{
				"user":{"type":"string"},
				"password":{"type":"string","secret":true},
				"retries":{"type":"integer","default":3}
			},
			"required":["user","password"]
		},
		"Charge":{
			"type":"object",
			"properties":{
				"amount":{"type":"integer"},
				"token":{"type":"string","secret":true}
			},
			"required":["amount"]
		}
	}
}`

func TestValidateAtSubpathResolvesRootDefs(t *testing.T) {
	sc := mustParse(t, nestedSchema)

	// Validate a value at outputs.charge — the subschema is a $ref into root $defs,
	// which only resolves because Infer carries the root defs into the sub-schema.
	got, err := sc.ValidateAt("outputs.charge", mustData(t, `{"amount":10,"token":"sk","junk":1}`))
	if err != nil {
		t.Fatalf("ValidateAt: %v", err)
	}
	want := mustData(t, `{"amount":10,"token":"sk"}`) // "junk" stripped
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ValidateAt normalized = %v, want %v", got, want)
	}

	// Defaults fill at a subpath too.
	gotIn, err := sc.ValidateAt("input", mustData(t, `{"user":"al","password":"p"}`))
	if err != nil {
		t.Fatalf("ValidateAt input: %v", err)
	}
	if gotIn.(map[string]any)["retries"] != float64(3) {
		t.Errorf("expected default retries=3, got %v", gotIn)
	}
}

func TestValidateAtRejectsBadSubpathValue(t *testing.T) {
	sc := mustParse(t, nestedSchema)
	if _, err := sc.ValidateAt("outputs.charge", mustData(t, `{"token":"x"}`)); err == nil {
		t.Error("expected error: amount is required at outputs.charge")
	}
	if _, err := sc.ValidateAt("input.retries", mustData(t, `"not-an-int"`)); err == nil {
		t.Error("expected error: retries must be integer")
	}
}

func TestSecretAtPath(t *testing.T) {
	sc := mustParse(t, nestedSchema)
	cases := []struct {
		path string
		want bool
	}{
		{"input.password", true},
		{"input.user", false},
		{"outputs.charge.token", true},
		{"outputs.charge.amount", false},
		{"outputs.charge", false}, // the object itself is not secret
	}
	for _, c := range cases {
		if got := sc.SecretAt(c.path); got != c.want {
			t.Errorf("SecretAt(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestRedactAndCollectSecretsWholeContext(t *testing.T) {
	sc := mustParse(t, nestedSchema)
	data := mustData(t, `{
		"input":{"user":"al","password":"hunter2","retries":3},
		"outputs":{"charge":{"amount":10,"token":"sk-live-xyz"}}
	}`)

	secrets := sc.CollectSecrets(data)
	if !contains(secrets, "hunter2") || !contains(secrets, "sk-live-xyz") {
		t.Errorf("CollectSecrets missed a secret: %v", secrets)
	}

	red := sc.Redact(data).(map[string]any)
	in := red["input"].(map[string]any)
	if in["password"] != "***" || in["user"] != "al" {
		t.Errorf("Redact input wrong: %v", in)
	}
	charge := red["outputs"].(map[string]any)["charge"].(map[string]any)
	if charge["token"] != "***" || charge["amount"] != float64(10) {
		t.Errorf("Redact charge wrong: %v", charge)
	}
}

// A property that is optional but has a default is guaranteed present after
// validation (conformObject fills the default), so navigation and inference must
// treat it as non-nullable — usable directly in a comparison. An optional property
// with no default stays nullable.
func TestOptionalWithDefaultIsNonNullable(t *testing.T) {
	sc := mustParse(t, `{
		"type":"object",
		"properties":{
			"input":{
				"type":"object",
				"properties":{
					"max_attempts":{"type":"integer","default":20},
					"limit":{"type":"integer"}
				}
			}
		},
		"required":["input"]
	}`)

	// Defaulted optional → non-nullable, and usable in a comparison.
	def, err := sc.At("input.max_attempts")
	if err != nil {
		t.Fatalf("At(input.max_attempts): %v", err)
	}
	if def.HasNull() {
		t.Errorf("input.max_attempts has a default, want non-nullable, got type %v", def.Type())
	}
	if _, err := sc.Infer("input.max_attempts >= 1"); err != nil {
		t.Errorf("Infer(comparison on defaulted optional) unexpected error: %v", err)
	}

	// Optional with no default → still nullable, so a bare comparison must fail.
	opt, err := sc.At("input.limit")
	if err != nil {
		t.Fatalf("At(input.limit): %v", err)
	}
	if !opt.HasNull() {
		t.Errorf("input.limit has no default, want nullable, got type %v", opt.Type())
	}
	if _, err := sc.Infer("input.limit >= 1"); err == nil {
		t.Error("Infer(comparison on non-defaulted optional) expected a non-nullable error, got nil")
	}
}

func TestInferChainsThroughDefs(t *testing.T) {
	sc := mustParse(t, nestedSchema)
	// Infer to a subpath, then validate against the returned sub-schema directly —
	// proves the sub-schema is self-sufficient (carries root defs).
	sub, err := sc.At("outputs.charge")
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if _, err := sub.Validate(mustData(t, `{"amount":5}`)); err != nil {
		t.Errorf("sub.Validate: %v", err)
	}
	if _, err := sub.Validate(mustData(t, `{"token":"x"}`)); err == nil {
		t.Error("expected required amount error on sub-schema")
	}
}
