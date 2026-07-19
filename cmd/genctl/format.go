package main

import (
	"fmt"
	"time"
)

// shortID returns a compact, distinguishing tag for an instance id in tree-log
// display. It uses the id's tail, not its head: instance ids are UUIDv7s whose
// leading bits are a millisecond timestamp, so a parent and a child spawned in the
// same millisecond share a long prefix — the random tail is what tells them apart.
func shortID(id string) string {
	if len(id) > 6 {
		return id[len(id)-6:]
	}
	return id
}

// ── time formatting ─────────────────────────────────────────────────────────────

// parseTime parses an RFC3339(/Nano) timestamp and converts it to local time.
func parseTime(rfc string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, rfc); err == nil {
			return t.Local(), true
		}
	}
	return time.Time{}, false
}

// shortTime renders a timestamp compactly for list columns: a relative age for
// recent times ("just now", "5m ago", "3h ago", "2d ago"), or a short absolute
// "YY-MM-DD HH:MM" beyond a week. Unparseable input is returned unchanged.
func shortTime(rfc string) string {
	t, ok := parseTime(rfc)
	if !ok {
		return rfc
	}
	return relAge(t)
}

func relAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 0, d >= 7*24*time.Hour:
		return t.Format("06-01-02 15:04")
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// longTime renders a full local timestamp with its relative age, for detail views:
// "2006-01-02 15:04:05  (5m ago)".
func longTime(rfc string) string {
	t, ok := parseTime(rfc)
	if !ok {
		return rfc
	}
	return fmt.Sprintf("%s  (%s)", t.Format("2006-01-02 15:04:05"), relAge(t))
}
