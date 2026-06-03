package gentschema_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gent/internal/gentschema"
	"gent/internal/model"
	"gent/internal/schema"
)

// stubGetter implements DefinitionGetter using a name-keyed map.
type stubGetter map[string]*model.ProcessDefinition

func (s stubGetter) GetDefinition(name string, version int) (*model.ProcessDefinition, error) {
	def, ok := s[name]
	if !ok || (version != 0 && def.Version != version) {
		return nil, fmt.Errorf("definition %q v%d not found", name, version)
	}
	return def, nil
}

func (s stubGetter) LatestVersion(name string) (int, error) {
	def, ok := s[name]
	if !ok {
		return 0, fmt.Errorf("no definitions found for %q", name)
	}
	return def.Version, nil
}

// normalizedSchema parses a JSON schema string and normalises it, as the DB
// would store it after a successful putDefinition call.
func normalizedSchema(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	out, err := schema.Normalize(m)
	if err != nil {
		t.Fatalf("normalize schema: %v", err)
	}
	return out
}

// childDef builds a minimal child ProcessDefinition whose InputSchema is the
// normalised form of rawSchema. Pass "" for no InputSchema.
func childDef(t *testing.T, name string, rawSchema string) *model.ProcessDefinition {
	t.Helper()
	def := &model.ProcessDefinition{
		Name:    name,
		Version: 1,
		Steps: []*model.Step{
			{ID: "noop", Switch: model.SwitchMap{{When: "default", Goto: model.GotoEnd}}},
		},
	}
	if rawSchema != "" {
		def.InputSchema = normalizedSchema(t, rawSchema)
	}
	return def
}

// parentDef builds a ProcessDefinition with a child_process step, normalises
// it (mirroring what Generate does), and returns it ready for
// ValidateChildProcessRefs.
func parentDef(t *testing.T, inputSchemaRaw string, processes []model.ChildProcessEntry) *model.ProcessDefinition {
	t.Helper()
	def := &model.ProcessDefinition{
		Name:    "parent",
		Version: 1,
		Steps: []*model.Step{
			{
				ID: "spawn",
				Call: &model.Call{
					Type:      model.CallTypeChildProcess,
					Processes: processes,
				},
			},
		},
	}
	if inputSchemaRaw != "" {
		def.InputSchema = normalizedSchema(t, inputSchemaRaw)
	}
	if err := def.Normalize(); err != nil {
		t.Fatalf("normalize parent def: %v", err)
	}
	return def
}

// parentInputSchema is reused by most tests: an object with integer "amount"
// and string "name", both required.
const parentInputSchema = `{
	"type": "object",
	"properties": {
		"amount": {"type": "integer"},
		"name":   {"type": "string"}
	},
	"required": ["amount", "name"]
}`

func assertValidateOK(t *testing.T, def *model.ProcessDefinition, getter gentschema.DefinitionGetter) {
	t.Helper()
	if err := gentschema.ValidateChildProcessRefs(def, getter); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func assertValidateErr(t *testing.T, def *model.ProcessDefinition, getter gentschema.DefinitionGetter, wantSubstr string) {
	t.Helper()
	err := gentschema.ValidateChildProcessRefs(def, getter)
	if err == nil {
		t.Errorf("expected error containing %q, got nil", wantSubstr)
		return
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error %q does not contain %q", err.Error(), wantSubstr)
	}
}

// --- tests ---

func TestValidateChildProcessRefs_noChildProcessSteps(t *testing.T) {
	def := &model.ProcessDefinition{
		Name:    "parent",
		Version: 1,
		Steps: []*model.Step{
			{ID: "fetch", Call: &model.Call{Type: model.CallTypeREST, Endpoint: "http://example.com"}},
		},
	}
	assertValidateOK(t, def, stubGetter{})
}

func TestValidateChildProcessRefs_childExistsNoInputSchema(t *testing.T) {
	getter := stubGetter{
		"worker": childDef(t, "worker", ""),
	}
	def := parentDef(t, "", []model.ChildProcessEntry{
		{Name: "worker", Version: 1},
	})
	assertValidateOK(t, def, getter)
}

func TestValidateChildProcessRefs_childNotFound(t *testing.T) {
	def := parentDef(t, "", []model.ChildProcessEntry{
		{Name: "missing", Version: 1},
	})
	assertValidateErr(t, def, stubGetter{}, "not found")
}

func TestValidateChildProcessRefs_versionZeroResolvesToLatest(t *testing.T) {
	getter := stubGetter{
		"worker": childDef(t, "worker", ""),
	}
	def := parentDef(t, "", []model.ChildProcessEntry{
		{Name: "worker", Version: 0}, // 0 = latest
	})
	assertValidateOK(t, def, getter)
}

func TestValidateChildProcessRefs_versionZeroChildNotFound(t *testing.T) {
	def := parentDef(t, "", []model.ChildProcessEntry{
		{Name: "ghost", Version: 0},
	})
	assertValidateErr(t, def, stubGetter{}, "ghost")
}

func TestValidateChildProcessRefs_compatibleInput(t *testing.T) {
	getter := stubGetter{
		"worker": childDef(t, "worker", `{
			"type": "object",
			"properties": {"amount": {"type": "integer"}},
			"required": ["amount"]
		}`),
	}
	def := parentDef(t, parentInputSchema, []model.ChildProcessEntry{
		{Name: "worker", Version: 1, Input: map[string]string{"amount": "{{input.amount}}"}},
	})
	assertValidateOK(t, def, getter)
}

func TestValidateChildProcessRefs_integerSubsetOfNumber(t *testing.T) {
	getter := stubGetter{
		"worker": childDef(t, "worker", `{
			"type": "object",
			"properties": {"amount": {"type": "number"}},
			"required": ["amount"]
		}`),
	}
	def := parentDef(t, parentInputSchema, []model.ChildProcessEntry{
		{Name: "worker", Version: 1, Input: map[string]string{"amount": "{{input.amount}}"}},
	})
	assertValidateOK(t, def, getter)
}

func TestValidateChildProcessRefs_missingRequiredField(t *testing.T) {
	getter := stubGetter{
		"worker": childDef(t, "worker", `{
			"type": "object",
			"properties": {
				"amount": {"type": "integer"},
				"label":  {"type": "string"}
			},
			"required": ["amount", "label"]
		}`),
	}
	// parent only passes "amount", but child also requires "label"
	def := parentDef(t, parentInputSchema, []model.ChildProcessEntry{
		{Name: "worker", Version: 1, Input: map[string]string{"amount": "{{input.amount}}"}},
	})
	assertValidateErr(t, def, getter, "not compatible")
}

func TestValidateChildProcessRefs_wrongFieldType(t *testing.T) {
	getter := stubGetter{
		"worker": childDef(t, "worker", `{
			"type": "object",
			"properties": {"amount": {"type": "string"}},
			"required": ["amount"]
		}`),
	}
	// input.amount is integer, child expects string
	def := parentDef(t, parentInputSchema, []model.ChildProcessEntry{
		{Name: "worker", Version: 1, Input: map[string]string{"amount": "{{input.amount}}"}},
	})
	assertValidateErr(t, def, getter, "not compatible")
}

func TestValidateChildProcessRefs_additionalPropertiesForbidden(t *testing.T) {
	getter := stubGetter{
		"worker": childDef(t, "worker", `{
			"type": "object",
			"properties": {"amount": {"type": "integer"}},
			"required": ["amount"],
			"additionalProperties": false
		}`),
	}
	// parent passes both "amount" and "extra" (mapped from input.name)
	def := parentDef(t, parentInputSchema, []model.ChildProcessEntry{
		{Name: "worker", Version: 1, Input: map[string]string{
			"amount": "{{input.amount}}",
			"extra":  "{{input.name}}",
		}},
	})
	assertValidateErr(t, def, getter, "not compatible")
}

func TestValidateChildProcessRefs_badExpression(t *testing.T) {
	getter := stubGetter{
		"worker": childDef(t, "worker", `{
			"type": "object",
			"properties": {"x": {"type": "integer"}},
			"required": ["x"]
		}`),
	}
	// parent has no InputSchema, so "{{input.amount}}" cannot be resolved
	def := parentDef(t, "", []model.ChildProcessEntry{
		{Name: "worker", Version: 1, Input: map[string]string{"x": "{{input.amount}}"}},
	})
	if err := gentschema.ValidateChildProcessRefs(def, getter); err == nil {
		t.Error("expected error for unresolvable expression, got nil")
	}
}

func TestValidateChildProcessRefs_emptyInputIncompatibleWithRequired(t *testing.T) {
	getter := stubGetter{
		"worker": childDef(t, "worker", `{
			"type": "object",
			"properties": {"amount": {"type": "integer"}},
			"required": ["amount"]
		}`),
	}
	// p.Input is nil — inferred schema is {type:object} with no required fields
	def := parentDef(t, parentInputSchema, []model.ChildProcessEntry{
		{Name: "worker", Version: 1},
	})
	assertValidateErr(t, def, getter, "not compatible")
}

func TestValidateChildProcessRefs_multipleProcessEntries(t *testing.T) {
	getter := stubGetter{
		"ok":  childDef(t, "ok", ""),
		"bad": childDef(t, "bad", `{"type":"object","properties":{"x":{"type":"integer"}},"required":["x"]}`),
	}
	def := parentDef(t, parentInputSchema, []model.ChildProcessEntry{
		{Name: "ok", Version: 1},
		{Name: "bad", Version: 1, Input: map[string]string{"x": "{{input.name}}"}}, // string passed for integer
	})
	assertValidateErr(t, def, getter, "not compatible")
}

func TestValidateChildProcessRefs_selfReference(t *testing.T) {
	// A process that spawns itself (e.g. recursive tree traversal).
	// The process does not exist in the DB yet, so the getter must not be called.
	// Both required fields (amount + name) are forwarded so the input is compatible.
	def := parentDef(t, parentInputSchema, []model.ChildProcessEntry{
		{Name: "parent", Version: 0, Input: map[string]string{
			"amount": "{{input.amount}}",
			"name":   "{{input.name}}",
		}},
	})
	// getter is empty — any DB call would return "not found"
	assertValidateOK(t, def, stubGetter{})
}

func TestValidateChildProcessRefs_selfReferenceIncompatibleInput(t *testing.T) {
	// Self-reference with an input that doesn't satisfy the process's own InputSchema.
	// Parent requires {amount: integer, name: string}; child entry only passes "amount"
	// as a string (via input.name), which is the wrong type.
	def := parentDef(t, parentInputSchema, []model.ChildProcessEntry{
		{Name: "parent", Version: 0, Input: map[string]string{"amount": "{{input.name}}"}},
	})
	assertValidateErr(t, def, stubGetter{}, "not compatible")
}

func TestValidateChildProcessRefs_inputWithNestedRef(t *testing.T) {
	// Parent InputSchema has a nested type that would produce $ref values in the
	// inferred schema. After normalization the refs must resolve correctly.
	parentSchema := `{
		"type": "object",
		"properties": {
			"order": {
				"type": "object",
				"properties": {
					"amount": {"type": "integer"},
					"currency": {"type": "string"}
				},
				"required": ["amount", "currency"]
			}
		},
		"required": ["order"]
	}`
	getter := stubGetter{
		"billing": childDef(t, "billing", `{
			"type": "object",
			"properties": {
				"amount":   {"type": "integer"},
				"currency": {"type": "string"}
			},
			"required": ["amount", "currency"]
		}`),
	}
	def := parentDef(t, parentSchema, []model.ChildProcessEntry{
		{Name: "billing", Version: 1, Input: map[string]string{
			"amount":   "{{input.order.amount}}",
			"currency": "{{input.order.currency}}",
		}},
	})
	assertValidateOK(t, def, getter)
}
