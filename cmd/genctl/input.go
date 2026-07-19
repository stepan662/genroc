package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ── input assembly (genctl run) ────────────────────────────────────────────────

// buildInput assembles an input/result value from a base source and any --set
// overrides. The base comes from exactly one of: literal (a JSON/YAML literal passed to
// --input/--result, or "-" for stdin) or file (a path passed to -f). Each --set
// key=value is then applied on top (requiring the base to be an object). Returns
// (value, present, error): present is false when no source and no --set was given, so
// the value is omitted entirely for processes/tasks that take none.
func buildInput(literal, file string, sets []string) (any, bool, error) {
	base, present, err := readBase(literal, file)
	if err != nil {
		return nil, false, err
	}
	if len(sets) > 0 {
		m, ok := base.(map[string]any)
		if base == nil {
			m, ok = map[string]any{}, true
		}
		if !ok {
			return nil, false, fmt.Errorf("--set needs the base to be an object, but it is %T", base)
		}
		for _, s := range sets {
			if err := applySet(m, s); err != nil {
				return nil, false, err
			}
		}
		base, present = m, true
	}
	return base, present, nil
}

// readBase resolves the base value from the mutually-exclusive literal (a JSON/YAML
// literal, or "-" for stdin) and file (a path — bare, so the shell tab-completes it)
// sources. Returns present=false when neither is set.
func readBase(literal, file string) (any, bool, error) {
	if literal != "" && file != "" {
		return nil, false, fmt.Errorf("provide the value inline or with -f, not both")
	}
	var data []byte
	switch {
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, false, err
		}
		data = b
	case literal == "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, false, err
		}
		data = b
	case literal != "":
		data = []byte(literal)
	default:
		return nil, false, nil
	}
	v, err := parseRelaxed(data)
	if err != nil {
		return nil, false, fmt.Errorf("parse value: %w", err)
	}
	return v, true, nil
}

// parseRelaxed parses data as YAML — a superset of JSON, so strict JSON works while
// also allowing the shell-friendly relaxed forms (unquoted keys, single quotes,
// trailing commas), e.g. {name: Sam, count: 3}. The result is round-tripped through
// JSON so the value contains only JSON-native types.
func parseRelaxed(data []byte) (any, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	jsonBytes, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(jsonBytes, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// applySet applies one "key=value" (or "a.b.c=value") override onto m, inferring
// the value's type and creating nested objects for dotted keys.
func applySet(m map[string]any, kv string) error {
	eq := strings.IndexByte(kv, '=')
	if eq < 0 {
		return fmt.Errorf("--set %q must be key=value", kv)
	}
	key, val := kv[:eq], kv[eq+1:]
	if key == "" {
		return fmt.Errorf("--set %q has an empty key", kv)
	}
	return setPath(m, strings.Split(key, "."), inferScalar(val))
}

// setPath walks/creates the nested objects named by path and sets the final key.
func setPath(m map[string]any, path []string, val any) error {
	for i := 0; i < len(path)-1; i++ {
		child, ok := m[path[i]]
		if !ok {
			next := map[string]any{}
			m[path[i]], m = next, next
			continue
		}
		next, ok := child.(map[string]any)
		if !ok {
			return fmt.Errorf("--set: %q is already set to a non-object", strings.Join(path[:i+1], "."))
		}
		m = next
	}
	m[path[len(path)-1]] = val
	return nil
}

// inferScalar maps a --set value string to a JSON-native scalar: true/false/null,
// then integer, then float, else the string unchanged. Use --input for values that
// must stay strings (e.g. "007") or for arrays / deep structures.
func inferScalar(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}
