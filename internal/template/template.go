// Package template parses and evaluates {{ }} template strings.
//
// Three modes:
//   - Plain string (no {{ }}): returned as a string literal.
//   - Single expression "{{expr}}": evaluated and returned as-is (preserves type).
//   - Mixed "text{{expr}}text": each {{expr}} is evaluated, must be string/number/bool,
//     results are stringified and concatenated with the surrounding literal text.
//
// A template is parsed once into a Template — literal chunks interleaved with
// parsed expression ASTs — and then evaluated or type-inferred against a context
// any number of times. Which of the three modes applies is fixed at parse time
// rather than re-derived per call. Get memoises parsing, since templates are
// static strings carried on process definitions.
//
// Where an expression ends is decided by the expression parser, not by scanning
// for the next "}}": at each "{{" the candidate terminators are tried in order
// and the first body that parses wins. A "}}" nested inside an object literal
// ({{ {a: {b: 1}} }}) or inside a string literal ({{ "x}}y" }}) therefore does
// not end the block, because a candidate that cuts through either fails to
// parse. That keeps the lexical rules in one place instead of duplicating a
// string-and-bracket scanner here, where it could silently drift.
package template

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"genroc/internal/expression"
	"genroc/internal/expression/syntax"
	"genroc/internal/schema"
)

// Template is a parsed {{ }} template. The zero value is not usable; obtain one
// from Parse or Get. A Template is immutable and safe for concurrent use.
type Template struct {
	src    string
	chunks []chunk
	// single marks a template that is exactly one expression and no literal text,
	// so evaluation returns the raw value instead of stringifying it.
	single bool
}

// chunk is either literal text (node == nil) or a parsed expression, in which
// case text holds its source for error messages.
type chunk struct {
	text string
	node syntax.Node
}

// Parse splits s into literal and expression chunks, parsing each expression.
func Parse(s string) (*Template, error) {
	t := &Template{src: s}
	rest := s
	for {
		start := strings.Index(rest, "{{")
		if start == -1 {
			if rest != "" {
				t.chunks = append(t.chunks, chunk{text: rest})
			}
			break
		}
		if start > 0 {
			t.chunks = append(t.chunks, chunk{text: rest[:start]})
		}
		expr, node, after, err := parseBlock(rest[start+2:])
		if err != nil {
			return nil, fmt.Errorf("template %q: %w", s, err)
		}
		t.chunks = append(t.chunks, chunk{text: expr, node: node})
		rest = after
	}
	t.single = len(t.chunks) == 1 && t.chunks[0].node != nil
	return t, nil
}

// parseBlock splits one {{ }} block off the front of s, which starts just past the
// opening "{{". Each "}}" is tried as the terminator in order and the first body
// that parses wins, so a "}}" inside a nested literal or a string does not end the
// block: a candidate that cuts mid-string leaves the string unterminated, which is
// itself a parse error, and the scan moves on.
//
// Shortest-match is sound. For a longer body to have been intended it would have to
// parse too, which requires the intervening "}}" to sit inside brackets or a string
// — and in both cases the shorter candidate fails to parse.
func parseBlock(s string) (expr string, node syntax.Node, rest string, err error) {
	var longest error
	for at := 0; ; {
		end := strings.Index(s[at:], "}}")
		if end == -1 {
			if longest == nil {
				return "", nil, "", errors.New("unclosed {{")
			}
			// Every candidate failed. Report the longest one's error: a truncated
			// candidate dies with an uninformative "unexpected EOF", while the full
			// body dies with the syntax error the author actually needs to see.
			return "", nil, "", longest
		}
		end += at
		body := s[:end]
		node, perr := syntax.Parse(body)
		if perr == nil {
			return body, node, s[end+2:], nil
		}
		longest = fmt.Errorf("expression %q: %w", body, perr)
		at = end + 1
	}
}

// Source returns the template string this was parsed from.
func (t *Template) Source() string { return t.src }

// EvalAny evaluates the template against ctx. A single-expression template returns
// the raw value, preserving its type; anything else stringifies each expression and
// concatenates it with the literal text.
func (t *Template) EvalAny(ctx map[string]any) (any, error) {
	if t.single {
		val, err := expression.EvalNode(t.chunks[0].node, ctx)
		if err != nil {
			return nil, fmt.Errorf("template %q: %w", t.src, err)
		}
		return val, nil
	}
	var out strings.Builder
	for _, c := range t.chunks {
		if c.node == nil {
			out.WriteString(c.text)
			continue
		}
		val, err := expression.EvalNode(c.node, ctx)
		if err != nil {
			return nil, fmt.Errorf("template expression %q: %w", c.text, err)
		}
		str, err := stringify(val)
		if err != nil {
			return nil, fmt.Errorf("template expression %q: %w", c.text, err)
		}
		out.WriteString(str)
	}
	return out.String(), nil
}

// InferType returns the static type of the template's result against sc.
func (t *Template) InferType(sc schema.Schema) (schema.Schema, error) {
	if t.single {
		return sc.InferNode(t.chunks[0].node)
	}
	// A mixed template always yields a string, but a null interpolation would
	// silently stringify to "null" — reject it so the author adds a ?? default.
	for _, c := range t.chunks {
		if c.node == nil {
			continue
		}
		inferred, err := sc.InferNode(c.node)
		if err != nil {
			return schema.Schema{}, fmt.Errorf("template expression %q: %w", c.text, err)
		}
		if inferred.HasNull() {
			return schema.Schema{}, fmt.Errorf("template expression %q may be null; use ?? to provide a default value", c.text)
		}
		// stringify accepts only string/number/bool, so an array or object here is a
		// guaranteed runtime failure. Catch it at registration instead: IsType is
		// "resolves uniformly to", so this fires only when the value provably cannot
		// be interpolated, never on a type we merely cannot pin down.
		if inferred.IsType("array") || inferred.IsType("object") {
			return schema.Schema{}, fmt.Errorf("template expression %q is %s; only string, number and boolean values can be interpolated into surrounding text", c.text, inferred.TypeName())
		}
	}
	return schema.Type("string"), nil
}

// ReferencesSecret reports whether any embedded expression reads a secret value
// (see schema.Schema.ReferencesSecret); a plain string is never secret.
func (t *Template) ReferencesSecret(sc schema.Schema) bool {
	for _, c := range t.chunks {
		if c.node != nil && sc.ReferencesSecretNode(c.node) {
			return true
		}
	}
	return false
}

// RootRefs reports which context roots the embedded expressions read, merged across
// every block, so the engine lazily resolves only the value-slots a template needs.
func (t *Template) RootRefs() expression.Roots {
	var out expression.Roots
	for _, c := range t.chunks {
		if c.node == nil {
			continue
		}
		r := expression.RootRefsNode(c.node)
		out.Input = out.Input || r.Input
		out.Error = out.Error || r.Error
		out.AllOutputs = out.AllOutputs || r.AllOutputs
		out.Outputs = append(out.Outputs, r.Outputs...)
		out.SelfPrevious = out.SelfPrevious || r.SelfPrevious
		out.SelfResult = out.SelfResult || r.SelfResult
	}
	return out
}

// OutputRefs returns the distinct outputs.<id> task ids across every block, for the
// output-dependency graph.
func (t *Template) OutputRefs() []string {
	set := map[string]struct{}{}
	for _, c := range t.chunks {
		if c.node == nil {
			continue
		}
		for _, id := range expression.OutputRefsNode(c.node) {
			set[id] = struct{}{}
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// cache memoises Parse. Template strings are static content carried on process
// definitions, so the key set is bounded by the registered definitions — the same
// bound the DB's definition cache already accepts. Failures are cached too, so a
// definition that somehow reaches the engine with a bad template does not re-parse
// on every tick.
var cache sync.Map // string -> parsed

type parsed struct {
	t   *Template
	err error
}

// Get returns the parsed form of s, parsing on first use. The returned Template is
// shared across callers and must not be mutated.
func Get(s string) (*Template, error) {
	if v, ok := cache.Load(s); ok {
		p := v.(parsed)
		return p.t, p.err
	}
	t, err := Parse(s)
	cache.Store(s, parsed{t: t, err: err})
	return t, err
}

func stringify(v any) (string, error) {
	switch val := v.(type) {
	case string:
		return val, nil
	case json.Number:
		// Already the exact literal; rendering it any other way would reintroduce
		// the float64 rounding this representation exists to avoid.
		return val.String(), nil
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
