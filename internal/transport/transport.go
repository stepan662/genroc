package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"gent/internal/model"
)

// Request is the message the engine sends to a service.
type Request struct {
	InstanceID string                 `json:"instance_id"`
	StepID     string                 `json:"step_id"`
	Data       map[string]interface{} `json:"data"`
}

// Response is the message the engine expects back from a service.
type Response struct {
	Status string `json:"status"` // "ok" or "error"
	Output any    `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Send dispatches a request to the appropriate endpoint based on the step's call config.
// headers contains pre-resolved header values (for rest calls).
func Send(ctx context.Context, call *model.Call, headers map[string]string, req Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	switch call.Type {
	case model.CallTypeREST:
		return sendHTTP(ctx, call.Endpoint, headers, body)
	case model.CallTypeScript:
		return sendScript(ctx, call.Exec, body)
default:
		return nil, fmt.Errorf("unknown call type: %q", call.Type)
	}
}

func sendHTTP(ctx context.Context, endpoint string, headers map[string]string, body []byte) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	var r Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode http response: %w", err)
	}
	return &r, nil
}

// sendScript runs exec via sh -c, writes newline-terminated JSON to stdin,
// and reads a newline-terminated JSON response from stdout.
func sendScript(ctx context.Context, command string, body []byte) (*Response, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(append(body, '\n'))

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("script: %w", err)
	}

	var r Response
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode script response: %w", err)
	}
	return &r, nil
}

// RetryDelay returns the backoff duration for a given retry attempt (exponential, capped at 5 min).
func RetryDelay(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * time.Second
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}
