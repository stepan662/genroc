package schema

// ─── Builders (immutable) ───────────────────────────────────────────────────────

func Object() Schema {
	return Schema{&node{Type: SchemaType{"object"}}}
}

func Type(types ...string) Schema {
	return Schema{&node{Type: SchemaType(types)}}
}

// Array returns an array Schema whose elements conform to item; a zero item yields
// an itemless array (any element). item is embedded structurally — any root $defs it
// carries are dropped, so it must reference the shared pool.
func Array(item Schema) Schema {
	n := &node{Type: SchemaType{"array"}}
	if item.n != nil {
		n.Items = item.n
	}
	return Schema{n}
}

// Map returns an open-object Schema whose undeclared keys must each conform to sub.
// Like Array, sub is embedded structurally — any root $defs it carries are dropped,
// so it should reference the shared pool.
func Map(sub Schema) Schema {
	n := &node{Type: SchemaType{"object"}}
	if sub.n != nil {
		n.AdditionalProperties = sub.n
	}
	return Schema{n}
}

func Ref(name string) Schema {
	return Schema{&node{Ref: "#/$defs/" + name}}
}

func OneOf(variants ...Schema) Schema {
	return Schema{&node{OneOf: nodesOf(variants)}}
}

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

// WithProperty returns a copy of s (treated as an object schema) with property name
// set to sub; required adds it to the required list if not already there. Root $defs
// are preserved.
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
