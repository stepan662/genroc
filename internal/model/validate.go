package model

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"genroc/internal/schema"

	"github.com/go-playground/validator/v10"
)

// Validate checks the definition and its tasks against the struct-tag rules, that attached
// JSON Schemas are well-formed, and that switch goto targets name known tasks.
func (d *ProcessDefinition) Validate() error {
	if err := fmtValidationErr(v.Struct(d)); err != nil {
		return err
	}
	if err := d.validateDefs(); err != nil {
		return err
	}
	if err := checkSchemaDoc("input_schema", d.InputSchema, d.Defs); err != nil {
		return err
	}
	if err := checkSchemaDoc("config_schema", d.ConfigSchema, schema.Defs{}); err != nil {
		return err
	}
	if err := validateConfigSchema(d.ConfigSchema); err != nil {
		return err
	}
	taskIDs := make(map[string]struct{}, len(d.Tasks))
	for _, s := range d.Tasks {
		taskIDs[s.ID] = struct{}{}
	}
	lastIdx := len(d.Tasks) - 1
	for i, s := range d.Tasks {
		if err := validateTask(s, taskIDs, i, lastIdx, d.Defs); err != nil {
			return err
		}
	}
	return nil
}

func validateTask(s *Task, taskIDs map[string]struct{}, taskIdx, lastIdx int, pool schema.Defs) error {
	// Reserved task IDs.
	if s.ID == GotoEnd || s.ID == GotoNext {
		return fmt.Errorf("task ID %q is reserved", s.ID)
	}
	if err := validateActionRequiredFields(s); err != nil {
		return err
	}
	if err := validateSwitch(s, taskIDs, taskIdx, lastIdx); err != nil {
		return err
	}
	if err := validateOnError(s, taskIDs); err != nil {
		return err
	}
	return validateActionSchemas(s, pool)
}

func validateActionRequiredFields(s *Task) error {
	if s.Action == nil {
		return nil
	}
	switch s.Action.Type {
	case ActionTypeFetch:
		if s.Action.URL == "" {
			return fmt.Errorf("task %q: action.url is required for type %q", s.ID, s.Action.Type)
		}
	case ActionTypeChildMap:
		if len(s.Action.Children) == 0 {
			return fmt.Errorf("task %q: action.children is required for type %q", s.ID, s.Action.Type)
		}
		for key, entry := range s.Action.Children {
			if entry.Name == "" {
				return fmt.Errorf("task %q: action.children[%q].name is required", s.ID, key)
			}
		}
	case ActionTypeChildList:
		if s.Action.Name == "" {
			return fmt.Errorf("task %q: action.name is required for type %q", s.ID, s.Action.Type)
		}
		if s.Action.Over == "" {
			return fmt.Errorf("task %q: action.over is required for type %q", s.ID, s.Action.Type)
		}
	case ActionTypeDelay:
		if s.Action.Ms == "" {
			return fmt.Errorf("task %q: action.ms is required for type %q", s.ID, s.Action.Type)
		}
	case ActionTypeExternal:
		// No required action fields: input and result_schema are both optional
		// (mirroring fetch). The wait timeout is the task's timeout_ms (0 = forever).
	default:
		return fmt.Errorf("task %q: action.type must be one of: fetch, child_map, child_list, delay, external", s.ID)
	}
	return nil
}

// validateSwitch checks the task's switch cases: catch-all ordering and goto targets.
func validateSwitch(s *Task, taskIDs map[string]struct{}, taskIdx, lastIdx int) error {
	if len(s.Switch) == 0 {
		return fmt.Errorf("task %q: switch is required", s.ID)
	}
	for i, c := range s.Switch {
		isLast := i == len(s.Switch)-1
		if c.Case == "" && !isLast {
			return fmt.Errorf("task %q switch: catch-all at index %d must be the last case (unreachable cases after it)", s.ID, i)
		}
		switch {
		case c.Goto == GotoEnd:
			// always valid
		case c.Goto == GotoNext:
			if taskIdx == lastIdx {
				return fmt.Errorf("task %q switch: 'next' is not allowed on the last task; use 'end' to terminate", s.ID)
			}
		case strings.HasPrefix(c.Goto, "$"):
			taskID := c.Goto[1:]
			if _, ok := taskIDs[taskID]; !ok {
				return fmt.Errorf("task %q switch: goto %q is not a known task", s.ID, c.Goto)
			}
		default:
			return fmt.Errorf("task %q switch: goto %q must be \"end\", \"next\", or a task reference like \"$task-id\"", s.ID, c.Goto)
		}
	}
	if s.Switch[len(s.Switch)-1].Case != "" {
		return fmt.Errorf("task %q switch: last case must be a catch-all (omit 'case' to match unconditionally)", s.ID)
	}
	return nil
}

// validateOnError checks the task's on_error rules: code patterns, catch-all ordering,
// goto targets, and the only_once retry restrictions.
func validateOnError(s *Task, taskIDs map[string]struct{}) error {
	onlyOnce := s.OnlyOnce != nil && *s.OnlyOnce
	for i, ec := range s.OnError {
		for _, pat := range ec.Code {
			if !validLikePattern(pat) {
				return fmt.Errorf("task %q on_error[%d]: code pattern must not be empty", s.ID, i)
			}
			if sqlLikeMatch(pat, "child.failed") {
				return fmt.Errorf("task %q on_error[%d]: catching child.failed is not supported; handle errors inside the child process and communicate them via return data", s.ID, i)
			}
		}
		isLast := i == len(s.OnError)-1
		if len(ec.Code) == 0 && !isLast {
			return fmt.Errorf("task %q on_error[%d]: catch-all must be the last rule (unreachable rules after it)", s.ID, i)
		}
		if ec.Goto != "" && ec.Goto != GotoEnd {
			if _, ok := taskIDs[ec.Goto]; !ok {
				return fmt.Errorf("task %q on_error[%d]: goto %q is not a known task", s.ID, i, ec.Goto)
			}
		}
		if onlyOnce && ec.Retries > 0 {
			// not_reached:true is an explicit user override — allow retries regardless of pattern.
			if ec.NotReached != nil && *ec.NotReached {
				continue
			}
			// Catch-all rules (empty Code) would match any error including reached ones.
			if len(ec.Code) == 0 {
				return fmt.Errorf("task %q on_error[%d]: catch-all rule cannot have retries on an only_once task; restrict to pre.%% or add not_reached:true", s.ID, i)
			}
			for _, pat := range ec.Code {
				if !patternOnlyMatchesPre(pat) {
					return fmt.Errorf("task %q on_error[%d]: pattern %q can match errors where the call may have executed; restrict to pre.%% patterns or add not_reached:true to assert the remote was not reached", s.ID, i, pat)
				}
			}
		}
	}
	return nil
}

// validateActionSchemas checks fetch accepted_status patterns and that any attached
// result_schema documents (task-level and child_map entries) are valid schemas.
func validateActionSchemas(s *Task, pool schema.Defs) error {
	if s.Action == nil {
		return nil
	}
	if s.Action.Type == ActionTypeFetch {
		for _, pat := range s.Action.AcceptedStatus {
			if !validAcceptedStatusPattern(pat) {
				return fmt.Errorf("task %q: accepted_status %q must be \"2xx\"/\"3xx\"/\"4xx\"/\"5xx\" or a 3-digit code", s.ID, pat)
			}
		}
	}
	if err := checkSchemaDoc(fmt.Sprintf("task %q action.result_schema", s.ID), s.Action.ResultSchema, pool); err != nil {
		return err
	}
	if s.Action.Type == ActionTypeChildMap {
		for key, entry := range s.Action.Children {
			if err := checkSchemaDoc(fmt.Sprintf("task %q action.children[%q].result_schema", s.ID, key), entry.ResultSchema, pool); err != nil {
				return err
			}
		}
	}
	return nil
}

func validLikePattern(p string) bool {
	return strings.TrimSpace(p) != ""
}

// patternOnlyMatchesPre reports whether a LIKE pattern can only match error codes in the
// pre.* namespace: its constant prefix (before the first % or _ wildcard) must start with
// "pre.".
func patternOnlyMatchesPre(p string) bool {
	for i := 0; i < len(p); i++ {
		if p[i] == '%' || p[i] == '_' {
			return strings.HasPrefix(p[:i], "pre.")
		}
	}
	return strings.HasPrefix(p, "pre.")
}

func sqlLikeMatch(p, s string) bool {
	for len(p) > 0 {
		switch p[0] {
		case '%':
			p = p[1:]
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if sqlLikeMatch(p, s[i:]) {
					return true
				}
			}
			return false
		case '_':
			if len(s) == 0 {
				return false
			}
			p, s = p[1:], s[1:]
		default:
			if len(s) == 0 || p[0] != s[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}

func validAcceptedStatusPattern(p string) bool {
	if len(p) == 3 && p[1] == 'x' && p[2] == 'x' && p[0] >= '1' && p[0] <= '5' {
		return true
	}
	if len(p) == 3 {
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// configNameRe matches a valid config var name; it is used in the
// GENROC_<PROCESS>_<NAME> / GENROC_GLOBAL_<NAME> environment variable names, so it
// must be an identifier.
var configNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateConfigSchema enforces the config_schema shape: a flat "object" whose properties
// each declare a single scalar type (string/integer/number/boolean) with no nested
// object/array, combinators, or $ref. Property names must be identifiers that don't
// collide once normalized to their env var suffix; a required property may not carry a default.
func validateConfigSchema(cs *schema.Schema) error {
	if cs == nil {
		return nil
	}
	if t := cs.Type(); len(t) != 1 || !t.Contains("object") {
		return errors.New("config_schema must be type \"object\"")
	}
	if cs.HasCombinators() || cs.HasRef() || cs.HasDefs() {
		return errors.New("config_schema must not use oneOf/anyOf/allOf/$ref/$defs")
	}
	props := cs.Properties()
	required := make(map[string]bool, len(cs.Required()))
	for _, r := range cs.Required() {
		if _, ok := props[r]; !ok {
			return fmt.Errorf("config_schema: required lists unknown property %q", r)
		}
		required[r] = true
	}
	envKeys := make(map[string]string, len(props))
	for name, prop := range props {
		if !configNameRe.MatchString(name) {
			return fmt.Errorf("config %q: name must be a valid identifier [A-Za-z_][A-Za-z0-9_]*", name)
		}
		key := envToken(name)
		if prev, dup := envKeys[key]; dup {
			return fmt.Errorf("config %q and %q both map to the same environment variable suffix %q", name, prev, key)
		}
		envKeys[key] = name
		pt := prop.Type()
		if len(pt) != 1 {
			return fmt.Errorf("config %q: must declare a single primitive type (string, integer, number, or boolean)", name)
		}
		switch pt[0] {
		case "string", "integer", "number", "boolean":
		default:
			return fmt.Errorf("config %q: unsupported type %q (use string, integer, number, or boolean)", name, pt[0])
		}
		if prop.HasProperties() || prop.HasItems() || prop.HasCombinators() || prop.HasRef() {
			return fmt.Errorf("config %q: must be a primitive value (no nested objects, arrays, combinators, or $ref)", name)
		}
		if required[name] && prop.Default() != nil {
			return fmt.Errorf("config %q: cannot be both required and have a default", name)
		}
	}
	return nil
}

// checkSchemaDoc verifies s is a well-formed schema document; the $defs pool is merged in
// so a schema referencing a shared definition validates before Normalize bakes it in.
func checkSchemaDoc(field string, s *schema.Schema, pool schema.Defs) error {
	if s == nil {
		return nil
	}
	if err := s.WithMergedDefs(pool).CheckDoc(); err != nil {
		return fmt.Errorf("%s is not a valid JSON Schema: %w", field, err)
	}
	return nil
}

// validateDefs checks each process-level $defs definition is well-formed, resolving $refs
// against the whole pool (definitions may reference each other). Collisions with generated
// schema names need no check — generation renames the colliding user definition.
func (d *ProcessDefinition) validateDefs() error {
	if d.Defs.IsZero() {
		return nil
	}
	for _, name := range d.Defs.Names() {
		def, _ := d.Defs.Get(name)
		// Merge the pool so definitions referencing each other check clean.
		if err := def.WithMergedDefs(d.Defs).CheckDoc(); err != nil {
			return fmt.Errorf("$defs %q is not a valid JSON Schema: %w", name, err)
		}
	}
	return nil
}

// v is the shared validator, configured to report JSON field names in errors.
var v = func() *validator.Validate {
	val := validator.New()
	val.RegisterTagNameFunc(func(f reflect.StructField) string {
		name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
		if name == "-" || name == "" {
			return f.Name
		}
		return name
	})
	return val
}()

func fmtValidationErr(err error) error {
	if err == nil {
		return nil
	}
	var ve validator.ValidationErrors
	if !errors.As(err, &ve) {
		return err
	}
	msgs := make([]string, len(ve))
	for i, fe := range ve {
		msgs[i] = describeFieldErr(fe)
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

func describeFieldErr(fe validator.FieldError) string {
	field := fe.Field()
	switch fe.Tag() {
	case "required", "required_if":
		return fmt.Sprintf("%s is required", field)
	case "min":
		return fmt.Sprintf("%s must have at least %s item(s)", field, fe.Param())
	case "oneof":
		return fmt.Sprintf("%s must be one of: %s", field, strings.ReplaceAll(fe.Param(), " ", ", "))
	default:
		return fe.Error()
	}
}
