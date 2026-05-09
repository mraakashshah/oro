package dispatcher //nolint:testpackage // white-box: writeExitMarker writes alongside the dispatcher DB path

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteExitMarker_Panic regression-tests oro-zxxn.
//
// Background: dispatcher PID 48471 died silently between 22:42-23:30 UTC on
// 2026-05-05 with no log line, no events row, no stderr file — leaving no
// way to tell whether it panicked, was OS-killed, or got SIGTERM'd. The fix
// adds writeExitMarker, deferred from Run(), which captures any panic stack
// to a file alongside the dispatcher DB so the next operator has at least
// one breadcrumb.
//
// The marker file lives next to d.cfg.DBPath (so it shares the project
// directory and is easy to find via the standard runbook path).
func TestWriteExitMarker_Panic(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "oro.db")
	d := &Dispatcher{cfg: Config{DBPath: dbPath}}

	d.writeExitMarker("panic", "boom: divide by zero", []byte("goroutine 42 [running]:\nfoo.bar(0xdead)\n"))

	markerPath := filepath.Join(dir, "dispatcher.exit.log")
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read exit marker %s: %v", markerPath, err)
	}
	body := string(data)
	if !strings.Contains(body, "panic") {
		t.Errorf("expected exit marker to contain 'panic', got: %q", body)
	}
	if !strings.Contains(body, "boom: divide by zero") {
		t.Errorf("expected exit marker to contain panic message, got: %q", body)
	}
	if !strings.Contains(body, "foo.bar") {
		t.Errorf("expected exit marker to contain stack trace, got: %q", body)
	}
}

// TestWriteExitMarker_Appends asserts that successive exits append rather
// than truncate. Multiple dispatcher restart cycles in the same project
// directory should leave a trail, not just the most recent reason.
func TestWriteExitMarker_Appends(t *testing.T) {
	dir := t.TempDir()
	d := &Dispatcher{cfg: Config{DBPath: filepath.Join(dir, "oro.db")}}

	d.writeExitMarker("normal", "ctx_done", nil)
	d.writeExitMarker("panic", "second exit", []byte("stack"))

	body, err := os.ReadFile(filepath.Join(dir, "dispatcher.exit.log"))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "ctx_done") {
		t.Errorf("first exit should still be present after second write, got: %q", s)
	}
	if !strings.Contains(s, "second exit") {
		t.Errorf("second exit should be appended, got: %q", s)
	}
}

func TestWriteExitMarker_SkipsMemoryDB(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	d := &Dispatcher{cfg: Config{DBPath: ":memory:"}}

	d.writeExitMarker("normal", "ctx_done", nil)

	if _, err := os.Stat(filepath.Join(dir, "dispatcher.exit.log")); !os.IsNotExist(err) {
		t.Fatalf("dispatcher.exit.log should not be written for :memory: DB, stat err: %v", err)
	}
}
