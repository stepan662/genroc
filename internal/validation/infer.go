package validation

import (
	"fmt"
	"slices"

	"genroc/internal/model"
	"genroc/internal/schema"
	"genroc/internal/template"
)

func buildInputs(tasks []*model.Task, taskSchemas map[string]TaskSchemas, processInput, configSchema schema.Schema, defs schema.Defs) error {
	if err := checkReachability(tasks); err != nil {
		return err
	}
	required, optional, mustErr, mayErr := computeContextSets(tasks)

	// Phase 1: infer every output-map task's exported type, in dependency order
	// (mutually-recursive tasks resolved jointly), writing each to defs so the
	// switches and later tasks below see the final types.
	if err := inferOutputs(tasks, taskSchemas, processInput, configSchema, defs, required, optional, mustErr, mayErr); err != nil {
		return err
	}

	// Phase 2: action inputs and switch type-checks.
	for _, s := range tasks {
		if s.Action != nil {
			ts, inMap := taskSchemas[s.ID]
			isREST := s.Action.Type == model.ActionTypeREST
			hasEndpoint := isREST && s.Action.Endpoint != ""
			hasHeaders := isREST && len(s.Action.Headers) > 0
			hasOver := s.Action.Type == model.ActionTypeChildList && s.Action.Over != ""
			if inMap || s.Action.Input.Present() || hasEndpoint || hasHeaders || hasOver {
				ctx := contextSchema(required[s.ID], optional[s.ID], taskSchemas, processInput, configSchema, mustErr[s.ID], mayErr[s.ID]).WithDefs(defs)
				// The child_list `over` expression must be a non-null array; each
				// element becomes one child's input. Type-check it here so a malformed or
				// non-array expression is rejected at registration.
				if hasOver {
					if _, err := checkArrayTemplate(s.Action.Over, ctx, s.ID); err != nil {
						return err
					}
				}
				// The rest endpoint and header values are templates evaluated against the
				// context; type-check them and reject a possibly-null result (a null URL or
				// header value would silently stringify to "null").
				if hasEndpoint {
					if err := checkNonNullTemplate(s.Action.Endpoint, ctx, fmt.Sprintf("task %q endpoint", s.ID)); err != nil {
						return err
					}
				}
				if hasHeaders {
					names := make([]string, 0, len(s.Action.Headers))
					for h := range s.Action.Headers {
						names = append(names, h)
					}
					slices.Sort(names)
					for _, h := range names {
						if err := checkNonNullTemplate(s.Action.Headers[h], ctx, fmt.Sprintf("task %q header %q", s.ID, h)); err != nil {
							return err
						}
					}
				}
				if inMap || s.Action.Input.Present() {
					input, err := inferInput(s, ctx)
					if err != nil {
						return err
					}
					if !inMap {
						ts.ActionType = s.Action.Type
					}
					ts.Input = input
					taskSchemas[s.ID] = ts
				}
			}
		}

		if len(s.Switch) > 0 {
			switchCtx := contextSchema(required[s.ID], optional[s.ID], taskSchemas, processInput, configSchema, mustErr[s.ID], mayErr[s.ID])
			if s.Action != nil || s.Output.Present() {
				loops := slices.Contains(optional[s.ID], s.ID) || slices.Contains(required[s.ID], s.ID)
				withSelf, err := addSelfSchema(switchCtx, s, loops, defs)
				if err != nil {
					return fmt.Errorf("task %q: %w", s.ID, err)
				}
				switchCtx = withSelf
			}
			switchCtx = switchCtx.WithDefs(defs)
			for _, c := range s.Switch {
				if c.Case == "" {
					continue
				}
				inferred, err := switchCtx.Infer(c.Case)
				if err != nil {
					return fmt.Errorf("task %q switch case %q: %w", s.ID, c.Case, err)
				}
				if !inferred.IsType("boolean") {
					return fmt.Errorf("task %q switch case %q: expression must evaluate to boolean, got %q", s.ID, c.Case, inferred.TypeName())
				}
			}
		}
	}
	return nil
}

// checkNonNullTemplate infers a template string (a rest endpoint or header value)
// against ctx and returns an error if it fails to type-check or may be null — a
// null URL or header value would silently stringify to "null".
func checkNonNullTemplate(expr string, ctx schema.Schema, label string) error {
	inferred, err := inferShape(expr, ctx, label)
	if err != nil {
		return err
	}
	if inferred.HasNull() {
		return fmt.Errorf("%s may be null; use ?? to provide a default value", label)
	}
	return nil
}

// checkArrayTemplate infers a child_list `over` expression against ctx and
// requires it to evaluate to a non-null array — the source of the per-child inputs.
// It returns the inferred array schema so callers can extract its element type.
func checkArrayTemplate(expr string, ctx schema.Schema, taskID string) (schema.Schema, error) {
	inferred, err := template.InferType(expr, ctx)
	if err != nil {
		return schema.Schema{}, fmt.Errorf("task %q over: %w", taskID, err)
	}
	if inferred.HasNull() {
		return schema.Schema{}, fmt.Errorf("task %q over may be null; use ?? to provide a default array", taskID)
	}
	if !inferred.IsType("array") {
		return schema.Schema{}, fmt.Errorf("task %q over must evaluate to an array, got %q", taskID, inferred.TypeName())
	}
	return inferred, nil
}

func inferInput(s *model.Task, ctx schema.Schema) (schema.Schema, error) {
	if !s.Action.Input.Present() {
		return schema.Object(), nil
	}
	return inferShape(s.Action.Input.Raw, ctx, fmt.Sprintf("task %q input", s.ID))
}

// inferShape infers the JSON Schema of a model.Shape value: a string leaf yields
// its template's inferred type (which may be any shape), and an object yields an
// object schema whose values are inferred recursively (all keys required). label
// prefixes errors. The string|object grammar is enforced at unmarshal.
func inferShape(node any, ctx schema.Schema, label string) (schema.Schema, error) {
	switch n := node.(type) {
	case string:
		inferred, err := template.InferType(n, ctx)
		if err != nil {
			return schema.Schema{}, fmt.Errorf("%s: %w", label, err)
		}
		// The inferred sub-schema carries the context's root $defs for its own
		// resolvability; the leaf is embedded into a structure whose root owns the
		// defs, so re-root it bare (also keeps generated schema files and the
		// recursive-fixpoint size bound free of per-leaf defs copies).
		out := inferred.WithoutDefs()
		// Taint the leaf if its expression reads a secret. Structural secrets (a
		// passed-through secret node) are already carried on `out`; this adds the
		// reference-taint that survives any transformation the expression applies.
		if sec, serr := template.ReferencesSecret(n, ctx); serr == nil && sec {
			out = out.Taint()
		}
		return out, nil
	case map[string]any:
		names := make([]string, 0, len(n))
		for name := range n {
			names = append(names, name)
		}
		slices.Sort(names)
		out := schema.Object()
		for _, name := range names {
			p, err := inferShape(n[name], ctx, fmt.Sprintf("%s.%s", label, name))
			if err != nil {
				return schema.Schema{}, err
			}
			out = out.WithProperty(name, p, true)
		}
		return out, nil
	default:
		return schema.Schema{}, fmt.Errorf("%s: invalid shape node %T", label, node)
	}
}

func contextSchema(preceding []string, optional []string, tasks map[string]TaskSchemas, processInput, configSchema schema.Schema, errRequired, errOptional bool) schema.Schema {
	ctx := schema.Object()
	if !processInput.IsZero() {
		ctx = ctx.WithProperty("input", processInput, true)
	}
	if !configSchema.IsZero() {
		ctx = ctx.WithProperty("config", configSchema, true)
	}

	outputs := schema.Object()
	seen := make(map[string]bool)
	for _, id := range preceding {
		if ts, ok := tasks[id]; ok && !ts.Output.IsZero() {
			outputs = outputs.WithProperty(id, ts.Output, true)
			seen[id] = true
		}
	}
	for _, id := range optional {
		if seen[id] {
			continue
		}
		if ts, ok := tasks[id]; ok && !ts.Output.IsZero() {
			outputs = outputs.WithProperty(id, ts.Output, false)
		}
	}
	ctx = ctx.WithProperty("outputs", outputs, true)

	if errRequired || errOptional {
		errSchema := schema.Object().
			WithProperty("task", schema.Type("string"), true).
			WithProperty("message", schema.Type("string"), true).
			WithProperty("code", schema.Type("string"), true)
		if errRequired {
			ctx = ctx.WithProperty("error", errSchema, true)
		} else {
			ctx = ctx.WithProperty("error", errSchema.WithNull(), false)
		}
	}
	return ctx
}

// addSelfSchema gives a switch context this task's transient self scope:
//   - self.result: the raw action result (typed by result_schema; null for delay
//     or a no-action task). Always present.
//   - self.output: the exported output projection — present only when the task
//     defines an `output`. Routing on the raw result of a task with no projection
//     uses self.result; referencing self.output there is an error.
//   - self.previous: this task's prior output — present only when it loops (and so
//     has a prior iteration). Both output and previous resolve through
//     $defs[<id>_output].
func addSelfSchema(ctx schema.Schema, s *model.Task, loops bool, defs schema.Defs) (schema.Schema, error) {
	resultType, err := actionResultType(s, defs)
	if err != nil {
		return schema.Schema{}, err
	}
	self := schema.Object().WithProperty("result", resultType, true)
	if s.Output.Present() {
		self = self.WithProperty("output", schema.Ref(s.ID+"_output"), true)
		if loops {
			self = self.WithProperty("previous", schema.Ref(s.ID+"_output"), false)
		}
	}
	return ctx.WithProperty("self", self, true), nil
}

// actionResultType is the type of a task's raw action result — self.result inside
// an output map (typed by result_schema when present, else permissive; null for
// delay or a no-action task).
func actionResultType(s *model.Task, defs schema.Defs) (schema.Schema, error) {
	if s.Action == nil {
		return schema.Type("null"), nil
	}
	switch s.Action.Type {
	case model.ActionTypeChildMap:
		return childMapOutputSchema(s, defs)
	case model.ActionTypeChildList:
		return childListOutputSchema(s, defs)
	case model.ActionTypeDelay:
		return schema.Type("null"), nil
	default:
		if s.Action.ResultSchema != nil {
			// The result schema is self-contained (shared $defs baked in at
			// Normalize). Hoist its definitions into the generation pool — reusing
			// content-equal entries, renaming collisions and rewriting the schema's
			// $refs — so they resolve in every inference context it is embedded in.
			return s.Action.ResultSchema.MergeInto(defs)
		}
		return schema.Object(), nil
	}
}

// outputMapContext builds the context for inferring a task's output map: the base
// context plus self.result (the raw action result), and — only when the task
// actually loops back to itself — self.previous (its own prior output).
//
// The self-reference is meaningful only for a looping task, which alone has a
// prior iteration. When loops is true, both self.previous and outputs.<id> (the
// latter supplied by the base context via reachability) resolve through
// $defs[<id>_output], the recursive placeholder the fixpoint drives. When the
// task does not loop, neither is available — referencing one's own output without
// looping is an error, since the task is not its own predecessor.
func outputMapContext(base schema.Schema, resultType schema.Schema, taskID string, loops bool) schema.Schema {
	self := schema.Object().WithProperty("result", resultType, true)
	if loops {
		self = self.WithProperty("previous", schema.Ref(taskID+"_output"), false)
	}
	return base.WithProperty("self", self, true)
}
