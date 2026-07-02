package expression

import (
	"encoding/json"
	"fmt"
	"math"

	"genroc/internal/schema"
)

// ErrUnsupported is returned when the expression uses a construct outside
// the supported subset.
type ErrUnsupported struct{ Detail string }

func (e ErrUnsupported) Error() string {
	return "unsupported expression: " + e.Detail
}

// binOp pairs the type-inference and runtime-evaluation behaviour of a binary operator.
type binOp struct {
	infer func(left, right schema.Schema) (schema.Schema, error)
	eval  func(left, right any) (any, error)
}

// unOp pairs the type-inference and runtime-evaluation behaviour of a unary operator.
type unOp struct {
	infer func(operand schema.Schema) (schema.Schema, error)
	eval  func(operand any) (any, error)
}

var binaryOps = map[string]binOp{
	"==": {infer: alwaysBoolean, eval: func(l, r any) (any, error) { return equalValues(l, r), nil }},
	"!=": {infer: alwaysBoolean, eval: func(l, r any) (any, error) { return !equalValues(l, r), nil }},
	"<":  {infer: inferOrderingCmp, eval: numCmp(func(a, b float64) bool { return a < b })},
	">":  {infer: inferOrderingCmp, eval: numCmp(func(a, b float64) bool { return a > b })},
	"<=": {infer: inferOrderingCmp, eval: numCmp(func(a, b float64) bool { return a <= b })},
	">=": {infer: inferOrderingCmp, eval: numCmp(func(a, b float64) bool { return a >= b })},
	"&&": {infer: inferLogical, eval: nil},
	"||": {infer: inferLogical, eval: nil},
	"+":  {infer: inferAdd, eval: evalAdd},
	"-":  {infer: inferArith, eval: numBinOp("-", func(a, b float64) float64 { return a - b })},
	"*":  {infer: inferArith, eval: numBinOp("*", func(a, b float64) float64 { return a * b })},
	"/":  {infer: inferDiv, eval: evalDiv},
	"%":  {infer: inferMod, eval: evalMod},
	"??": {infer: inferNullCoalesce, eval: nil},
}

var unaryOps = map[string]unOp{
	"!": {infer: inferNot, eval: func(v any) (any, error) { return evalNot(v) }},
	"-": {infer: numericPassthrough, eval: func(v any) (any, error) { return negateNum(v) }},
	"+": {infer: numericPassthrough, eval: func(v any) (any, error) { return requireNum(v) }},
}

// ---- infer helpers ----

func alwaysBoolean(_, _ schema.Schema) (schema.Schema, error) {
	return typeSchema("boolean"), nil
}

func inferOrderingCmp(left, right schema.Schema) (schema.Schema, error) {
	if left.HasNull() || right.HasNull() {
		return schema.Schema{}, fmt.Errorf("comparison requires non-nullable operands")
	}
	lt, ok := concreteTypeOf(left)
	if !ok {
		return schema.Schema{}, fmt.Errorf("comparison requires an unambiguous operand")
	}
	rt, ok := concreteTypeOf(right)
	if !ok {
		return schema.Schema{}, fmt.Errorf("comparison requires an unambiguous operand")
	}
	if !isNumeric(lt) || !isNumeric(rt) {
		return schema.Schema{}, fmt.Errorf("comparison requires numeric operands, got %q and %q", lt, rt)
	}
	return typeSchema("boolean"), nil
}

func inferLogical(left, right schema.Schema) (schema.Schema, error) {
	if left.HasNull() || right.HasNull() {
		return schema.Schema{}, fmt.Errorf("logical operator requires non-nullable boolean operands")
	}
	lt, ok := concreteTypeOf(left)
	if !ok {
		return schema.Schema{}, fmt.Errorf("logical operator requires an unambiguous operand")
	}
	rt, ok := concreteTypeOf(right)
	if !ok {
		return schema.Schema{}, fmt.Errorf("logical operator requires an unambiguous operand")
	}
	if lt != "boolean" || rt != "boolean" {
		return schema.Schema{}, fmt.Errorf("logical operator requires boolean operands, got %q and %q", lt, rt)
	}
	return typeSchema("boolean"), nil
}

func inferNot(operand schema.Schema) (schema.Schema, error) {
	if operand.HasNull() {
		return schema.Schema{}, fmt.Errorf("! requires a non-nullable boolean operand")
	}
	t, ok := concreteTypeOf(operand)
	if !ok {
		return schema.Schema{}, fmt.Errorf("! requires an unambiguous operand")
	}
	if t != "boolean" {
		return schema.Schema{}, fmt.Errorf("! requires a boolean operand, got %q", t)
	}
	return typeSchema("boolean"), nil
}

func evalNot(v any) (any, error) {
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("! requires a boolean operand, got %T", v)
	}
	return !b, nil
}

func inferAdd(left, right schema.Schema) (schema.Schema, error) {
	if left.HasNull() || right.HasNull() {
		return schema.Schema{}, fmt.Errorf("operator requires non-nullable operands")
	}
	lt, ltOK := concreteTypeOf(left)
	rt, rtOK := concreteTypeOf(right)
	if !ltOK || !rtOK {
		return schema.Schema{}, fmt.Errorf("operator requires an unambiguous operand")
	}
	if lt == "string" && rt == "string" {
		return typeSchema("string"), nil
	}
	return inferArith(left, right)
}

func inferArith(left, right schema.Schema) (schema.Schema, error) {
	if left.HasNull() || right.HasNull() {
		return schema.Schema{}, fmt.Errorf("operator requires non-nullable operands")
	}
	lt, ltOK := concreteTypeOf(left)
	rt, rtOK := concreteTypeOf(right)
	if !ltOK || !rtOK {
		return schema.Schema{}, fmt.Errorf("operator requires an unambiguous numeric operand")
	}
	if !isNumeric(lt) || !isNumeric(rt) {
		return schema.Schema{}, fmt.Errorf("operator requires numeric operands, got %q and %q", lt, rt)
	}
	if lt == "integer" && rt == "integer" {
		return typeSchema("integer"), nil
	}
	return typeSchema("number"), nil
}

func inferMod(left, right schema.Schema) (schema.Schema, error) {
	if left.HasNull() || right.HasNull() {
		return schema.Schema{}, fmt.Errorf("%% requires non-nullable operands")
	}
	lt, ltOK := concreteTypeOf(left)
	rt, rtOK := concreteTypeOf(right)
	if !ltOK || !rtOK {
		return schema.Schema{}, fmt.Errorf("%% requires an unambiguous integer operand")
	}
	if lt != "integer" || rt != "integer" {
		return schema.Schema{}, fmt.Errorf("%% requires integer operands, got %q and %q", lt, rt)
	}
	return typeSchema("integer"), nil
}

func inferDiv(left, right schema.Schema) (schema.Schema, error) {
	if left.HasNull() || right.HasNull() {
		return schema.Schema{}, fmt.Errorf("/ requires non-nullable operands")
	}
	lt, ltOK := concreteTypeOf(left)
	rt, rtOK := concreteTypeOf(right)
	if !ltOK || !rtOK {
		return schema.Schema{}, fmt.Errorf("/ requires an unambiguous numeric operand")
	}
	if !isNumeric(lt) || !isNumeric(rt) {
		return schema.Schema{}, fmt.Errorf("/ requires numeric operands, got %q and %q", lt, rt)
	}
	return typeSchema("number"), nil
}

func numericPassthrough(operand schema.Schema) (schema.Schema, error) {
	if operand.HasNull() {
		return schema.Schema{}, fmt.Errorf("unary operator requires a non-nullable numeric operand")
	}
	t, ok := concreteTypeOf(operand)
	if !ok {
		return schema.Schema{}, fmt.Errorf("unary operator requires an unambiguous numeric operand")
	}
	if !isNumeric(t) {
		return schema.Schema{}, fmt.Errorf("unary operator requires a numeric operand, got %q", t)
	}
	return operand, nil
}

func inferNullCoalesce(left, right schema.Schema) (schema.Schema, error) {
	if left.IsNull() {
		return right, nil
	}
	nonNullLeft := left.StripNull()
	if schemasEqual(left, nonNullLeft) {
		return left, nil
	}
	if schemasEqual(nonNullLeft, right) {
		return nonNullLeft, nil
	}
	lct, lOK := concreteTypeOf(nonNullLeft)
	rct, rOK := concreteTypeOf(right)
	if lOK && rOK && isNumeric(lct) && isNumeric(rct) {
		if lct == rct {
			return typeSchema(lct), nil
		}
		return typeSchema("number"), nil
	}
	return schema.OneOf(nonNullLeft, right), nil
}

// ---- eval helpers ----

func equalValues(l, r any) bool {
	if lf, rf, ok := bothNumeric(l, r); ok {
		return lf == rf
	}
	if ls, rs, ok := bothString(l, r); ok {
		return ls == rs
	}
	if lb, rb, ok := bothBool(l, r); ok {
		return lb == rb
	}
	return l == r
}

func numCmp(fn func(float64, float64) bool) func(any, any) (any, error) {
	return func(l, r any) (any, error) {
		lf, rf, ok := bothNumeric(l, r)
		if !ok {
			return nil, fmt.Errorf("comparison requires numeric operands")
		}
		return fn(lf, rf), nil
	}
}

func evalAdd(l, r any) (any, error) {
	if ls, rs, ok := bothString(l, r); ok {
		return ls + rs, nil
	}
	return numBinOp("+", func(a, b float64) float64 { return a + b })(l, r)
}

func numBinOp(op string, fn func(float64, float64) float64) func(any, any) (any, error) {
	return func(l, r any) (any, error) {
		lf, rf, ok := bothNumeric(l, r)
		if !ok {
			return nil, fmt.Errorf("%s requires numeric operands", op)
		}
		result := fn(lf, rf)
		if isIntLike(l) && isIntLike(r) && result == math.Trunc(result) {
			return int(result), nil
		}
		return result, nil
	}
}

func evalDiv(l, r any) (any, error) {
	lf, rf, ok := bothNumeric(l, r)
	if !ok {
		return nil, fmt.Errorf("/ requires numeric operands")
	}
	if rf == 0 {
		return nil, fmt.Errorf("division by zero")
	}
	return lf / rf, nil
}

func evalMod(l, r any) (any, error) {
	if !isIntLike(l) || !isIntLike(r) {
		return nil, fmt.Errorf("%% requires integer operands, got %T and %T", l, r)
	}
	lf, rf, _ := bothNumeric(l, r)
	if rf == 0 {
		return nil, fmt.Errorf("modulo by zero")
	}
	return int(math.Mod(lf, rf)), nil
}

func negateNum(v any) (any, error) {
	f, ok := toFloat64(v)
	if !ok {
		return nil, fmt.Errorf("unary - requires a numeric operand")
	}
	if isIntLike(v) {
		return -int(f), nil
	}
	return -f, nil
}

func requireNum(v any) (any, error) {
	if _, ok := toFloat64(v); !ok {
		return nil, fmt.Errorf("unary + requires a numeric operand")
	}
	return v, nil
}

// ---- schema helpers ----

func typeSchema(t string) schema.Schema {
	return schema.Type(t)
}

func isNumeric(t string) bool {
	return t == "integer" || t == "number"
}

func nullableSchema(a, b schema.Schema) (schema.Schema, bool) {
	if s, ok := tryNullable(a, b); ok {
		return s, true
	}
	return tryNullable(b, a)
}

// tryNullable checks if other is {type:"null"} and self can be made nullable in
// place. Schemas with properties are excluded (they need the oneOf wrapper the
// caller builds).
func tryNullable(self, other schema.Schema) (schema.Schema, bool) {
	if !other.IsNull() {
		return schema.Schema{}, false
	}
	if self.HasNull() {
		return self, true
	}
	if self.HasProperties() {
		return schema.Schema{}, false
	}
	if t := self.Type(); len(t) == 1 && t[0] != "null" {
		// WithNull widens the type list in place ({type:[T,"null"]}), preserving
		// any other constraints on the schema.
		return self.WithNull(), true
	}
	return schema.Schema{}, false
}

// concreteTypeOf extracts a single effective type string from a schema.
func concreteTypeOf(s schema.Schema) (string, bool) {
	if t := s.Type(); len(t) == 1 {
		return t[0], true
	}
	variants := s.Variants()
	if variants == nil {
		return "", false
	}
	var types []string
	for _, v := range variants {
		if v.IsZero() || v.IsNull() {
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
func unwrapSingleVariant(s schema.Schema) schema.Schema {
	variants := s.Variants()
	if variants == nil {
		return s
	}
	var nonNull []schema.Schema
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
func schemasEqual(a, b schema.Schema) bool {
	aj, err1 := json.Marshal(a.WithoutDefs())
	bj, err2 := json.Marshal(b.WithoutDefs())
	return err1 == nil && err2 == nil && string(aj) == string(bj)
}

// ---- runtime type / numeric utilities ----

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	}
	return 0, false
}

func isIntLike(v any) bool {
	switch v.(type) {
	case int, int64:
		return true
	}
	return false
}

func bothNumeric(a, b any) (float64, float64, bool) {
	af, aok := toFloat64(a)
	bf, bok := toFloat64(b)
	return af, bf, aok && bok
}

func bothString(a, b any) (string, string, bool) {
	as, aok := a.(string)
	bs, bok := b.(string)
	return as, bs, aok && bok
}

func bothBool(a, b any) (bool, bool, bool) {
	ab, aok := a.(bool)
	bb, bok := b.(bool)
	return ab, bb, aok && bok
}

func mustBool(v any) bool {
	b, _ := v.(bool)
	return b
}
