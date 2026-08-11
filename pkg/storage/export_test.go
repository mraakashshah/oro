package storage

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOroHomeCleanupOpenedRootConfinement(t *testing.T) {
	t.Parallel()

	t.Run("intermediate symlink preserves outside file", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		logs := filepath.Join(home, "logs")
		if err := os.Mkdir(logs, 0o750); err != nil {
			t.Fatalf("create original logs directory: %v", err)
		}
		root, err := os.OpenRoot(home)
		if err != nil {
			t.Fatalf("open home root: %v", err)
		}
		t.Cleanup(func() { _ = root.Close() })
		outsideFile := filepath.Join(outside, "expired.log")
		if err := os.WriteFile(outsideFile, []byte("preserve"), 0o600); err != nil {
			t.Fatalf("write outside fixture: %v", err)
		}
		if err := os.Remove(logs); err != nil {
			t.Fatalf("remove original logs directory: %v", err)
		}
		if err := os.Symlink(outside, logs); err != nil {
			t.Fatalf("symlink logs outside home: %v", err)
		}

		err = removeOroHomeEntry(root, "logs/expired.log")
		if err == nil {
			t.Fatal("removeOroHomeEntry() error = nil, want intermediate symlink refusal")
		}
		contents, readErr := os.ReadFile(outsideFile)
		if readErr != nil {
			t.Fatalf("outside fixture was removed: %v", readErr)
		}
		if string(contents) != "preserve" {
			t.Fatalf("outside fixture = %q, want preserve", contents)
		}
	})

	t.Run("allowlist and entry type checks", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, "logs", "kept.log"), 0o750); err != nil {
			t.Fatalf("create logs fixtures: %v", err)
		}
		regular := filepath.Join(home, "logs", "expired.log")
		if err := os.WriteFile(regular, []byte("remove"), 0o600); err != nil {
			t.Fatalf("write regular fixture: %v", err)
		}
		if err := os.Symlink("expired.log", filepath.Join(home, "logs", "kept-link.log")); err != nil {
			t.Fatalf("create final symlink fixture: %v", err)
		}
		root, err := os.OpenRoot(home)
		if err != nil {
			t.Fatalf("open home root: %v", err)
		}
		t.Cleanup(func() { _ = root.Close() })

		if err := removeOroHomeEntry(root, "logs/expired.log"); err != nil {
			t.Fatalf("remove allowlisted regular entry: %v", err)
		}
		if _, err := os.Lstat(regular); !os.IsNotExist(err) {
			t.Fatalf("regular entry remains after removal: %v", err)
		}

		for _, test := range []struct {
			name string
			path string
			want string
		}{
			{name: "final symlink", path: "logs/kept-link.log", want: "refuse unsafe"},
			{name: "directory", path: "logs/kept.log", want: "refuse unsafe"},
			{name: "non-allowlisted", path: "config.yaml", want: "refuse non-allowlisted"},
			{name: "invalid parent traversal", path: "../outside.log", want: "refuse non-allowlisted"},
			{name: "missing entry", path: "logs/missing.log", want: "revalidate"},
		} {
			t.Run(test.name, func(t *testing.T) {
				err := removeOroHomeEntry(root, test.path)
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("removeOroHomeEntry(%q) error = %v, want containing %q", test.path, err, test.want)
				}
			})
		}

		if err := root.Close(); err != nil {
			t.Fatalf("close home root: %v", err)
		}
		if err := removeOroHomeEntry(root, "logs/kept-link.log"); err == nil || !strings.Contains(err.Error(), "revalidate") {
			t.Fatalf("remove through closed root error = %v, want revalidation error", err)
		}
	})

	t.Run("non-regular socket is preserved", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix-domain socket fixture is unavailable on Windows")
		}
		home, err := os.MkdirTemp("/tmp", "oro-home-root-")
		if err != nil {
			t.Fatalf("create short home fixture: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(home) })
		if err := os.Mkdir(filepath.Join(home, "logs"), 0o750); err != nil {
			t.Fatalf("create logs fixture: %v", err)
		}
		root, err := os.OpenRoot(home)
		if err != nil {
			t.Fatalf("open home root: %v", err)
		}
		t.Cleanup(func() { _ = root.Close() })
		socketPath := filepath.Join(home, "logs", "kept-socket.log")
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Fatalf("create Unix socket fixture: %v", err)
		}
		t.Cleanup(func() { _ = listener.Close() })

		if err := removeOroHomeEntry(root, "logs/kept-socket.log"); err == nil || !strings.Contains(err.Error(), "refuse unsafe") {
			t.Fatalf("remove socket error = %v, want unsafe-entry refusal", err)
		}
		if _, err := os.Lstat(socketPath); err != nil {
			t.Fatalf("socket fixture was removed: %v", err)
		}
	})
}
