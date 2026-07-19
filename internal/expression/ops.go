package expression

import (
	"fmt"
	"reflect"

	"github.com/cockroachdb/apd/v3"

	"genroc/internal/numeric"
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
	"==": func(l, r any) (any, error) { return equalValues(l, r) },
	"!=": func(l, r any) (any, error) {
		eq, err := equalValues(l, r)
		return !eq && err == nil, err
	},
	"<":  numCmp(func(c int) bool { return c < 0 }),
	">":  numCmp(func(c int) bool { return c > 0 }),
	"<=": numCmp(func(c int) bool { return c <= 0 }),
	">=": numCmp(func(c int) bool { return c >= 0 }),
	"+":  evalAdd,
	"-":  decBinOp("-", exactCtx.Sub),
	"*":  decBinOp("*", exactCtx.Mul),
	"/":  evalDiv,
	"%":  evalMod,
}

// Arithmetic is exact base-10, not float64. A workflow engine forwards order ids
// and monetary amounts, and float64 silently corrupts both: 0.1+0.2 != 0.3, and
// integers lose precision above 2^53.
//
// exactCtx has unlimited precision, which is safe for + - * because their result
// length is bounded by the operands. It must never be used for division, where an
// unlimited precision would try to produce infinitely many digits; divCtx caps
// that at decimal128's 34 significant digits with the spec's default rounding.
var (
	exactCtx = apd.BaseContext.WithPrecision(0)
	divCtx   = apd.BaseContext.WithPrecision(34)
)

func bothDecimal(l, r any) (*apd.Decimal, *apd.Decimal, bool) {
	x, xok := numeric.ToDecimal(l)
	y, yok := numeric.ToDecimal(r)
	return x, y, xok && yok
}

// decimalResult renders a computed decimal as the json.Number that is this
// language's canonical numeric value: it marshals as a bare JSON number and
// round-trips through storage without ever touching float64.
func decimalResult(d *apd.Decimal) (any, error) {
	n, ok := numeric.Format(d)
	if !ok {
		return nil, fmt.Errorf("arithmetic produced a non-finite result (%s)", d.String())
	}
	return n, nil
}

func decBinOp(op string, fn func(d, x, y *apd.Decimal) (apd.Condition, error)) func(any, any) (any, error) {
	return func(l, r any) (any, error) {
		x, y, ok := bothDecimal(l, r)
		if !ok {
			return nil, fmt.Errorf("%s requires numeric operands", op)
		}
		out := new(apd.Decimal)
		if _, err := fn(out, x, y); err != nil {
			return nil, fmt.Errorf("%s: %w", op, err)
		}
		return decimalResult(out)
	}
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

// equalValues compares two runtime values. Numbers compare across their int and
// float representations; comparing two structured values is an error.
//
// Structured comparison is rejected rather than defined: the useful answer would
// have to be a deep walk, and a language whose whole point is being statically
// checkable should not hide an unbounded traversal behind ==. Inference rejects
// the same pairing (inferEquality), so this is unreachable from a registered
// definition and guards hand-built contexts only.
//
// Returning an error also removes a panic. Go's == panics when both operands
// share an uncomparable dynamic type, so the old fallback crashed the whole
// process — not just the instance — on `input.a == input.b` between two arrays.
func equalValues(l, r any) (bool, error) {
	if x, y, ok := bothDecimal(l, r); ok {
		return x.Cmp(y) == 0, nil
	}
	if ls, rs, ok := bothString(l, r); ok {
		return ls == rs, nil
	}
	if lb, rb, ok := bothBool(l, r); ok {
		return lb == rb, nil
	}
	if isContainer(l) && isContainer(r) {
		return false, fmt.Errorf("cannot compare %T with %T; == and != are not supported between arrays or objects", l, r)
	}
	if l == nil || r == nil {
		return l == nil && r == nil, nil
	}
	// Anything left is a pair of unmodelled types. Two interfaces of differing
	// dynamic types compare false safely; identical uncomparable types would
	// panic, so screen those out rather than trusting ==.
	if !reflect.TypeOf(l).Comparable() || !reflect.TypeOf(r).Comparable() {
		return false, nil
	}
	return l == r, nil
}

func isContainer(v any) bool {
	switch v.(type) {
	case []any, map[string]any:
		return true
	}
	return false
}

// numCmp compares exactly, so ordering never disagrees with equality the way a
// float64 round-trip can.
func numCmp(fn func(int) bool) func(any, any) (any, error) {
	return func(l, r any) (any, error) {
		x, y, ok := bothDecimal(l, r)
		if !ok {
			return nil, fmt.Errorf("comparison requires numeric operands")
		}
		return fn(x.Cmp(y)), nil
	}
}

func evalAdd(l, r any) (any, error) {
	if ls, rs, ok := bothString(l, r); ok {
		return ls + rs, nil
	}
	return decBinOp("+", exactCtx.Add)(l, r)
}

func evalDiv(l, r any) (any, error) {
	x, y, ok := bothDecimal(l, r)
	if !ok {
		return nil, fmt.Errorf("/ requires numeric operands")
	}
	if y.IsZero() {
		return nil, fmt.Errorf("division by zero")
	}
	out := new(apd.Decimal)
	// Inexact division (10/3) rounds at divCtx's precision rather than erroring:
	// refusing it would be surprising, and the rounding point is documented and
	// deterministic.
	if _, err := divCtx.Quo(out, x, y); err != nil {
		return nil, fmt.Errorf("/: %w", err)
	}
	return decimalResult(out)
}

func evalMod(l, r any) (any, error) {
	if !isIntLike(l) || !isIntLike(r) {
		return nil, fmt.Errorf("%% requires integer operands, got %T and %T", l, r)
	}
	x, y, _ := bothDecimal(l, r)
	if y.IsZero() {
		return nil, fmt.Errorf("modulo by zero")
	}
	out := new(apd.Decimal)
	if _, err := divCtx.Rem(out, x, y); err != nil {
		return nil, fmt.Errorf("%%: %w", err)
	}
	return decimalResult(out)
}

func negateNum(v any) (any, error) {
	d, ok := numeric.ToDecimal(v)
	if !ok {
		return nil, fmt.Errorf("unary - requires a numeric operand")
	}
	return decimalResult(new(apd.Decimal).Neg(d))
}

func requireNum(v any) (any, error) {
	if _, ok := numeric.ToDecimal(v); !ok {
		return nil, fmt.Errorf("unary + requires a numeric operand")
	}
	return v, nil
}

// ---- runtime type / numeric utilities ----

// isIntLike reports whether a value is a whole number, which % requires.
func isIntLike(v any) bool { return numeric.IsIntegral(v) }

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
