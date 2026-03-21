package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDoctorRecoverCorruptDolt verifies the full happy path:
//   - corrupt dolt detected via .dolt subdir in beadsDir/dolt
//   - backed up to .beads/backup/dolt-corrupt-DATE
//   - reinitialised via injected bd init --from-jsonl
//   - full-state.jsonl copied to issues.jsonl before reinit
func TestDoctorRecoverCorruptDolt(t *testing.T) {
	beadsDir := t.TempDir()

	// Create corrupt dolt: place a .dolt subdirectory directly under beadsDir/dolt
	// (instead of inside a named database subdirectory) to simulate corruption.
	corruptDoltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(filepath.Join(corruptDoltDir, ".dolt"), 0o750); err != nil {
		t.Fatalf("setup corrupt dolt: %v", err)
	}

	// Place a full-state.jsonl in beadsDir.
	fullStateJSONL := `{"id":"bead-1","title":"test bead"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "full-state.jsonl"), []byte(fullStateJSONL), 0o600); err != nil {
		t.Fatalf("setup full-state.jsonl: %v", err)
	}

	// Record calls to the injected runner.
	var calledArgs [][]string
	var buf bytes.Buffer

	fixedTime := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)

	cfg := &doctorRecoverConfig{
		beadsDir: beadsDir,
		w:        &buf,
		now:      func() time.Time { return fixedTime },
		runCmd: func(name string, args ...string) error {
			calledArgs = append(calledArgs, append([]string{name}, args...))
			return nil
		},
	}

	if err := runDoctorRecoverDolt(cfg); err != nil {
		t.Fatalf("runDoctorRecoverDolt: %v", err)
	}

	out := buf.String()

	// Assert corrupt detection message.
	if !strings.Contains(out, "corrupt") {
		t.Errorf("expected 'corrupt' in output, got: %s", out)
	}

	// Assert backup was created at the expected path.
	expectedBackup := filepath.Join(beadsDir, "backup", "dolt-corrupt-20260320-120000")
	if _, err := os.Stat(expectedBackup); os.IsNotExist(err) {
		t.Errorf("expected backup dir at %s, not found", expectedBackup)
	}

	// Assert original dolt dir was removed/moved.
	if _, err := os.Stat(corruptDoltDir); err == nil {
		t.Errorf("expected original dolt dir to be moved away, but it still exists at %s", corruptDoltDir)
	}

	// Assert issues.jsonl was written from full-state.jsonl.
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	issuesData, err := os.ReadFile(issuesPath)
	if err != nil {
		t.Errorf("expected issues.jsonl to be written: %v", err)
	} else if string(issuesData) != fullStateJSONL {
		t.Errorf("issues.jsonl content mismatch: got %q, want %q", string(issuesData), fullStateJSONL)
	}

	// Assert bd init --from-jsonl was called.
	var foundInit bool
	for _, call := range calledArgs {
		if len(call) >= 3 && call[0] == "bd" && call[1] == "init" && call[2] == "--from-jsonl" {
			foundInit = true
		}
	}
	if !foundInit {
		t.Errorf("expected bd init --from-jsonl to be called; got calls: %v", calledArgs)
	}

	// Assert output includes restore success message.
	if !strings.Contains(out, "restored") {
		t.Errorf("expected 'restored' in output, got: %s", out)
	}
}

// TestDoctorRecoverCorruptDolt_NoFullState verifies the edge case where
// full-state.jsonl is absent: warn user, reinit with empty db.
func TestDoctorRecoverCorruptDolt_NoFullState(t *testing.T) {
	beadsDir := t.TempDir()

	// Create corrupt dolt dir.
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", ".dolt"), 0o750); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// No full-state.jsonl.

	var calledArgs [][]string
	var buf bytes.Buffer

	cfg := &doctorRecoverConfig{
		beadsDir: beadsDir,
		w:        &buf,
		now:      func() time.Time { return time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC) },
		runCmd: func(name string, args ...string) error {
			calledArgs = append(calledArgs, append([]string{name}, args...))
			return nil
		},
	}

	if err := runDoctorRecoverDolt(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()

	// Warn about missing full-state.jsonl.
	if !strings.Contains(out, "full-state.jsonl") {
		t.Errorf("expected warning about missing full-state.jsonl, got: %s", out)
	}

	// bd init still called, but without --from-jsonl.
	var foundInit bool
	for _, call := range calledArgs {
		if len(call) >= 2 && call[0] == "bd" && call[1] == "init" {
			foundInit = true
			// Ensure --from-jsonl is NOT in args.
			for _, a := range call[2:] {
				if a == "--from-jsonl" {
					t.Errorf("expected bd init WITHOUT --from-jsonl when no full-state.jsonl exists")
				}
			}
		}
	}
	if !foundInit {
		t.Errorf("expected bd init to be called; got: %v", calledArgs)
	}
}

// TestDoctorRecoverCorruptDolt_InitFails verifies that bd init failure aborts with an error.
func TestDoctorRecoverCorruptDolt_InitFails(t *testing.T) {
	beadsDir := t.TempDir()

	// Create corrupt dolt dir.
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", ".dolt"), 0o750); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var buf bytes.Buffer
	cfg := &doctorRecoverConfig{
		beadsDir: beadsDir,
		w:        &buf,
		now:      func() time.Time { return time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC) },
		runCmd: func(name string, args ...string) error {
			if name == "bd" {
				return fmt.Errorf("bd init failed: database error")
			}
			return nil
		},
	}

	err := runDoctorRecoverDolt(cfg)
	if err == nil {
		t.Fatal("expected error when bd init fails, got nil")
	}
	if !strings.Contains(err.Error(), "bd init") {
		t.Errorf("expected error to mention 'bd init', got: %v", err)
	}
}

// TestDoctorRecoverCorruptDolt_NotCorrupt verifies that no action is taken
// when dolt is healthy (no .dolt subdir directly under beadsDir/dolt).
func TestDoctorRecoverCorruptDolt_NotCorrupt(t *testing.T) {
	beadsDir := t.TempDir()

	// Create a healthy dolt: .dolt is nested under a named DB directory.
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "beads", ".dolt"), 0o750); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var calledArgs [][]string
	var buf bytes.Buffer

	cfg := &doctorRecoverConfig{
		beadsDir: beadsDir,
		w:        &buf,
		now:      func() time.Time { return time.Now() },
		runCmd: func(name string, args ...string) error {
			calledArgs = append(calledArgs, append([]string{name}, args...))
			return nil
		},
	}

	if err := runDoctorRecoverDolt(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No commands should have been called.
	if len(calledArgs) > 0 {
		t.Errorf("expected no commands for healthy dolt, got: %v", calledArgs)
	}

	out := buf.String()
	if !strings.Contains(out, "OK") && !strings.Contains(out, "ok") && !strings.Contains(out, "healthy") {
		t.Errorf("expected healthy-dolt message in output, got: %s", out)
	}
}
