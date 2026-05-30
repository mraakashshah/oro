package main

import (
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPkgMemoryRetiredFromRepository(t *testing.T) {
	repoRoot := currentRepoRoot()
	memoryDir := filepath.Join(repoRoot, "pkg", "memory")
	if _, err := os.Stat(memoryDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("pkg/memory must be removed after retirement, stat error = %v", err)
	}

	imports, err := scanPkgMemoryImports(repoRoot)
	if err != nil {
		t.Fatalf("scan repo imports: %v", err)
	}
	if len(imports) > 0 {
		t.Fatalf("oro/pkg/memory imports remain after retirement: %v", imports)
	}
}

func TestPkgMemoryRetiredScanSkipsIgnoredCacheDirs(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		".cache/go-mod/cache/download/example.com/broken.go": "package broken\n\nimport (",
		"pkg/live/live.go":     "package live\n\nimport \"oro/pkg/memory\"\n",
		"pkg/source/source.go": "package source\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	imports, err := scanPkgMemoryImports(root)
	if err != nil {
		t.Fatalf("scanPkgMemoryImports: %v", err)
	}
	if got, want := imports, []string{"pkg/live/live.go"}; !sameStrings(got, want) {
		t.Fatalf("imports = %v, want %v", got, want)
	}
}

func scanPkgMemoryImports(repoRoot string) ([]string, error) {
	var imports []string
	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".cache", ".git", ".worktrees", "ad_hoc", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			if strings.Trim(spec.Path.Value, `"`) == "oro/pkg/memory" {
				rel, relErr := filepath.Rel(repoRoot, path)
				if relErr != nil {
					rel = path
				}
				imports = append(imports, rel)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return imports, nil
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
