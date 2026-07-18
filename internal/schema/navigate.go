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
func navigate(s *node, defs map[string]*node, path string) (*node, error) {
	steps, err := parsePath(path)
	if err != nil {
		return nil, err
	}
	return navigateSchema(s, defs, steps)
}

// lookupProperty returns the subschema for a single named property within s.
// Optional properties with no default are returned wrapped as nullable; required
// properties and optionals with a default (always filled by validation) are not.
//
// A property whose value is a `$ref` is returned as the reference itself, not
// its expansion: passing a referenced value through whole keeps the reference
// in the inferred type (which is what lets a recursive output type stay a
// finite recursive schema). Descending *into* it — the next lookupProperty on
// that base, an operator, a union walk below — resolves it then, on demand.
func lookupProperty(s *node, name string, defs map[string]*node) (*node, error) {
	return lookupPropertyGuard(s, name, defs, nil)
}

// lookupPropertyGuard is lookupProperty with a union-walk cycle guard:
// visiting holds union nodes already being walked at this value position, so a
// reference cycle threaded through union variants fails that variant (treated
// as a miss) instead of recursing forever. Recursion through properties/items
// starts fresh — that is productive structure, not a cycle.
func lookupPropertyGuard(s *node, name string, defs map[string]*node, visiting map[*node]bool) (*node, error) {
	resolved, err := deref(s, defs)
	if err != nil {
		return nil, err
	}

	for _, kw := range []struct {
		name     string
		variants []*node
	}{
		{"anyOf", resolved.AnyOf},
		{"oneOf", resolved.OneOf},
	} {
		if kw.variants == nil {
			continue
		}
		if visiting[resolved] {
			return nil, fmt.Errorf("cannot access .%s: schema reference cycle", name)
		}
		next := make(map[*node]bool, len(visiting)+1)
		for k := range visiting {
			next[k] = true
		}
		next[resolved] = true
		results := make([]*node, 0, len(kw.variants))
		hadNull := false
		hadMiss := false
		for i, v := range kw.variants {
			if v == nil {
				return nil, fmt.Errorf("cannot access .%s: %s[%d] is nil", name, kw.name, i)
			}
			// Accessing a property *inside* a union variant is a look-inside
			// operation: resolve a $ref variant first, so a reference to a
			// null seed (mid-solve estimate) counts as the null arm rather
			// than a missing field.
			rv, verr := deref(v, defs)
			if verr != nil {
				return nil, verr
			}
			if isNullType(rv) {
				hadNull = true
				continue
			}
			r, err := lookupPropertyGuard(rv, name, defs, next)
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
			return &node{Type: SchemaType{"null"}}, nil
		}
		var result *node
		if allSame(results) {
			result = results[0]
		} else {
			cp := make([]*node, len(results))
			copy(cp, results)
			if kw.name == "oneOf" {
				result = &node{OneOf: cp}
			} else {
				result = &node{AnyOf: cp}
			}
		}
		if hadNull {
			return withNull(result), nil
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
	// The property value is returned as declared — a $ref stays a $ref (see the
	// function comment); a taint on it rides along on the ref node itself.
	//
	// A property is non-nullable when it is guaranteed present after validation:
	// either it is required, or it has a default (conformObject fills an absent
	// optional's default, so the value can never be missing). Only a truly optional
	// property with no default comes back nullable.
	if !isRequired(resolved, name) && propDefault(prop, defs) == nil {
		return withNull(prop), nil
	}
	return prop, nil
}

// inferIndex returns the (nullable) element type for array index access on s.
// Always nullable because the index may be out of bounds at runtime.
func inferIndex(s *node, defs map[string]*node) (*node, error) {
	return inferIndexGuard(s, defs, nil)
}

// inferIndexGuard carries the same union-walk cycle guard as
// lookupPropertyGuard.
func inferIndexGuard(s *node, defs map[string]*node, visiting map[*node]bool) (*node, error) {
	resolved, err := deref(s, defs)
	if err != nil {
		return nil, err
	}
	resolved = stripNull(resolved)

	for _, variants := range [][]*node{resolved.AnyOf, resolved.OneOf} {
		if variants == nil {
			continue
		}
		if visiting[resolved] {
			return nil, fmt.Errorf("index access [n]: schema reference cycle")
		}
		next := make(map[*node]bool, len(visiting)+1)
		for k := range visiting {
			next[k] = true
		}
		next[resolved] = true
		results := make([]*node, 0, len(variants))
		hadNull := false
		for _, v := range variants {
			if v == nil {
				continue
			}
			// Indexing into a union variant is a look-inside operation:
			// resolve a $ref variant first (a mid-solve null seed counts as
			// the null arm).
			rv, verr := deref(v, defs)
			if verr != nil {
				return nil, verr
			}
			if isNullType(rv) {
				hadNull = true
				continue
			}
			r, err := inferIndexGuard(rv, defs, next)
			if err != nil {
				hadNull = true
				continue
			}
			results = append(results, r)
		}
		if len(results) == 0 {
			return &node{Type: SchemaType{"null"}}, nil
		}
		var result *node
		if allSame(results) {
			result = results[0]
		} else {
			result = &node{AnyOf: results}
		}
		if hadNull && !hasNullType(result) {
			return withNull(result), nil
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
		return &node{}, nil
	}
	return withNull(resolved.Items), nil
}

// deref resolves s to a concrete (non-ref) node, following chains of $ref
// pointers through defs — a definition may itself be a bare alias for another
// (e.g. after a collision rename), so a single hop is not enough. A repeated
// node means a pure ref cycle, which can never bottom out and is an error.
// Returns s unchanged if no $ref is present.
//
// A target that is a solver sentinel routes back to its owning Solver, which
// computes the definition on demand (or serves the running estimate when the
// read closes a cycle) — this is the hook that makes definition resolution
// demand-driven during generation.
func deref(s *node, defs map[string]*node) (*node, error) {
	var seen map[*node]bool
	for s != nil && s.Ref != "" {
		if seen[s] {
			return nil, fmt.Errorf("circular $ref %q never resolves to a schema", s.Ref)
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
		if seen == nil {
			seen = map[*node]bool{}
		}
		seen[s] = true
		if p, pending := lookupPending(target); pending {
			resolved, err := p.solver.resolvePending(p.name)
			if err != nil {
				return nil, err
			}
			s = resolved
			continue
		}
		if target.Anchor == pendingAnchor {
			return nil, fmt.Errorf("internal: $ref %q points at an unresolved pending definition", s.Ref)
		}
		s = target
	}
	return s, nil
}

// isSecret reports whether s is a secret value (marked secret:true), looking
// through nullable / single-variant union wrappers so an optional or wrapped
// secret is still recognised.
func isSecret(s *node) bool {
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
func taintNode(s *node) *node {
	if s == nil {
		return &node{Secret: true}
	}
	if s.Secret {
		return s
	}
	n := *s
	n.Secret = true
	return &n
}

// nodeOrTargetSecret reports whether n, or the definition it resolves to, is
// marked secret. A taint on a $ref node marks the pointer rather than the
// shared target (tainting the target would over-taint its other users), so
// both sides must be consulted.
func nodeOrTargetSecret(n *node, defs map[string]*node) bool {
	if isSecret(n) {
		return true
	}
	if n != nil && n.Ref != "" {
		if resolved, err := deref(n, defs); err == nil && isSecret(resolved) {
			return true
		}
	}
	return false
}

// pathHitsSecret reports whether navigating path from s passes through (or ends
// at) a node marked secret — reading from inside a secret object is itself
// secret. Returns false if the path cannot be resolved.
func pathHitsSecret(s *node, defs map[string]*node, path string) bool {
	steps, err := parsePath(path)
	if err != nil {
		return false
	}
	if nodeOrTargetSecret(s, defs) {
		return true
	}
	cur, err := deref(s, defs)
	if err != nil {
		return false
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
		if nodeOrTargetSecret(cur, defs) {
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
func collectSecrets(value any, node *node, defs map[string]*node, out *[]string) {
	if node == nil || value == nil {
		return
	}
	if nodeOrTargetSecret(node, defs) {
		if s := SecretString(value); s != "" {
			*out = append(*out, s)
		}
		return
	}
	resolved, err := deref(node, defs)
	if err != nil {
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
func redact(value any, node *node, defs map[string]*node) any {
	if node == nil || value == nil {
		return value
	}
	if nodeOrTargetSecret(node, defs) {
		return "***"
	}
	resolved, err := deref(node, defs)
	if err != nil {
		return value
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

// isNullType reports whether s is exactly {type:"null"}.
func isNullType(s *node) bool {
	return s != nil && len(s.Type) == 1 && s.Type[0] == "null"
}

// hasNullType reports whether null is a possible type for s.
func hasNullType(s *node) bool {
	if s == nil {
		return false
	}
	if s.Type.Contains("null") {
		return true
	}
	for _, v := range s.OneOf {
		if isNullType(v) {
			return true
		}
	}
	for _, v := range s.AnyOf {
		if isNullType(v) {
			return true
		}
	}
	return false
}

// IsType reports whether s resolves to exactly the given type: its type list is
// non-empty with every entry equal to typ, or it is a oneOf/anyOf all of whose
// variants themselves resolve to typ. $refs are followed (against the root
// $defs), so a reference to a boolean definition is a boolean. It is the "is
// this uniformly type X" gate used, e.g., to require a switch expression to be
// boolean.
func (s Schema) IsType(typ string) bool { return nodeIsType(s.n, s.rootDefs(), typ) }

func nodeIsType(s *node, defs map[string]*node, typ string) bool {
	s, err := deref(s, defs)
	if err != nil || s == nil {
		return false
	}
	if len(s.Type) > 0 {
		for _, t := range s.Type {
			if t != typ {
				return false
			}
		}
		return true
	}
	for _, variants := range [][]*node{s.OneOf, s.AnyOf} {
		if variants == nil {
			continue
		}
		for _, v := range variants {
			if !nodeIsType(v, defs, typ) {
				return false
			}
		}
		return len(variants) > 0
	}
	return false
}

// hasNullResolved reports whether null is a possible runtime value for s,
// following $refs (top-level and one union level, matching hasNullType's
// structural depth) so nullability declared inside a referenced definition is
// seen. Resolution failures degrade to the structural answer.
func hasNullResolved(s *node, defs map[string]*node) bool {
	r, err := deref(s, defs)
	if err != nil {
		return hasNullType(s)
	}
	if hasNullType(r) {
		return true
	}
	for _, variants := range [][]*node{r.OneOf, r.AnyOf} {
		for _, v := range variants {
			rv, verr := deref(v, defs)
			if verr != nil {
				continue
			}
			if isNullType(rv) || rv.Type.Contains("null") {
				return true
			}
		}
	}
	return false
}

// TypeName returns a readable name for s's type — a single type ("string"), a
// union ("string|null"), or "unknown" when it can't be determined. Intended for
// error messages.
func (s Schema) TypeName() string { return nodeTypeName(s.n) }

func nodeTypeName(s *node) string {
	if s == nil {
		return "unknown"
	}
	if len(s.Type) > 0 {
		return strings.Join([]string(s.Type), "|")
	}
	for _, variants := range [][]*node{s.OneOf, s.AnyOf} {
		if variants == nil {
			continue
		}
		seen := make(map[string]bool, len(variants))
		var parts []string
		for _, v := range variants {
			if v == nil {
				continue
			}
			name := nodeTypeName(v)
			if !seen[name] {
				seen[name] = true
				parts = append(parts, name)
			}
		}
		return strings.Join(parts, "|")
	}
	return "unknown"
}

// WithNull makes s nullable. Simple types produce {type:[T,"null"]};
// complex schemas are wrapped in {oneOf:[s,{type:"null"}]}.
func withNull(s *node) *node {
	if s == nil || isEmptyNode(s) {
		return s
	}
	if s.Type.Contains("null") {
		return s
	}
	for _, v := range s.OneOf {
		if isNullType(v) {
			return s
		}
	}
	for _, v := range s.AnyOf {
		if isNullType(v) {
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
	return &node{OneOf: []*node{s, {Type: SchemaType{"null"}}}}
}

// StripNull removes null from a schema's possible types.
func stripNull(s *node) *node {
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
		var nonNull []*node
		for _, v := range s.OneOf {
			if !isNullType(v) {
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
		var nonNull []*node
		for _, v := range s.AnyOf {
			if !isNullType(v) {
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

// isEmptyNode reports whether s constrains nothing. Root $defs are deliberately
// ignored: a node carrying only a resolution context (as sub-schemas returned by
// navigation do) still accepts any value.
func isEmptyNode(s *node) bool {
	return s == nil || (len(s.Type) == 0 && s.Properties == nil && s.Required == nil &&
		s.Items == nil && s.OneOf == nil && s.AnyOf == nil && s.AllOf == nil &&
		s.Enum == nil && s.Ref == "" && s.Minimum == nil &&
		s.Maximum == nil && s.MinLength == nil && s.MaxLength == nil &&
		s.MinItems == nil && s.MaxItems == nil)
}

func navigateSchema(s *node, defs map[string]*node, steps []pathStep) (*node, error) {
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

func isRequired(s *node, name string) bool {
	for _, r := range s.Required {
		if r == name {
			return true
		}
	}
	return false
}

func allSame(schemas []*node) bool {
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
