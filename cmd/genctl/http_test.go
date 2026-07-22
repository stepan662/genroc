package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
)

type idRow struct {
	ID string `json:"id"`
}

func rowIDs(rows []idRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

// pagedServer serves `total` synthetic items (i0 = newest … i{total-1} = oldest) as
// keyset pages, the way the real list endpoints do: it honors order/limit/after and
// caps a single page at pageCap. The after cursor is the index of the next item.
// It rejects order != desc so the test also proves listNewest asks for newest-first.
func pagedServer(total, pageCap int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("order") != "desc" {
			http.Error(w, `{"error":"expected order=desc"}`, http.StatusBadRequest)
			return
		}
		start := 0
		if a := q.Get("after"); a != "" {
			start, _ = strconv.Atoi(a)
		}
		limit := pageCap
		if l, err := strconv.Atoi(q.Get("limit")); err == nil && l < limit {
			limit = l
		}
		end := min(start+limit, total)
		items := make([]json.RawMessage, 0, end-start)
		for i := start; i < end; i++ {
			items = append(items, json.RawMessage(fmt.Sprintf(`{"id":"i%d"}`, i)))
		}
		after := ""
		if end < total {
			after = strconv.Itoa(end)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": items,
			"page":  map[string]any{"after": after},
		})
	}))
}

func TestListNewest(t *testing.T) {
	tests := []struct {
		name           string
		total, pageCap int
		limit          int
		want           []string
	}{
		{"single page, limit within page", 5, 100, 3, []string{"i0", "i1", "i2"}},
		{"crosses page boundaries to fill the limit", 5, 2, 5, []string{"i0", "i1", "i2", "i3", "i4"}},
		{"stops at the limit mid-source", 10, 2, 3, []string{"i0", "i1", "i2"}},
		{"exact page multiple", 4, 2, 4, []string{"i0", "i1", "i2", "i3"}},
		{"limit exceeds source — stops when exhausted", 3, 2, 10, []string{"i0", "i1", "i2"}},
		{"empty source", 0, 2, 5, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := pagedServer(tt.total, tt.pageCap)
			defer ts.Close()

			rows, err := listNewest[idRow](ts.URL, tt.limit)
			if err != nil {
				t.Fatalf("listNewest: %v", err)
			}
			if got := rowIDs(rows); !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// A well-behaved server never returns more than the requested limit, but listNewest
// truncates defensively in case one does — verify the guard so an over-eager page can
// never leak past --limit.
func TestListNewestTruncatesOverfetch(t *testing.T) {
	// Ignore the requested per-page limit and always hand back a full 4-item page.
	overfetch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := 0
		if a := r.URL.Query().Get("after"); a != "" {
			start, _ = strconv.Atoi(a)
		}
		end := min(start+4, 10)
		items := make([]json.RawMessage, 0, end-start)
		for i := start; i < end; i++ {
			items = append(items, json.RawMessage(fmt.Sprintf(`{"id":"i%d"}`, i)))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": items,
			"page":  map[string]any{"after": strconv.Itoa(end)},
		})
	}))
	defer overfetch.Close()

	rows, err := listNewest[idRow](overfetch.URL, 3)
	if err != nil {
		t.Fatalf("listNewest: %v", err)
	}
	if got := rowIDs(rows); !slices.Equal(got, []string{"i0", "i1", "i2"}) {
		t.Fatalf("got %v, want the first 3 (truncated)", got)
	}
}
