package api

import (
	"encoding/json"
	"strings"
	"sync"
)

var (
	specOnce  sync.Once
	specBytes []byte
)

// buildSpec generates the OpenAPI 3.0 spec entirely from the action registry.
// Adding an entry to actions.go is sufficient — no changes needed here.
func buildSpec() []byte {
	specOnce.Do(func() {
		paths := map[string]interface{}{}
		for _, a := range registry {
			op := buildOperation(a)
			entry, ok := paths[a.Path].(map[string]interface{})
			if !ok {
				entry = map[string]interface{}{}
			}
			entry[strings.ToLower(a.Method)] = op
			paths[a.Path] = entry
		}

		spec := map[string]interface{}{
			"openapi": "3.0.3",
			"info": map[string]interface{}{
				"title":       "gent",
				"description": "Minimalist business process orchestrator. HTTP endpoints are generated from the action registry.",
				"version":     "1.0.0",
			},
			"paths": paths,
		}
		specBytes, _ = json.Marshal(spec)
	})
	return specBytes
}

func buildOperation(a actionDef) map[string]interface{} {
	op := map[string]interface{}{
		"summary": a.Summary,
		"tags":    a.Tags,
	}

	// Parameters: path params (from {name} in path) + query params
	var params []interface{}
	for _, seg := range strings.Split(a.Path, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			name := seg[1 : len(seg)-1]
			params = append(params, map[string]interface{}{
				"name": name, "in": "path", "required": true,
				"schema": map[string]interface{}{"type": "string", "format": "uuid"},
			})
		}
	}
	for _, qp := range a.QueryParams {
		p := map[string]interface{}{
			"name": qp.Name, "in": "query",
			"required":    qp.Required,
			"description": qp.Desc,
			"schema":      map[string]interface{}{"type": "string"},
		}
		if len(qp.Enum) > 0 {
			p["schema"] = map[string]interface{}{"type": "string", "enum": qp.Enum}
		}
		params = append(params, p)
	}
	if len(params) > 0 {
		op["parameters"] = params
	}

	// Request body (non-GET with an example)
	if a.Method != "GET" && a.Req != nil {
		op["requestBody"] = map[string]interface{}{
			"required": true,
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"schema":  map[string]interface{}{"type": "object"},
					"example": jsonRoundtrip(a.Req),
				},
			},
		}
	}

	// Response
	respContent := map[string]interface{}{
		"application/json": map[string]interface{}{
			"schema": map[string]interface{}{
				"type":     "object",
				"required": []string{"ok"},
				"properties": map[string]interface{}{
					"ok":    map[string]interface{}{"type": "boolean"},
					"data":  map[string]interface{}{"description": "Action result"},
					"error": map[string]interface{}{"type": "string"},
				},
			},
		},
	}
	if a.Resp != nil {
		respContent["application/json"].(map[string]interface{})["example"] = map[string]interface{}{
			"ok":   true,
			"data": jsonRoundtrip(a.Resp),
		}
	}
	op["responses"] = map[string]interface{}{
		"200": map[string]interface{}{"description": "OK", "content": respContent},
		"400": map[string]interface{}{"description": "Error",
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"example": map[string]interface{}{"ok": false, "error": "error message"},
				},
			},
		},
	}

	return op
}

// jsonRoundtrip marshals v and back so it embeds cleanly as a plain JSON value.
func jsonRoundtrip(v interface{}) interface{} {
	b, _ := json.Marshal(v)
	var out interface{}
	json.Unmarshal(b, &out)
	return out
}

const swaggerUI = `<!DOCTYPE html>
<html>
<head>
  <title>gent API</title>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
<div id="swagger-ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>
  SwaggerUIBundle({
    url: "/openapi.json",
    dom_id: '#swagger-ui',
    presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset],
    layout: "BaseLayout",
    deepLinking: true,
  })
</script>
</body>
</html>`
