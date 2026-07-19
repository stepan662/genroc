package api

import (
	"context"
	"encoding/json"
	"fmt"

	"genroc/internal/db"
	"genroc/internal/model"
)

const defaultChannel = "latest"

// engineService is the slice of the engine the API depends on: triggering a tick
// and recording the instance_created audit milestone for a root instance.
type engineService interface {
	Tick(ctx context.Context) (int, error)
	ManualTick() bool
	AuditCreated(inst *model.ProcessInstance)
	NotifyWork()
}

// Handlers holds business logic for all API operations.
type Handlers struct {
	db     *db.DB
	engine engineService
}

func NewHandlers(database *db.DB, eng engineService) *Handlers {
	return &Handlers{db: database, engine: eng}
}

// --- Envelope ---

type Envelope struct {
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload"`
	// For GET-style actions that only need an ID.
	ID string `json:"id,omitempty"`
}

type Reply struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Handle is the single entry-point shared by all transports (HTTP, TCP, UDS); it
// dispatches to the matching action in the registry (actions.go).
func (h *Handlers) Handle(env Envelope) Reply {
	for i := range registry {
		if registry[i].Name == env.Action {
			return registry[i].handle(h, env)
		}
	}
	return errReply(fmt.Errorf("unknown action %q", env.Action))
}

func okReply(v interface{}) Reply {
	data, _ := json.Marshal(v)
	return Reply{OK: true, Data: data}
}

func errReply(err error) Reply {
	return Reply{OK: false, Error: err.Error()}
}

// decodeBody unmarshals a required JSON body into T; an empty or malformed body is an
// error wrapped with the "decode:" prefix.
func decodeBody[T any](raw json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("decode: %w", err)
	}
	return v, nil
}

// decodeOptionalBody best-effort unmarshals an optional JSON body into T: an empty body
// yields the zero T and a malformed body is ignored.
func decodeOptionalBody[T any](raw json.RawMessage) T {
	var v T
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}
