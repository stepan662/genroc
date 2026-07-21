// Package errcode is the single source of truth for genroc's engine-produced error codes:
// the machine-readable discriminators stored in an instance's error_code and matched by
// on_error rules. It has no genroc dependencies, so every layer — transport, engine,
// validation — references the same constants without an import cycle.
//
// Authored codes (raise / panic) are deliberately NOT here: those are user-defined,
// lower_snake_case, and forbidden from containing a dot — which is exactly what keeps them
// distinct from the dotted engine codes below. See docs/child-error-handling.md.
package errcode

import (
	"fmt"
	"strings"
)

// Call codes — reported by an action's call, and CATCHABLE by on_error on the action task.
const (
	HTTPTimeout     = "http.timeout"     // connected, but no response arrived in time
	PreTimeout      = "pre.timeout"      // timed out during dial — the request never left
	PreError        = "pre.error"        // dial-phase failure — the request never left
	OutputParse     = "output.parse"     // the response body was not valid JSON
	OutputInvalid   = "output.invalid"   // the response did not satisfy its result_schema
	ExternalTimeout = "external.timeout" // an external task's wait deadline elapsed
)

// HTTP formats the code for a rejected HTTP status: HTTP(500) == "http.500". The status is
// unbounded, so this family is a function rather than a constant — the only dynamic code.
func HTTP(status int) string { return fmt.Sprintf("http.%d", status) }

// NotReached is the prefix of the codes that mean the remote was never reached (the call
// failed before the request left). A retry of such a code is safe even for an only_once
// task, since nothing happened remotely.
const NotReached = "pre."

// IsNotReached reports whether code is in the pre.* "call never reached the remote" family.
func IsNotReached(code string) bool { return strings.HasPrefix(code, NotReached) }

// Engine-internal codes — the engine failed the instance itself, not a call. These are
// TERMINAL: they go straight to failInstance and are never routed through on_error, so they
// cannot be caught. Every terminal failure still carries one so error_code is uniformly
// queryable.
const (
	EngineDefinition = "engine.definition" // definition unusable: missing, or names a task/goto not in it
	EngineExpression = "engine.expression" // an expression could not be evaluated against this context
	EngineConfig     = "engine.config"     // config could not be resolved from the environment
	EngineInput      = "engine.input"      // a child's input did not satisfy its input_schema
	EngineSpawn      = "engine.spawn"      // spawning a batch of children (or arming an external task) failed
	EngineCollect    = "engine.collect"    // collecting a settled batch's outputs failed
	EngineOnlyOnce   = "engine.only_once"  // an only_once task was interrupted and cannot be safely re-run
)
