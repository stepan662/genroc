// Package schema provides a normalizer, validator, and type helpers for a strict
// subset of JSON Schema.
//
// Supported keywords: type, properties, required, items, oneOf, anyOf, enum,
// minimum, maximum, minLength, maxLength, minItems, maxItems, $ref, $defs, $anchor, $id.
// Any other keyword causes an unmarshal error.
//
// The package exposes two value types. Parse yields a Raw — an unnormalized
// document that may carry nested $defs and unresolved anchors; the only thing to
// do with it is Normalize (or marshal it back out). Normalize yields a Schema —
// the operating type: $defs live only at its root and every operation (Validate,
// At/Infer, SecretAt, Redact, …) is a method. The underlying node tree is not
// exported; sub-schemas returned by navigation are full Schemas carrying the root
// $defs, so they stay resolvable at any depth.
//
// allOf is deliberately NOT accepted: it is an intersection that schema navigation
// (Property / Index, and thus type inference and secret detection) cannot
// resolve a member through, so accepting it would be a half-supported keyword. The
// AllOf field remains only as an internal vehicle for bundling refs during
// normalization (see FlattenNamed); it is never populated from user JSON.
package schema

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SchemaType holds one or more JSON Schema type strings.
// A single type marshals as a JSON string; multiple types marshal as a JSON array.
type SchemaType []string

func (t SchemaType) MarshalJSON() ([]byte, error) {
	if len(t) == 1 {
		return json.Marshal(t[0])
	}
	return json.Marshal([]string(t))
}

func (t *SchemaType) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*t = SchemaType{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf("schema type must be a string or array of strings: %w", err)
	}
	*t = arr
	return nil
}

// Contains reports whether t includes the given type string.
func (t SchemaType) Contains(s string) bool {
	for _, v := range t {
		if v == s {
			return true
		}
	}
	return false
}

// allowedKeywords is the set of JSON Schema keywords accepted by node.
// "default" is the standard annotation; "secret" is a genroc extension that is only
// meaningful inside a process config_schema (it drives log redaction) and ignored
// elsewhere.
var allowedKeywords = map[string]bool{
	"type": true, "properties": true, "required": true, "items": true,
	"oneOf": true, "anyOf": true, "enum": true,
	"minimum": true, "maximum": true, "minLength": true, "maxLength": true,
	"minItems": true, "maxItems": true,
	"$ref": true, "$defs": true, "$anchor": true, "$id": true,
	"default": true, "secret": true,
	// "allOf" is intentionally omitted — see the package doc.
}

// node is the structural representation of the supported JSON Schema subset. It is
// unexported: callers hold a Raw or a Schema and use their methods. The fields stay
// exported so encoding/json (and in-package code) can reach them.
// Any JSON key absent from allowedKeywords causes an UnmarshalJSON error.
type node struct {
	Type       SchemaType       `json:"type,omitempty"`
	Properties map[string]*node `json:"properties,omitempty"`
	Required   []string         `json:"required,omitempty"`
	Items      *node            `json:"items,omitempty"`
	OneOf      []*node          `json:"oneOf,omitempty"`
	AnyOf      []*node          `json:"anyOf,omitempty"`
	AllOf      []*node          `json:"allOf,omitempty"`
	Enum       []any            `json:"enum,omitempty"`
	Minimum    *float64         `json:"minimum,omitempty"`
	Maximum    *float64         `json:"maximum,omitempty"`
	MinLength  *int             `json:"minLength,omitempty"`
	MaxLength  *int             `json:"maxLength,omitempty"`
	MinItems   *int             `json:"minItems,omitempty"`
	MaxItems   *int             `json:"maxItems,omitempty"`
	Ref        string           `json:"$ref,omitempty"`
	Defs       map[string]*node `json:"$defs,omitempty"`
	Anchor     string           `json:"$anchor,omitempty"`
	ID         string           `json:"$id,omitempty"`
	Default    any              `json:"default,omitempty"`
	Secret     bool             `json:"secret,omitempty"`
}

// UnmarshalJSON implements strict decoding: any JSON key not in allowedKeywords
// returns an error.
func (n *node) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k := range raw {
		if !allowedKeywords[k] {
			return fmt.Errorf("unsupported schema keyword %q", k)
		}
	}
	type alias node
	return json.Unmarshal(data, (*alias)(n))
}

// ─── Raw: the unnormalized document ─────────────────────────────────────────────

// Raw is an unnormalized parsed schema: it may carry nested $defs, $id resources,
// and anchor-style $refs. The only operations are Normalize (yielding the operating
// Schema type) and JSON round-tripping — a Raw cannot be validated or navigated,
// because its $refs are not yet resolved against a single root.
type Raw struct {
	n *node
}

// Parse parses a JSON-encoded schema into an unnormalized Raw, enforcing the strict
// keyword allowlist. Call Normalize on the result to obtain an operable Schema.
func Parse(data []byte) (Raw, error) {
	var n node
	if err := json.Unmarshal(data, &n); err != nil {
		return Raw{}, err
	}
	return Raw{&n}, nil
}

// Normalize flattens all $defs to the root, drops unused definitions, rewrites
// $refs to their flat locations, and returns the result as a Schema. The receiver
// is not modified.
func (r Raw) Normalize() (Schema, error) {
	cloned, err := deepClone(r.n)
	if err != nil {
		return Schema{}, err
	}
	out, err := normalize(cloned)
	if err != nil {
		return Schema{}, err
	}
	if out == nil {
		out = &node{}
	}
	return Schema{out}, nil
}

// AssumeNormalized wraps the parsed document as a Schema without normalizing it.
// It is an escape hatch for input that is known to already be in normalized form
// (defs only at the root) — e.g. a schema this package itself marshaled earlier.
// Prefer Normalize when in doubt; it is idempotent.
func (r Raw) AssumeNormalized() Schema {
	if r.n == nil {
		return Schema{&node{}}
	}
	return Schema{r.n}
}

// CheckDoc reports whether the document is structurally well-formed in the
// supported subset (see Schema.CheckDoc).
func (r Raw) CheckDoc() error {
	return checkDocRoot(r.n)
}

// MarshalJSON emits the raw schema (including any nested $defs) unchanged.
func (r Raw) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.n)
}

// UnmarshalJSON parses a schema document with the strict keyword allowlist.
func (r *Raw) UnmarshalJSON(data []byte) error {
	var n node
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	r.n = &n
	return nil
}

// JSONSchemaBytes returns a permissive JSON Schema for OpenAPI reflection.
func (Raw) JSONSchemaBytes() ([]byte, error) {
	return []byte(`{"type":"object","additionalProperties":true}`), nil
}

// ─── Schema: the normalized, operable schema ────────────────────────────────────

// Schema is a normalized JSON Schema you operate on. It wraps a root node whose
// $defs are the sole resolution context for every $ref in the tree — after
// normalization no nested node carries $defs, so there is exactly one place refs
// resolve against. All operations are methods; the node tree itself is private.
//
// Navigation (Infer/At/Property/Index) returns a Schema whose node is the type at
// the path and whose $defs are carried over from the parent, so every sub-schema
// stays fully resolvable. Builders (Object/Type/Ref/OneOf/AnyOf, WithProperty,
// WithDef, WithDefs) never mutate their receiver.
type Schema struct {
	n *node
}

// fromNode wraps an already-normalized root node as a Schema; its own $defs are
// the resolution context. A nil node becomes an empty schema. TEMPORARY migration
// shim — goes away with the SchemaNode alias.
func fromNode(n *node) Schema {
	if n == nil {
		n = &node{}
	}
	return Schema{n}
}

// wrap builds a Schema whose node is n but whose resolution context is the given
// defs map. TEMPORARY migration shim — use Schema.WithDefs / Defs instead.
func wrap(n *node, defs map[string]*node) Schema {
	if n == nil {
		return Schema{&node{Defs: defs}}
	}
	m := *n
	m.Defs = defs
	return Schema{&m}
}

// Load wraps a raw schema map as a Schema, silently dropping unrecognised keywords
// via a JSON roundtrip. Intended for programmatic construction of already-flat
// schemas; use Parse for user-supplied JSON.
func Load(raw map[string]any) Schema {
	if len(raw) == 0 {
		return Schema{&node{}}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return Schema{&node{}}
	}
	type alias node // bypass strict UnmarshalJSON
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return Schema{&node{}}
	}
	n := node(a)
	return Schema{&n}
}

// MarshalJSON emits the schema with its root $defs.
func (s Schema) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.n)
}

// UnmarshalJSON parses a schema document with the strict keyword allowlist. The
// caller is responsible for the content being normalized (this is the decode path
// for schemas this package produced — e.g. stored definitions); parse untrusted
// input via Parse + Normalize instead.
func (s *Schema) UnmarshalJSON(data []byte) error {
	var n node
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	s.n = &n
	return nil
}

// JSONSchemaBytes returns a permissive JSON Schema for OpenAPI reflection.
// The actual keyword restrictions are enforced at parse/unmarshal time, not at
// the spec level — keeping the API surface broad so callers can write schemas
// in standard JSON Schema syntax without TypeScript type errors.
func (Schema) JSONSchemaBytes() ([]byte, error) {
	return []byte(`{"type":"object","additionalProperties":true}`), nil
}

// AsMap returns the schema as a plain map. Intended for compatibility and testing;
// avoid in new code.
func (s Schema) AsMap() map[string]any {
	if s.n == nil {
		return nil
	}
	b, err := json.Marshal(s.n)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

// IsZero reports whether s is the zero Schema — `var s Schema`, no underlying
// document ("no schema declared"), as opposed to IsNull (the schema {type:"null"}).
// The name is the stdlib IsZero convention for value types, and is also the hook
// encoding/json's `omitzero` tag option calls to drop absent value fields.
func (s Schema) IsZero() bool {
	return s.n == nil
}

// Normalize re-normalizes the schema (idempotent when already normalized) and
// returns a fresh Schema. Handy when a schema was assembled from parts that may
// still carry nested defs. The receiver is not modified.
func (s Schema) Normalize() (Schema, error) {
	return Raw{s.n}.Normalize()
}

// CheckDoc reports whether the schema is structurally well-formed in the supported
// subset: every $ref resolves against the root $defs, combinator and property
// entries are non-nil, paired numeric/length/item bounds are ordered, and declared
// defaults validate against their schema.
func (s Schema) CheckDoc() error {
	return checkDocRoot(s.n)
}

// rootDefs returns the resolution context, nil-safe against a zero Schema.
func (s Schema) rootDefs() map[string]*node {
	if s.n == nil {
		return nil
	}
	return s.n.Defs
}

// ─── Navigation ─────────────────────────────────────────────────────────────────

// Infer navigates a dot-path expression (e.g. "user.issues[0].value") and returns
// the subschema for the value at that path as a Schema carrying the same root
// $defs, so the result stays navigable/validatable.
func (s Schema) Infer(path string) (Schema, error) {
	return s.subSchema(navigate(s.n, s.rootDefs(), path))
}

// At is an alias for Infer, reading better where the intent is "the schema at
// this subpath" rather than "the inferred type of this expression".
func (s Schema) At(path string) (Schema, error) {
	return s.Infer(path)
}

// Property returns the subschema for a single named property, carrying the same
// root $defs. An optional property comes back nullable, matching Infer's per-step
// semantics. It is the single-step form of Infer used by the type inferrer.
func (s Schema) Property(name string) (Schema, error) {
	return s.subSchema(lookupProperty(s.n, name, s.rootDefs()))
}

// Index returns the (nullable) element subschema for array index access, carrying
// the same root $defs. Always nullable because the index may be out of bounds.
func (s Schema) Index() (Schema, error) {
	return s.subSchema(inferIndex(s.n, s.rootDefs()))
}

// subSchema wraps a one-step navigation result as a Schema whose node is the type
// at the path and whose $defs are carried from the parent, so the sub-schema
// resolves $refs against the same root. It threads the navigation error through.
func (s Schema) subSchema(n *node, err error) (Schema, error) {
	if err != nil {
		return Schema{}, err
	}
	return wrap(n, s.rootDefs()), nil
}

// ─── Builders (immutable) ───────────────────────────────────────────────────────

// Object returns a new, empty object Schema ({"type":"object"}) to build up with
// WithProperty (and WithDef). It is the seed for assembling a context/shape schema
// declaratively rather than juggling raw property maps.
func Object() Schema {
	return Schema{&node{Type: SchemaType{"object"}}}
}

// Type returns a Schema constraining a value to the given JSON type(s),
// e.g. Type("string") or Type("string", "null").
func Type(types ...string) Schema {
	return Schema{&node{Type: SchemaType(types)}}
}

// Ref returns a Schema that is a reference to the named root definition:
// {"$ref": "#/$defs/<name>"}.
func Ref(name string) Schema {
	return Schema{&node{Ref: "#/$defs/" + name}}
}

// OneOf returns a Schema matching exactly one of the given variants.
func OneOf(variants ...Schema) Schema {
	return Schema{&node{OneOf: nodesOf(variants)}}
}

// AnyOf returns a Schema matching at least one of the given variants.
func AnyOf(variants ...Schema) Schema {
	return Schema{&node{AnyOf: nodesOf(variants)}}
}

func nodesOf(vs []Schema) []*node {
	out := make([]*node, len(vs))
	for i, v := range vs {
		out[i] = v.n
	}
	return out
}

// WithProperty returns a copy of s with property name set to sub, marking it
// required when required is true (a no-op if already required). s is treated as an
// object schema; the receiver is not modified and the root $defs are preserved.
func (s Schema) WithProperty(name string, sub Schema, required bool) Schema {
	base := s.n
	if base == nil {
		base = &node{}
	}
	n := *base
	n.Properties = make(map[string]*node, len(base.Properties)+1)
	for k, v := range base.Properties {
		n.Properties[k] = v
	}
	n.Properties[name] = sub.n
	if required && !isRequired(base, name) {
		n.Required = append(append([]string{}, base.Required...), name)
	}
	return Schema{&n}
}

// WithDef returns a new Schema with the given definition added under the root $defs.
func (s Schema) WithDef(name string, def Schema) Schema {
	cloned, _ := deepClone(s.n)
	if cloned == nil {
		cloned = &node{}
	}
	if cloned.Defs == nil {
		cloned.Defs = make(map[string]*node)
	} else {
		newDefs := make(map[string]*node, len(cloned.Defs)+1)
		for k, v := range cloned.Defs {
			newDefs[k] = v
		}
		cloned.Defs = newDefs
	}
	cloned.Defs[name] = def.n
	return Schema{cloned}
}

// ─── Defs: the shared root-definitions handle ───────────────────────────────────

// Defs is a handle over a set of root definitions. It intentionally wraps a
// SHARED, MUTABLE map: attach the same handle to several Schemas via WithDefs and
// a later Set is observed by all of them through their $refs. That aliasing is the
// mechanism the recursive output-type fixpoint drives — each pass updates a def
// in place and re-infers until the estimates stabilize.
type Defs struct {
	m map[string]*node
}

// NewDefs returns an empty, mutable definitions handle.
func NewDefs() Defs {
	return Defs{m: make(map[string]*node)}
}

// Set inserts or replaces the named definition, in place: every Schema sharing
// this handle observes the update.
func (d Defs) Set(name string, s Schema) {
	d.m[name] = s.n
}

// Get returns the named definition as it is stored — bare, without the handle
// attached. A definition's own $refs point back into this same set; attach the
// handle explicitly (WithDefs) when the result needs to resolve them.
func (d Defs) Get(name string) (Schema, bool) {
	n, ok := d.m[name]
	if !ok {
		return Schema{}, false
	}
	return Schema{n}, true
}

// Has reports whether the named definition exists.
func (d Defs) Has(name string) bool {
	_, ok := d.m[name]
	return ok
}

// Len returns the number of definitions.
func (d Defs) Len() int {
	return len(d.m)
}

// Names returns the definition names in sorted order.
func (d Defs) Names() []string {
	out := make([]string, 0, len(d.m))
	for k := range d.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// IsZero reports whether the handle holds no definitions. It deliberately treats
// an empty-but-live map the same as no map: encoding/json's `omitzero` calls this,
// and a definition-less process must omit "$defs" from its emitted schema file
// rather than write `"$defs": {}` (which a plain reflect-zero check would).
func (d Defs) IsZero() bool {
	return len(d.m) == 0
}

// MarshalJSON emits the definitions as a plain name→schema object.
func (d Defs) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.m)
}

// UnmarshalJSON parses a name→schema object with the strict keyword allowlist.
func (d *Defs) UnmarshalJSON(data []byte) error {
	var m map[string]*node
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	d.m = m
	return nil
}

// WithDefs returns a copy of s whose resolution context is the given handle's
// underlying map (shared, not copied — see Defs). An empty-but-live handle is
// attached too: the recursive fixpoint seeds definitions into it afterwards and
// the schema must observe them. Only a nil handle (zero Defs) is a no-op.
// The receiver is not modified.
func (s Schema) WithDefs(d Defs) Schema {
	if d.m == nil {
		return s
	}
	return wrap(s.n, d.m)
}

// WithMergedDefs returns a copy of s whose root $defs are the union of its own
// and the handle's — the schema's own definitions win on a name clash. Unlike
// WithDefs the maps are merged into a fresh map, so neither the receiver nor the
// handle observes later changes. A zero/empty handle is a no-op.
func (s Schema) WithMergedDefs(d Defs) Schema {
	if len(d.m) == 0 {
		return s
	}
	own := s.rootDefs()
	merged := make(map[string]*node, len(own)+len(d.m))
	for k, v := range d.m {
		merged[k] = v
	}
	for k, v := range own {
		merged[k] = v
	}
	return wrap(s.n, merged)
}

// MergeInto hoists the schema's root $defs into the handle and returns a copy of
// the schema with its refs pointing at the merged locations, leaving the copy
// itself defs-free. Collisions are resolved safely: a definition that is
// content-equal to one already in the handle (under any name) is reused, and a
// genuinely different one is renamed with a unique suffix — with every $ref in
// the returned schema and in the moved definition bodies rewritten to match.
// Existing entries in the handle always keep their names, so definitions seeded
// first (e.g. the generated schema names during process generation) take
// precedence. The receiver is not modified; the handle is mutated in place.
func (s Schema) MergeInto(d Defs) (Schema, error) {
	if s.n == nil || len(s.n.Defs) == 0 {
		return s, nil
	}
	cloned, err := deepClone(s.n)
	if err != nil {
		return Schema{}, err
	}
	moved := cloned.Defs
	cloned.Defs = nil

	// Assign a target name per definition, deterministically (sorted names).
	names := make([]string, 0, len(moved))
	for name := range moved {
		names = append(names, name)
	}
	sort.Strings(names)
	rename := make(map[string]string, len(moved))
	insert := make(map[string]*node, len(moved))
	for _, name := range names {
		def := moved[name]
		if target, ok := findEqualDef(d.m, name, def); ok {
			rename[name] = target // reuse the existing content-equal definition
			continue
		}
		newName := name
		if _, taken := d.m[name]; taken {
			newName = getUniqueName(name, d.m)
		}
		rename[name] = newName
		insert[newName] = def
		d.m[newName] = def // claim immediately so later names stay unique
	}

	// Rewrite refs in the schema body and in the moved definition bodies.
	applyRename(cloned, rename)
	for _, def := range insert {
		applyRename(def, rename)
	}
	return Schema{cloned}, nil
}

// findEqualDef returns the name of an existing definition content-equal to def,
// which is about to be inserted under name. Equality is judged modulo that
// insertion rename: a self-referencing definition must also match an earlier
// copy whose self-refs already spell the renamed name — plain textual equality
// would re-merge a recursive definition as a fresh input_1, input_2, … every
// time. (Mutually recursive definitions renamed as a group are still compared
// textually and may duplicate; only self-references are rename-normalized.)
func findEqualDef(existing map[string]*node, name string, def *node) (string, bool) {
	names := make([]string, 0, len(existing))
	for n := range existing {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, en := range names {
		if nodesEqual(existing[en], def) {
			return en, true
		}
	}
	if !referencesDef(def, name) {
		return "", false
	}
	for _, en := range names {
		if en == name {
			continue // no rename would occur; the plain comparison above covered it
		}
		clone, err := deepClone(def)
		if err != nil {
			return "", false
		}
		applyRename(clone, map[string]string{name: en})
		if nodesEqual(existing[en], clone) {
			return en, true
		}
	}
	return "", false
}

// referencesDef reports whether any $ref in the tree points at #/$defs/<name>.
func referencesDef(root *node, name string) bool {
	found := false
	walkTree(root, nil, func(nd *node, _ []string, _ string) {
		if nd.Ref == "#/$defs/"+name {
			found = true
		}
	})
	return found
}

// applyRename rewrites every "#/$defs/<old>" ref in the tree to its renamed
// location. Renames to the same name are no-ops.
func applyRename(root *node, rename map[string]string) {
	walkTree(root, nil, func(nd *node, _ []string, _ string) {
		const prefix = "#/$defs/"
		if !strings.HasPrefix(nd.Ref, prefix) {
			return
		}
		if newName, ok := rename[strings.TrimPrefix(nd.Ref, prefix)]; ok {
			nd.Ref = prefix + newName
		}
	})
}

// Flatten resolves the handle's definitions against each other — nested $defs
// hoisted, cross-refs rewritten, every named entry kept — and returns a fresh,
// flat handle. It is how a user-supplied definition pool (whose entries may
// reference one another) is brought into normal form.
func (d Defs) Flatten() (Defs, error) {
	named := make(map[string]Schema, len(d.m))
	for k, v := range d.m {
		named[k] = Schema{v} // bare nodes: cross-refs resolve at the container level
	}
	return FlattenNamed(named)
}

// JSONSchemaBytes returns a permissive JSON Schema for OpenAPI reflection.
func (Defs) JSONSchemaBytes() ([]byte, error) {
	return []byte(`{"type":"object","additionalProperties":true}`), nil
}

// DefsHandle returns a handle over the schema's own root $defs. The handle shares
// the schema's map: a Set through it is visible to the schema (and to every
// sub-schema carrying the same defs).
func (s Schema) DefsHandle() Defs {
	return Defs{m: s.rootDefs()}
}

// WithoutDefs returns a copy of s with every $defs attachment dropped — at the
// root and on any nested node. Use it when embedding a sub-schema into a
// container that owns the resolution context (its own root $defs): the embedded
// tree is re-rooted and must not carry defs copies of its own. Navigation and
// inference attach the shared root defs to the sub-schemas they hand out; if such
// a node were stored back into that same defs set, the attachment would form a
// marshal cycle — stripping deeply makes the stored form clean and finite.
// The receiver is not modified.
func (s Schema) WithoutDefs() Schema {
	return Schema{stripDefsDeep(s.n)}
}

// stripDefsDeep returns a structural copy of n with all Defs fields cleared. It
// deliberately walks the tree instead of JSON round-tripping: an attached defs
// map can reach back into this very tree, which would cycle the marshaler, while
// the walk skips Defs and therefore always terminates.
func stripDefsDeep(n *node) *node {
	if n == nil {
		return &node{}
	}
	m := *n
	m.Defs = nil
	if n.Properties != nil {
		m.Properties = make(map[string]*node, len(n.Properties))
		for k, v := range n.Properties {
			if v != nil {
				m.Properties[k] = stripDefsDeep(v)
			} else {
				m.Properties[k] = nil
			}
		}
	}
	if n.Items != nil {
		m.Items = stripDefsDeep(n.Items)
	}
	m.OneOf = stripDefsList(n.OneOf)
	m.AnyOf = stripDefsList(n.AnyOf)
	m.AllOf = stripDefsList(n.AllOf)
	return &m
}

func stripDefsList(vs []*node) []*node {
	if vs == nil {
		return nil
	}
	out := make([]*node, len(vs))
	for i, v := range vs {
		if v != nil {
			out[i] = stripDefsDeep(v)
		}
	}
	return out
}

// FlattenNamed bundles a set of named schemas — each of which may carry its own
// nested $defs — into one flat definitions set: every input becomes a root
// definition under its name (collisions suffixed), nested defs are hoisted, and
// all $refs are rewritten to the flat locations. It is the def-preparation step
// process generation runs before inference.
func FlattenNamed(named map[string]Schema) (Defs, error) {
	defs := make(map[string]*node, len(named))
	refs := make([]*node, 0, len(named))
	for name, s := range named {
		entry, err := deepClone(s.n)
		if err != nil {
			return Defs{}, err
		}
		if entry == nil {
			entry = &node{}
		}
		entry.ID = name
		defs[name] = entry
		refs = append(refs, &node{Ref: "#/$defs/" + name})
	}
	// AllOf is the internal bundling vehicle: it makes every named schema "used" so
	// normalize keeps it, while their $refs are rewritten against the merged root.
	container := &node{Defs: defs, AllOf: refs}
	normalized, err := normalize(container)
	if err != nil {
		return Defs{}, err
	}
	if normalized == nil || normalized.Defs == nil {
		return NewDefs(), nil
	}
	return Defs{m: normalized.Defs}, nil
}

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

// HasNull reports whether null is a possible type for s.
func (s Schema) HasNull() bool {
	return hasNullType(s.n)
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

// ─── internal ───────────────────────────────────────────────────────────────────

// deepClone returns a fully independent copy via JSON roundtrip.
func deepClone(n *node) (*node, error) {
	if n == nil {
		return nil, nil
	}
	b, err := json.Marshal(n)
	if err != nil {
		return nil, err
	}
	// Use alias to avoid the strict UnmarshalJSON on a round-trip of already-valid data.
	type alias node
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, err
	}
	result := node(a)
	return &result, nil
}
