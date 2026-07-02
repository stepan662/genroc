package schema

import "fmt"

// CheckDoc reports whether node is a well-formed schema document in the supported
// subset: every $ref resolves against the root $defs, combinator and property
// entries are non-nil, any paired numeric/length/item bounds are ordered, and
// every declared default validates against the schema it is attached to.
//
// Keyword validity is already guaranteed by node's strict UnmarshalJSON, so
// CheckDoc only catches the structural errors that survive parsing — chiefly an
// unresolvable $ref. It replaces the previous gojsonschema.NewSchema compile step.
func checkDocRoot(nd *node) error {
	if nd == nil {
		return nil
	}
	return checkDoc(nd, nd.Defs, map[*node]bool{})
}

func checkDoc(nd *node, defs map[string]*node, seen map[*node]bool) error {
	if nd == nil || seen[nd] {
		return nil
	}
	seen[nd] = true

	if nd.Ref != "" {
		if _, err := deref(nd, defs); err != nil {
			return err
		}
	}
	if nd.Minimum != nil && nd.Maximum != nil && *nd.Minimum > *nd.Maximum {
		return fmt.Errorf("minimum %v exceeds maximum %v", *nd.Minimum, *nd.Maximum)
	}
	if nd.MinLength != nil && nd.MaxLength != nil && *nd.MinLength > *nd.MaxLength {
		return fmt.Errorf("minLength %d exceeds maxLength %d", *nd.MinLength, *nd.MaxLength)
	}
	if nd.MinItems != nil && nd.MaxItems != nil && *nd.MinItems > *nd.MaxItems {
		return fmt.Errorf("minItems %d exceeds maxItems %d", *nd.MinItems, *nd.MaxItems)
	}
	// An invalid default would surface only when Validate fills it into an
	// absent property; reject it here, where the error points at the schema.
	if nd.Default != nil {
		if _, err := conform(nd, defs, nd.Default, ""); err != nil {
			return fmt.Errorf("default does not validate against its schema: %w", err)
		}
	}

	for name, p := range nd.Properties {
		if p == nil {
			return fmt.Errorf("property %q is null", name)
		}
		if err := checkDoc(p, defs, seen); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := checkDoc(nd.Items, defs, seen); err != nil {
		return fmt.Errorf("items: %w", err)
	}
	for i, v := range nd.OneOf {
		if v == nil {
			return fmt.Errorf("oneOf[%d] is null", i)
		}
		if err := checkDoc(v, defs, seen); err != nil {
			return err
		}
	}
	for i, v := range nd.AnyOf {
		if v == nil {
			return fmt.Errorf("anyOf[%d] is null", i)
		}
		if err := checkDoc(v, defs, seen); err != nil {
			return err
		}
	}
	for name, d := range nd.Defs {
		if err := checkDoc(d, defs, seen); err != nil {
			return fmt.Errorf("$defs.%s: %w", name, err)
		}
	}
	return nil
}
