package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoLegacyManagerForwardingAPI(t *testing.T) {
	repoRoot := currentRepoRoot()
	forbidden := []string{
		"Forward" + "CommandTo" + "Manager",
		"Format" + "Forward" + "Message",
	}

	refs, err := scanForbiddenGoIdentifiers(repoRoot, forbidden)
	if err != nil {
		t.Fatalf("scan Go identifiers: %v", err)
	}
	if len(refs) > 0 {
		t.Fatalf("legacy manager forwarding API identifiers remain: %v", refs)
	}
}

func scanForbiddenGoIdentifiers(repoRoot string, forbidden []string) ([]string, error) {
	forbiddenSet := make(map[string]struct{}, len(forbidden))
	for _, name := range forbidden {
		forbiddenSet[name] = struct{}{}
	}

	var refs []string
	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if shouldSkipLegacyForwardScanDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			if _, found := forbiddenSet[ident.Name]; !found {
				return true
			}
			rel, relErr := filepath.Rel(repoRoot, path)
			if relErr != nil {
				rel = path
			}
			pos := fileSet.Position(ident.Pos())
			refs = append(refs, rel+":"+pos.String())
			return true
		})
		return nil
	})
	return refs, err
}

func shouldSkipLegacyForwardScanDir(name string) bool {
	switch name {
	case ".git", ".worktrees", "ad_hoc", "vendor", ".cache":
		return true
	default:
		return false
	}
}
