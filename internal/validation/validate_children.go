package validation

import (
	"fmt"

	"genroc/internal/model"
	"genroc/internal/schema"
)

// DefinitionGetter looks up process definitions. *db.DB satisfies this interface.
type DefinitionGetter interface {
	GetDefinition(name string, version int) (*model.ProcessDefinition, error)
	LatestVersion(name string) (int, error)
}

// ValidateChildProcessRefs checks every child_map/child_list task in def:
//  1. The referenced process exists (version 0 resolves to latest).
//  2. The schema inferred from the input expressions is a subset of the child's InputSchema.
//
// currentVersion is the server-assigned version of def (used for self-reference detection).
// def must already be normalised (Generate calls Normalize internally, so call this after Generate).
func ValidateChildProcessRefs(def *model.ProcessDefinition, currentVersion int, getter DefinitionGetter) error {
	defs, tasks, processInput, configSchema, err := buildSchemaContext(def)
	if err != nil {
		return err
	}

	required, optional, mustErr, mayErr := computeContextSets(def.Tasks)

	for _, s := range def.Tasks {
		if s.Action == nil {
			continue
		}
		ctx := contextSchema(required[s.ID], optional[s.ID], tasks, processInput, configSchema, mustErr[s.ID], mayErr[s.ID]).WithDefs(defs)

		switch s.Action.Type {
		case model.ActionTypeChildMap:
			for key, entry := range s.Action.Children {
				if err := validateChildEntry(s.ID, fmt.Sprintf("children[%q]", key), entry, ctx, defs, def, currentVersion, getter); err != nil {
					return err
				}
			}
		case model.ActionTypeChildList:
			if err := validateChildListEntry(s.ID, s.Action, ctx, defs, def, currentVersion, getter); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateChildEntry(taskID string, label string, p model.ChildEntry, ctx schema.Schema, defs schema.Defs, current *model.ProcessDefinition, currentVersion int, getter DefinitionGetter) error {
	prefix := fmt.Sprintf("task %q: %s", taskID, label)

	var child *model.ProcessDefinition
	var childVersion int
	if p.Name == current.Name && (p.Version == 0 || p.Version == currentVersion) {
		child = current
		childVersion = currentVersion
	} else {
		childVersion = p.Version
		if childVersion == 0 {
			v, err := getter.LatestVersion(p.Name)
			if err != nil {
				return fmt.Errorf("%s: %w", prefix, err)
			}
			childVersion = v
		}
		var err error
		child, err = getter.GetDefinition(p.Name, childVersion)
		if err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
	}

	if child.InputSchema == nil {
		return nil
	}

	var inferred schema.Schema
	if p.Input.Present() {
		var err error
		inferred, err = inferShape(p.Input.Raw, ctx, fmt.Sprintf("%s input", prefix))
		if err != nil {
			return err
		}
	} else {
		inferred = schema.Object()
	}

	// Attach the shared defs so the inferred shape's $refs resolve, then normalize:
	// the flatten inlines/retains exactly the definitions the shape uses, giving a
	// self-contained schema the subset check can compare against the child's.
	normalized, err := inferred.WithDefs(defs).Normalize()
	if err != nil {
		return fmt.Errorf("%s: normalize inferred input: %w", prefix, err)
	}

	if !normalized.IsSubset(*child.InputSchema) {
		return fmt.Errorf("%s: input is not compatible with %q v%d input_schema", prefix, p.Name, childVersion)
	}
	return nil
}

// validateChildListEntry checks a child_list task: the referenced child exists,
// `over` is a non-null array, and the array's element type (each element is one
// child's input) is a subset of the child's InputSchema.
func validateChildListEntry(taskID string, action *model.Action, ctx schema.Schema, defs schema.Defs, current *model.ProcessDefinition, currentVersion int, getter DefinitionGetter) error {
	prefix := fmt.Sprintf("task %q: child_list", taskID)

	var child *model.ProcessDefinition
	var childVersion int
	if action.Name == current.Name && (action.Version == 0 || action.Version == currentVersion) {
		child = current
		childVersion = currentVersion
	} else {
		childVersion = action.Version
		if childVersion == 0 {
			v, err := getter.LatestVersion(action.Name)
			if err != nil {
				return fmt.Errorf("%s: %w", prefix, err)
			}
			childVersion = v
		}
		var err error
		child, err = getter.GetDefinition(action.Name, childVersion)
		if err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
	}

	// Infer `over` and confirm it is a non-null array. This also type-checks the
	// expression itself (done again here — with the child in scope — after buildInputs).
	arr, err := checkArrayTemplate(action.Over, ctx, taskID)
	if err != nil {
		return err
	}
	if child.InputSchema == nil {
		return nil
	}

	// Extract the element type (resolving `over` through a $ref first, so an array
	// reached via a shared definition still yields its item schema), then subset-check
	// it against the child's input schema.
	if arr.HasRef() {
		if resolved, rerr := arr.Resolve(); rerr == nil {
			arr = resolved
		}
	}
	if !arr.HasItems() {
		return fmt.Errorf("%s: over is an array with no declared element type, so it cannot be checked against %q v%d input_schema; give the array a typed item schema", prefix, action.Name, childVersion)
	}
	elem := arr.Items()

	normalized, err := elem.WithDefs(defs).Normalize()
	if err != nil {
		return fmt.Errorf("%s: normalize element type: %w", prefix, err)
	}
	if !normalized.IsSubset(*child.InputSchema) {
		return fmt.Errorf("%s: array element type is not compatible with %q v%d input_schema", prefix, action.Name, childVersion)
	}
	return nil
}
