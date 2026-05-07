package mg

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPkgMgFinalState is the composite verification that runs after all 5 deletion
// children (app, components, tmux, ui, work) land. It asserts:
// 1. All deleted subpackages and files are absent
// 2. Remaining data/ and views/ directories contain Go files (regression guard)
// 3. No source file imports any deleted subpackage
func TestPkgMgFinalState(t *testing.T) {
	root := mgFindModuleRoot(t)
	mgPath := filepath.Join(root, "pkg", "mg")

	// Step 1: Assert deleted items don't exist
	deletedPaths := []string{
		filepath.Join("pkg", "mg", "app"),
		filepath.Join("pkg", "mg", "components"),
		filepath.Join("pkg", "mg", "tmux"),
		filepath.Join("pkg", "mg", "ui"),
		filepath.Join("pkg", "mg", "work.go"),
		filepath.Join("pkg", "mg", "work_test.go"),
	}

	for _, relPath := range deletedPaths {
		fullPath := filepath.Join(root, relPath)
		_, err := os.Stat(fullPath)
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("expected %s to not exist, got: %v", relPath, err)
		}
	}

	// Step 2: Assert data/ and views/ still contain Go files (regression guard)
	dataGo, viewsGo := false, false

	for _, dir := range []string{"data", "views"} {
		dirPath := filepath.Join(mgPath, dir)
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			t.Errorf("failed to read pkg/mg/%s: %v", dir, err)
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") &&
				!strings.HasSuffix(entry.Name(), "_test.go") {
				if dir == "data" {
					dataGo = true
				} else {
					viewsGo = true
				}
			}
		}
	}

	if !dataGo {
		t.Error("pkg/mg/data/ should contain Go source files")
	}
	if !viewsGo {
		t.Error("pkg/mg/views/ should contain Go source files")
	}

	// Step 3: Walk all .go files and verify no imports of deleted packages
	forbiddenImports := map[string]bool{
		"oro/pkg/mg/app":        false,
		"oro/pkg/mg/components": false,
		"oro/pkg/mg/tmux":       false,
		"oro/pkg/mg/ui":         false,
	}

	fset := token.NewFileSet()
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if rel == ".worktrees" || rel == filepath.Join(".claude", "worktrees") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		// Exclude _test.go files under pkg/mg/data and pkg/mg/views
		if strings.HasSuffix(path, "_test.go") {
			dir := filepath.Dir(rel)
			if dir == filepath.Join("pkg", "mg", "data") ||
				dir == filepath.Join("pkg", "mg", "views") {
				return nil
			}
		}

		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return nil // skip unparseable files
		}

		for _, imp := range f.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if _, isForbidden := forbiddenImports[importPath]; isForbidden {
				forbiddenImports[importPath] = true
				t.Errorf("file %s imports forbidden package %s", rel, importPath)
			}
		}

		return nil
	})

	// Step 4: Assert no work.go symbols are referenced anywhere
	workGoSymbols := []string{
		"InTmux",
		"TmuxAvailable",
		"WorkAvailable",
		"LaunchWorkInTmux",
		"PollWorkerPanes",
		"parseWorkerPanes",
		"KillWorkerPane",
		"SelectWorkerPane",
		"WorkCommand",
	}

	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			base := d.Name()
			if base == "archive" ||
				rel == ".worktrees" ||
				rel == filepath.Join(".claude", "worktrees") ||
				strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil
		}

		ast.Inspect(f, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, sym := range workGoSymbols {
				if ident.Name == sym {
					t.Errorf("file %s references deleted work.go symbol %q", rel, sym)
				}
			}
			return true
		})

		return nil
	})
}
