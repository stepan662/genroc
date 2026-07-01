package schema

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// pathStep is one segment of a dot-path expression.
type pathStep struct {
	prop  string
	index int
}

// parsePath splits a path like "user.issues[0].value" into typed steps.
func parsePath(path string) ([]pathStep, error) {
	if path == "" {
		return nil, fmt.Errorf("path must not be empty")
	}
	var steps []pathStep
	for _, segment := range strings.Split(path, ".") {
		if segment == "" {
			return nil, fmt.Errorf("invalid path %q: empty segment", path)
		}
		for {
			open := strings.Index(segment, "[")
			if open == -1 {
				break
			}
			close := strings.Index(segment, "]")
			if close == -1 || close < open {
				return nil, fmt.Errorf("invalid path %q: unmatched '[' in segment %q", path, segment)
			}
			name := segment[:open]
			if name != "" {
				steps = append(steps, pathStep{prop: name})
			}
			idx, err := strconv.Atoi(segment[open+1 : close])
			if err != nil {
				return nil, fmt.Errorf("invalid path %q: non-integer index in %q", path, segment)
			}
			steps = append(steps, pathStep{index: idx})
			segment = segment[close+1:]
		}
		if segment != "" {
			steps = append(steps, pathStep{prop: segment})
		}
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("invalid path %q: no steps", path)
	}
	return steps, nil
}

// navigate navigates a dot-path expression from the root of s, resolving $refs
// against defs, and returns the subschema at the end of the path.
func navigate(s *SchemaNode, defs map[string]*SchemaNode, path string) (*SchemaNode, error) {
	steps, err := parsePath(path)
	if err != nil {
		return nil, err
	}
	return navigateSchema(s, defs, steps)
}

// lookupProperty returns the subschema for a single named property within s.
// Optional properties are returned wrapped as nullable.
func lookupProperty(s *SchemaNode, name string, defs map[string]*SchemaNode) (*SchemaNode, error) {
	resolved, err := Deref(s, defs)
	if err != nil {
		return nil, err
	}

	for _, kw := range []struct {
		name     string
		variants []*SchemaNode
	}{
		{"anyOf", resolved.AnyOf},
		{"oneOf", resolved.OneOf},
	} {
		if kw.variants == nil {
			continue
		}
		results := make([]*SchemaNode, 0, len(kw.variants))
		hadNull := false
		hadMiss := false
		for i, v := range kw.variants {
			if v == nil {
				return nil, fmt.Errorf("cannot access .%s: %s[%d] is nil", name, kw.name, i)
			}
			if IsNullType(v) {
				hadNull = true
				continue
			}
			r, err := lookupProperty(v, name, defs)
			if err != nil {
				hadMiss = true
				hadNull = true
				continue
			}
			results = append(results, r)
		}
		if len(results) == 0 {
			if hadMiss {
				return nil, fmt.Errorf("field %q not found in any %s variant", name, kw.name)
			}
			return &SchemaNode{Type: SchemaType{"null"}}, nil
		}
		var result *SchemaNode
		if allSame(results) {
			result = results[0]
		} else {
			cp := make([]*SchemaNode, len(results))
			copy(cp, results)
			if kw.name == "oneOf" {
				result = &SchemaNode{OneOf: cp}
			} else {
				result = &SchemaNode{AnyOf: cp}
			}
		}
		if hadNull {
			return WithNull(result), nil
		}
		return result, nil
	}

	if resolved.Properties == nil {
		return nil, fmt.Errorf("cannot access .%s: schema has no properties", name)
	}
	prop, ok := resolved.Properties[name]
	if !ok {
		return nil, fmt.Errorf("field %q not found in schema", name)
	}
	result, err := Deref(prop, defs)
	if err != nil {
		return nil, err
	}
	if !isRequired(resolved, name) {
		return WithNull(result), nil
	}
	return result, nil
}

// inferIndex returns the (nullable) element type for array index access on s.
// Always nullable because the index may be out of bounds at runtime.
func inferIndex(s *SchemaNode, defs map[string]*SchemaNode) (*SchemaNode, error) {
	resolved, err := Deref(s, defs)
	if err != nil {
		return nil, err
	}
	resolved = StripNull(resolved)

	for _, variants := range [][]*SchemaNode{resolved.AnyOf, resolved.OneOf} {
		if variants == nil {
			continue
		}
		results := make([]*SchemaNode, 0, len(variants))
		hadNull := false
		for _, v := range variants {
			if v == nil {
				continue
			}
			if IsNullType(v) {
				hadNull = true
				continue
			}
			r, err := inferIndex(v, defs)
			if err != nil {
				hadNull = true
				continue
			}
			results = append(results, r)
		}
		if len(results) == 0 {
			return &SchemaNode{Type: SchemaType{"null"}}, nil
		}
		var result *SchemaNode
		if allSame(results) {
			result = results[0]
		} else {
			result = &SchemaNode{AnyOf: results}
		}
		if hadNull && !HasNullType(result) {
			return WithNull(result), nil
		}
		return result, nil
	}

	if !resolved.Type.Contains("array") {
		t := ""
		if len(resolved.Type) > 0 {
			t = resolved.Type[0]
		}
		return nil, fmt.Errorf("index access [n] requires an array schema, got type %q", t)
	}
	if resolved.Items == nil {
		return &SchemaNode{}, nil
	}
	return WithNull(resolved.Items), nil
}

// Deref follows a $ref pointer if present, looking it up in defs.
// Returns s unchanged if no $ref is present.
func Deref(s *SchemaNode, defs map[string]*SchemaNode) (*SchemaNode, error) {
	if s == nil || s.Ref == "" {
		return s, nil
	}
	if defs == nil {
		return nil, fmt.Errorf("cannot resolve $ref %q: no defs available", s.Ref)
	}
	const prefix = "#/$defs/"
	if !strings.HasPrefix(s.Ref, prefix) {
		return nil, fmt.Errorf("unsupported $ref %q: only #/$defs/<name> is supported", s.Ref)
	}
	target, ok := defs[strings.TrimPrefix(s.Ref, prefix)]
	if !ok || target == nil {
		return nil, fmt.Errorf("$ref %q not found in defs", s.Ref)
	}
	return target, nil
}

// isSecret reports whether s is a secret value (marked secret:true), looking
// through nullable / single-variant union wrappers so an optional or wrapped
// secret is still recognised.
func isSecret(s *SchemaNode) bool {
	if s == nil {
		return false
	}
	if s.Secret {
		return true
	}
	for _, v := range s.OneOf {
		if isSecret(v) {
			return true
		}
	}
	for _, v := range s.AnyOf {
		if isSecret(v) {
			return true
		}
	}
	return false
}

// Taint returns a copy of s marked secret:true. It is used to taint the result of
// an expression that reads a secret value (conservatively, the whole value).
func Taint(s *SchemaNode) *SchemaNode {
	if s == nil {
		return &SchemaNode{Secret: true}
	}
	if s.Secret {
		return s
	}
	n := *s
	n.Secret = true
	return &n
}

// pathHitsSecret reports whether navigating path from s passes through (or ends
// at) a node marked secret — reading from inside a secret object is itself
// secret. Returns false if the path cannot be resolved.
func pathHitsSecret(s *SchemaNode, defs map[string]*SchemaNode, path string) bool {
	steps, err := parsePath(path)
	if err != nil {
		return false
	}
	cur, err := Deref(s, defs)
	if err != nil {
		return false
	}
	if isSecret(cur) {
		return true
	}
	for _, step := range steps {
		if step.prop != "" {
			cur, err = lookupProperty(cur, step.prop, defs)
		} else {
			cur, err = inferIndex(cur, defs)
		}
		if err != nil {
			return false
		}
		if isSecret(cur) {
			return true
		}
	}
	return false
}

// collectSecrets appends to *out the string form of every value in value whose
// schema is marked secret. It descends objects and arrays with the same primitives
// type inference uses — lookupProperty / inferIndex, which resolve $refs, nullable
// wrappers, and oneOf/anyOf/allOf combinators — so the walk cannot drift from the
// schema navigation. It is the gather half of log redaction: the collected values
// are then scrubbed from free-form log text.
func collectSecrets(value any, node *SchemaNode, defs map[string]*SchemaNode, out *[]string) {
	if node == nil || value == nil {
		return
	}
	resolved, err := Deref(node, defs)
	if err != nil {
		return
	}
	if isSecret(resolved) {
		if s := SecretString(value); s != "" {
			*out = append(*out, s)
		}
		return
	}
	switch v := value.(type) {
	case map[string]any:
		for k, val := range v {
			if child, err := lookupProperty(resolved, k, defs); err == nil {
				collectSecrets(val, child, defs, out)
			}
		}
	case []any:
		child, err := inferIndex(resolved, defs)
		if err != nil {
			return
		}
		for _, el := range v {
			collectSecrets(el, child, defs, out)
		}
	}
}

// redact returns value with every field whose schema is marked secret replaced by
// "***". Like collectSecrets it descends via lookupProperty / inferIndex, so $ref,
// nullable, and combinator handling lives in one place. Non-secret values pass
// through unchanged. Used to scrub secret-derived values before they cross a public
// boundary (API, logs).
func redact(value any, node *SchemaNode, defs map[string]*SchemaNode) any {
	if node == nil || value == nil {
		return value
	}
	resolved, err := Deref(node, defs)
	if err != nil {
		return value
	}
	if isSecret(resolved) {
		return "***"
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			if child, err := lookupProperty(resolved, k, defs); err == nil {
				out[k] = redact(val, child, defs)
			} else {
				out[k] = val // key not in schema — leave untouched
			}
		}
		return out
	case []any:
		child, err := inferIndex(resolved, defs)
		if err != nil {
			return value
		}
		out := make([]any, len(v))
		for i, el := range v {
			out[i] = redact(el, child, defs)
		}
		return out
	default:
		return value
	}
}

// SecretString renders a secret value the way it appears in logs so the substring
// scrub matches it. Strings pass through raw — they appear unquoted (e.g. inside a
// URL) and as a substring of their quoted JSON form, so the raw value catches both.
// Everything else uses its JSON encoding: notably a number is "1000000" as
// json.Marshal writes it, not fmt's "1e+06", which would never match the log text.
func SecretString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}

// IsNullType reports whether s is exactly {type:"null"}.
func IsNullType(s *SchemaNode) bool {
	return s != nil && len(s.Type) == 1 && s.Type[0] == "null"
}

// HasNullType reports whether null is a possible type for s.
func HasNullType(s *SchemaNode) bool {
	if s == nil {
		return false
	}
	if s.Type.Contains("null") {
		return true
	}
	for _, v := range s.OneOf {
		if IsNullType(v) {
			return true
		}
	}
	for _, v := range s.AnyOf {
		if IsNullType(v) {
			return true
		}
	}
	return false
}

// WithNull makes s nullable. Simple types produce {type:[T,"null"]};
// complex schemas are wrapped in {oneOf:[s,{type:"null"}]}.
func WithNull(s *SchemaNode) *SchemaNode {
	if s == nil || isEmptyNode(s) {
		return s
	}
	if s.Type.Contains("null") {
		return s
	}
	for _, v := range s.OneOf {
		if IsNullType(v) {
			return s
		}
	}
	for _, v := range s.AnyOf {
		if IsNullType(v) {
			return s
		}
	}
	// Simple type without properties — widen type array to include null.
	if len(s.Type) >= 1 && s.Properties == nil {
		n := *s
		n.Type = make(SchemaType, len(s.Type)+1)
		copy(n.Type, s.Type)
		n.Type[len(s.Type)] = "null"
		return &n
	}
	return &SchemaNode{OneOf: []*SchemaNode{s, {Type: SchemaType{"null"}}}}
}

// StripNull removes null from a schema's possible types.
func StripNull(s *SchemaNode) *SchemaNode {
	if s == nil {
		return s
	}
	if len(s.Type) > 0 {
		var nonNull SchemaType
		for _, t := range s.Type {
			if t != "null" {
				nonNull = append(nonNull, t)
			}
		}
		if len(nonNull) == len(s.Type) {
			return s
		}
		n := *s
		n.Type = nonNull
		return &n
	}
	if len(s.OneOf) > 0 {
		var nonNull []*SchemaNode
		for _, v := range s.OneOf {
			if !IsNullType(v) {
				nonNull = append(nonNull, v)
			}
		}
		if len(nonNull) == len(s.OneOf) {
			return s
		}
		if len(nonNull) == 1 {
			return nonNull[0]
		}
		n := *s
		n.OneOf = nonNull
		return &n
	}
	if len(s.AnyOf) > 0 {
		var nonNull []*SchemaNode
		for _, v := range s.AnyOf {
			if !IsNullType(v) {
				nonNull = append(nonNull, v)
			}
		}
		if len(nonNull) == len(s.AnyOf) {
			return s
		}
		if len(nonNull) == 1 {
			return nonNull[0]
		}
		n := *s
		n.AnyOf = nonNull
		return &n
	}
	return s
}

func isEmptyNode(s *SchemaNode) bool {
	return s == nil || (len(s.Type) == 0 && s.Properties == nil && s.Required == nil &&
		s.Items == nil && s.OneOf == nil && s.AnyOf == nil && s.AllOf == nil &&
		s.Enum == nil && s.Ref == "" && s.Defs == nil && s.Minimum == nil &&
		s.Maximum == nil && s.MinLength == nil && s.MaxLength == nil &&
		s.MinItems == nil && s.MaxItems == nil)
}

func navigateSchema(s *SchemaNode, defs map[string]*SchemaNode, steps []pathStep) (*SchemaNode, error) {
	current := s
	for _, step := range steps {
		var err error
		if step.prop != "" {
			current, err = lookupProperty(current, step.prop, defs)
		} else {
			current, err = inferIndex(current, defs)
		}
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}

func isRequired(s *SchemaNode, name string) bool {
	for _, r := range s.Required {
		if r == name {
			return true
		}
	}
	return false
}

func allSame(schemas []*SchemaNode) bool {
	if len(schemas) == 0 {
		return true
	}
	first, _ := json.Marshal(schemas[0])
	for _, s := range schemas[1:] {
		other, _ := json.Marshal(s)
		if string(first) != string(other) {
			return false
		}
	}
	return true
}
