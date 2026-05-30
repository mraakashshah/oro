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
	repoRoot := t.TempDir()
	cacheDir := filepath.Join(repoRoot, ".cache", "go-mod", "example")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "bad.go"), []byte("package bad\nimport ,\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	imports, err := scanPkgMemoryImports(repoRoot)
	if err != nil {
		t.Fatalf("scan repo imports: %v", err)
	}
	if len(imports) > 0 {
		t.Fatalf("oro/pkg/memory imports remain after retirement: %v", imports)
	}
}

func scanPkgMemoryImports(repoRoot string) ([]string, error) {
	var imports []string
	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if shouldSkipPkgMemoryScanDir(entry.Name()) {
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
	return imports, err
}

func shouldSkipPkgMemoryScanDir(name string) bool {
	switch name {
	case ".git", ".worktrees", "ad_hoc", "vendor", ".cache":
		return true
	default:
		return false
	}
}
