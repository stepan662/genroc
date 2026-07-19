package schema

// ─── Accessors (read-only views) ────────────────────────────────────────────────

// Type returns the schema's declared type list (empty when unconstrained).
func (s Schema) Type() SchemaType {
	if s.n == nil {
		return nil
	}
	return s.n.Type
}

// Required returns the names the schema declares as required.
func (s Schema) Required() []string {
	if s.n == nil {
		return nil
	}
	return s.n.Required
}

// Properties returns the schema's declared properties, each wrapped as a Schema
// carrying the root $defs. Unlike Property (single-step navigation), this is a
// raw structural view: no $ref resolution and no nullable-wrapping of optionals.
func (s Schema) Properties() map[string]Schema {
	if s.n == nil || s.n.Properties == nil {
		return nil
	}
	out := make(map[string]Schema, len(s.n.Properties))
	for name, p := range s.n.Properties {
		out[name] = wrap(p, s.rootDefs())
	}
	return out
}

// Default returns the schema's declared default value, or nil.
func (s Schema) Default() any {
	if s.n == nil {
		return nil
	}
	return s.n.Default
}

// AdditionalProperties returns the open-map value subschema (carrying the root
// $defs) and true when the schema declares additionalProperties; else (zero, false).
func (s Schema) AdditionalProperties() (Schema, bool) {
	if s.n == nil || s.n.AdditionalProperties == nil {
		return Schema{}, false
	}
	return wrap(s.n.AdditionalProperties, s.rootDefs()), true
}

// HasRef reports whether the schema is a $ref pointer.
func (s Schema) HasRef() bool {
	return s.n != nil && s.n.Ref != ""
}

// HasDefs reports whether the schema carries root $defs.
func (s Schema) HasDefs() bool {
	return s.n != nil && len(s.n.Defs) > 0
}

// HasItems reports whether the schema declares an array item type.
func (s Schema) HasItems() bool {
	return s.n != nil && s.n.Items != nil
}

// HasProperties reports whether the schema declares object properties.
func (s Schema) HasProperties() bool {
	return s.n != nil && len(s.n.Properties) > 0
}

// HasCombinators reports whether the schema uses oneOf/anyOf/allOf.
func (s Schema) HasCombinators() bool {
	return s.n != nil && len(s.n.OneOf)+len(s.n.AnyOf)+len(s.n.AllOf) > 0
}

// Variants returns the schema's union members — the anyOf list when present,
// else the oneOf list — each wrapped as a Schema carrying the root $defs. It
// returns nil for a non-union schema. A nil member is returned as a zero Schema
// (IsZero reports true).
func (s Schema) Variants() []Schema {
	if s.n == nil {
		return nil
	}
	variants := s.n.AnyOf
	if variants == nil {
		variants = s.n.OneOf
	}
	if variants == nil {
		return nil
	}
	out := make([]Schema, len(variants))
	for i, v := range variants {
		if v != nil {
			out[i] = wrap(v, s.rootDefs())
		}
	}
	return out
}

// Items returns the array element schema (the zero Schema when none is declared),
// carrying the root $defs.
func (s Schema) Items() Schema {
	if s.n == nil || s.n.Items == nil {
		return Schema{}
	}
	return wrap(s.n.Items, s.rootDefs())
}

// Enum returns the schema's declared enum members (nil when none). The caller
// must not modify the returned slice.
func (s Schema) Enum() []any {
	if s.n == nil {
		return nil
	}
	return s.n.Enum
}

// Minimum returns the declared numeric minimum, and whether one is set.
func (s Schema) Minimum() (float64, bool) {
	if s.n == nil || s.n.Minimum == nil {
		return 0, false
	}
	return *s.n.Minimum, true
}

// Maximum returns the declared numeric maximum, and whether one is set.
func (s Schema) Maximum() (float64, bool) {
	if s.n == nil || s.n.Maximum == nil {
		return 0, false
	}
	return *s.n.Maximum, true
}

// MinLength returns the declared minimum string length, and whether one is set.
func (s Schema) MinLength() (int, bool) {
	if s.n == nil || s.n.MinLength == nil {
		return 0, false
	}
	return *s.n.MinLength, true
}

// MaxLength returns the declared maximum string length, and whether one is set.
func (s Schema) MaxLength() (int, bool) {
	if s.n == nil || s.n.MaxLength == nil {
		return 0, false
	}
	return *s.n.MaxLength, true
}

// MinItems returns the declared minimum array length, and whether one is set.
func (s Schema) MinItems() (int, bool) {
	if s.n == nil || s.n.MinItems == nil {
		return 0, false
	}
	return *s.n.MinItems, true
}

// MaxItems returns the declared maximum array length, and whether one is set.
func (s Schema) MaxItems() (int, bool) {
	if s.n == nil || s.n.MaxItems == nil {
		return 0, false
	}
	return *s.n.MaxItems, true
}

// Resolve follows the schema's $ref (if any) to its target in the root $defs,
// returning the target as a Schema carrying the same defs. A non-ref schema is
// returned unchanged; an unresolvable ref is an error.
func (s Schema) Resolve() (Schema, error) {
	if s.n == nil || s.n.Ref == "" {
		return s, nil
	}
	target, err := deref(s.n, s.rootDefs())
	if err != nil {
		return Schema{}, err
	}
	return wrap(target, s.rootDefs()), nil
}

// ─── Node algebra (immutable transforms and predicates) ─────────────────────────

// WithNull returns s widened to also accept null.
func (s Schema) WithNull() Schema {
	return wrap(withNull(s.n), s.rootDefs())
}

// StripNull returns s with null removed from its possible types.
func (s Schema) StripNull() Schema {
	return wrap(stripNull(s.n), s.rootDefs())
}

// Taint returns s marked secret:true (the whole value, conservatively).
func (s Schema) Taint() Schema {
	return wrap(taintNode(s.n), s.rootDefs())
}

// IsNull reports whether s is exactly {type:"null"}.
func (s Schema) IsNull() bool {
	return isNullType(s.n)
}

// HasNull reports whether null is a possible runtime value for s, following
// $refs against the root $defs (nullability may be declared inside a
// referenced definition, not just on the use-site wrapper).
func (s Schema) HasNull() bool {
	return hasNullResolved(s.n, s.rootDefs())
}

// Join returns the least upper bound of s and o: a schema accepting every value
// either accepts. Used by the recursive-output fixpoint to grow estimates.
func (s Schema) Join(o Schema) Schema {
	return wrap(joinNodes(s.n, o.n), s.rootDefs())
}

// Canonicalize returns s in canonical form (stable ordering, merged variants), so
// equal types compare equal.
func (s Schema) Canonicalize() Schema {
	return wrap(canonicalizeNode(s.n), s.rootDefs())
}

// Size returns the marshaled byte size of the schema — the growth bound the
// recursive fixpoint enforces.
func (s Schema) Size() int {
	return nodeSize(s.n)
}

// Equal reports whether s and o are structurally identical schemas.
func (s Schema) Equal(o Schema) bool {
	return nodesEqual(s.n, o.n)
}

// IsSubset reports whether every value valid under s is also valid under super.
// Both schemas must be normalized.
func (s Schema) IsSubset(super Schema) bool {
	return isSubset(s.n, super.n)
}

// ─── Secrets ────────────────────────────────────────────────────────────────────

// IsSecret reports whether this schema (the value at the root) is marked secret,
// looking through nullable / single-variant union wrappers.
func (s Schema) IsSecret() bool {
	return isSecret(s.n)
}

// SecretAt reports whether the value at path is secret — either the path passes
// through a node marked secret, or it ends at one. Reading from inside a secret
// object is itself secret. Returns false if the path cannot be resolved.
func (s Schema) SecretAt(path string) bool {
	return pathHitsSecret(s.n, s.rootDefs(), path)
}

// Redact returns data with every field whose schema is marked secret replaced by
// "***", descending via the same navigation the type inference uses.
func (s Schema) Redact(data any) any {
	return redact(data, s.n, s.rootDefs())
}

// CollectSecrets returns the string form of every value in data whose schema is
// marked secret — the gather half of log redaction.
func (s Schema) CollectSecrets(data any) []string {
	var out []string
	collectSecrets(data, s.n, s.rootDefs(), &out)
	return out
}
