package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// allowedStartSharedDoltServerCallers enumerates the ONLY top-level functions
// permitted to call startSharedDoltServer directly.
//
// startSharedDoltServer LEGAL CALLERS: newDoltSetupCmd, newDoltRepairCmd
// DO NOT add more without updating D6 in
// docs/plans/2026-04-20-oro-dolt-shared-lifecycle-coordination-design.md.
var allowedStartSharedDoltServerCallers = map[string]bool{
	"newDoltSetupCmd":  true,
	"newDoltRepairCmd": true,
}

// TestStartSharedDoltServer_CallerAllowlist walks cmd/oro/*.go via go/parser +
// go/ast, collects every CallExpr node whose function name is
// "startSharedDoltServer", and asserts the enclosing top-level function is in
// the allowlist above.
//
// Rules:
//   - Test files (*_test.go) are skipped — they are allowed to call the
//     function for unit testing purposes.
//   - Files with a //go:build integration constraint are skipped — integration
//     harnesses may exercise the full spawn path.
//   - Any other caller causes the test to fail with an actionable message.
func TestStartSharedDoltServer_CallerAllowlist(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	dir := filepath.Dir(thisFile)

	fset := token.NewFileSet()
	//nolint:staticcheck // SA1019: ParseDir is intentionally used here because we need to
	// inspect ALL files before applying our own build-tag filter; go/packages would
	// only return files matching the current build constraints, hiding integration-tagged files.
	pkgs, err := parser.ParseDir(fset, dir, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse dir %s: %v", dir, err)
	}

	for _, pkg := range pkgs {
		for filename, file := range pkg.Files {
			base := filepath.Base(filename)

			// Skip test files — unit tests may call startSharedDoltServer directly.
			if strings.HasSuffix(base, "_test.go") {
				continue
			}

			// Skip integration-tagged files — excluded from the allowlist contract.
			if hasIntegrationBuildTag(file) {
				continue
			}

			// Collect top-level function spans for enclosing-function lookup.
			type span struct {
				name string
				pos  token.Pos
				end  token.Pos
			}
			var spans []span
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				spans = append(spans, span{fn.Name.Name, fn.Pos(), fn.End()})
			}

			// Walk AST looking for CallExpr nodes naming startSharedDoltServer.
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok || ident.Name != "startSharedDoltServer" {
					return true
				}

				// Find the enclosing top-level function by position.
				enclosing := "<top-level>"
				for _, s := range spans {
					if call.Pos() >= s.pos && call.Pos() <= s.end {
						enclosing = s.name
						break
					}
				}

				if !allowedStartSharedDoltServerCallers[enclosing] {
					t.Errorf(
						"%s: unauthorized caller of startSharedDoltServer: %q\n"+
							"\tAdd it to allowedStartSharedDoltServerCallers in allowlist_test.go\n"+
							"\tand update D6 in docs/plans/...shared-lifecycle-coordination-design.md",
						base, enclosing,
					)
				}
				return true
			})
		}
	}
}

// hasIntegrationBuildTag returns true if any comment in the file's preamble
// contains a //go:build constraint that includes the "integration" tag.
func hasIntegrationBuildTag(f *ast.File) bool {
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			text := strings.TrimSpace(c.Text)
			if strings.HasPrefix(text, "//go:build") && strings.Contains(text, "integration") {
				return true
			}
		}
	}
	return false
}
