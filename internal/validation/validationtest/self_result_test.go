package validationtest

import (
	"strings"
	"testing"
)

// A fetch/external action with no result_schema has an untyped raw result. It stays
// routable in the switch (transient), but exporting it through an output is a type
// error — you cannot persist an untyped value. Adding a result_schema types it.
func TestGenerate_OutputOfUntypedResult_Errors(t *testing.T) {
	// Bare self.result in an output, no result_schema → error mentioning result_schema.
	err := runGenerateErr(t, `{
		"name": "p",
		"tasks": [
			{ "id": "call", "action": { "type": "fetch", "url": "http://x" }, "output": "{{ self.result }}", "switch": "end" }
		]
	}`)
	if err == nil {
		t.Fatal("expected an error exporting self.result without a result_schema")
	}
	if !strings.Contains(err.Error(), "result_schema") {
		t.Errorf("error should point at the missing result_schema, got: %v", err)
	}

	// A member access under an output map is the same error.
	if err := runGenerateErr(t, `{"name":"p","tasks":[
		{"id":"call","action":{"type":"fetch","url":"http://x"},"output":{"v":"{{ self.result.x }}"},"switch":"end"}
	]}`); err == nil {
		t.Error("expected an error exporting self.result.x without a result_schema")
	}

	// With a result_schema the output is well-typed and accepted.
	if err := runGenerateErr(t, `{"name":"p","tasks":[
		{"id":"call","action":{"type":"fetch","url":"http://x","result_schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}},"output":"{{ self.result }}","switch":"end"}
	]}`); err != nil {
		t.Errorf("exporting self.result with a result_schema should be valid: %v", err)
	}

	// The switch may still route on the raw result without a schema (transient use).
	if err := runGenerateErr(t, `{"name":"p","tasks":[
		{"id":"call","action":{"type":"fetch","url":"http://x"},"switch":[{"case":"self.result == null","goto":"end"}]}
	]}`); err != nil {
		t.Errorf("routing on self.result in a switch should be allowed without a result_schema: %v", err)
	}
}
