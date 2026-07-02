// Static type inference for a subset of expr-lang expressions evaluated
// against a Schema context. The matching runtime evaluator lives in
// internal/expression (Eval); the two must accept the same subset:
//
//   - Literals: integer, float, string, bool, null
//   - Field access via dot notation: input.x, outputs.task.y
//   - Arithmetic: +, -, *, /, % (numbers; + also concatenates strings)
//   - Comparison: ==, !=, <, >, <=, >= → boolean
//   - Logical: &&, || → boolean (short-circuit); ! → boolean
//   - Conditional: cond ? a : b
//   - Null coalescing: a ?? b (returns a if non-nil, else b)
//
// All other expr-lang constructs return ErrUnsupported.
package schema

import (
	"fmt"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// ErrUnsupported is returned when an expression uses a construct outside the
// supported subset. internal/expression aliases it so inference and evaluation
// report the same error type.
type ErrUnsupported struct{ Detail string }

func (e ErrUnsupported) Error() string {
	return "unsupported expression: " + e.Detail
}

// inferCtx is the immutable type-inference context threaded through all infer
// calls. s is the context schema (carrying the root $defs every navigation
// resolves against); guards is a shallow-copied overlay mapping dot-paths to
// schema overrides for type-narrowed branches.
type inferCtx struct {
	s      Schema
	guards map[string]Schema
}

func (c inferCtx) withGuard(path string, narrowed Schema) inferCtx {
	guards := make(map[string]Schema, len(c.guards)+1)
	for k, v := range c.guards {
		guards[k] = v
	}
	guards[path] = narrowed
	return inferCtx{s: c.s, guards: guards}
}

// Infer statically determines the JSON Schema type of an expr-lang expression
// when evaluated against s (e.g. "user.issues[0].value ?? 0"). $refs are
// resolved against s's root $defs, and the result carries them, so it stays
// navigable/validatable. For plain sub-path lookup without expression
// semantics, see At.
func (s Schema) Infer(expression string) (Schema, error) {
	tree, err := parser.Parse(expression)
	if err != nil {
		return Schema{}, fmt.Errorf("parse %q: %w", expression, err)
	}
	return inferNode(tree.Node, inferCtx{s: s})
}

// ReferencesSecret reports whether expression reads any value whose schema — or
// an enclosing object's schema along the access path — is marked secret. It is
// deliberately conservative: any path that passes through a secret node taints
// the whole expression, regardless of what the expression then does with the
// value. This is the reliable half of secret taint tracking (the structural half
// is the secret bit carried on the schema node itself).
func (s Schema) ReferencesSecret(expression string) (bool, error) {
	tree, err := parser.Parse(expression)
	if err != nil {
		return false, fmt.Errorf("parse %q: %w", expression, err)
	}
	return walkSecretRefs(tree.Node, s), nil
}

func walkSecretRefs(n ast.Node, s Schema) bool {
	if n == nil {
		return false
	}
	if path := nodePath(n); path != "" && s.SecretAt(path) {
		return true
	}
	switch x := n.(type) {
	case *ast.MemberNode:
		return walkSecretRefs(x.Node, s) || walkSecretRefs(x.Property, s)
	case *ast.BinaryNode:
		return walkSecretRefs(x.Left, s) || walkSecretRefs(x.Right, s)
	case *ast.UnaryNode:
		return walkSecretRefs(x.Node, s)
	case *ast.ConditionalNode:
		return walkSecretRefs(x.Cond, s) || walkSecretRefs(x.Exp1, s) || walkSecretRefs(x.Exp2, s)
	}
	return false
}

func inferNode(node ast.Node, ictx inferCtx) (Schema, error) {
	switch n := node.(type) {
	case *ast.IntegerNode:
		return Type("integer"), nil
	case *ast.FloatNode:
		return Type("number"), nil
	case *ast.StringNode:
		return Type("string"), nil
	case *ast.BoolNode:
		return Type("boolean"), nil
	case *ast.NilNode:
		return Schema{}, fmt.Errorf("nil is not supported; use null")
	case *ast.IdentifierNode:
		if n.Value == "null" {
			return Type("null"), nil
		}
		if s, ok := ictx.guards[n.Value]; ok {
			return s, nil
		}
		return ictx.s.Property(n.Value)
	case *ast.MemberNode:
		return inferMember(n, ictx)
	case *ast.BinaryNode:
		return inferBinary(n, ictx)
	case *ast.UnaryNode:
		return inferUnary(n, ictx)
	case *ast.ConditionalNode:
		return inferConditional(n, ictx)
	default:
		return Schema{}, ErrUnsupported{Detail: fmt.Sprintf("node type %T", node)}
	}
}

func inferMember(n *ast.MemberNode, ictx inferCtx) (Schema, error) {
	if path := nodePath(n); path != "" {
		if s, ok := ictx.guards[path]; ok {
			return s, nil
		}
	}
	base, err := inferNode(n.Node, ictx)
	if err != nil {
		return Schema{}, err
	}
	// The base may be a composed result (an operator-built union) that carries no
	// resolution context of its own — re-anchor it to the context's root $defs so
	// any $refs inside still resolve.
	base = base.WithDefs(ictx.s.DefsHandle())
	// Member access on a known-null base is null, matching runtime optional
	// chaining (eval returns nil for a nil base). This is also what lets the
	// recursive-inference seed work: the self-reference's previous value is null
	// on the first iteration, and `self.previous.x` must resolve to null so a
	// `?? default` base case can fire rather than erroring on a missing property.
	// A $ref base is resolved for this check — mid-solve it lands on the null
	// seed estimate, which must behave exactly like a structural null.
	if base.IsNull() {
		return Type("null"), nil
	}
	if base.HasRef() {
		rb, rerr := base.Resolve()
		if rerr != nil {
			// Resolution may have demanded solving the referenced definition;
			// its failure is the real error and must not be masked.
			return Schema{}, rerr
		}
		if rb.IsNull() {
			return Type("null"), nil
		}
	}
	switch prop := n.Property.(type) {
	case *ast.StringNode:
		return base.Property(prop.Value)
	case *ast.IntegerNode:
		return base.Index()
	default:
		return Schema{}, ErrUnsupported{Detail: "computed member access [expr]"}
	}
}

func inferBinary(n *ast.BinaryNode, ictx inferCtx) (Schema, error) {
	op, ok := inferBinaryOps[n.Operator]
	if !ok {
		return Schema{}, ErrUnsupported{Detail: fmt.Sprintf("operator %q", n.Operator)}
	}
	left, err := inferNode(n.Left, ictx)
	if err != nil {
		return Schema{}, err
	}
	right, err := inferNode(n.Right, ictx)
	if err != nil {
		return Schema{}, err
	}
	// Operands may be composed results (or preserved $refs) with no resolution
	// context of their own; re-anchor so operator-level analysis can resolve.
	left = left.WithDefs(ictx.s.DefsHandle())
	right = right.WithDefs(ictx.s.DefsHandle())
	return op(unwrapSingleVariant(left), unwrapSingleVariant(right))
}

func inferUnary(n *ast.UnaryNode, ictx inferCtx) (Schema, error) {
	op, ok := inferUnaryOps[n.Operator]
	if !ok {
		return Schema{}, ErrUnsupported{Detail: fmt.Sprintf("unary operator %q", n.Operator)}
	}
	operand, err := inferNode(n.Node, ictx)
	if err != nil {
		return Schema{}, err
	}
	operand = operand.WithDefs(ictx.s.DefsHandle())
	return op(unwrapSingleVariant(operand))
}

func inferConditional(n *ast.ConditionalNode, ictx inferCtx) (Schema, error) {
	if _, err := inferNode(n.Cond, ictx); err != nil {
		return Schema{}, err
	}
	thenCtx, elseCtx := narrowCondition(n.Cond, ictx)
	t, err := inferNode(n.Exp1, thenCtx)
	if err != nil {
		return Schema{}, err
	}
	f, err := inferNode(n.Exp2, elseCtx)
	if err != nil {
		return Schema{}, err
	}
	if schemasEqual(t, f) {
		return t, nil
	}
	if s, ok := nullableSchema(t, f); ok {
		return s, nil
	}
	return OneOf(t, f), nil
}

// narrowCondition returns then/else contexts narrowed by an equality condition.
func narrowCondition(cond ast.Node, ictx inferCtx) (thenCtx, elseCtx inferCtx) {
	thenCtx, elseCtx = ictx, ictx
	bin, ok := cond.(*ast.BinaryNode)
	if !ok || (bin.Operator != "==" && bin.Operator != "!=") {
		return
	}

	var subject, litNode ast.Node
	switch {
	case isLiteralNode(bin.Right):
		subject, litNode = bin.Left, bin.Right
	case isLiteralNode(bin.Left):
		subject, litNode = bin.Right, bin.Left
	default:
		return
	}

	path := nodePath(subject)
	if path == "" {
		return
	}

	litSchema, err := inferNode(litNode, ictx)
	if err != nil {
		return
	}

	litIsNull := isNullLiteral(litNode)

	if bin.Operator == "==" {
		thenCtx = ictx.withGuard(path, litSchema)
		if litIsNull {
			if subjectSchema, err := inferNode(subject, ictx); err == nil {
				elseCtx = ictx.withGuard(path, subjectSchema.StripNull())
			}
		}
	} else {
		elseCtx = ictx.withGuard(path, litSchema)
		if litIsNull {
			if subjectSchema, err := inferNode(subject, ictx); err == nil {
				thenCtx = ictx.withGuard(path, subjectSchema.StripNull())
			}
		}
	}
	return
}

func isLiteralNode(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.BoolNode, *ast.StringNode, *ast.IntegerNode, *ast.FloatNode:
		return true
	case *ast.IdentifierNode:
		return n.Value == "null"
	}
	return false
}

func isNullLiteral(n ast.Node) bool {
	id, ok := n.(*ast.IdentifierNode)
	return ok && id.Value == "null"
}

func nodePath(node ast.Node) string {
	if node == nil {
		return ""
	}
	switch n := node.(type) {
	case *ast.IdentifierNode:
		return n.Value
	case *ast.MemberNode:
		if base := nodePath(n.Node); base != "" {
			switch prop := n.Property.(type) {
			case *ast.StringNode:
				return base + "." + prop.Value
			case *ast.IntegerNode:
				return fmt.Sprintf("%s[%d]", base, prop.Value)
			}
		}
	}
	return ""
}
