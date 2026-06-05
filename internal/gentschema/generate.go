// Package gentschema infers and type-checks JSON Schemas for process definitions.
package gentschema

import (
	"encoding/json"
	"fmt"
	"strings"

	"gent/internal/model"
	"gent/internal/schema"
	"gent/internal/template"
)

// TaskSchemas holds the schemas associated with a single task step.
type TaskSchemas struct {
	CallType model.CallType     `json:"call_type"`
	Input    *schema.SchemaNode `json:"input,omitempty"`
	Output   *schema.SchemaNode `json:"output,omitempty"`
}

// SchemaFile is the top-level output.
type SchemaFile struct {
	Process       string                        `json:"process"`
	Version       int                           `json:"version"`
	ProcessInput  *schema.SchemaNode            `json:"process_input,omitempty"`
	ProcessOutput *schema.SchemaNode            `json:"process_output,omitempty"`
	Tasks         map[string]TaskSchemas        `json:"tasks,omitempty"`
	Defs          map[string]*schema.SchemaNode `json:"$defs,omitempty"`
}

// Generate normalises all schemas in def and builds the SchemaFile output.
func Generate(def *model.ProcessDefinition, version int) (SchemaFile, error) {
	if err := def.Normalize(); err != nil {
		return SchemaFile{}, err
	}
	result := SchemaFile{Process: def.Name, Version: version}

	named := make(map[string]*schema.SchemaNode)
	if def.InputSchema != nil {
		named["input"] = def.InputSchema
	}
	collectNamedOutputs(def.Steps, named)

	var defs map[string]*schema.SchemaNode
	if len(named) > 0 {
		var err error
		defs, err = flattenNamedSchemas(named)
		if err != nil {
			return SchemaFile{}, err
		}
	}

	if named["input"] != nil {
		result.ProcessInput = schemaRef("input")
	}

	tasks := make(map[string]TaskSchemas)
	collectTaskRefs(def.Steps, tasks)
	if _, err := buildInputs(def.Steps, nil, tasks, result.ProcessInput, defs); err != nil {
		return SchemaFile{}, err
	}

	if defs == nil {
		defs = make(map[string]*schema.SchemaNode)
	}

	for _, s := range def.Steps {
		if ts, ok := tasks[s.ID]; ok {
			if ts.Input != nil && ts.Input.Properties != nil {
				name := uniqueDefName(s.ID+"_input", defs)
				defs[name] = ts.Input
				ts.Input = schemaRef(name)
				tasks[s.ID] = ts
			}
		}
	}

	if len(def.Output) > 0 {
		outputSchema, err := inferProcessOutput(def, tasks, result.ProcessInput, defs)
		if err != nil {
			return SchemaFile{}, err
		}
		name := uniqueDefName("output", defs)
		defs[name] = outputSchema
		result.ProcessOutput = schemaRef(name)
	}

	if len(tasks) > 0 {
		result.Tasks = tasks
	}
	if len(defs) > 0 {
		result.Defs = defs
	}
	return result, nil
}

func inferProcessOutput(def *model.ProcessDefinition, tasks map[string]TaskSchemas, processInput *schema.SchemaNode, defs map[string]*schema.SchemaNode) (*schema.SchemaNode, error) {
	req, opt := outputContextSets(def)
	ctx := contextSchema(req, opt, tasks, processInput)
	if len(defs) > 0 {
		ctx = withDefs(ctx, defs)
	}
	return inferObjectSchema(def.Output, ctx, func(name string) string {
		return fmt.Sprintf("output %q", name)
	})
}

func outputContextSets(def *model.ProcessDefinition) (required, optional []string) {
	steps := def.Steps
	n := len(steps)
	if n == 0 {
		return
	}

	reqMap, optMap := computeContextSets(steps)

	type endSet struct {
		must map[string]bool
		may  map[string]bool
	}

	var terminals []endSet
	for i, s := range steps {
		isTerminal := false
		if len(s.Switch) == 0 && i == n-1 {
			isTerminal = true
		}
		for _, c := range s.Switch {
			if c.Goto == model.GotoEnd {
				isTerminal = true
				break
			}
		}
		if !isTerminal {
			continue
		}

		must := make(map[string]bool)
		for _, id := range reqMap[s.ID] {
			must[id] = true
		}
		if stepHasOutput(s) {
			must[s.ID] = true
		}

		may := make(map[string]bool)
		for id := range must {
			may[id] = true
		}
		for _, id := range optMap[s.ID] {
			may[id] = true
		}

		terminals = append(terminals, endSet{must: must, may: may})
	}

	if len(terminals) == 0 {
		return
	}

	mustAtEnd := make(map[string]bool)
	for id := range terminals[0].must {
		mustAtEnd[id] = true
	}
	for _, t := range terminals[1:] {
		for id := range mustAtEnd {
			if !t.must[id] {
				delete(mustAtEnd, id)
			}
		}
	}

	mayAtEnd := make(map[string]bool)
	for _, t := range terminals {
		for id := range t.may {
			mayAtEnd[id] = true
		}
	}

	for id := range mustAtEnd {
		required = append(required, id)
	}
	for id := range mayAtEnd {
		if !mustAtEnd[id] {
			optional = append(optional, id)
		}
	}
	return
}

func stepHasOutput(s *model.Step) bool {
	if s.Call == nil {
		return false
	}
	if s.Call.Type == model.CallTypeChildProcess {
		return true
	}
	return s.Call.OutputSchema != nil
}

func childProcessOutputSchema(s *model.Step) *schema.SchemaNode {
	itemProps := map[string]*schema.SchemaNode{
		"id": {Type: schema.SchemaType{"string"}},
	}
	itemRequired := []string{"id"}
	if s.Call.ChildOutputSchema != nil {
		itemProps["output"] = s.Call.ChildOutputSchema
		itemRequired = append(itemRequired, "output")
	}
	return &schema.SchemaNode{
		Type: schema.SchemaType{"array"},
		Items: &schema.SchemaNode{
			Type:       schema.SchemaType{"object"},
			Properties: itemProps,
			Required:   itemRequired,
		},
	}
}

func collectNamedOutputs(steps []*model.Step, named map[string]*schema.SchemaNode) {
	for _, s := range steps {
		if !stepHasOutput(s) {
			continue
		}
		if s.Call.Type == model.CallTypeChildProcess {
			named[s.ID+"_output"] = childProcessOutputSchema(s)
		} else {
			named[s.ID+"_output"] = s.Call.OutputSchema
		}
	}
}

func collectTaskRefs(steps []*model.Step, out map[string]TaskSchemas) {
	for _, s := range steps {
		if stepHasOutput(s) {
			out[s.ID] = TaskSchemas{CallType: s.Call.Type, Output: schemaRef(s.ID + "_output")}
		}
	}
}

func flattenNamedSchemas(named map[string]*schema.SchemaNode) (map[string]*schema.SchemaNode, error) {
	defs := make(map[string]*schema.SchemaNode, len(named))
	refs := make([]*schema.SchemaNode, 0, len(named))
	for name, s := range named {
		entry := deepCopyNode(s)
		entry.ID = name
		defs[name] = entry
		refs = append(refs, schemaRef(name))
	}
	container := &schema.SchemaNode{Defs: defs, AllOf: refs}
	normalised, err := schema.Normalize(container)
	if err != nil {
		return nil, err
	}
	return normalised.Defs, nil
}

func buildInputs(steps []*model.Step, _ []string, tasks map[string]TaskSchemas, processInput *schema.SchemaNode, defs map[string]*schema.SchemaNode) ([]string, error) {
	required, optional := computeContextSets(steps)
	var accumulated []string
	for _, s := range steps {
		if s.Call != nil {
			ctx := contextSchema(required[s.ID], optional[s.ID], tasks, processInput)
			if len(defs) > 0 {
				ctx = withDefs(ctx, defs)
			}
			ts, inMap := tasks[s.ID]
			if inMap || len(s.Params) > 0 {
				input, err := inferInput(s, ctx, defs)
				if err != nil {
					return nil, err
				}
				if !inMap {
					ts.CallType = s.Call.Type
				}
				ts.Input = input
				tasks[s.ID] = ts
			}
			accumulated = append(accumulated, s.ID)
		}

		if len(s.Switch) > 0 {
			req := required[s.ID]
			opt := optional[s.ID]
			if stepHasOutput(s) {
				req = append(req, s.ID)
				var filtered []string
				for _, id := range opt {
					if id != s.ID {
						filtered = append(filtered, id)
					}
				}
				opt = filtered
			}
			switchCtx := contextSchema(req, opt, tasks, processInput)
			if s.Call != nil {
				switchCtx = addSelfSchema(switchCtx, s)
			}
			if len(defs) > 0 {
				switchCtx = withDefs(switchCtx, defs)
			}
			for _, c := range s.Switch {
				if c.When == "default" {
					continue
				}
				inferred, err := template.InferType(c.When, schema.FromNode(switchCtx))
				if err != nil {
					return nil, fmt.Errorf("step %q switch when %q: %w", s.ID, c.When, err)
				}
				if !isType(inferred.Node(), "boolean") {
					return nil, fmt.Errorf("step %q switch when %q: expression must evaluate to boolean, got %q", s.ID, c.When, schemaTypeName(inferred.Node()))
				}
			}
		}
	}
	return accumulated, nil
}

func computeContextSets(steps []*model.Step) (required, optional map[string][]string) {
	n := len(steps)
	required = make(map[string][]string, n)
	optional = make(map[string][]string, n)
	if n == 0 {
		return
	}

	idx := make(map[string]int, n)
	for i, s := range steps {
		idx[s.ID] = i
	}

	preds := make([][]int, n)
	preds[0] = append(preds[0], -1)
	for i, s := range steps {
		for _, c := range s.Switch {
			if c.Goto != model.GotoEnd {
				if j, ok := idx[c.Goto]; ok {
					preds[j] = append(preds[j], i)
				}
			}
		}
		if len(s.Switch) == 0 && i+1 < n {
			preds[i+1] = append(preds[i+1], i)
		}
	}

	hasOutput := make([]bool, n)
	for i, s := range steps {
		hasOutput[i] = stepHasOutput(s)
	}

	allTrue := func() []bool { s := make([]bool, n); for i := range s { s[i] = true }; return s }
	allFalse := func() []bool { return make([]bool, n) }
	eq := func(a, b []bool) bool {
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	mustOut := make([][]bool, n)
	for i := range mustOut {
		mustOut[i] = allTrue()
	}
	for {
		changed := false
		for i := range steps {
			in := allTrue()
			for _, p := range preds[i] {
				if p == -1 {
					in = allFalse()
					break
				}
				for j := range in {
					in[j] = in[j] && mustOut[p][j]
				}
			}
			if len(preds[i]) == 0 {
				in = allFalse()
			}
			out := append([]bool{}, in...)
			if hasOutput[i] {
				out[i] = true
			}
			if !eq(mustOut[i], out) {
				mustOut[i] = out
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	mayOut := make([][]bool, n)
	for i := range mayOut {
		mayOut[i] = allFalse()
	}
	for {
		changed := false
		for i := range steps {
			in := allFalse()
			for _, p := range preds[i] {
				if p != -1 {
					for j := range in {
						in[j] = in[j] || mayOut[p][j]
					}
				}
			}
			out := append([]bool{}, in...)
			if hasOutput[i] {
				out[i] = true
			}
			if !eq(mayOut[i], out) {
				mayOut[i] = out
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	for i, s := range steps {
		mustIn := allTrue()
		for _, p := range preds[i] {
			if p == -1 {
				mustIn = allFalse()
				break
			}
			for j := range mustIn {
				mustIn[j] = mustIn[j] && mustOut[p][j]
			}
		}
		if len(preds[i]) == 0 {
			mustIn = allFalse()
		}

		mayIn := allFalse()
		for _, p := range preds[i] {
			if p != -1 {
				for j := range mayIn {
					mayIn[j] = mayIn[j] || mayOut[p][j]
				}
			}
		}

		for j, ss := range steps {
			switch {
			case mustIn[j]:
				required[s.ID] = append(required[s.ID], ss.ID)
			case mayIn[j]:
				optional[s.ID] = append(optional[s.ID], ss.ID)
			}
		}
	}
	return
}

func addSelfSchema(ctx *schema.SchemaNode, s *model.Step) *schema.SchemaNode {
	var selfSchema *schema.SchemaNode
	if s.Call != nil {
		selfSchema = s.Call.OutputSchema
	}
	if selfSchema == nil {
		selfSchema = &schema.SchemaNode{Type: schema.SchemaType{"object"}}
	}
	// Shallow-copy ctx and its Properties map to avoid mutating shared nodes.
	n := *ctx
	newProps := make(map[string]*schema.SchemaNode, len(ctx.Properties)+1)
	for k, v := range ctx.Properties {
		newProps[k] = v
	}
	newProps["self"] = selfSchema
	n.Properties = newProps
	n.Required = append(append([]string{}, ctx.Required...), "self")
	return &n
}

func inferObjectSchema(exprs map[string]string, ctx *schema.SchemaNode, errFmt func(string) string) (*schema.SchemaNode, error) {
	props := make(map[string]*schema.SchemaNode, len(exprs))
	required := make([]string, 0, len(exprs))
	for name, expr := range exprs {
		inferred, err := template.InferType(expr, schema.FromNode(ctx))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errFmt(name), err)
		}
		props[name] = inferred.Node()
		required = append(required, name)
	}
	return &schema.SchemaNode{
		Type:       schema.SchemaType{"object"},
		Properties: props,
		Required:   required,
	}, nil
}

func inferInput(s *model.Step, ctx *schema.SchemaNode, defs map[string]*schema.SchemaNode) (*schema.SchemaNode, error) {
	if len(s.Params) == 0 {
		return &schema.SchemaNode{Type: schema.SchemaType{"object"}}, nil
	}
	if len(defs) > 0 {
		ctx = withDefs(ctx, defs)
	}
	return inferObjectSchema(s.Params, ctx, func(name string) string {
		return fmt.Sprintf("task %q param %q", s.ID, name)
	})
}

func contextSchema(preceding []string, optional []string, tasks map[string]TaskSchemas, processInput *schema.SchemaNode) *schema.SchemaNode {
	props := make(map[string]*schema.SchemaNode)
	required := []string{"outputs"}
	if processInput != nil {
		props["input"] = processInput
		required = append(required, "input")
	}
	outputProps := make(map[string]*schema.SchemaNode)
	outputRequired := make([]string, 0)
	for _, id := range preceding {
		if ts, ok := tasks[id]; ok && ts.Output != nil {
			outputProps[id] = ts.Output
			outputRequired = append(outputRequired, id)
		}
	}
	for _, id := range optional {
		if _, already := outputProps[id]; already {
			continue
		}
		if ts, ok := tasks[id]; ok && ts.Output != nil {
			outputProps[id] = ts.Output
		}
	}
	outputs := &schema.SchemaNode{Type: schema.SchemaType{"object"}}
	if len(outputProps) > 0 {
		outputs.Properties = outputProps
		outputs.Required = outputRequired
	}
	props["outputs"] = outputs
	return &schema.SchemaNode{
		Type:       schema.SchemaType{"object"},
		Properties: props,
		Required:   required,
	}
}

// withDefs returns a shallow copy of ctx with Defs set.
func withDefs(ctx *schema.SchemaNode, defs map[string]*schema.SchemaNode) *schema.SchemaNode {
	if len(defs) == 0 || ctx == nil {
		return ctx
	}
	n := *ctx
	n.Defs = defs
	return &n
}

func isType(s *schema.SchemaNode, typ string) bool {
	if s == nil {
		return false
	}
	if len(s.Type) > 0 {
		for _, t := range s.Type {
			if t != typ {
				return false
			}
		}
		return len(s.Type) > 0
	}
	for _, variants := range [][]*schema.SchemaNode{s.OneOf, s.AnyOf} {
		if variants == nil {
			continue
		}
		for _, v := range variants {
			if v == nil || !isType(v, typ) {
				return false
			}
		}
		return len(variants) > 0
	}
	return false
}

func schemaTypeName(s *schema.SchemaNode) string {
	if s == nil {
		return "unknown"
	}
	if len(s.Type) > 0 {
		return strings.Join([]string(s.Type), "|")
	}
	for _, variants := range [][]*schema.SchemaNode{s.OneOf, s.AnyOf} {
		if variants == nil {
			continue
		}
		seen := make(map[string]bool, len(variants))
		var parts []string
		for _, v := range variants {
			if v == nil {
				continue
			}
			name := schemaTypeName(v)
			if !seen[name] {
				seen[name] = true
				parts = append(parts, name)
			}
		}
		return strings.Join(parts, "|")
	}
	return "unknown"
}

func uniqueDefName(base string, defs map[string]*schema.SchemaNode) string {
	name := base
	for i := 1; defs[name] != nil; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	return name
}

func schemaRef(name string) *schema.SchemaNode {
	return &schema.SchemaNode{Ref: "#/$defs/" + name}
}

func deepCopyNode(n *schema.SchemaNode) *schema.SchemaNode {
	if n == nil {
		return nil
	}
	b, _ := json.Marshal(n)
	// Use alias to bypass strict UnmarshalJSON on a round-trip of already-valid data.
	type alias schema.SchemaNode
	var a alias
	json.Unmarshal(b, &a) //nolint:errcheck
	result := schema.SchemaNode(a)
	return &result
}
