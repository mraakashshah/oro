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

// workGoSymbols are the exported and unexported identifiers defined in work.go.
var workGoSymbols = []string{
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

func TestNoMgWorkOnDisk(t *testing.T) {
	root := mgFindModuleRoot(t)

	// Both deletion targets must not exist.
	for _, rel := range []string{
		filepath.Join("pkg", "mg", "work.go"),
		filepath.Join("pkg", "mg", "work_test.go"),
	} {
		path := filepath.Join(root, rel)
		_, err := os.Stat(path)
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("expected %s to not exist, got: %v", rel, err)
		}
	}

	// No .go file outside of the deleted files may reference mg.Work symbols.
	fset := token.NewFileSet()
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip archive and hidden dirs.
			base := d.Name()
			if base == "archive" || strings.HasPrefix(base, ".") {
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
			return nil // skip unparseable files
		}

		ast.Inspect(f, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, sym := range workGoSymbols {
				if ident.Name == sym {
					t.Errorf("file %s references deleted symbol %q", rel, sym)
				}
			}
			return true
		})

		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk failed: %v", walkErr)
	}
}
