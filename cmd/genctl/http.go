package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// callGet sends a GET request with no body and decodes the response into out.
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
		return json.Unmarshal(raw, out)
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

// listAll fetches every page of a list endpoint, following page.after until it is
// absent (set only while more rows remain), and returns the concatenated items.
// base is the request URL without an after cursor (it may already carry other
// query params).
func listAll[T any](base string) ([]T, error) {
	var all []T
	after := ""
	for {
		u := base
		if after != "" {
			sep := "?"
			if strings.Contains(u, "?") {
				sep = "&"
			}
			u += sep + "after=" + url.QueryEscape(after)
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

// printListJSON writes a list endpoint's items as an indented JSON array to stdout.
// This is the one standard --json output shared by every list command: lossless
// (each item verbatim from the server, not a re-marshaled subset) and honoring the
// same paging as the table — a single --limit page, or every page when all is true.
// See the "genctl command conventions" note above main.
func printListJSON(u string, all bool) {
	var items []json.RawMessage
	var err error
	if all {
		items, err = listAll[json.RawMessage](u)
	} else {
		var p page[json.RawMessage]
		if err = callGet(u, &p); err == nil {
			items = p.Items
		}
	}
	if err != nil {
		fatal("%v", err)
	}
	if items == nil {
		items = []json.RawMessage{} // render an empty result as [] rather than null
	}
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		fatal("%v", err)
	}
	os.Stdout.Write(b)
	os.Stdout.Write([]byte("\n"))
}

// call sends body as JSON to url and decodes the response into out.
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
		return json.Unmarshal(raw, out)
	}
	return nil
}
