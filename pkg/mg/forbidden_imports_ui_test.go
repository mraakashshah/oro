package mg_test

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoMgUiImports guards that pkg/mg/ui has been deleted and no source file
// imports it. Excluded: _test.go files under pkg/mg/views, which may keep
// render tests that reference ui until they are separately cleaned up.
func TestNoMgUiImports(t *testing.T) {
	// Assert the ui subdirectory is gone.
	if _, err := os.Stat("ui"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pkg/mg/ui must not exist: delete the directory as part of this task")
	}

	// Walk every .go file in the repo.
	repoRoot := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var violators []string

	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if d.IsDir() {
			if relErr == nil && (rel == ".worktrees" || rel == filepath.Join(".claude", "worktrees")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		// Excluded: _test.go files inside pkg/mg/views.
		if relErr == nil && strings.HasSuffix(path, "_test.go") {
			dir := filepath.Dir(rel)
			if dir == filepath.Join("pkg", "mg", "views") {
				return nil
			}
		}

		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return nil // skip unparseable files
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "oro/pkg/mg/ui" {
				violators = append(violators, rel)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	if len(violators) > 0 {
		t.Errorf("files that still import oro/pkg/mg/ui (all must be updated):\n  %s",
			strings.Join(violators, "\n  "))
	}
}
