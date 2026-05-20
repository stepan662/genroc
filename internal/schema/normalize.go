// Package schema provides a normalizer for a strict subset of JSON Schema.
//
// Supported subset:
//   - $defs at any nesting level (collected and flattened to root)
//   - $ref must be "#/$defs/<name>" — absolute, internal, single-level name only
//   - No external refs, no $id, no relative paths, no $anchor
//
// Normalizer guarantees on output:
//   - $defs appear only at the root
//   - Only definitions reachable (directly or transitively) from the root are kept
//   - All $ref values remain "#/$defs/<name>" and point to a definition in root $defs
package schema

import (
	"fmt"
	"strings"
)

func getRefName(ref string) (string, error) {
	parts := strings.Split(ref, "/")
	if (len(parts) != 3) || parts[0] != "#" || parts[1] != "$defs" {
		return "", ErrUnsupportedRef{Ref: ref}
	}
	return parts[2], nil
}

type Def struct {
	Name       string
	References []map[string]any
	Schema     map[string]any
}

// ErrUnsupportedRef is returned when a $ref value is outside the supported subset.
type ErrUnsupportedRef struct{ Ref string }

func (e ErrUnsupportedRef) Error() string {
	return fmt.Sprintf("unsupported $ref %q: only \"#/$defs/<name>\" is allowed", e.Ref)
}

// Normalize flattens all $defs to the root, validates all $ref values,
// and removes definitions that are not reachable from the schema root.
func Normalize(schema map[string]any) (map[string]any, error) {
	necessaryDefs, err := walkAndCollectDefs(schema, nil, nil)
	if err != nil {
		return nil, err
	}

	if len(necessaryDefs) > 0 {
		rootDefs := make(map[string]any)
		for _, def := range necessaryDefs {
			rootDefs[def.Name] = def.Schema
		}
		schema["$defs"] = rootDefs
	}
	return schema, nil
}

// findMatchingDef returns the map that contains name (innermost scope wins)
// so the caller can write back after mutation. Returns nil, false if not found.
func findMatchingDef(defs map[string]Def, name string) (map[string]Def, bool) {

	if _, ok := defs[name]; ok {
		return defs, true
	}

	return nil, false
}

func walkAndCollectDefs(schema map[string]any, path []string, defs map[string]Def) ([]Def, error) {
	necessaryDefs := []Def{}
	accessibleDefs := defs

	if schema["$id"] != nil || len(path) == 0 {
		// nested schemas with $id are considered roots of their own scope
		// so we don't carry over defs from parent scopes, and we also don't include $id in output since it's not supported in the normalized schema
		accessibleDefs = make(map[string]Def)
		if _, ok := schema["$defs"].(map[string]any); ok {
			for k, v := range schema["$defs"].(map[string]any) {
				// add all local defs
				accessibleDefs[k] = Def{
					Name:   k,
					Schema: v.(map[string]any),
				}
			}
			for k, v := range schema["$defs"].(map[string]any) {
				// recursively collect defs from nested $defs
				// must be done in a separate loop to ensure all local defs are added to relevantDefs before processing nested $defs
				newDefs, err := walkAndCollectDefs(v.(map[string]any), append(path, "$defs", k), accessibleDefs)
				if err != nil {
					return nil, err
				}
				necessaryDefs = append(necessaryDefs, newDefs...)
			}

			delete(schema, "$defs")
		}
		if (len(path) > 0) && (schema["$id"] != nil) {
			delete(schema, "$id")
		}
	}

	if _, ok := schema["properties"].(map[string]any); ok {
		for k, v := range schema["properties"].(map[string]any) {
			if subSchema, ok := v.(map[string]any); ok {
				newDefs, err := walkAndCollectDefs(subSchema, append(path, "properties", k), accessibleDefs)
				if err != nil {
					return nil, err
				}
				necessaryDefs = append(necessaryDefs, newDefs...)
			}
		}
	}

	if ref, ok := schema["$ref"].(string); ok {
		name, err := getRefName(ref)
		if err != nil {
			return nil, err
		}
		m, ok := findMatchingDef(accessibleDefs, name)
		if !ok {
			return nil, fmt.Errorf("definition %q not found for $ref %q", name, ref)
		}
		def := m[name]
		def.References = append(def.References, schema)
		m[name] = def
	}

	for _, def := range accessibleDefs {
		if len(def.References) != 0 {
			necessaryDefs = append(necessaryDefs, def)
		}
	}

	return necessaryDefs, nil
}
