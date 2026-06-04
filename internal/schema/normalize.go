package schema

import (
	"fmt"
	"sort"
	"strings"
)

type normContext struct {
	definitions map[string]*Def
	anchors     map[string]*Def
	references  []*Ref
}

// Def holds a collected definition and its eventual flattened name.
type Def struct {
	OriginalName string
	NewName      string
	Node         *SchemaNode
	Used         bool
}

// Ref holds a collected $ref and a pointer to the SchemaNode containing it
// so the value can be rewritten in place later.
type Ref struct {
	RefValue     string
	Node         *SchemaNode
	ResourceBase string
}

// ErrUnsupportedRef is returned when a $ref value is structurally outside the
// supported subset (e.g. an external URL or a relative JSON Pointer).
type ErrUnsupportedRef struct{ Ref string }

func (e ErrUnsupportedRef) Error() string {
	return fmt.Sprintf("unsupported $ref %q: must be \"#/$defs/<name>\" or \"#<anchor>\"", e.Ref)
}

// ErrUnresolvedAnchor is returned when a "#<anchor>" ref names an anchor that
// is not registered in the root resource.
type ErrUnresolvedAnchor struct{ Ref string }

func (e ErrUnresolvedAnchor) Error() string {
	anchor := strings.TrimPrefix(e.Ref, "#")
	return fmt.Sprintf("unresolved $ref %q: anchor %q is not defined in the root resource", e.Ref, anchor)
}

// ErrUnresolvedRef is returned when a "#/$defs/<name>" ref cannot be matched to
// any known definition.
type ErrUnresolvedRef struct{ Ref string }

func (e ErrUnresolvedRef) Error() string {
	return fmt.Sprintf("unresolved $ref %q: no matching definition", e.Ref)
}

// Normalize flattens all $defs to the root, removes unused definitions,
// and rewrites $ref values to point to the new flat locations.
func Normalize(schema *SchemaNode) (*SchemaNode, error) {
	if schema == nil {
		return nil, nil
	}
	ctx := &normContext{
		definitions: make(map[string]*Def),
		anchors:     make(map[string]*Def),
		references:  make([]*Ref, 0),
	}

	// Phase 1: collect all definitions, anchors, and references from the whole tree.
	walkTree(schema, nil, func(node *SchemaNode, path []string, resourceBase string) {
		if len(path) >= 2 && path[len(path)-2] == "$defs" {
			key := strings.Join(path, "/")
			if _, exists := ctx.definitions[key]; !exists {
				def := &Def{OriginalName: path[len(path)-1], Node: node}
				ctx.definitions[key] = def
				if node.Anchor != "" && resourceBase == "" {
					ctx.anchors[node.Anchor] = def
				}
			}
		} else if node.Anchor != "" && resourceBase == "" {
			key := strings.Join(append(cp(path), "$anchor", node.Anchor), "/")
			if _, exists := ctx.definitions[key]; !exists {
				def := &Def{OriginalName: node.Anchor, Node: node}
				ctx.definitions[key] = def
				ctx.anchors[node.Anchor] = def
			}
		}
		if node.Ref != "" {
			ctx.references = append(ctx.references, &Ref{
				RefValue:     node.Ref,
				Node:         node,
				ResourceBase: resourceBase,
			})
		}
	})

	// Phase 2: resolve each ref to its target definition and mark it as used.
	for _, ref := range ctx.references {
		def, err := ctx.resolveRef(ref.RefValue, ref.ResourceBase)
		if err != nil {
			return nil, err
		}
		if def != nil {
			def.Used = true
		}
	}

	// Phase 3: strip $defs, $id, and $anchor from every node in the tree.
	walkTree(schema, nil, func(node *SchemaNode, _ []string, _ string) {
		node.ID = ""
		node.Defs = nil
		node.Anchor = ""
	})
	for _, def := range ctx.definitions {
		walkTree(def.Node, nil, func(node *SchemaNode, _ []string, _ string) {
			node.ID = ""
			node.Defs = nil
			node.Anchor = ""
		})
	}

	// Build root $defs from used definitions, resolving name collisions.
	defKeys := make([]string, 0, len(ctx.definitions))
	for k, def := range ctx.definitions {
		if def.Used {
			defKeys = append(defKeys, k)
		}
	}
	sort.Strings(defKeys)
	rootDefs := make(map[string]*SchemaNode)
	for _, k := range defKeys {
		def := ctx.definitions[k]
		def.NewName = getUniqueName(def.OriginalName, rootDefs)
		rootDefs[def.NewName] = def.Node
	}

	// Rewrite $ref values to the new flat paths.
	for _, ref := range ctx.references {
		def, _ := ctx.resolveRef(ref.RefValue, ref.ResourceBase)
		if def != nil && def.Used {
			ref.Node.Ref = "#/$defs/" + def.NewName
		}
	}

	if len(rootDefs) > 0 {
		schema.Defs = rootDefs
	}
	return schema, nil
}

// walkTree calls fn(node, path, resourceBase) for the given node and all nested
// schema nodes, covering every keyword in SchemaNode that can contain sub-schemas.
func walkTree(node *SchemaNode, path []string, fn func(*SchemaNode, []string, string)) {
	var walk func(*SchemaNode, []string, string)
	walk = func(n *SchemaNode, p []string, resourceBase string) {
		if n == nil {
			return
		}
		if n.ID != "" && len(p) > 0 {
			resourceBase = strings.Join(p, "/")
		}
		fn(n, p, resourceBase)
		for name, prop := range n.Properties {
			if prop != nil {
				walk(prop, append(cp(p), "properties", name), resourceBase)
			}
		}
		for name, def := range n.Defs {
			if def != nil {
				walk(def, append(cp(p), "$defs", name), resourceBase)
			}
		}
		if n.Items != nil {
			walk(n.Items, append(cp(p), "items"), resourceBase)
		}
		for i, s := range n.OneOf {
			if s != nil {
				walk(s, append(cp(p), "oneOf", fmt.Sprintf("%d", i)), resourceBase)
			}
		}
		for i, s := range n.AnyOf {
			if s != nil {
				walk(s, append(cp(p), "anyOf", fmt.Sprintf("%d", i)), resourceBase)
			}
		}
		for i, s := range n.AllOf {
			if s != nil {
				walk(s, append(cp(p), "allOf", fmt.Sprintf("%d", i)), resourceBase)
			}
		}
	}
	walk(node, path, "")
}

func (ctx *normContext) resolveRef(ref string, resourceBase string) (*Def, error) {
	if strings.HasPrefix(ref, "#/$defs/") {
		path := strings.TrimPrefix(ref, "#/")
		def := ctx.resolveDef(path, resourceBase)
		if def == nil {
			return nil, ErrUnresolvedRef{Ref: ref}
		}
		return def, nil
	}
	if strings.HasPrefix(ref, "#") && !strings.HasPrefix(ref, "#/") {
		anchor := strings.TrimPrefix(ref, "#")
		def := ctx.anchors[anchor]
		if def == nil {
			return nil, ErrUnresolvedAnchor{Ref: ref}
		}
		return def, nil
	}
	return nil, ErrUnsupportedRef{Ref: ref}
}

func (ctx *normContext) resolveDef(path string, resourceBase string) *Def {
	if def, ok := ctx.definitions[path]; ok {
		return def
	}
	if resourceBase != "" {
		if def, ok := ctx.definitions[resourceBase+"/"+path]; ok {
			return def
		}
	}
	return nil
}

func getUniqueName(name string, existing map[string]*SchemaNode) string {
	newName := name
	for i := 1; existing[newName] != nil; i++ {
		newName = fmt.Sprintf("%s_%d", name, i)
	}
	return newName
}

// cp returns a shallow copy of a string slice to avoid append aliasing.
func cp(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	return out
}
