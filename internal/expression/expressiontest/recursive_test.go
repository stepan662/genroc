package expressiontest

import (
	"encoding/json"
	"testing"

	"gent/internal/expression"
	"gent/internal/schema"
)

func sprim(types ...string) *schema.SchemaNode {
	return &schema.SchemaNode{Type: schema.SchemaType(types)}
}

func sobj(req []string, props map[string]*schema.SchemaNode) *schema.SchemaNode {
	return &schema.SchemaNode{Type: schema.SchemaType{"object"}, Properties: props, Required: req}
}

// recCtx builds a context {outputs: {<given step outputs>}}; the self step's own
// output is supplied by the fixpoint, so callers only list sibling outputs.
func recCtx(outputs map[string]*schema.SchemaNode) *schema.SchemaNode {
	if outputs == nil {
		outputs = map[string]*schema.SchemaNode{}
	}
	// Sibling outputs listed here are always-available (required), so accessing
	// them yields non-nullable types.
	var req []string
	for k := range outputs {
		req = append(req, k)
	}
	return &schema.SchemaNode{
		Type:     schema.SchemaType{"object"},
		Required: []string{"outputs"},
		Properties: map[string]*schema.SchemaNode{
			"outputs": {Type: schema.SchemaType{"object"}, Properties: outputs, Required: req},
		},
	}
}

func jsonStr(t *testing.T, n *schema.SchemaNode) string {
	t.Helper()
	b, _ := json.Marshal(schema.Canonicalize(n))
	return string(b)
}

func TestInferRecursiveObject(t *testing.T) {
	tests := []struct {
		name    string
		exprs   map[string]string
		ctx     map[string]*schema.SchemaNode
		selfID  string
		want    *schema.SchemaNode
		wantErr bool
	}{
		{
			name:   "integer counter",
			exprs:  map[string]string{"n": "(outputs.count.n ?? 0) + 1"},
			selfID: "count",
			want:   sobj([]string{"n"}, map[string]*schema.SchemaNode{"n": sprim("integer")}),
		},
		{
			name:   "string accumulator",
			exprs:  map[string]string{"s": `(outputs.cat.s ?? "") + "x"`},
			selfID: "cat",
			want:   sobj([]string{"s"}, map[string]*schema.SchemaNode{"s": sprim("string")}),
		},
		{
			name:   "boolean toggle",
			exprs:  map[string]string{"f": "!(outputs.tog.f ?? false)"},
			selfID: "tog",
			want:   sobj([]string{"f"}, map[string]*schema.SchemaNode{"f": sprim("boolean")}),
		},
		{
			name:   "sum folding another step's output (number widening)",
			exprs:  map[string]string{"total": "(outputs.acc.total ?? 0) + outputs.item.value"},
			ctx:    map[string]*schema.SchemaNode{"item": sobj([]string{"value"}, map[string]*schema.SchemaNode{"value": sprim("number")})},
			selfID: "acc",
			want:   sobj([]string{"total"}, map[string]*schema.SchemaNode{"total": sprim("number")}),
		},
		{
			name: "multiple recursive fields",
			exprs: map[string]string{
				"n": "(outputs.s.n ?? 0) + 1",
				"f": "!(outputs.s.f ?? false)",
			},
			selfID: "s",
			want: sobj([]string{"f", "n"}, map[string]*schema.SchemaNode{
				"n": sprim("integer"),
				"f": sprim("boolean"),
			}),
		},
		{
			name:    "no base case (no ?? default) is rejected",
			exprs:   map[string]string{"n": "outputs.c.n + 1"},
			selfID:  "c",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expression.InferRecursiveObject(tt.exprs, recCtx(tt.ctx), tt.selfID)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %s", jsonStr(t, got))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !schema.Equal(got, tt.want) {
				t.Errorf("type mismatch:\n  got:  %s\n  want: %s", jsonStr(t, got), jsonStr(t, tt.want))
			}
		})
	}
}

// TestInferRecursiveObject_Converges asserts the fixpoint actually reaches a
// stable point (rather than erroring at the pass cap) for a typical accumulator.
func TestInferRecursiveObject_Converges(t *testing.T) {
	got, err := expression.InferRecursiveObject(
		map[string]string{"n": "(outputs.count.n ?? 0) + 1"},
		recCtx(nil), "count",
	)
	if err != nil {
		t.Fatalf("did not converge: %v", err)
	}
	want := sobj([]string{"n"}, map[string]*schema.SchemaNode{"n": sprim("integer")})
	if !schema.Equal(got, want) {
		t.Fatalf("got %s want %s", jsonStr(t, got), jsonStr(t, want))
	}
}
