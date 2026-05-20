package schema

import (
	"encoding/json"
	"testing"
)

func schema(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("invalid test schema: %v", err)
	}
	return m
}

func mustNormalize(t *testing.T, s map[string]any) map[string]any {
	t.Helper()
	out, err := Normalize(s)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return out
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}

func TestNormalize_noRefs(t *testing.T) {
	in := schema(t, `{"type":"object","properties":{"name":{"type":"string"}}}`)
	out := mustNormalize(t, in)
	if _, ok := out["$defs"]; ok {
		t.Error("expected no $defs in output")
	}
	if out["type"] != "object" {
		t.Error("expected type object")
	}
}

func TestNormalize_flattenNestedDefs(t *testing.T) {
	// $defs nested inside a property definition should be moved to root.
	in := schema(t, `{
		"type": "object",
		"properties": {
			"order": {"$ref": "#/$defs/Order"}
		},
		"$defs": {
			"Order": {
				"$id": "Order",
				"type": "object",
				"properties": {"item": {"$ref": "#/$defs/Item"}},
				"$defs": {
					"Item": {"type": "object", "properties": {"name": {"type": "string"}}}
				}
			}
		}
	}`)
	out := mustNormalize(t, in)

	defs, ok := out["$defs"].(map[string]any)
	if !ok {
		t.Fatal("expected $defs at root")
	}
	if _, ok := defs["Order"]; !ok {
		t.Error("expected Order in root $defs")
	}
	if _, ok := defs["Item"]; !ok {
		t.Error("expected Item in root $defs")
	}

	// Nested $defs should be gone from Order.
	order := defs["Order"].(map[string]any)
	if _, ok := order["$defs"]; ok {
		t.Error("nested $defs should be removed from Order")
	}
}

func TestNormalize_pruneUnused(t *testing.T) {
	in := schema(t, `{
		"type": "object",
		"properties": {"a": {"$ref": "#/$defs/Used"}},
		"$defs": {
			"Used": {"type": "string"},
			"Unused": {"type": "integer"}
		}
	}`)
	out := mustNormalize(t, in)

	defs := out["$defs"].(map[string]any)
	if _, ok := defs["Used"]; !ok {
		t.Error("Used should be kept")
	}
	if _, ok := defs["Unused"]; ok {
		t.Error("Unused should be pruned")
	}
}

func TestNormalize_transitiveRefs(t *testing.T) {
	in := schema(t, `{
		"$ref": "#/$defs/A",
		"$defs": {
			"A": {"$ref": "#/$defs/B"},
			"B": {"type": "string"},
			"Unreachable": {"type": "boolean"}
		}
	}`)
	out := mustNormalize(t, in)

	defs := out["$defs"].(map[string]any)
	if _, ok := defs["A"]; !ok {
		t.Error("A should be kept")
	}
	if _, ok := defs["B"]; !ok {
		t.Error("B should be kept (transitively reachable)")
	}
	if _, ok := defs["Unreachable"]; ok {
		t.Error("Unreachable should be pruned")
	}
}

func TestNormalize_unsupportedRef(t *testing.T) {
	in := schema(t, `{"$ref": "https://example.com/schema"}`)
	_, err := Normalize(in)
	if err == nil {
		t.Fatal("expected error for external ref")
	}
}

func TestNormalize_relativeRefRejected(t *testing.T) {
	in := schema(t, `{"$ref": "#/properties/foo"}`)
	_, err := Normalize(in)
	if err == nil {
		t.Fatal("expected error for relative ref")
	}
}
