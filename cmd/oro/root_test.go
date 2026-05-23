package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacySourceFilesDeleted(t *testing.T) {
	root := repoRootForSourceDeletionTest(t)
	legacyFiles := []string{
		"cmd/oro/cmd_dolt.go",
		"cmd/oro/cmd_bd.go",
		"cmd/oro/dolt.go",
		"pkg/dispatcher/dolt_recovery.go",
		"pkg/dispatcher/beadsource.go",
		"cmd/oro/port_registry.go",
	}

	for _, name := range legacyFiles {
		path := filepath.Join(root, filepath.FromSlash(name))
		_, err := os.Stat(path)
		if err == nil {
			t.Fatalf("legacy source file still exists: %s", name)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat %s: %v", name, err)
		}
	}
}

func repoRootForSourceDeletionTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root")
		}
		dir = parent
	}
}
