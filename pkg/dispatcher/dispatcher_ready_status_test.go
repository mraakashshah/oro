package dispatcher //nolint:testpackage // white-box test inspects unexported status helper calls

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestDispatcherDoesNotWriteReadyStatus(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dispatcher.go", nil, 0)
	if err != nil {
		t.Fatalf("parse dispatcher.go: %v", err)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isUpdateBeadStatusCall(call.Fun) || len(call.Args) == 0 {
			return true
		}
		statusArg := call.Args[len(call.Args)-1]
		statusLiteral, ok := statusArg.(*ast.BasicLit)
		if ok && statusLiteral.Kind == token.STRING && statusLiteral.Value == `"ready"` {
			t.Fatalf("dispatcher writes ready status at %s; use open instead", fset.Position(statusLiteral.Pos()))
		}
		return true
	})
}

func isUpdateBeadStatusCall(fn ast.Expr) bool {
	switch expr := fn.(type) {
	case *ast.Ident:
		return expr.Name == "updateBeadStatus"
	case *ast.SelectorExpr:
		return expr.Sel.Name == "updateBeadStatus"
	default:
		return false
	}
}
