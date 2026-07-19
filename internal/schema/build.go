package schema

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

// Array returns a new array Schema ({"type":"array","items":item}) whose elements
// conform to item. A zero item (Schema{}) yields an itemless array (any element).
// The item is embedded structurally; any root $defs it carries are dropped (it is
// expected to reference the shared pool, like MergeInto results and WithProperty subs).
func Array(item Schema) Schema {
	n := &node{Type: SchemaType{"array"}}
	if item.n != nil {
		n.Items = item.n
	}
	return Schema{n}
}

// Map returns an open-object Schema ({"type":"object","additionalProperties":sub})
// whose undeclared keys must each conform to sub (and survive normalization). Like
// Array, sub is embedded structurally; any root $defs it carries are dropped, so it
// should reference the shared pool.
func Map(sub Schema) Schema {
	n := &node{Type: SchemaType{"object"}}
	if sub.n != nil {
		n.AdditionalProperties = sub.n
	}
	return Schema{n}
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
