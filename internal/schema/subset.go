package schema

import (
	"encoding/json"
	"strings"
)

// IsSubset reports whether every value valid under sub is also valid under super.
// Both schemas must be normalized (flat $defs at root, only #/$defs/<name> refs).
func IsSubset(sub, super map[string]any) bool {
	subDefs, _ := sub["$defs"].(map[string]any)
	superDefs, _ := super["$defs"].(map[string]any)
	ctx := &subsetCtx{
		subDefs:   subDefs,
		superDefs: superDefs,
		visiting:  make(map[string]bool),
	}
	return ctx.check(sub, super)
}

type subsetCtx struct {
	subDefs   map[string]any
	superDefs map[string]any
	visiting  map[string]bool
}

func (ctx *subsetCtx) check(sub, super map[string]any) bool {
	// Cycle detection before any deref — keyed on $ref identity.
	subRef, _ := sub["$ref"].(string)
	superRef, _ := super["$ref"].(string)
	if subRef != "" || superRef != "" {
		key := subRef + "||" + superRef
		if ctx.visiting[key] {
			return true // optimistic: assume subset while in recursive check
		}
		ctx.visiting[key] = true
		defer delete(ctx.visiting, key)
	}

	// Resolve $refs.
	sub = derefSubset(sub, ctx.subDefs)
	super = derefSubset(super, ctx.superDefs)

	// {} accepts anything.
	if len(super) == 0 {
		return true
	}
	if len(sub) == 0 {
		return false
	}

	// Composition in sub (anyOf / oneOf): every variant must be ⊆ super.
	for _, kw := range []string{"anyOf", "oneOf"} {
		if variants, ok := sub[kw].([]any); ok {
			for _, v := range variants {
				vs, ok := v.(map[string]any)
				if !ok || !ctx.check(vs, super) {
					return false
				}
			}
			return true
		}
	}

	// allOf in sub: the conjunction is at least as restrictive as each individual
	// constraint, so if any single constraint is ⊆ super then the allOf is too.
	if allOf, ok := sub["allOf"].([]any); ok {
		for _, v := range allOf {
			if vs, ok := v.(map[string]any); ok && ctx.check(vs, super) {
				return true
			}
		}
		return false
	}

	// Composition in super: sub must fit at least one anyOf/oneOf variant.
	for _, kw := range []string{"anyOf", "oneOf"} {
		if variants, ok := super[kw].([]any); ok {
			for _, v := range variants {
				if vs, ok := v.(map[string]any); ok && ctx.check(sub, vs) {
					return true
				}
			}
			return false
		}
	}

	// allOf in super: sub must satisfy every constraint.
	if allOf, ok := super["allOf"].([]any); ok {
		for _, v := range allOf {
			vs, ok := v.(map[string]any)
			if !ok || !ctx.check(sub, vs) {
				return false
			}
		}
		return true
	}

	// Type compatibility: every type sub allows must be permitted by super.
	subTypes := schemaTypes(sub)
	superTypes := schemaTypes(super)
	if len(superTypes) > 0 {
		if len(subTypes) == 0 {
			return false
		}
		for _, st := range subTypes {
			if !typeAllowed(st, superTypes) {
				return false
			}
		}
	}

	// Structural checks — triggered by the presence of keywords in super.
	if super["properties"] != nil || super["required"] != nil || super["additionalProperties"] != nil {
		if !ctx.checkObject(sub, super) {
			return false
		}
	}
	if super["items"] != nil {
		if !ctx.checkArray(sub, super) {
			return false
		}
	}
	if super["minimum"] != nil || super["maximum"] != nil {
		if !checkNumericBounds(sub, super) {
			return false
		}
	}
	if super["minLength"] != nil || super["maxLength"] != nil {
		if !checkStringLength(sub, super) {
			return false
		}
	}
	if super["enum"] != nil {
		if !checkEnum(sub, super) {
			return false
		}
	}

	return true
}

// schemaTypes returns all type strings declared by s's "type" field.
func schemaTypes(s map[string]any) []string {
	switch t := s["type"].(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, v := range t {
			if str, ok := v.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// typeAllowed reports whether subType is permitted by superTypes.
// integer satisfies a super that includes number (numeric widening).
func typeAllowed(subType string, superTypes []string) bool {
	for _, st := range superTypes {
		if st == subType || (subType == "integer" && st == "number") {
			return true
		}
	}
	return false
}

func (ctx *subsetCtx) checkObject(sub, super map[string]any) bool {
	superProps, _ := super["properties"].(map[string]any)
	subProps, _ := sub["properties"].(map[string]any)
	superReq := stringSet(super["required"])
	subReq := stringSet(sub["required"])

	// Every field super requires must also be required by sub.
	for f := range superReq {
		if !subReq[f] {
			return false
		}
	}

	// For each property defined in both schemas, sub's schema must be ⊆ super's.
	for name, superPropAny := range superProps {
		superProp, ok := superPropAny.(map[string]any)
		if !ok {
			continue
		}
		subPropAny, exists := subProps[name]
		if !exists {
			continue // super defines the property but sub doesn't constrain it
		}
		subProp, ok := subPropAny.(map[string]any)
		if !ok {
			return false
		}
		if !ctx.check(subProp, superProp) {
			return false
		}
	}

	// additionalProperties in super restricts what sub can declare beyond superProps.
	switch ap := super["additionalProperties"].(type) {
	case bool:
		if !ap {
			for name := range subProps {
				if _, inSuper := superProps[name]; !inSuper {
					return false
				}
			}
		}
	case map[string]any:
		for name, subPropAny := range subProps {
			if _, inSuper := superProps[name]; inSuper {
				continue
			}
			subProp, ok := subPropAny.(map[string]any)
			if !ok {
				return false
			}
			if !ctx.check(subProp, ap) {
				return false
			}
		}
	}

	return true
}

func (ctx *subsetCtx) checkArray(sub, super map[string]any) bool {
	superItems, ok := super["items"].(map[string]any)
	if !ok {
		return true
	}
	subItems, ok := sub["items"].(map[string]any)
	if !ok {
		return false // super constrains element type, sub doesn't
	}
	return ctx.check(subItems, superItems)
}

func checkNumericBounds(sub, super map[string]any) bool {
	if superMin, ok := toFloat(super["minimum"]); ok {
		subMin, has := toFloat(sub["minimum"])
		if !has || subMin < superMin {
			return false
		}
	}
	if superMax, ok := toFloat(super["maximum"]); ok {
		subMax, has := toFloat(sub["maximum"])
		if !has || subMax > superMax {
			return false
		}
	}
	return true
}

func checkStringLength(sub, super map[string]any) bool {
	if superMin, ok := toFloat(super["minLength"]); ok {
		subMin, has := toFloat(sub["minLength"])
		if !has || subMin < superMin {
			return false
		}
	}
	if superMax, ok := toFloat(super["maxLength"]); ok {
		subMax, has := toFloat(sub["maxLength"])
		if !has || subMax > superMax {
			return false
		}
	}
	return true
}

func checkEnum(sub, super map[string]any) bool {
	superEnum, ok := super["enum"].([]any)
	if !ok {
		return true
	}
	subEnum, ok := sub["enum"].([]any)
	if !ok {
		return false
	}
	superSet := make(map[string]bool, len(superEnum))
	for _, v := range superEnum {
		superSet[jsonKey(v)] = true
	}
	for _, v := range subEnum {
		if !superSet[jsonKey(v)] {
			return false
		}
	}
	return true
}

func jsonKey(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func stringSet(v any) map[string]bool {
	// Handle both []any (from json.Unmarshal) and []string (from programmatic schema construction).
	switch arr := v.(type) {
	case []any:
		if len(arr) == 0 {
			return nil
		}
		out := make(map[string]bool, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				out[s] = true
			}
		}
		return out
	case []string:
		if len(arr) == 0 {
			return nil
		}
		out := make(map[string]bool, len(arr))
		for _, s := range arr {
			out[s] = true
		}
		return out
	}
	return nil
}

// derefSubset resolves a $ref in s against defs, returning the target schema.
// Returns s unchanged if no $ref is present or resolution fails.
func derefSubset(s map[string]any, defs map[string]any) map[string]any {
	ref, ok := s["$ref"].(string)
	if !ok || defs == nil {
		return s
	}
	const prefix = "#/$defs/"
	if !strings.HasPrefix(ref, prefix) {
		return s
	}
	if target, ok := defs[strings.TrimPrefix(ref, prefix)].(map[string]any); ok {
		return target
	}
	return s
}
