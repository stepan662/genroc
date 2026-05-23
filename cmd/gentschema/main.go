// gentschema reads a process definition JSON file and writes a single JSON file
// containing normalised JSON Schemas for the process input and every task output.
//
// Usage:
//
//	gentschema -i definition.json [-o out.json]
//
// If -i is omitted, the definition is read from stdin.
// If -o is omitted, the result is written to stdout.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"gent/internal/exprtype"
	"gent/internal/model"
	"gent/internal/schema"
)

// TaskSchemas holds the schemas associated with a single task step.
// Output is a $ref into $defs; Input is inferred from params expressions or
// equals the full context schema when the task has no params.
type TaskSchemas struct {
	Input  map[string]any `json:"input,omitempty"`
	Output map[string]any `json:"output,omitempty"`
}

// SchemaFile is the top-level output. $defs collects every named schema so
// that code generators (e.g. json-schema-to-typescript) can emit one type per
// entry. All other schema fields are $ref pointers into $defs.
type SchemaFile struct {
	Process      string                 `json:"process"`
	Version      int                    `json:"version"`
	ProcessInput map[string]any         `json:"process_input,omitempty"`
	Tasks        map[string]TaskSchemas `json:"tasks,omitempty"`
	Defs         map[string]any         `json:"$defs,omitempty"`
}

func main() {
	in := flag.String("i", "", `input definition file (omit or "-" to read from stdin)`)
	out := flag.String("o", "-", `output file path (omit or "-" for stdout)`)
	flag.Parse()

	var src io.Reader = os.Stdin
	if *in != "" && *in != "-" {
		f, err := os.Open(*in)
		if err != nil {
			fatal("open %s: %v", *in, err)
		}
		defer f.Close()
		src = f
	}

	var def model.ProcessDefinition
	if err := json.NewDecoder(src).Decode(&def); err != nil {
		fatal("decode definition: %v", err)
	}

	result, err := Generate(&def)
	if err != nil {
		fatal("generate schemas: %v", err)
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatal("marshal output: %v", err)
	}
	data = append(data, '\n')

	if *out == "-" {
		os.Stdout.Write(data)
		return
	}

	if err := os.WriteFile(*out, data, 0644); err != nil {
		fatal("write %s: %v", *out, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", *out, len(data))
}

// Generate normalises all schemas in def and builds the SchemaFile output.
func Generate(def *model.ProcessDefinition) (SchemaFile, error) {
	if err := def.Normalize(); err != nil {
		return SchemaFile{}, err
	}
	result := SchemaFile{Process: def.Name, Version: def.Version}

	// Collect named schemas: "input" for process input, "<id>_output" per action step.
	named := make(map[string]map[string]any)
	if len(def.InputSchema) > 0 {
		named["input"] = def.InputSchema
	}
	collectNamedOutputs(def.Steps, named)

	if len(named) > 0 {
		defs, err := flattenNamedSchemas(named)
		if err != nil {
			return SchemaFile{}, err
		}
		result.Defs = defs
	}

	if named["input"] != nil {
		result.ProcessInput = schemaRef("input")
	}

	tasks := make(map[string]TaskSchemas)
	collectTaskRefs(def.Steps, tasks)
	if _, err := buildInputs(def.Steps, nil, tasks, result.ProcessInput, result.Defs); err != nil {
		return SchemaFile{}, err
	}
	if len(tasks) > 0 {
		result.Tasks = tasks
	}
	return result, nil
}

// collectNamedOutputs walks steps and adds each action step's OutputSchema
// to named under the key "<id>_output".
func collectNamedOutputs(steps []*model.Step, named map[string]map[string]any) {
	for _, s := range steps {
		if len(s.OutputSchema) > 0 {
			named[s.ID+"_output"] = s.OutputSchema
		}
	}
}

// collectTaskRefs walks steps and populates out with a $ref output
// for every step that has an OutputSchema.
func collectTaskRefs(steps []*model.Step, out map[string]TaskSchemas) {
	for _, s := range steps {
		if len(s.OutputSchema) > 0 {
			out[s.ID] = TaskSchemas{Output: schemaRef(s.ID + "_output")}
		}
	}
}

// flattenNamedSchemas builds a container schema with all named schemas as $defs,
// each tagged with $id so that schema.Normalize scopes their internal $refs
// correctly. After normalisation the flat root $defs map is returned.
//
// Using $id means Normalize handles naming conflicts between inner $defs of
// different schemas automatically, exactly as it does for nested sub-resources.
func flattenNamedSchemas(named map[string]map[string]any) (map[string]any, error) {
	defs := make(map[string]any, len(named))
	refs := make([]any, 0, len(named))
	for name, s := range named {
		entry := deepCopy(s)
		entry["$id"] = name
		defs[name] = entry
		refs = append(refs, schemaRef(name))
	}
	// allOf refs ensure every def is reachable from the root so Normalize
	// does not prune them as unused.
	container := map[string]any{"$defs": defs, "allOf": refs}
	normalised, err := schema.Normalize(container)
	if err != nil {
		return nil, err
	}
	rootDefs, _ := normalised["$defs"].(map[string]any)
	return rootDefs, nil
}

// buildInputs walks the step list in definition order, infers input schemas for
// action steps, and type-checks switch expressions. It returns the IDs of all
// action steps encountered so callers can track context accumulation.
func buildInputs(steps []*model.Step, accumulated []string, tasks map[string]TaskSchemas, processInput map[string]any, defs map[string]any) ([]string, error) {
	for _, s := range steps {
		if s.Transport != "" {
			ctx := contextSchema(accumulated, tasks, processInput)
			if len(defs) > 0 {
				ctx["$defs"] = defs
			}
			ts, inMap := tasks[s.ID]
			if inMap || len(s.Params) > 0 {
				input, err := inferInput(s, ctx, defs)
				if err != nil {
					return nil, err
				}
				ts.Input = input
				tasks[s.ID] = ts
			}
			accumulated = append(accumulated, s.ID)
		}

		if len(s.Switch) > 0 {
			// Switch runs after the action, so accumulated already includes this step.
			switchCtx := contextSchema(accumulated, tasks, processInput)
			if s.Transport != "" {
				addSelfSchema(switchCtx, s)
			}
			if len(defs) > 0 {
				switchCtx["$defs"] = defs
			}
			for _, c := range s.Switch {
				if _, err := exprtype.InferType(c.When, switchCtx); err != nil {
					return nil, fmt.Errorf("step %q switch when %q: %w", s.ID, c.When, err)
				}
			}
		}
	}
	return accumulated, nil
}

// addSelfSchema injects a "self" property into ctx representing this step's own
// action output. Used when type-checking switch expressions.
func addSelfSchema(ctx map[string]any, s *model.Step) {
	props, _ := ctx["properties"].(map[string]any)
	selfSchema := map[string]any(s.OutputSchema)
	if len(selfSchema) == 0 {
		selfSchema = map[string]any{"type": "object"}
	}
	props["self"] = selfSchema
	req, _ := ctx["required"].([]any)
	ctx["required"] = append(req, "self")
}

// inferInput returns the input schema for an action step. Each param name maps to
// the inferred type of its expression against ctx. Steps with no params receive an
// empty payload from the engine, so their input schema is an empty object.
func inferInput(s *model.Step, ctx map[string]any, defs map[string]any) (map[string]any, error) {
	if len(s.Params) == 0 {
		return map[string]any{"type": "object"}, nil
	}
	if len(defs) > 0 {
		ctx["$defs"] = defs
	}
	props := make(map[string]any, len(s.Params))
	required := make([]string, 0, len(s.Params))
	for name, expr := range s.Params {
		inferred, err := exprtype.InferType(expr, ctx)
		if err != nil {
			return nil, fmt.Errorf("task %q param %q: %w", s.ID, name, err)
		}
		props[name] = inferred
		required = append(required, name)
	}
	return map[string]any{"type": "object", "properties": props, "required": required}, nil
}

// contextSchema builds a JSON Schema for the context available to a step:
// the process input (if any, as a $ref) and $ref outputs of all preceding action steps.
// All fields listed are guaranteed present at runtime, so they are marked required.
func contextSchema(preceding []string, tasks map[string]TaskSchemas, processInput map[string]any) map[string]any {
	props := make(map[string]any)
	required := []any{"outputs"}
	if len(processInput) > 0 {
		props["input"] = processInput
		required = append(required, "input")
	}
	outputProps := make(map[string]any)
	outputRequired := make([]any, 0)
	for _, id := range preceding {
		if ts, ok := tasks[id]; ok && len(ts.Output) > 0 {
			outputProps[id] = ts.Output
			outputRequired = append(outputRequired, id)
		}
	}
	outputs := map[string]any{"type": "object"}
	if len(outputProps) > 0 {
		outputs["properties"] = outputProps
		outputs["required"] = outputRequired
	}
	props["outputs"] = outputs
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func schemaRef(name string) map[string]any {
	return map[string]any{"$ref": "#/$defs/" + name}
}

func deepCopy(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	json.Unmarshal(b, &out)
	return out
}

func sliceCopy(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	return out
}

func sliceUnion(a, b []string) []string {
	seen := make(map[string]bool, len(a))
	result := append([]string{}, a...)
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		if !seen[v] {
			result = append(result, v)
			seen[v] = true
		}
	}
	return result
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gentschema: "+format+"\n", args...)
	os.Exit(1)
}
