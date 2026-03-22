package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/protocol"
)

// TestAssignPayloadUsesProjectPaths verifies that buildAssignPayload reads the
// worker-program.md from cfg.WorkerProgram (populated from ProjectPaths) rather
// than the hardcoded filepath.Join(cfg.RepoRoot, "worker-program.md").
func TestAssignPayloadUsesProjectPaths(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	repoRoot := t.TempDir()
	d.cfg.RepoRoot = repoRoot

	// Place worker-program.md at a custom path that differs from
	// repoRoot/worker-program.md. This proves cfg.WorkerProgram is used.
	customDir := t.TempDir()
	customWorkerProgramPath := filepath.Join(customDir, "worker-program.md")
	wpContent := "# Project-Specific Worker Program\nThis is NOT at repoRoot."
	if err := os.WriteFile(customWorkerProgramPath, []byte(wpContent), 0o600); err != nil {
		t.Fatal(err)
	}

	d.cfg.WorkerProgram = customWorkerProgramPath

	// Explicitly do NOT write worker-program.md at repoRoot so that if
	// buildAssignPayload falls back to the hardcoded path it gets empty content.

	beadSrc.shown["bead-wp"] = &protocol.BeadDetail{Title: "Bead WP"}

	w := &trackedWorker{
		id:     "worker-1",
		beadID: "bead-wp",
	}
	d.shutdownRunner = &mockCommandRunner{output: []byte("abc git log")}

	got := d.buildAssignPayload(context.Background(), w, 1, "", "")

	if got.WorkerProgram != wpContent {
		t.Errorf("WorkerProgram = %q, want %q\n(cfg.WorkerProgram should be used, not filepath.Join(repoRoot, \"worker-program.md\"))",
			got.WorkerProgram, wpContent)
	}
}
