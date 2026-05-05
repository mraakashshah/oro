// Package closecheck flags direct Store.Close calls outside of tests and
// migration files. Production code must call Dispatcher.CloseBead instead.
package closecheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Finding records a disallowed Store.Close call site.
type Finding struct {
	File string
	Line int
	Text string
}

// CheckDir scans all non-test, non-migration .go files in dir (non-recursive)
// and returns any direct Store.Close / beads.Close call sites found.
func CheckDir(dir string) ([]Finding, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("closecheck: read dir %s: %w", dir, err)
	}

	fset := token.NewFileSet()
	var findings []Finding

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("closecheck: parse %s: %w", path, err)
		}
		if hasMigrationTag(f) {
			continue
		}
		findings = append(findings, checkFile(fset, f)...)
	}

	return findings, nil
}

// hasMigrationTag reports whether f carries a //go:build migration constraint.
func hasMigrationTag(f *ast.File) bool {
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "go:build migration") {
				return true
			}
		}
	}
	return false
}

// checkFile walks f's AST and collects disallowed Store.Close calls.
// It skips any FuncDecl named CloseBead or ClosePremortemBeadWithStore so the
// blessed wrappers' bodies are exempt.
func checkFile(fset *token.FileSet, f *ast.File) []Finding {
	var findings []Finding

	ast.Inspect(f, func(n ast.Node) bool {
		// Don't descend into the CloseBead implementation itself.
		if fn, ok := n.(*ast.FuncDecl); ok && isBlessedCloser(fn.Name.Name) {
			return false
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// Must be the 3-arg Store.Close(ctx, id, reason) signature.
		if sel.Sel.Name != "Close" || len(call.Args) != 3 {
			return true
		}
		if !hasBeadsOrStoreReceiver(sel.X) {
			return true
		}

		pos := fset.Position(call.Pos())
		findings = append(findings, Finding{
			File: pos.Filename,
			Line: pos.Line,
			Text: "direct Store.Close call; use Dispatcher.CloseBead instead",
		})
		return true
	})

	return findings
}

// isBlessedCloser reports whether name is a blessed wrapper that may call
// Store.Close directly. The dispatcher's main wrapper is CloseBead;
// ClosePremortemBeadWithStore is the dispatcher-free analogue used by the
// `oro bead premortem-close` CLI when no Dispatcher exists to delegate to.
func isBlessedCloser(name string) bool {
	switch name {
	case "CloseBead", "ClosePremortemBeadWithStore":
		return true
	default:
		return false
	}
}

// hasBeadsOrStoreReceiver reports whether expr refers to a field or variable
// named "beads" or "store" at any selector depth.
func hasBeadsOrStoreReceiver(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name == "beads" || v.Name == "store"
	case *ast.SelectorExpr:
		return v.Sel.Name == "beads" || v.Sel.Name == "store" || hasBeadsOrStoreReceiver(v.X)
	}
	return false
}
