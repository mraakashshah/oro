package mg

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoMgTmuxImports(t *testing.T) {
	repoRoot := findRepoRoot(t)

	// Check that pkg/mg/tmux does not exist
	tmuxPath := filepath.Join(repoRoot, "pkg", "mg", "tmux")
	_, err := os.Stat(tmuxPath)
	if err == nil {
		t.Fatalf("pkg/mg/tmux directory still exists at %s, must be deleted", tmuxPath)
	}
	if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking pkg/mg/tmux: %v", err)
	}

	// Walk all .go files (excluding _test.go in pkg/mg/views)
	fset := token.NewFileSet()
	pkgImports := make(map[string]bool)

	err = filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		// Skip _test.go files in pkg/mg/views
		if strings.HasSuffix(path, "_test.go") {
			if strings.Contains(path, "pkg/mg/views") {
				return nil
			}
		}

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", path, err)
		}

		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, "\"")
			pkgImports[importPath] = true
		}

		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk repo: %v", err)
	}

	// Assert no source file imports oro/pkg/mg/tmux
	if pkgImports["oro/pkg/mg/tmux"] {
		t.Errorf("found import of oro/pkg/mg/tmux, which should not exist")
	}
}

// findRepoRoot walks up from pwd to find the directory containing go.mod
func findRepoRoot(t *testing.T) string {
	t.Helper()
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(pwd, "go.mod")); err == nil {
			return pwd
		}

		parent := filepath.Dir(pwd)
		if parent == pwd {
			t.Fatal("could not find repo root (go.mod)")
		}
		pwd = parent
	}
}
