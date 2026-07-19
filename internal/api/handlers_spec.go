package api

import (
	"encoding/json"
	"fmt"
	"maps"
)

// ProcessSpec returns the full OpenAPI spec with POST /instances' input schema patched to
// the given process definition (left as `any` when it has no input_schema).
func (h *Handlers) ProcessSpec(name string, version int) ([]byte, error) {
	if version == 0 {
		v, err := h.resolveDefaultVersion(name)
		if err != nil {
			return nil, err
		}
		version = v
	}
	def, err := h.db.GetDefinition(name, version)
	if err != nil {
		return nil, err
	}

	// Deep-copy the shared spec so we can mutate freely.
	var spec map[string]any
	if err := json.Unmarshal(buildSpec(), &spec); err != nil {
		return nil, err
	}

	// Update info to reflect the specific process.
	spec["info"] = map[string]any{
		"title":   fmt.Sprintf("%s v%d", def.Name, version),
		"version": fmt.Sprintf("%d", version),
	}

	// Patch ApiStartInstanceReq.properties.input with the process's input_schema.
	if def.InputSchema != nil {
		if err := patchInputSchema(spec, def.InputSchema); err != nil {
			return nil, err
		}
	}

	return json.MarshalIndent(spec, "", "  ")
}

func patchInputSchema(spec map[string]any, inputSchema any) error {
	components, ok := spec["components"].(map[string]any)
	if !ok {
		return fmt.Errorf("spec missing components")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		return fmt.Errorf("spec missing components.schemas")
	}
	reqSchema, ok := schemas["ApiStartInstanceReq"].(map[string]any)
	if !ok {
		return fmt.Errorf("spec missing ApiStartInstanceReq schema")
	}
	props, ok := reqSchema["properties"].(map[string]any)
	if !ok {
		return fmt.Errorf("ApiStartInstanceReq missing properties")
	}

	// Marshal the typed schema node to a plain map for OpenAPI spec injection.
	b, err := json.Marshal(inputSchema)
	if err != nil {
		return fmt.Errorf("marshal input schema: %w", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(b, &asMap); err != nil {
		return fmt.Errorf("unmarshal input schema: %w", err)
	}
	if asMap["$id"] == nil {
		asMap = maps.Clone(asMap)
		asMap["$id"] = "instance_input_schema"
	}
	props["input"] = asMap
	reqSchema["required"] = []string{"process", "input"}
	return nil
}
