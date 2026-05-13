package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHeartbeatFullStateBackup verifies that the heartbeat loop periodically
// runs bd export and writes .beads/backup/full-state.jsonl with all issues.
func TestHeartbeatFullStateBackup(t *testing.T) {
	t.Parallel()
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// Use a temp dir as the .beads/ directory so backup output is isolated.
	beadsDir := t.TempDir()
	d.beadsDir = beadsDir
	d.cfg.BackupInterval = 20 * time.Millisecond

	exportData := "{\"id\":\"oro-1\",\"title\":\"Open bead\",\"status\":\"open\"}\n" +
		"{\"id\":\"oro-2\",\"title\":\"Closed bead\",\"status\":\"closed\"}\n"
	beadSrc.mu.Lock()
	beadSrc.exportData = []byte(exportData)
	beadSrc.mu.Unlock()

	cancel := startDispatcher(t, d)
	defer cancel()

	backupPath := filepath.Join(beadsDir, "backup", "full-state.jsonl")
	waitFor(t, func() bool {
		_, err := os.Stat(backupPath)
		return err == nil
	}, 2*time.Second)

	got, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup file: %v", err)
	}
	if !bytes.Equal(got, []byte(exportData)) {
		t.Errorf("backup content = %q, want %q", got, exportData)
	}
}

func TestWriteFileAtomicReplacesContentAndCleansTemp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "full-state.jsonl")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	if err := writeFileAtomic(path, []byte("new\n"), 0o640); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read atomic output: %v", err)
	}
	if string(got) != "new\n" {
		t.Fatalf("atomic output = %q, want %q", got, "new\n")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat atomic output: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o640 {
		t.Fatalf("atomic output mode = %v, want %v", gotMode, os.FileMode(0o640))
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".full-state.jsonl.*.tmp"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}

func TestHeartbeatFullStateBackup_SkipsLegacyPathInSQLiteMode(t *testing.T) {
	t.Parallel()
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	beadsDir := t.TempDir()
	d.beadSourceMode = "sqlite"
	d.beadsDir = beadsDir

	beadSrc.mu.Lock()
	beadSrc.exportData = []byte("{\"id\":\"oro-native\",\"status\":\"open\"}\n")
	beadSrc.mu.Unlock()

	d.backupFullState(t.Context())

	backupPath := filepath.Join(beadsDir, "backup", "full-state.jsonl")
	if _, err := os.Stat(backupPath); err == nil {
		t.Fatal("sqlite mode wrote legacy full-state backup")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat backup path: %v", err)
	}
}

// TestHeartbeatFullStateBackup_ExportError verifies that a bd export failure
// logs a warning and skips writing (non-fatal: no file created).
func TestHeartbeatFullStateBackup_ExportError(t *testing.T) {
	t.Parallel()
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	beadsDir := t.TempDir()
	d.beadsDir = beadsDir
	d.cfg.BackupInterval = 20 * time.Millisecond

	beadSrc.mu.Lock()
	beadSrc.exportErr = os.ErrPermission
	beadSrc.mu.Unlock()

	cancel := startDispatcher(t, d)
	defer cancel()

	// Wait long enough for at least one backup attempt.
	time.Sleep(100 * time.Millisecond)
	cancel()

	// File must NOT exist — export failed, nothing was written.
	backupPath := filepath.Join(beadsDir, "backup", "full-state.jsonl")
	if _, err := os.Stat(backupPath); err == nil {
		t.Error("backup file written despite export error")
	}
}

// TestHeartbeatFullStateBackup_EmptyExport verifies that an empty bd export
// output skips writing the file (nothing to save).
func TestHeartbeatFullStateBackup_EmptyExport(t *testing.T) {
	t.Parallel()
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	beadsDir := t.TempDir()
	d.beadsDir = beadsDir
	d.cfg.BackupInterval = 20 * time.Millisecond

	// exportData defaults to nil → Export() returns nil bytes.
	beadSrc.mu.Lock()
	beadSrc.exportData = nil
	beadSrc.mu.Unlock()

	cancel := startDispatcher(t, d)
	defer cancel()

	time.Sleep(100 * time.Millisecond)
	cancel()

	backupPath := filepath.Join(beadsDir, "backup", "full-state.jsonl")
	if _, err := os.Stat(backupPath); err == nil {
		t.Error("backup file written despite empty export")
	}
}
