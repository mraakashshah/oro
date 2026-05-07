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

const forbiddenMgComponentsImport = "oro/pkg/mg/components"

func TestNoMgComponentsImports(t *testing.T) {
	root := mgFindModuleRoot(t)

	// Assert pkg/mg/components directory does not exist
	componentsDir := filepath.Join(root, "pkg", "mg", "components")
	_, statErr := os.Stat(componentsDir)
	if !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("expected pkg/mg/components to not exist, got: %v", statErr)
	}

	// Walk all .go files and assert none import oro/pkg/mg/components
	fset := token.NewFileSet()
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		// Exclude _test.go files under pkg/mg/views
		rel, _ := filepath.Rel(root, path)
		if strings.HasSuffix(path, "_test.go") {
			dir := filepath.Dir(rel)
			if strings.HasPrefix(dir, filepath.Join("pkg", "mg", "views")) {
				return nil
			}
		}

		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return nil // skip files that cannot be parsed
		}

		for _, imp := range f.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if importPath == forbiddenMgComponentsImport {
				t.Errorf("file %s imports forbidden package %s", rel, forbiddenMgComponentsImport)
			}
		}

		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk failed: %v", walkErr)
	}
}
