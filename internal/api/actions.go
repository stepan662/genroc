package api

import (
	"encoding/json"
	"net/http"

	"gent/internal/model"
)

// queryParam describes a single HTTP query parameter for docs and routing.
type queryParam struct {
	Name     string
	Desc     string
	Required bool
	Enum     []string
}

// actionDef is the single source of truth for one API action.
// It drives three things simultaneously:
//   - HTTP routing (Method + Path)
//   - TCP/UDS envelope dispatch (Name)
//   - Swagger documentation (Summary, Tags, Req, Resp, QueryParams)
type actionDef struct {
	Name        string
	Method      string
	Path        string
	Summary     string
	Tags        []string
	QueryParams []queryParam
	// Req is a concrete example of the request body (nil = no body).
	Req interface{}
	// Resp is a concrete example of the response data field.
	Resp interface{}
	// fromHTTP extracts an Envelope from an HTTP request.
	// nil = default: decode body as JSON payload.
	fromHTTP func(r *http.Request) (Envelope, error)
	// handle is the actual handler, shared by HTTP, TCP, and UDS.
	handle func(h *Handlers, env Envelope) Reply
}

// envelope builds an Envelope from an HTTP request using the action's fromHTTP func.
func (a actionDef) envelope(r *http.Request) (Envelope, error) {
	if a.fromHTTP != nil {
		return a.fromHTTP(r)
	}
	var payload json.RawMessage
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return Envelope{}, err
		}
	}
	return Envelope{Action: a.Name, Payload: payload}, nil
}

// registry is the authoritative list of all actions.
// Order here determines order in Swagger.
var registry = func() []actionDef {
	v1 := 1
	return []actionDef{
		{
			Name:    "put_definition",
			Method:  http.MethodPut,
			Path:    "/definitions",
			Summary: "Register or update a process definition",
			Tags:    []string{"Definitions"},
			Req: model.ProcessDefinition{
				Name:    "order_pipeline",
				Version: 1,
				Steps: []*model.Step{
					{
						Type: model.StepTypeTask, ID: "charge",
						Transport: model.TransportHTTP, Endpoint: "http://localhost:9001/charge",
						TimeoutMs: 5000, Retries: 3,
					},
					{
						Type: model.StepTypeConditional, ID: "check_payment",
						Condition: "context.charged == true",
						Then: []*model.Step{{
							Type: model.StepTypeTask, ID: "ship",
							Transport: model.TransportHTTP, Endpoint: "http://localhost:9002/ship",
							TimeoutMs: 3000, Retries: 2,
						}},
						Else: []*model.Step{{
							Type: model.StepTypeTask, ID: "refund",
							Transport: model.TransportHTTP, Endpoint: "http://localhost:9003/refund",
							TimeoutMs: 3000, Retries: 1,
						}},
					},
				},
			},
			Resp: map[string]interface{}{"name": "order_pipeline", "version": 1, "saved": true},
			handle: func(h *Handlers, env Envelope) Reply {
				return h.putDefinition(env.Payload)
			},
		},
		{
			Name:    "list_definitions",
			Method:  http.MethodGet,
			Path:    "/definitions",
			Summary: "List all registered process definitions",
			Tags:    []string{"Definitions"},
			Resp:    []DefinitionSummary{{Name: "order_pipeline", Version: 1}},
			fromHTTP: func(_ *http.Request) (Envelope, error) {
				return Envelope{Action: "list_definitions"}, nil
			},
			handle: func(h *Handlers, _ Envelope) Reply {
				return h.listDefinitions()
			},
		},
		{
			Name:    "start_instance",
			Method:  http.MethodPost,
			Path:    "/instances",
			Summary: "Start a new process instance (omit version to use latest)",
			Tags:    []string{"Instances"},
			Req: StartInstanceReq{
				Process: "order_pipeline",
				Version: &v1,
				Input:   &map[string]any{"order_id": 42},
			},
			Resp: StartInstanceResp{
				ID: "550e8400-e29b-41d4-a716-446655440000", Process: "order_pipeline",
				Version: 1, Status: model.StatusRunning,
			},
			handle: func(h *Handlers, env Envelope) Reply {
				return h.startInstance(env.Payload)
			},
		},
		{
			Name:   "list_instances",
			Method: http.MethodGet,
			Path:   "/instances",
			QueryParams: []queryParam{
				{Name: "status", Desc: "Filter by status", Enum: []string{"running", "completed", "failed"}},
			},
			Summary: "List process instances",
			Tags:    []string{"Instances"},
			Resp:    []InstanceStatusResp{},
			fromHTTP: func(r *http.Request) (Envelope, error) {
				b, _ := json.Marshal(ListInstancesReq{Status: r.URL.Query().Get("status")})
				return Envelope{Action: "list_instances", Payload: b}, nil
			},
			handle: func(h *Handlers, env Envelope) Reply {
				return h.listInstances(env.Payload)
			},
		},
		{
			Name:    "get_instance",
			Method:  http.MethodGet,
			Path:    "/instances/{id}",
			Summary: "Get status of a process instance",
			Tags:    []string{"Instances"},
			Resp: InstanceStatusResp{
				ID: "550e8400-e29b-41d4-a716-446655440000", Process: "order_pipeline",
				Version: 1, Status: model.StatusCompleted,
				Context: map[string]any{"order_id": 42, "charged": true},
			},
			fromHTTP: func(r *http.Request) (Envelope, error) {
				return Envelope{Action: "get_instance", ID: r.PathValue("id")}, nil
			},
			handle: func(h *Handlers, env Envelope) Reply {
				return h.getInstance(env.ID)
			},
		},
	}
}()
