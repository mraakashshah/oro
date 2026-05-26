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

	var imports []string
	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".worktrees", "ad_hoc", "vendor":
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
		t.Fatalf("scan repo imports: %v", err)
	}
	if len(imports) > 0 {
		t.Fatalf("oro/pkg/memory imports remain after retirement: %v", imports)
	}
}
