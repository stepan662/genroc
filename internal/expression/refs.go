package expression

import (
	"sort"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// OutputRefs returns the distinct task ids referenced via outputs.<id> in expr
// (e.g. "outputs.charge.ok + outputs.ship.n" → ["charge", "ship"]). Used to build
// the output-dependency graph for ordering and recursion detection.
func OutputRefs(expression string) ([]string, error) {
	tree, err := parser.Parse(expression)
	if err != nil {
		return nil, err
	}
	set := map[string]struct{}{}
	collectOutputRefs(tree.Node, set)
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func collectOutputRefs(node ast.Node, set map[string]struct{}) {
	switch n := node.(type) {
	case *ast.MemberNode:
		if id := outputRefID(n); id != "" {
			set[id] = struct{}{}
		}
		collectOutputRefs(n.Node, set)
	case *ast.BinaryNode:
		collectOutputRefs(n.Left, set)
		collectOutputRefs(n.Right, set)
	case *ast.UnaryNode:
		collectOutputRefs(n.Node, set)
	case *ast.ConditionalNode:
		collectOutputRefs(n.Cond, set)
		collectOutputRefs(n.Exp1, set)
		collectOutputRefs(n.Exp2, set)
	}
}

// Roots describes which top-level context roots an expression reads. Used by the
// engine to resolve only the externalized value-slots an expression actually needs
// (slot-level lazy loading) instead of materializing every big value every tick.
type Roots struct {
	Input      bool     // reads the process input
	Error      bool     // reads the error namespace
	Outputs    []string // reads outputs.<id> for these specific task ids
	AllOutputs bool     // reads the outputs map in a way we can't pin to static ids
}

// RootRefs reports which context roots expr reads.
func RootRefs(expr string) (Roots, error) {
	tree, err := parser.Parse(expr)
	if err != nil {
		return Roots{}, err
	}
	var r Roots
	collectRoots(tree.Node, &r)
	return r, nil
}

func collectRoots(node ast.Node, r *Roots) {
	switch n := node.(type) {
	case *ast.MemberNode:
		if id := outputRefID(n); id != "" {
			r.Outputs = append(r.Outputs, id)
			return // consumed outputs.<id>; don't descend into the "outputs" identifier
		}
		collectRoots(n.Node, r)
		collectRoots(n.Property, r)
	case *ast.IdentifierNode:
		switch n.Value {
		case "input":
			r.Input = true
		case "error":
			r.Error = true
		case "outputs":
			r.AllOutputs = true // bare/dynamic outputs reference
		}
	case *ast.BinaryNode:
		collectRoots(n.Left, r)
		collectRoots(n.Right, r)
	case *ast.UnaryNode:
		collectRoots(n.Node, r)
	case *ast.ConditionalNode:
		collectRoots(n.Cond, r)
		collectRoots(n.Exp1, r)
		collectRoots(n.Exp2, r)
	}
}

// outputRefID returns <id> when n is exactly outputs.<id> (base identifier
// "outputs", string property), else "".
func outputRefID(n *ast.MemberNode) string {
	base, ok := n.Node.(*ast.IdentifierNode)
	if !ok || base.Value != "outputs" {
		return ""
	}
	prop, ok := n.Property.(*ast.StringNode)
	if !ok {
		return ""
	}
	return prop.Value
}
