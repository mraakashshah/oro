package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
)

func TestExecuteWorkDryRunReportsEffectiveWorkerAndReviewRouting(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	oroHome := filepath.Join(tmpDir, "oro-home")
	if err := os.MkdirAll(oroHome, 0o755); err != nil {
		t.Fatalf("create ORO_HOME: %v", err)
	}
	config := `agent:
  tiers:
    balanced: {runtime: codex, model: gpt-5.6-terra, reasoning: medium}
  roles:
    worker: {transport: cli, tier: balanced}
    ops_review: {transport: cli, runtime: claude, model: fable}
`
	if err := os.WriteFile(filepath.Join(oroHome, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write agent config: %v", err)
	}
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "routing-test")

	db, err := openStateDB(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := beadstore.NewSQLiteStore(db)
	if _, err := store.Create(ctx, beadstore.CreateParams{
		ID:                 "oro-routing-dry-run",
		Title:              "Verify dry-run routing",
		Type:               "task",
		AcceptanceCriteria: "routing is observable",
	}); err != nil {
		t.Fatalf("create bead: %v", err)
	}

	var output bytes.Buffer
	previousLogOut := logOut
	logOut = &output
	t.Cleanup(func() { logOut = previousLogOut })

	err = executeWork(ctx, &workConfig{
		beadID:  "oro-routing-dry-run",
		timeout: time.Minute,
		dryRun:  true,
	}, &workDeps{beadSrc: store, repoRoot: tmpDir})
	if err != nil {
		t.Fatalf("executeWork dry-run: %v", err)
	}

	got := output.String()
	for _, want := range []string{
		"runtime=codex",
		"model=gpt-5.6-terra",
		"reasoning=medium",
		"review-runtime=claude",
		"review-model=fable",
		"review-reasoning=",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, got)
		}
	}
}
