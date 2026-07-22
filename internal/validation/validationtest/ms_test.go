package validationtest

import (
	"strings"
	"testing"
)

func delayMsDef(ms string) string {
	return `{
		"name": "delay-ms",
		"input_schema": {"type":"object","properties":{"n":{"type":"integer"},"tags":{"type":"array","items":{"type":"string"}}},"required":["n","tags"]},
		"tasks": [
			{"id": "wait", "action": {"type": "delay", "ms": "` + ms + `"}, "switch": "end"}
		]
	}`
}

// A delay ms is now type-checked at registration (previously it was only evaluated at
// runtime, so a bad ms failed when the process reached the delay).
func TestGenerate_DelayMs_TypeChecked(t *testing.T) {
	// Accepted: a literal (stringifies), and a numeric expression.
	for _, ms := range []string{"30000", "$: input.n * 2"} {
		if err := runGenerateErr(t, delayMsDef(ms)); err != nil {
			t.Errorf("ms %q should be accepted: %v", ms, err)
		}
	}

	// Rejected: an array-typed expression can never be a millisecond count.
	if err := runGenerateErr(t, delayMsDef("$: input.tags")); err == nil || !strings.Contains(err.Error(), "number of milliseconds") {
		t.Errorf("array ms = %v; want a 'number of milliseconds' rejection", err)
	}

	// Rejected: an undefined root reference (previously slipped to runtime).
	if err := runGenerateErr(t, delayMsDef("$: input.nope")); err == nil {
		t.Error("undefined reference in ms should be rejected at registration")
	}
}
