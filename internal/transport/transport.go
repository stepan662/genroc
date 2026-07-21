package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"genroc/internal/numeric"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"genroc/internal/model"
)

// Identity headers genroc stamps on every fetch request so the receiving service can
// correlate a call back to the instance/task that made it — the context the request
// body used to carry as an envelope before fetch switched to a raw body.
const (
	HeaderInstanceID = "X-Genroc-Instance-Id"
	HeaderTaskID     = "X-Genroc-Task-Id"
)

// Response carries the result of a Send call.
// ErrorCode is non-empty on failure ("http.404", "output.parse", "start.error", etc.).
// ErrorMessage is a human-readable description of the failure (may include trimmed response body).
// Body holds the raw decoded JSON body on success.
// Status is the HTTP status code for a REST call (success or failure); 0 for non-HTTP transports.
type Response struct {
	Body         any
	ErrorCode    string
	ErrorMessage string
	Status       int
}

// Send dispatches a fetch HTTP request. url, method, and headers are pre-resolved; body is
// the raw payload — an object is marshaled to JSON, a string sent as-is, nil sends no body.
func Send(ctx context.Context, call *model.Action, url, method string, headers map[string]string, body any) (*Response, error) {
	switch call.Type {
	case model.ActionTypeFetch:
		return sendHTTP(ctx, url, method, call.AcceptedStatus, headers, body)
	default:
		return nil, fmt.Errorf("unknown call type: %q", call.Type)
	}
}

func sendHTTP(ctx context.Context, url, method string, acceptedStatus []string, headers map[string]string, body any) (*Response, error) {
	if method == "" {
		method = http.MethodPost
	}
	var bodyReader io.Reader
	jsonBody := false
	if body != nil && methodAllowsBody(method) {
		switch b := body.(type) {
		case string:
			bodyReader = strings.NewReader(b)
		default:
			raw, err := json.Marshal(b)
			if err != nil {
				return nil, fmt.Errorf("marshal body: %w", err)
			}
			bodyReader = bytes.NewReader(raw)
			jsonBody = true
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build http request: %w", err)
	}
	// Default JSON content type for an object body; a header may override it.
	if jsonBody {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err // caller uses ClassifyGoError
	}
	defer resp.Body.Close()

	if !matchAcceptedStatus(resp.StatusCode, acceptedStatus) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = fmt.Sprintf("request failed with status %d without response body", resp.StatusCode)
		}
		return &Response{ErrorCode: fmt.Sprintf("http.%d", resp.StatusCode), ErrorMessage: msg, Status: resp.StatusCode}, nil
	}

	var b any
	if err := numeric.DecodeReader(resp.Body, &b); err != nil {
		return &Response{ErrorCode: "output.parse", Status: resp.StatusCode}, nil
	}
	return &Response{Body: b, Status: resp.StatusCode}, nil
}

func methodAllowsBody(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead:
		return false
	}
	return true
}

// matchAcceptedStatus reports whether code is covered by patterns — "2xx".."5xx"
// hundred-ranges or exact 3-digit strings like "404". Empty patterns accepts any 2xx.
func matchAcceptedStatus(code int, patterns []string) bool {
	if len(patterns) == 0 {
		return code >= 200 && code <= 299
	}
	for _, p := range patterns {
		if len(p) == 3 && p[1] == 'x' && p[2] == 'x' {
			hundreds := int(p[0]-'0') * 100
			if code >= hundreds && code <= hundreds+99 {
				return true
			}
			continue
		}
		if n, err := strconv.Atoi(p); err == nil && n == code {
			return true
		}
	}
	return false
}

// ClassifyGoError maps a transport-level Go error (a REST call that never got an HTTP
// response) to an error code: pre.timeout / pre.error for a failure during the dial phase
// (server never received the request), http.timeout when the connection was established
// but no response arrived in time.
func ClassifyGoError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		var netErr *net.OpError
		if errors.As(err, &netErr) && netErr.Op == "dial" {
			return "pre.timeout"
		}
		return "http.timeout"
	}
	return "pre.error"
}

// RetryDelay returns the backoff duration for a given retry attempt (exponential, capped at 5 min).
func RetryDelay(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * time.Second
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

// MatchCode reports whether the error code s matches the pattern p. '%' is the only
// wildcard: it matches any sequence of characters (including none). Every other character
// is literal — in particular '_' and '.' match themselves, because both are ordinary
// characters in an error code (snake_case, and dotted namespaces like http.500 /
// order.rejected). This is deliberately NOT full SQL LIKE: LIKE's '_' single-char wildcard
// is a footgun for codes that contain underscores, so `order_%` matches `order_placed` but
// not `order.placed`.
func MatchCode(p, s string) bool {
	for len(p) > 0 {
		switch p[0] {
		case '%':
			p = p[1:]
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if MatchCode(p, s[i:]) {
					return true
				}
			}
			return false
		default:
			if len(s) == 0 || p[0] != s[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}

// ValidLikePattern reports whether p is a valid SQL LIKE pattern — for now just a
// non-empty check.
func ValidLikePattern(p string) bool {
	return len(strings.TrimSpace(p)) > 0
}
