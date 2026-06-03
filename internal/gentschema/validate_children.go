package gentschema

import (
	"fmt"

	"gent/internal/model"
	"gent/internal/schema"
)

// DefinitionGetter looks up process definitions. *db.DB satisfies this interface.
type DefinitionGetter interface {
	GetDefinition(name string, version int) (*model.ProcessDefinition, error)
	LatestVersion(name string) (int, error)
}

// ValidateChildProcessRefs checks every child_process step in def:
//  1. The referenced process exists (version 0 resolves to latest).
//  2. The schema inferred from p.Input is a subset of the child's InputSchema.
//
// def must already be normalised (Generate calls Normalize internally, so
// call this after Generate).
func ValidateChildProcessRefs(def *model.ProcessDefinition, getter DefinitionGetter) error {
	// Rebuild the context the same way Generate/buildInputs does.
	required, optional := computeContextSets(def.Steps)

	named := make(map[string]map[string]any)
	if len(def.InputSchema) > 0 {
		named["input"] = def.InputSchema
	}
	collectNamedOutputs(def.Steps, named)

	var defs map[string]any
	if len(named) > 0 {
		var err error
		defs, err = flattenNamedSchemas(named)
		if err != nil {
			return err
		}
	}

	tasks := make(map[string]TaskSchemas)
	collectTaskRefs(def.Steps, tasks)

	var processInput map[string]any
	if named["input"] != nil {
		processInput = schemaRef("input")
	}

	for _, s := range def.Steps {
		if s.Call == nil || s.Call.Type != model.CallTypeChildProcess {
			continue
		}

		ctx := contextSchema(required[s.ID], optional[s.ID], tasks, processInput)
		if len(defs) > 0 {
			ctx["$defs"] = defs
		}

		for i, p := range s.Call.Processes {
			if err := validateChildEntry(s.ID, i, p, ctx, defs, def, getter); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateChildEntry(stepID string, idx int, p model.ChildProcessEntry, ctx, defs map[string]any, current *model.ProcessDefinition, getter DefinitionGetter) error {
	prefix := fmt.Sprintf("step %q: processes[%d]", stepID, idx)

	// Resolve the child definition. When the entry names the process being defined
	// (self-reference or same version), use the current definition directly to
	// avoid a circular DB lookup that would always fail for a first save.
	var child *model.ProcessDefinition
	if p.Name == current.Name && (p.Version == 0 || p.Version == current.Version) {
		child = current
	} else {
		version := p.Version
		if version == 0 {
			v, err := getter.LatestVersion(p.Name)
			if err != nil {
				return fmt.Errorf("%s: %w", prefix, err)
			}
			version = v
		}
		var err error
		child, err = getter.GetDefinition(p.Name, version)
		if err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
	}

	if len(child.InputSchema) == 0 {
		return nil
	}

	// Infer the schema from the p.Input expression map.
	inferred, err := inferObjectSchema(p.Input, ctx, func(name string) string {
		return fmt.Sprintf("%s input %q", prefix, name)
	})
	if err != nil {
		return err
	}

	// Normalize the inferred schema so that any $ref values that were resolved
	// from the context defs are embedded as proper root-level $defs. IsSubset
	// requires both schemas to be normalized.
	if len(defs) > 0 {
		inferred["$defs"] = defs
	}
	inferred, err = schema.Normalize(inferred)
	if err != nil {
		return fmt.Errorf("%s: normalize inferred input: %w", prefix, err)
	}

	if !schema.IsSubset(inferred, child.InputSchema) {
		return fmt.Errorf("%s: input is not compatible with %q v%d input_schema", prefix, p.Name, child.Version)
	}
	return nil
}
