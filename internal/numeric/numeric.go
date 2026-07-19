// Package numeric holds the one definition of what a number is at runtime.
//
// genroc forwards order ids and monetary amounts, and float64 corrupts both:
// 0.1+0.2 != 0.3, and integers lose precision above 2^53 — including on decode
// alone, before any expression runs. So JSON is decoded with UseNumber and every
// numeric value is carried as its exact literal, with arithmetic and comparison
// done in base-10.
//
// Evaluation (internal/expression) and validation (internal/schema) both compare
// numbers, and they must agree; sharing this package is what stops them drifting.
package numeric

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"

	"github.com/cockroachdb/apd/v3"
)

// ToDecimal converts any runtime numeric representation to an exact decimal.
// Values reach us as json.Number from decoded JSON, as int from expression
// literals, and as int64/float64/float32 from Go-populated contexts and DB scans.
func ToDecimal(v any) (*apd.Decimal, bool) {
	switch n := v.(type) {
	case json.Number:
		d, _, err := apd.NewFromString(n.String())
		return d, err == nil
	case *apd.Decimal:
		return n, true
	case int:
		return apd.New(int64(n), 0), true
	case int64:
		return apd.New(n, 0), true
	case int32:
		return apd.New(int64(n), 0), true
	case float64:
		return fromFloat(n, 64)
	case float32:
		return fromFloat(float64(n), 32)
	}
	return nil, false
}

// fromFloat converts through the shortest text that round-trips, so a value
// written 0.1 becomes decimal 0.1 rather than its binary expansion
// (0.1000000000000000055511151231257827…), which is what the author meant.
func fromFloat(f float64, bits int) (*apd.Decimal, bool) {
	d, _, err := apd.NewFromString(strconv.FormatFloat(f, 'g', -1, bits))
	return d, err == nil
}

// Compare returns -1, 0 or 1 comparing a and b exactly. ok is false unless both
// are numeric.
func Compare(a, b any) (int, bool) {
	x, xok := ToDecimal(a)
	y, yok := ToDecimal(b)
	if !xok || !yok {
		return 0, false
	}
	return x.Cmp(y), true
}

// Equal reports whether a and b are both numeric and numerically equal. It is
// deliberately value-based, not literal-based: 1 and 1.0 are the same number, and
// an enum declared as 1 must keep accepting an input decoded as "1.0".
func Equal(a, b any) bool {
	c, ok := Compare(a, b)
	return ok && c == 0
}

// IsIntegral reports whether v is a whole number.
func IsIntegral(v any) bool {
	d, ok := ToDecimal(v)
	if !ok || d.Form != apd.Finite {
		return false
	}
	var rounded apd.Decimal
	if _, err := apd.BaseContext.RoundToIntegralValue(&rounded, d); err != nil {
		return false
	}
	return rounded.Cmp(d) == 0
}

// Format renders a decimal as the json.Number this language uses as its
// canonical numeric value: it marshals as a bare JSON number and round-trips
// through storage without ever passing through float64. Trailing zeros left by a
// division's precision are trimmed; the value is unchanged.
func Format(d *apd.Decimal) (json.Number, bool) {
	if d.Form != apd.Finite {
		return "", false
	}
	var reduced apd.Decimal
	reduced.Set(d)
	reduced.Reduce(&reduced)
	return json.Number(reduced.Text('f')), true
}

// Decode unmarshals JSON runtime data with numbers preserved as their exact
// literal (json.Number) instead of collapsed into float64.
//
// This is the boundary that matters: plain json.Unmarshal corrupts a large
// integer on decode alone, so a definition that merely forwards an order id
// mangles it before any expression runs. UseNumber only affects values decoded
// into interface{}, so applying it to a typed struct is a no-op — the risk is
// only ever the reverse, forgetting it somewhere data flows in.
func Decode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

// DecodeReader is Decode for a stream, e.g. an HTTP request or response body.
func DecodeReader(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	return dec.Decode(v)
}
