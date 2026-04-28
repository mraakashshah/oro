package main

import (
	"bytes"
	"database/sql"
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

func TestMigrateUpdatedAtVerbatim(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	const wantUpdatedAt = "2026-01-02T03:04:05.678Z"
	jsonl := strings.Join([]string{
		`{"id":"oro-preserve","title":"Preserve updated_at","description":"Imported from bd","acceptance_criteria":"Keep source timestamps","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00.000Z","updated_at":"` + wantUpdatedAt + `","tags":["migration"],"labels":["phase-3"],"metadata":{"source":"bd"},"notes":["source note"]}`,
		"",
	}, "\n")
	jsonlPath := filepath.Join(t.TempDir(), "export.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(jsonl), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	cmd := newBeadCmdWithStore(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"migrate-from-dolt", "--from-jsonl", jsonlPath})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("bead migrate-from-dolt error: %v\n%s", err, out.String())
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	defer db.Close()

	var gotUpdatedAt string
	if err := db.QueryRow(`SELECT updated_at FROM beads WHERE id='oro-preserve'`).Scan(&gotUpdatedAt); err != nil {
		t.Fatalf("query migrated updated_at: %v", err)
	}
	if gotUpdatedAt != wantUpdatedAt {
		t.Fatalf("updated_at = %q, want %q", gotUpdatedAt, wantUpdatedAt)
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
