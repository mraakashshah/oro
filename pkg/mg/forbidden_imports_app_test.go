package mg

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const forbiddenMgAppImport = "oro/pkg/mg/app"

func TestNoMgAppImports(t *testing.T) {
	root := mgFindModuleRoot(t)

	// Assert pkg/mg/app directory does not exist
	appDir := filepath.Join(root, "pkg", "mg", "app")
	_, statErr := os.Stat(appDir)
	if !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("expected pkg/mg/app to not exist, got: %v", statErr)
	}

	// Walk all .go files and assert none import oro/pkg/mg/app
	fset := token.NewFileSet()
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		// Exclude _test.go files under pkg/mg/data and pkg/mg/views
		rel, _ := filepath.Rel(root, path)
		if strings.HasSuffix(path, "_test.go") {
			dir := filepath.Dir(rel)
			if strings.HasPrefix(dir, filepath.Join("pkg", "mg", "data")) ||
				strings.HasPrefix(dir, filepath.Join("pkg", "mg", "views")) {
				return nil
			}
		}

		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return nil // skip files that cannot be parsed
		}

		for _, imp := range f.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if importPath == forbiddenMgAppImport {
				t.Errorf("file %s imports forbidden package %s", rel, forbiddenMgAppImport)
			}
		}

		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk failed: %v", walkErr)
	}
}

func mgFindModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}
