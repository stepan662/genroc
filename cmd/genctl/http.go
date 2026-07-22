package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"genroc/internal/numeric"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func callGet(url string, out any) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("connect to server: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &errResp); err != nil {
			return fmt.Errorf("server error (status %d)", resp.StatusCode)
		}
		return fmt.Errorf("server: %s", errResp.Error)
	}
	if out != nil {
		// Exact literals: a plain Unmarshal would round a large id back through
		// float64 purely for display, making the CLI disagree with the value the
		// server actually holds.
		return numeric.Decode(raw, out)
	}
	return nil
}

// page is the {items, page:{...}} envelope every list endpoint now returns.
type page[T any] struct {
	Items []T `json:"items"`
	Page  struct {
		After string `json:"after"`
	} `json:"page"`
}

// appendQuery adds one query parameter to a URL that may already carry a query string.
func appendQuery(u, key, val string) string {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + key + "=" + url.QueryEscape(val)
}

// listAll fetches every page of a list endpoint, following page.after until absent
// (set only while more rows remain). base must omit an after cursor.
func listAll[T any](base string) ([]T, error) {
	var all []T
	after := ""
	for {
		u := base
		if after != "" {
			u = appendQuery(u, "after", after)
		}
		var p page[T]
		if err := callGet(u, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Items...)
		if p.Page.After == "" {
			return all, nil
		}
		after = p.Page.After
	}
}

// listNewest fetches up to limit of the most-recent items from a list endpoint,
// following page.after across pages until it has that many (or the source runs out).
// It requests order=desc (newest first); base carries only filters/sort and must omit
// order/limit/after. Items come back newest-first — callers reverse them for display
// so the newest row lands at the bottom, nearest the prompt (tail-style).
func listNewest[T any](base string, limit int) ([]T, error) {
	all := make([]T, 0, limit)
	after := ""
	for len(all) < limit {
		u := appendQuery(base, "order", "desc")
		u = appendQuery(u, "limit", strconv.Itoa(limit-len(all)))
		if after != "" {
			u = appendQuery(u, "after", after)
		}
		var p page[T]
		if err := callGet(u, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Items...)
		if p.Page.After == "" || len(p.Items) == 0 {
			break
		}
		after = p.Page.After
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// printJSONItems writes items as an indented JSON array — the shared, lossless --json
// output. An empty result renders as [] rather than null.
func printJSONItems(items []json.RawMessage) {
	if items == nil {
		items = []json.RawMessage{}
	}
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		fatal("%v", err)
	}
	os.Stdout.Write(b)
	os.Stdout.Write([]byte("\n"))
}

func call(url, method string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect to server: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &errResp); err != nil {
			return fmt.Errorf("server error (status %d)", resp.StatusCode)
		}
		return fmt.Errorf("server: %s", errResp.Error)
	}
	if out != nil {
		// Exact literals: a plain Unmarshal would round a large id back through
		// float64 purely for display, making the CLI disagree with the value the
		// server actually holds.
		return numeric.Decode(raw, out)
	}
	return nil
}
