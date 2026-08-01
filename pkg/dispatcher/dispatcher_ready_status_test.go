package dispatcher //nolint:testpackage // white-box test inspects unexported status helper calls

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestDispatcherDoesNotWriteReadyStatus(t *testing.T) {
	fset, files := parseDispatcherSourceFiles(t)
	for path, file := range files {
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
				t.Fatalf("dispatcher writes ready status at %s:%d; use open instead", path, fset.Position(statusLiteral.Pos()).Line)
			}
			return true
		})
	}
}

func parseDispatcherSourceFiles(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, ".", func(info os.FileInfo) bool {
		name := info.Name()
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse dispatcher source: %v", err)
	}
	files := packages["dispatcher"]
	if files == nil || len(files.Files) == 0 {
		t.Fatal("parse dispatcher source: no non-test Go files found")
	}
	return fset, files.Files
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
