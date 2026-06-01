package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNoLegacyManagerRoleAPI(t *testing.T) {
	t.Parallel()

	repoRoot := testRepoRoot(t)
	for _, name := range []string{"manager.go", "manager_test.go"} {
		path := filepath.Join(repoRoot, "cmd", "oro", name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s must be deleted; stat error = %v", path, err)
		}
	}

	for _, path := range goFilesInDir(t, filepath.Join(repoRoot, "cmd", "oro")) {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, legacy := range []string{
			"Manager" + "Beacon",
			"Manager" + "Nudge",
			"manager" + "Beacon",
			"manager" + "Nudge",
		} {
			if strings.Contains(string(content), legacy) {
				t.Fatalf("%s still references legacy manager role API %q", path, legacy)
			}
		}
	}
}

func testRepoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func goFilesInDir(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}

	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	return paths
}
