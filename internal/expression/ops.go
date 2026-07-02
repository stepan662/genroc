package expression

import (
	"fmt"
	"math"

	"genroc/internal/schema"
)

// ErrUnsupported is returned when the expression uses a construct outside the
// supported subset. It is an alias of the type the static-inference half
// (schema.Schema.Infer) returns, so both halves report the same error type.
type ErrUnsupported = schema.ErrUnsupported

// binaryOps maps each binary operator to its runtime evaluation. The
// type-inference halves live in the schema package (inferBinaryOps); the two
// must accept the same operator set. The short-circuit operators ??, && and ||
// are absent here — evalBinary handles them before the table lookup.
var binaryOps = map[string]func(left, right any) (any, error){
	"==": func(l, r any) (any, error) { return equalValues(l, r), nil },
	"!=": func(l, r any) (any, error) { return !equalValues(l, r), nil },
	"<":  numCmp(func(a, b float64) bool { return a < b }),
	">":  numCmp(func(a, b float64) bool { return a > b }),
	"<=": numCmp(func(a, b float64) bool { return a <= b }),
	">=": numCmp(func(a, b float64) bool { return a >= b }),
	"+":  evalAdd,
	"-":  numBinOp("-", func(a, b float64) float64 { return a - b }),
	"*":  numBinOp("*", func(a, b float64) float64 { return a * b }),
	"/":  evalDiv,
	"%":  evalMod,
}

// unaryOps is the unary counterpart of binaryOps.
var unaryOps = map[string]func(operand any) (any, error){
	"!": evalNot,
	"-": negateNum,
	"+": requireNum,
}

func evalNot(v any) (any, error) {
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("! requires a boolean operand, got %T", v)
	}
	return !b, nil
}

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
