package expressiontest

import (
	"testing"
)

// secretContextJSON has secret scalars (config.api_key, self.result.token), a
// whole secret object (box), and non-secret siblings — to probe taint reliably.
const secretContextJSON = `{
	"type": "object",
	"properties": {
		"config": {
			"type": "object",
			"properties": {
				"api_key": { "type": "string", "secret": true },
				"url":     { "type": "string" }
			},
			"required": ["api_key", "url"]
		},
		"self": {
			"type": "object",
			"properties": {
				"result": {
					"type": "object",
					"properties": {
						"token": { "type": "string", "secret": true },
						"name":  { "type": "string" }
					},
					"required": ["token", "name"]
				}
			},
			"required": ["result"]
		},
		"box": {
			"type": "object",
			"secret": true,
			"properties": { "inner": { "type": "string" } },
			"required": ["inner"]
		},
		"input": { "type": "string" }
	},
	"required": ["config", "self", "box", "input"]
}`

// Every one of these reads a secret somewhere — must taint regardless of what
// the expression then does with it.
func TestReferencesSecret_Tainted(t *testing.T) {
	c := ctx(t, secretContextJSON)
	secretRefAll(t, c, true, []secretCase{
		{"secret_field", `config.api_key`},
		{"secret_concatenated", `config.api_key + "x"`},
		{"secret_appended_to_a_prefix", `"Bearer " + config.api_key`},
		{"secret_with_a_default", `config.api_key ?? "default"`},
		{"secret_only_in_a_branch", `config.url == "x" ? config.api_key : "fallback"`},
		{"secret_as_a_comparison_operand", `config.url == config.api_key`},
		{"secret_only_in_the_condition", `config.api_key == "" ? "a" : "b"`},
		{"secret_under_self_result", `self.result.token`},
		{"field_inside_a_secret_object", `box.inner`},
		{"the_secret_object_itself", `box`},
	})
}

// None of these touch a secret.
func TestReferencesSecret_NotTainted(t *testing.T) {
	c := ctx(t, secretContextJSON)
	secretRefAll(t, c, false, []secretCase{
		{"plain_field", `config.url`},
		{"plain_root", `input`},
		{"plain_field_concatenated", `"Bearer " + config.url`},
		{"plain_sibling_of_a_secret", `self.result.name`},
		// object whose sub-field is secret, but the object node itself is not
		{"object_with_a_secret_sub_field", `self.result`},
		{"plain_comparison", `config.url == "x"`},
		{"plain_field_with_a_default", `config.url ?? "default"`},
		{"string_literal", `"static text"`},
		{"number_literal", `42`},
	})
}
