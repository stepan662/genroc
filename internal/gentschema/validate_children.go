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
	required, optional := computeContextSets(def.Steps)

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
			return err
		}
	}

	tasks := make(map[string]TaskSchemas)
	collectTaskRefs(def.Steps, tasks)

	var processInput *schema.SchemaNode
	if named["input"] != nil {
		processInput = schemaRef("input")
	}

	for _, s := range def.Steps {
		if s.Call == nil || s.Call.Type != model.CallTypeChildProcess {
			continue
		}

		ctx := contextSchema(required[s.ID], optional[s.ID], tasks, processInput)
		if len(defs) > 0 {
			ctx = withDefs(ctx, defs)
		}

		for i, p := range s.Call.Processes {
			if err := validateChildEntry(s.ID, i, p, ctx, defs, def, getter); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateChildEntry(stepID string, idx int, p model.ChildProcessEntry, ctx *schema.SchemaNode, defs map[string]*schema.SchemaNode, current *model.ProcessDefinition, getter DefinitionGetter) error {
	prefix := fmt.Sprintf("step %q: processes[%d]", stepID, idx)

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

	if child.InputSchema == nil {
		return nil
	}

	inferred, err := inferObjectSchema(p.Input, ctx, func(name string) string {
		return fmt.Sprintf("%s input %q", prefix, name)
	})
	if err != nil {
		return err
	}

	if len(defs) > 0 {
		inferred = withDefs(inferred, defs)
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
