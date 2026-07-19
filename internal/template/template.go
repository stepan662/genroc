// Package template parses and evaluates {{ }} template strings.
//
// Three modes:
//   - Plain string (no {{ }}): returned as a string literal.
//   - Single expression "{{expr}}": evaluated and returned as-is (preserves type).
//   - Mixed "text{{expr}}text": each {{expr}} is evaluated, must be string/number/bool,
//     results are stringified and concatenated with the surrounding literal text.
package template

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"genroc/internal/expression"
	"genroc/internal/schema"
)

// EvalAny evaluates s as a template string against ctx.
func EvalAny(s string, ctx map[string]any) (any, error) {
	if expr, ok := singleExpr(s); ok {
		val, err := expression.Eval(expr, ctx)
		if err != nil {
			return nil, fmt.Errorf("template %q: %w", s, err)
		}
		return val, nil
	}
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	return evalMixed(s, ctx)
}

// InferType infers the JSON Schema type of template s against sc.
func InferType(s string, sc schema.Schema) (schema.Schema, error) {
	if expr, ok := singleExpr(s); ok {
		return sc.Infer(expr)
	}
	if strings.Contains(s, "{{") {
		if err := checkMixedNullability(s, sc); err != nil {
			return schema.Schema{}, err
		}
	}
	return schema.Type("string"), nil
}

// ReferencesSecret reports whether any expression embedded in the template reads
// a secret value (see schema.Schema.ReferencesSecret). A plain string with no
// {{ }} is never secret.
func ReferencesSecret(s string, sc schema.Schema) (bool, error) {
	found := false
	err := forEachExpr(s, func(expr string) error {
		sec, err := sc.ReferencesSecret(expr)
		if err != nil {
			return err
		}
		if sec {
			found = true
			return errStopScan // short-circuit on the first secret
		}
		return nil
	})
	if err == errStopScan {
		return true, nil
	}
	return found, err
}

// checkMixedNullability rejects mixed templates where any expression may be null.
// Null values would silently become the string "null" at runtime, which is almost
// never intentional. The user must add a ?? default to make the intent explicit.
func checkMixedNullability(s string, sc schema.Schema) error {
	return forEachExpr(s, func(expr string) error {
		inferred, err := sc.Infer(expr)
		if err != nil {
			return fmt.Errorf("template expression %q: %w", expr, err)
		}
		if inferred.HasNull() {
			return fmt.Errorf("template expression %q may be null; use ?? to provide a default value", expr)
		}
		return nil
	})
}

// OutputRefs returns the distinct task ids referenced via outputs.<id> across all
// {{ }} expressions in template s (used to build the output-dependency graph).
func OutputRefs(s string) ([]string, error) {
	set := map[string]struct{}{}
	err := forEachExpr(s, func(expr string) error {
		refs, err := expression.OutputRefs(expr)
		if err != nil {
			return fmt.Errorf("template expression %q: %w", expr, err)
		}
		for _, r := range refs {
			set[r] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// RootRefs reports which context roots (input / error / specific or all outputs) the
// expressions embedded in template s read, merged across every {{ }} block. A plain
// string with no {{ }} reads nothing. Used by the engine to lazily resolve only the
// externalized value-slots a template needs.
func RootRefs(s string) (expression.Roots, error) {
	var out expression.Roots
	err := forEachExpr(s, func(expr string) error {
		r, err := expression.RootRefs(expr)
		if err != nil {
			return fmt.Errorf("template expression %q: %w", expr, err)
		}
		out.Input = out.Input || r.Input
		out.Error = out.Error || r.Error
		out.AllOutputs = out.AllOutputs || r.AllOutputs
		out.Outputs = append(out.Outputs, r.Outputs...)
		out.SelfPrevious = out.SelfPrevious || r.SelfPrevious
		out.SelfResult = out.SelfResult || r.SelfResult
		return nil
	})
	return out, err
}

// singleExpr reports whether s is exactly "{{expr}}" with nothing outside.
func singleExpr(s string) (string, bool) {
	if !strings.HasPrefix(s, "{{") || !strings.HasSuffix(s, "}}") {
		return "", false
	}
	inner := s[2 : len(s)-2]
	if strings.Contains(inner, "{{") || strings.Contains(inner, "}}") {
		return "", false
	}
	return inner, true
}

// errStopScan is a sentinel a forEachExpr callback returns to end the scan early
// without reporting a real error (e.g. once a match is found).
var errStopScan = errors.New("stop scan")

// forEachExpr calls fn with the body of each {{ }} block in s, in order. A missing
// closing }} silently ends the scan — the inspection helpers ignore trailing
// unterminated text, whereas evalMixed treats it as an error and scans separately.
func forEachExpr(s string, fn func(expr string) error) error {
	rest := s
	for {
		start := strings.Index(rest, "{{")
		if start == -1 {
			return nil
		}
		rest = rest[start+2:]
		end := strings.Index(rest, "}}")
		if end == -1 {
			return nil
		}
		expr := rest[:end]
		rest = rest[end+2:]
		if err := fn(expr); err != nil {
			return err
		}
	}
}

// evalMixed evaluates a mixed template: literal text interleaved with {{expr}} blocks.
// Each expression result must be a string, number, or bool.
func evalMixed(s string, ctx map[string]any) (string, error) {
	var result strings.Builder
	rest := s
	for {
		start := strings.Index(rest, "{{")
		if start == -1 {
			result.WriteString(rest)
			break
		}
		result.WriteString(rest[:start])
		rest = rest[start+2:]
		end := strings.Index(rest, "}}")
		if end == -1 {
			return "", fmt.Errorf("template %q: unclosed {{", s)
		}
		expr := rest[:end]
		rest = rest[end+2:]
		val, err := expression.Eval(expr, ctx)
		if err != nil {
			return "", fmt.Errorf("template expression %q: %w", expr, err)
		}
		str, err := stringify(val)
		if err != nil {
			return "", fmt.Errorf("template expression %q: %w", expr, err)
		}
		result.WriteString(str)
	}
	return result.String(), nil
}

func stringify(v any) (string, error) {
	switch val := v.(type) {
	case string:
		return val, nil
	case int:
		return fmt.Sprintf("%d", val), nil
	case int64:
		return fmt.Sprintf("%d", val), nil
	case float32:
		return fmt.Sprintf("%g", float64(val)), nil
	case float64:
		return fmt.Sprintf("%g", val), nil
	case bool:
		if val {
			return "true", nil
		}
		return "false", nil
	default:
		return "", fmt.Errorf("cannot stringify %T in mixed template (only string, number, bool allowed)", v)
	}
}
