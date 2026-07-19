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
			isFetch := s.Action.Type == model.ActionTypeFetch
			hasURL := isFetch && s.Action.URL != ""
			hasMethod := isFetch && s.Action.Method != ""
			hasHeaders := isFetch && s.Action.Headers.Present()
			hasBody := s.Action.Body.Present()
			hasInput := s.Action.Input.Present()
			hasOver := s.Action.Type == model.ActionTypeChildList && s.Action.Over != ""
			if inMap || hasBody || hasInput || hasURL || hasMethod || hasHeaders || hasOver {
				ctx := contextSchema(required[s.ID], optional[s.ID], taskSchemas, processInput, configSchema, mustErr[s.ID], mayErr[s.ID]).WithDefs(defs)
				// The child_list `over` expression must be a non-null array; each
				// element becomes one child's input. Type-check it here so a malformed or
				// non-array expression is rejected at registration.
				if hasOver {
					if _, err := checkArrayTemplate(s.Action.Over, ctx, s.ID); err != nil {
						return err
					}
				}
				// The fetch url and method are templates evaluated against the context;
				// type-check them and reject a possibly-null result (a null URL or method
				// would silently stringify to "null").
				if hasURL {
					if err := checkNonNullTemplate(s.Action.URL, ctx, fmt.Sprintf("task %q url", s.ID)); err != nil {
						return err
					}
				}
				if hasMethod {
					if err := checkNonNullTemplate(s.Action.Method, ctx, fmt.Sprintf("task %q method", s.ID)); err != nil {
						return err
					}
				}
				// Headers is a shape that must evaluate to a non-null object.
				if hasHeaders {
					if err := checkHeadersShape(s.Action.Headers.Raw, ctx, s.ID); err != nil {
						return err
					}
				}
				if inMap || hasBody || hasInput {
					input, err := inferActionPayload(s, ctx)
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

// inferActionPayload infers the schema of an action's payload shape — the fetch request
// body (Body) or the external snapshot (Input).
func inferActionPayload(s *model.Task, ctx schema.Schema) (schema.Schema, error) {
	shape := s.Action.Input
	label := "input"
	if s.Action.Type == model.ActionTypeFetch {
		shape = s.Action.Body
		label = "body"
	}
	if !shape.Present() {
		return schema.Object(), nil
	}
	return inferShape(shape.Raw, ctx, fmt.Sprintf("task %q %s", s.ID, label))
}

// checkHeadersShape verifies the fetch Headers shape infers to a non-null object (a
// literal map of templated values, or an expression yielding a map).
func checkHeadersShape(raw any, ctx schema.Schema, taskID string) error {
	hdr, err := inferShape(raw, ctx, fmt.Sprintf("task %q headers", taskID))
	if err != nil {
		return err
	}
	if hdr.HasNull() || !hdr.IsType("object") {
		return fmt.Errorf("task %q headers must evaluate to a non-null object", taskID)
	}
	return nil
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
	resultType, typed, err := actionResultType(s, defs)
	if err != nil {
		return schema.Schema{}, err
	}
	if !typed {
		// An untyped result (no result_schema) is still routable in the switch as the
		// raw value; expose it as a bare object so member access without a schema fails
		// (there are no known properties) while a whole-value reference resolves.
		resultType = schema.Object()
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

// actionResultType is the type of a task's raw action result — self.result. The
// bool return is whether that result is a typed value: true for a result_schema, a
// child call, delay, or a no-action task (null); false for a fetch/external action
// with no result_schema, whose response is untyped. An untyped result stays usable in
// the switch (transient routing) but must not be exported through an output, so the
// output context drops it and a reference there is an error.
func actionResultType(s *model.Task, defs schema.Defs) (schema.Schema, bool, error) {
	if s.Action == nil {
		return schema.Type("null"), true, nil
	}
	switch s.Action.Type {
	case model.ActionTypeChildMap:
		sc, err := childMapOutputSchema(s, defs)
		return sc, true, err
	case model.ActionTypeChildList:
		sc, err := childListOutputSchema(s, defs)
		return sc, true, err
	case model.ActionTypeDelay:
		return schema.Type("null"), true, nil
	default:
		if s.Action.ResultSchema != nil {
			// The result schema is self-contained (shared $defs baked in at
			// Normalize). Hoist its definitions into the generation pool — reusing
			// content-equal entries, renaming collisions and rewriting the schema's
			// $refs — so they resolve in every inference context it is embedded in.
			sc, err := s.Action.ResultSchema.MergeInto(defs)
			return sc, true, err
		}
		return schema.Schema{}, false, nil
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
func outputMapContext(base schema.Schema, resultType schema.Schema, typed bool, taskID string, loops bool) schema.Schema {
	self := schema.Object()
	// An untyped result (fetch/external with no result_schema) is omitted here, so an
	// output that references self.result is a registration error: you cannot export an
	// untyped value — add a result_schema to type the response.
	if typed {
		self = self.WithProperty("result", resultType, true)
	}
	if loops {
		self = self.WithProperty("previous", schema.Ref(taskID+"_output"), false)
	}
	return base.WithProperty("self", self, true)
}
