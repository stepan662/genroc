package template

import "testing"

// Escaping is $-doubling (collision-free with JSON/YAML, which don't treat $ as an escape).
// "$$" renders one literal "$", so "$${" is a literal "${" and a leaf-leading "$$:" is a
// literal "$:". These assert the rendered (evaluated) string.
func TestEscape_RendersLiterally(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"escaped interpolation", `$${input.n}`, `${input.n}`},
		{"double dollar", `$$`, `$`},
		{"bare dollar is literal", `price $5`, `price $5`},
		{"double dollar then digits", `$$5`, `$5`},
		{"literal dollar then live interpolation", `$$${input.n}`, `$3`},
		{"escaped leaf marker", `$$: input.n`, `$: input.n`},
		{"escaped leaf marker then live interpolation", `$$: total ${input.n}`, `$: total 3`},
		{"escaped marker mid text", `pre $${x} post`, `pre ${x} post`},
		{"live interpolation still works", `n=${input.n}`, `n=3`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := mustParse(t, c.src).EvalAny(evalCtx)
			if err != nil {
				t.Fatalf("EvalAny(%q): %v", c.src, err)
			}
			if got != c.want {
				t.Errorf("EvalAny(%q) = %q, want %q", c.src, got, c.want)
			}
		})
	}
}

// Escaping shapes the chunk structure: an escaped ${ is literal text, a literal $ before a
// live ${ stays literal, and an escaped leaf marker is literal text preceding interpolation.
func TestEscape_Split(t *testing.T) {
	assertSplit(t, `$${x}`, `LIT("${x}")`)
	assertSplit(t, `$$${x}`, `LIT("$") EXPR("x")`)
	assertSplit(t, `$$: foo ${x}`, `LIT("$: foo ") EXPR("x")`)
}

// An escaped leaf marker is not a $: expression — it is a plain (literal) template, so it
// infers to string, and a nullable would not be reported (it is never evaluated).
func TestEscape_LeafMarkerIsLiteralString(t *testing.T) {
	assertInferType(t, `$$: input.n`, "string")
}

// $ is not an escape character in JSON or YAML, so $$ survives every quoting style. (A
// backslash escape would break in JSON and double-quoted YAML, where \$ is an invalid
// escape — the reason for $-doubling.)
func TestEscape_SurvivesHostQuoting(t *testing.T) {
	// The genroc string these hosts would decode to is $${x}; it must render literal ${x}.
	got, err := mustParse(t, `$${x}`).EvalAny(evalCtx)
	if err != nil || got != `${x}` {
		t.Errorf("EvalAny(`$${x}`) = %q, %v; want `${x}`", got, err)
	}
}

// Two-layer trap: escaping applies only to literal text. Inside a ${ } (or $:) body the raw
// source goes to the expression lexer — a \t there is a string escape (-> a tab), and a $
// there is ordinary expression content, neither touched by the template's $-handling.
func TestEscape_TwoLayer_BodyIsExpressionLayer(t *testing.T) {
	// "${ 'a\\tb' }" is the literal chars ${ 'a\tb' } — the expression string 'a\tb'.
	got, err := mustParse(t, "${ 'a\\tb' }").EvalAny(evalCtx)
	if err != nil {
		t.Fatalf("EvalAny (block \\t): %v", err)
	}
	if got != "a\tb" {
		t.Errorf("block \\t = %q, want %q (a<tab>b)", got, "a\tb")
	}

	// A $ inside the block string is ordinary expression content, not template escaping.
	got2, err := mustParse(t, `${ 'a$b' }`).EvalAny(evalCtx)
	if err != nil {
		t.Fatalf("EvalAny (block $): %v", err)
	}
	if got2 != "a$b" {
		t.Errorf("block $ = %q, want a$b", got2)
	}

	// Same for a $: leaf body.
	got3, err := mustParse(t, "$: 'a\\tb'").EvalAny(evalCtx)
	if err != nil {
		t.Fatalf("EvalAny ($: \\t): %v", err)
	}
	if got3 != "a\tb" {
		t.Errorf("$: \\t = %q, want %q (a<tab>b)", got3, "a\tb")
	}
}

// A } that follows an escaped quote inside a block string is not the block terminator.
func TestEscape_BackslashInBlockDoesNotEndBlock(t *testing.T) {
	assertSplit(t, `${ "x\"}y" }`, `EXPR(" \"x\\\"}y\" ")`)
}
