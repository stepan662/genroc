package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Fault is a terminal error: a machine-readable code and a human-readable message,
// both static literals. Used by both `raise` and `panic` — they carry the same thing
// for the same reasons and differ only in what they do, so one type serves both and
// there is no pair of near-identical structs to drift apart. The distinction lives in
// the field name at the use site (Raise / Panic), which is where a reader looks anyway.
//
// Both fields are literals, never expressions: a computed code would make a
// definition's raise set uncomputable and error_code unqueryable, and a computed
// message would smuggle data across a process boundary that this design exists to
// keep closed. See docs/child-error-handling.md §2.1 and R2.
type Fault struct {
	Code    string `json:"code"    validate:"required" description:"Error code, lower_snake_case, no dots (dots are reserved for engine-produced codes). A literal — never an expression."`
	Message string `json:"message" validate:"required" description:"Human-readable message explaining the condition. A literal string — never an expression."`
}

// SwitchCase is a single entry in a Task's switch list: a boolean expression
// evaluated against the process context (and this task's own output as "self"),
// and what to do when the expression is true.
// An empty Case means "catch-all" — it matches unconditionally and must be last.
//
// Exactly one of Goto, Raise and Panic is set (enforced at registration, not on
// decode, so the rejection message can name the task and case index):
//   - Goto routes, storing the raw wire value: "end", "next", or "$task-id".
//   - Raise concludes the process as 'raised' — an anticipated condition its parent
//     may react to by naming the code.
//   - Panic fails the process — a defect nothing may react to, ever.
type SwitchCase struct {
	Case  string
	Goto  string
	Raise *Fault
	Panic *Fault
}

// Terminates reports whether the case ends the process rather than routing onward.
func (c SwitchCase) Terminates() bool {
	return c.Goto == GotoEnd || c.Raise != nil || c.Panic != nil
}

// SwitchMap is an ordered list of SwitchCase entries. It marshals as a plain
// JSON object so the wire format is readable:
//
//	{"self.paid == true": "ship", "self.paid == false": "refund"}
//
// JSON object key order is preserved on unmarshal by reading tokens sequentially
// rather than decoding into a map.
type SwitchMap []SwitchCase

// switchWireCase is the JSON wire form of a SwitchCase, shared by SwitchMap's
// MarshalJSON and UnmarshalJSON so the tags can't drift. omitempty is ignored on
// decode, so the same type serves both directions.
type switchWireCase struct {
	Case  string `json:"case,omitempty"`
	Goto  string `json:"goto,omitempty"`
	Raise *Fault `json:"raise,omitempty"`
	Panic *Fault `json:"panic,omitempty"`
}

func (s SwitchMap) MarshalJSON() ([]byte, error) {
	items := make([]switchWireCase, len(s))
	for i, c := range s {
		items[i] = switchWireCase{Case: c.Case, Goto: c.Goto, Raise: c.Raise, Panic: c.Panic}
	}
	return json.Marshal(items)
}

func (s *SwitchMap) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = nil
		return nil
	}
	// Scalar shorthand: "next", "end", or "$task-id" — desugars to a single catch-all.
	if len(data) > 0 && data[0] == '"' {
		var v string
		if err := json.Unmarshal(data, &v); err != nil {
			return fmt.Errorf("switch: %w", err)
		}
		if v != GotoEnd && v != GotoNext && !strings.HasPrefix(v, "$") {
			return fmt.Errorf("switch: %q must be \"next\", \"end\", or a task reference like \"$task-id\"", v)
		}
		*s = SwitchMap{{Goto: v}}
		return nil
	}
	// Array form.
	var items []switchWireCase
	if err := json.Unmarshal(data, &items); err != nil {
		return fmt.Errorf("switch: %w", err)
	}
	*s = (*s)[:0]
	for _, item := range items {
		// Only the *shape* of a goto is checked here. Which of goto/raise/panic a case
		// must carry (exactly one — R3) is a registration rule, not a decoding one, so
		// that its rejection can name the task and the case index instead of surfacing
		// as an opaque JSON error.
		if item.Goto != "" && item.Goto != GotoEnd && item.Goto != GotoNext && !strings.HasPrefix(item.Goto, "$") {
			return fmt.Errorf("switch: goto %q must be \"end\", \"next\", or a task reference like \"$task-id\"", item.Goto)
		}
		*s = append(*s, SwitchCase{Case: item.Case, Goto: item.Goto, Raise: item.Raise, Panic: item.Panic})
	}
	return nil
}

// JSONSchemaBytes returns the JSON Schema for SwitchMap so that OpenAPI
// reflection produces the correct schema for its wire format.
func (SwitchMap) JSONSchemaBytes() ([]byte, error) {
	return []byte(`{
		"oneOf": [
			{
				"type": "string",
				"description": "Shorthand for a single unconditional route. \"next\" advances to the next task (not valid on the last task), \"end\" terminates the instance, \"$task-id\" jumps to a named task."
			},
			{
				"type": "array",
				"description": "Ordered routing rules evaluated after the call. Cases are evaluated in order; first match wins. The last entry must be a catch-all (omit 'case'). Each case sets exactly one of 'goto', 'raise' or 'panic'.",
				"items": {
					"type": "object",
					"properties": {
						"case": {"type": "string", "description": "Boolean expression. Omit for a catch-all; must be last."},
						"goto": {"type": "string", "description": "\"end\" to terminate, \"next\" to advance, or \"$task-id\" to jump to a task."},
						"raise": {"$ref": "#/$defs/ModelFault", "description": "Terminate as 'raised' with this code and message — an anticipated condition a parent process may react to by naming the code in its on_error."},
						"panic": {"$ref": "#/$defs/ModelFault", "description": "Terminate as 'failed' with this code and message — a defect. Nothing can catch a panic; the code exists to classify the failure, not to branch on it."}
					},
					"additionalProperties": false
				},
				"minItems": 1
			}
		]
	}`), nil
}

// Shape is a templated value used by the data-shaping fields (action input, output,
// process output). It is recursively either a string expression
// (a {{ }} template — literal text, a single expression preserving type, or a
// mixed string) or an object whose values are themselves Shapes:
//
//	type Shape = string | Record<string, Shape>
//
// Arrays and non-string literals are not allowed structurally — produce them
// from an expression at a string leaf instead (e.g. "{{ 5 }}", "{{ [a, b] }}").
// The authoring structure is string|object, but the evaluated/inferred value can
// be any shape, because a string leaf may evaluate to any type.
type Shape struct {
	Raw any // string | map[string]any (recursively)
}

func (s *Shape) UnmarshalJSON(b []byte) error {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if err := checkShape(raw); err != nil {
		return fmt.Errorf("shape: %w", err)
	}
	s.Raw = raw
	return nil
}

func (s Shape) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.Raw)
}

// Present reports whether the shape carries a value; nil-safe so callers can skip a
// separate nil check.
func (s *Shape) Present() bool {
	return s != nil && s.Raw != nil
}

// Strings returns every string leaf in the shape, used to collect outputs.<id>
// references for the output-dependency graph.
func (s *Shape) Strings() []string {
	if s == nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(n any) {
		switch v := n.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			for _, c := range v {
				walk(c)
			}
		}
	}
	walk(s.Raw)
	return out
}

// JSONSchemaBytes exposes the recursive Shape schema (string | object of Shape) for
// OpenAPI reflection. The self-reference uses swaggest's generated def name
// (#/$defs/ModelShape), which the spec builder rewrites to #/components/schemas/ModelShape.
func (Shape) JSONSchemaBytes() ([]byte, error) {
	return []byte(`{
		"oneOf": [
			{"type": "string", "description": "An expression / template ({{ ... }}) or a literal string."},
			{
				"type": "object",
				"description": "Nested object; each value is recursively a Shape.",
				"additionalProperties": {"$ref": "#/$defs/ModelShape"}
			}
		]
	}`), nil
}

// checkShape recursively enforces the string | Record<string, Shape> grammar, rejecting
// arrays and non-string scalar literals.
func checkShape(n any) error {
	switch v := n.(type) {
	case string:
		return nil
	case map[string]any:
		for k, c := range v {
			if err := checkShape(c); err != nil {
				return fmt.Errorf("%q: %w", k, err)
			}
		}
		return nil
	default:
		return fmt.Errorf("must be a string expression or a nested object, got %T", n)
	}
}

// ErrorCase is a single error-routing rule evaluated when a task's call fails.
// Rules are evaluated in order; the first match applies.
// An empty Code list is a catch-all matching any error.
//
// A rule may route (Goto), conclude the process (Raise), or declare the error a defect
// (Panic) — at most one of the three; setting none fails the instance, which is the
// default when a rule exists only to document a code or to cap retries.
type ErrorCase struct {
	Code       []string `json:"code,omitempty"        description:"SQL LIKE patterns matched against the error code. '%' = any chars, '_' = one char. Empty = catch-all. Known codes — REST: http.NNN (e.g. http.500), http.timeout, pre.error, pre.timeout, output.parse, output.invalid; Script: script.N (exit code, e.g. script.1), script.timeout, pre.exec, output.parse; Child process: output.invalid. pre.* codes mean the call never reached the remote. On a child_map/child_list task the codes are instead the literal codes the child processes can raise, and patterns are not allowed. A child that failed is never catchable — convert the failure into a raise inside the child, at the task that understands it."`
	Retries    int      `json:"retries,omitempty"     description:"Number of retries before following goto or failing. 0 = no retries. On only_once:true tasks only pre.* codes (or rules with not_reached:true) may have retries > 0. Not supported on child tasks — retry inside the child, then raise."`
	Goto       string   `json:"goto,omitempty"        description:"Task to route to when retries are exhausted. '$task-id' or 'end'. Omit to fail the instance."`
	Raise      *Fault   `json:"raise,omitempty"       description:"Terminate as 'raised' with this code and message instead of routing — an anticipated condition a parent process may react to. Mutually exclusive with goto and panic."`
	Panic      *Fault   `json:"panic,omitempty"       description:"Terminate as 'failed' with this code and message instead of routing — a defect. Nothing can catch a panic; the code exists to classify the failure, not to branch on it. Mutually exclusive with goto and raise."`
	NotReached *bool    `json:"not_reached,omitempty" description:"Assert that this error code means the remote call was never reached. When true, retries are allowed even on only_once:true tasks. Omit to use the engine's default classification (pre.* = not reached, everything else = potentially reached)."`
}

// errorCaseWire is the JSON wire form of an ErrorCase, shared by its MarshalJSON and
// UnmarshalJSON so the tags stay in lockstep.
type errorCaseWire struct {
	Code       []string `json:"code,omitempty"`
	Retries    int      `json:"retries,omitempty"`
	Goto       string   `json:"goto,omitempty"`
	Raise      *Fault   `json:"raise,omitempty"`
	Panic      *Fault   `json:"panic,omitempty"`
	NotReached *bool    `json:"not_reached,omitempty"`
}

func (e ErrorCase) MarshalJSON() ([]byte, error) {
	w := errorCaseWire{Code: e.Code, Retries: e.Retries, Raise: e.Raise, Panic: e.Panic, NotReached: e.NotReached}
	if e.Goto != "" {
		if e.Goto == GotoEnd {
			w.Goto = "end"
		} else {
			w.Goto = "$" + e.Goto
		}
	}
	return json.Marshal(w)
}

func (e *ErrorCase) UnmarshalJSON(data []byte) error {
	var w errorCaseWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	e.Code = w.Code
	e.Retries = w.Retries
	e.Raise = w.Raise
	e.Panic = w.Panic
	e.NotReached = w.NotReached
	if w.Goto == "" {
		e.Goto = ""
	} else if w.Goto == "end" {
		e.Goto = GotoEnd
	} else if strings.HasPrefix(w.Goto, "$") {
		e.Goto = w.Goto[1:]
	} else {
		return fmt.Errorf("on_error: goto %q must be \"end\" or a task reference like \"$task-id\"", w.Goto)
	}
	return nil
}

// Terminates reports whether the rule ends the process rather than routing onward.
// A rule with none of goto/raise/panic also ends it — by failing — but that is the
// engine's generic failure, not an authored terminal clause.
func (e ErrorCase) Terminates() bool {
	return e.Goto == GotoEnd || e.Raise != nil || e.Panic != nil
}
