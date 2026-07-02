package schema

import (
	"encoding/json"
	"fmt"
)

// inferBinaryOps maps each supported binary operator to its type-inference
// rule. The runtime halves live in internal/expression (evalBinary); the two
// must accept the same operator set.
var inferBinaryOps = map[string]func(left, right Schema) (Schema, error){
	"==": alwaysBoolean,
	"!=": alwaysBoolean,
	"<":  inferOrderingCmp,
	">":  inferOrderingCmp,
	"<=": inferOrderingCmp,
	">=": inferOrderingCmp,
	"&&": inferLogical,
	"||": inferLogical,
	"+":  inferAdd,
	"-":  inferArith,
	"*":  inferArith,
	"/":  inferDiv,
	"%":  inferMod,
	"??": inferNullCoalesce,
}

// inferUnaryOps is the unary counterpart of inferBinaryOps.
var inferUnaryOps = map[string]func(operand Schema) (Schema, error){
	"!": inferNot,
	"-": numericPassthrough,
	"+": numericPassthrough,
}

func alwaysBoolean(_, _ Schema) (Schema, error) {
	return Type("boolean"), nil
}

func inferOrderingCmp(left, right Schema) (Schema, error) {
	if left.HasNull() || right.HasNull() {
		return Schema{}, fmt.Errorf("comparison requires non-nullable operands")
	}
	lt, ok := concreteTypeOf(left)
	if !ok {
		return Schema{}, fmt.Errorf("comparison requires an unambiguous operand")
	}
	rt, ok := concreteTypeOf(right)
	if !ok {
		return Schema{}, fmt.Errorf("comparison requires an unambiguous operand")
	}
	if !isNumeric(lt) || !isNumeric(rt) {
		return Schema{}, fmt.Errorf("comparison requires numeric operands, got %q and %q", lt, rt)
	}
	return Type("boolean"), nil
}

func inferLogical(left, right Schema) (Schema, error) {
	if left.HasNull() || right.HasNull() {
		return Schema{}, fmt.Errorf("logical operator requires non-nullable boolean operands")
	}
	lt, ok := concreteTypeOf(left)
	if !ok {
		return Schema{}, fmt.Errorf("logical operator requires an unambiguous operand")
	}
	rt, ok := concreteTypeOf(right)
	if !ok {
		return Schema{}, fmt.Errorf("logical operator requires an unambiguous operand")
	}
	if lt != "boolean" || rt != "boolean" {
		return Schema{}, fmt.Errorf("logical operator requires boolean operands, got %q and %q", lt, rt)
	}
	return Type("boolean"), nil
}

func inferNot(operand Schema) (Schema, error) {
	if operand.HasNull() {
		return Schema{}, fmt.Errorf("! requires a non-nullable boolean operand")
	}
	t, ok := concreteTypeOf(operand)
	if !ok {
		return Schema{}, fmt.Errorf("! requires an unambiguous operand")
	}
	if t != "boolean" {
		return Schema{}, fmt.Errorf("! requires a boolean operand, got %q", t)
	}
	return Type("boolean"), nil
}

func inferAdd(left, right Schema) (Schema, error) {
	if left.HasNull() || right.HasNull() {
		return Schema{}, fmt.Errorf("operator requires non-nullable operands")
	}
	lt, ltOK := concreteTypeOf(left)
	rt, rtOK := concreteTypeOf(right)
	if !ltOK || !rtOK {
		return Schema{}, fmt.Errorf("operator requires an unambiguous operand")
	}
	if lt == "string" && rt == "string" {
		return Type("string"), nil
	}
	return inferArith(left, right)
}

func inferArith(left, right Schema) (Schema, error) {
	if left.HasNull() || right.HasNull() {
		return Schema{}, fmt.Errorf("operator requires non-nullable operands")
	}
	lt, ltOK := concreteTypeOf(left)
	rt, rtOK := concreteTypeOf(right)
	if !ltOK || !rtOK {
		return Schema{}, fmt.Errorf("operator requires an unambiguous numeric operand")
	}
	if !isNumeric(lt) || !isNumeric(rt) {
		return Schema{}, fmt.Errorf("operator requires numeric operands, got %q and %q", lt, rt)
	}
	if lt == "integer" && rt == "integer" {
		return Type("integer"), nil
	}
	return Type("number"), nil
}

func inferMod(left, right Schema) (Schema, error) {
	if left.HasNull() || right.HasNull() {
		return Schema{}, fmt.Errorf("%% requires non-nullable operands")
	}
	lt, ltOK := concreteTypeOf(left)
	rt, rtOK := concreteTypeOf(right)
	if !ltOK || !rtOK {
		return Schema{}, fmt.Errorf("%% requires an unambiguous integer operand")
	}
	if lt != "integer" || rt != "integer" {
		return Schema{}, fmt.Errorf("%% requires integer operands, got %q and %q", lt, rt)
	}
	return Type("integer"), nil
}

func inferDiv(left, right Schema) (Schema, error) {
	if left.HasNull() || right.HasNull() {
		return Schema{}, fmt.Errorf("/ requires non-nullable operands")
	}
	lt, ltOK := concreteTypeOf(left)
	rt, rtOK := concreteTypeOf(right)
	if !ltOK || !rtOK {
		return Schema{}, fmt.Errorf("/ requires an unambiguous numeric operand")
	}
	if !isNumeric(lt) || !isNumeric(rt) {
		return Schema{}, fmt.Errorf("/ requires numeric operands, got %q and %q", lt, rt)
	}
	return Type("number"), nil
}

func numericPassthrough(operand Schema) (Schema, error) {
	if operand.HasNull() {
		return Schema{}, fmt.Errorf("unary operator requires a non-nullable numeric operand")
	}
	t, ok := concreteTypeOf(operand)
	if !ok {
		return Schema{}, fmt.Errorf("unary operator requires an unambiguous numeric operand")
	}
	if !isNumeric(t) {
		return Schema{}, fmt.Errorf("unary operator requires a numeric operand, got %q", t)
	}
	return operand, nil
}

// inferNullCoalesce types `a ?? b`. It is a union-shaped (symbolic) operator:
// a $ref operand is preserved in the result rather than expanded — that is
// what keeps a recursive output type a finite recursive schema. Refs are
// resolved for *analysis* only (nullability, the null seed, scalar merging),
// via resolveTolerant, so the decisions are made on actual types while the
// constructed result keeps the symbolic form.
func inferNullCoalesce(left, right Schema) (Schema, error) {
	if left.IsNull() {
		return right, nil
	}
	nonNullLeft := left.StripNull()
	leftWrapperNullable := !schemasEqual(left, nonNullLeft)

	// Resolve the (possibly $ref) left for analysis. Mid-solve, a reference to
	// a definition being computed lands on its running estimate — the null
	// seed on the first pass, which must take the `?? default` arm exactly
	// like a structural null.
	analysisLeft := resolveTolerant(nonNullLeft)
	if analysisLeft.IsNull() {
		return right, nil
	}
	if !leftWrapperNullable && !analysisLeft.HasNull() {
		return left, nil // left can never be null; ?? is a no-op
	}
	if !leftWrapperNullable {
		// The nullability lives inside the referenced type, where no wrapper
		// can strip it — materialize the stripped form for this rare case.
		nonNullLeft = analysisLeft.StripNull()
	}
	if schemasEqual(nonNullLeft, right) {
		return nonNullLeft, nil
	}
	// Scalar merging analyzes the resolved left stripped of its estimate
	// wrapper (a mid-solve estimate is served nullable): a numeric accumulator
	// materializes to its scalar type so arithmetic on the result works. A
	// non-scalar left keeps the symbolic (possibly $ref) form below.
	lct, lOK := concreteTypeOf(analysisLeft.StripNull())
	rct, rOK := concreteTypeOf(right)
	if lOK && rOK && isNumeric(lct) && isNumeric(rct) {
		if lct == rct {
			return Type(lct), nil
		}
		return Type("number"), nil
	}
	return OneOf(nonNullLeft, right), nil
}

func isNumeric(t string) bool {
	return t == "integer" || t == "number"
}

func nullableSchema(a, b Schema) (Schema, bool) {
	if s, ok := tryNullable(a, b); ok {
		return s, true
	}
	return tryNullable(b, a)
}

// tryNullable checks if other is {type:"null"} and self can be made nullable in
// place. Schemas with properties are excluded (they need the oneOf wrapper the
// caller builds).
func tryNullable(self, other Schema) (Schema, bool) {
	if !other.IsNull() {
		return Schema{}, false
	}
	if self.HasNull() {
		return self, true
	}
	if self.HasProperties() {
		return Schema{}, false
	}
	if t := self.Type(); len(t) == 1 && t[0] != "null" {
		// WithNull widens the type list in place ({type:[T,"null"]}), preserving
		// any other constraints on the schema.
		return self.WithNull(), true
	}
	return Schema{}, false
}

// resolveTolerant follows a $ref operand to its target for analysis — the
// concrete type for solved definitions, the running (nullable) estimate for a
// definition mid-solve. A resolution failure returns the schema unchanged:
// the caller's structural analysis then reports its own (less specific) error,
// and the underlying failure still surfaces through a look-inside path.
func resolveTolerant(s Schema) Schema {
	if !s.HasRef() {
		return s
	}
	r, err := s.Resolve()
	if err != nil {
		return s
	}
	return r
}

// concreteTypeOf extracts a single effective type string from a schema,
// resolving $refs (top-level and per union variant) so referenced scalar
// types participate in operator typing.
func concreteTypeOf(s Schema) (string, bool) {
	s = resolveTolerant(s)
	if t := s.Type(); len(t) == 1 {
		return t[0], true
	}
	variants := s.Variants()
	if variants == nil {
		return "", false
	}
	var types []string
	for _, v := range variants {
		if v.IsZero() {
			return "", false
		}
		v = resolveTolerant(v)
		if v.IsNull() {
			return "", false
		}
		vt := v.Type()
		if len(vt) != 1 {
			return "", false
		}
		types = append(types, vt[0])
	}
	if len(types) == 0 {
		return "", false
	}
	if allEqual(types) {
		return types[0], true
	}
	if allSatisfy(types, isNumeric) {
		return "number", true
	}
	return "", false
}

func allEqual(ss []string) bool {
	for _, s := range ss[1:] {
		if s != ss[0] {
			return false
		}
	}
	return true
}

func allSatisfy(ss []string, fn func(string) bool) bool {
	for _, s := range ss {
		if !fn(s) {
			return false
		}
	}
	return true
}

// unwrapSingleVariant simplifies a oneOf/anyOf schema that has exactly one
// non-null variant into that variant directly.
func unwrapSingleVariant(s Schema) Schema {
	variants := s.Variants()
	if variants == nil {
		return s
	}
	var nonNull []Schema
	for _, v := range variants {
		if v.IsZero() || v.IsNull() {
			return s
		}
		nonNull = append(nonNull, v)
	}
	if len(nonNull) == 1 {
		return nonNull[0]
	}
	return s
}

// schemasEqual compares two schemas structurally, ignoring the root $defs each
// may carry (navigation attaches the shared resolution context; two identical
// types must compare equal whether or not they were reached via navigation).
func schemasEqual(a, b Schema) bool {
	aj, err1 := json.Marshal(a.WithoutDefs())
	bj, err2 := json.Marshal(b.WithoutDefs())
	return err1 == nil && err2 == nil && string(aj) == string(bj)
}
