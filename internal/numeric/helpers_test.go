package numeric

import (
	"encoding/json"
	"testing"

	"github.com/cockroachdb/apd/v3"
)

func mustDecimal(t *testing.T, s string) *apd.Decimal {
	t.Helper()
	d, _, err := apd.NewFromString(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func assertToDecimal(t *testing.T, in any, want string) {
	t.Helper()
	d, ok := ToDecimal(in)
	if !ok {
		t.Fatalf("ToDecimal(%#v): not numeric", in)
	}
	if got := d.Text('f'); got != want {
		t.Errorf("ToDecimal(%#v) = %s, want %s", in, got, want)
	}
}

func assertNotNumeric(t *testing.T, in any) {
	t.Helper()
	if _, ok := ToDecimal(in); ok {
		t.Errorf("ToDecimal(%#v): expected non-numeric", in)
	}
}

func assertEqual(t *testing.T, a, b any) {
	t.Helper()
	if !Equal(a, b) {
		t.Errorf("Equal(%#v, %#v) = false, want true", a, b)
	}
}

func assertNotEqual(t *testing.T, a, b any) {
	t.Helper()
	if Equal(a, b) {
		t.Errorf("Equal(%#v, %#v) = true, want false", a, b)
	}
}

func assertIntegral(t *testing.T, v any) {
	t.Helper()
	if !IsIntegral(v) {
		t.Errorf("IsIntegral(%#v) = false, want true", v)
	}
}

func assertNotIntegral(t *testing.T, v any) {
	t.Helper()
	if IsIntegral(v) {
		t.Errorf("IsIntegral(%#v) = true, want false", v)
	}
}

// assertFormat checks both the rendered text and that it is readable back as JSON.
func assertFormat(t *testing.T, in, want string) {
	t.Helper()
	d := mustDecimal(t, in)
	got, ok := Format(d)
	if !ok {
		t.Fatalf("Format(%s): not finite", in)
	}
	if got.String() != want {
		t.Errorf("Format(%s) = %s, want %s", in, got, want)
	}
	var back any
	if err := json.Unmarshal([]byte(got.String()), &back); err != nil {
		t.Errorf("Format(%s) = %s, which is not valid JSON: %v", in, got, err)
	}
}

func assertFormatRefused(t *testing.T, in string) {
	t.Helper()
	d, _, err := apd.NewFromString(in)
	if err != nil {
		t.Skipf("apd cannot parse %q: %v", in, err)
	}
	if _, ok := Format(d); ok {
		t.Errorf("Format(%s) should be refused", in)
	}
}
