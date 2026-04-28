package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBeadMigrateFromDoltDryRunFixturePrintsPlanWithoutMutatingDB(t *testing.T) {
	repoRoot := findRepoRoot(t)
	fixture := filepath.Join(repoRoot, "testdata", "dolt-100")
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	cmd := newBeadCmdWithStore(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"migrate-from-dolt", "--dry-run", "--from-fixture", fixture})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("bead migrate-from-dolt dry-run error: %v\n%s", err, out.String())
	}

	got := out.String()
	for _, want := range []string{
		"Migration plan",
		"source: fixture",
		"beads: 100",
		"dependencies: 3",
		"tags: 5",
		"labels: 2",
		"metadata entries: 4",
		"notes: 3",
		"DRY RUN -- no writes performed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, got)
		}
	}

	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run mutated DB path %s: stat err=%v", dbPath, err)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root from %s", wd)
		}
	}
}
