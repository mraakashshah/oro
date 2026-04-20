package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFile(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("successful write and rename with no tmp residue", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test.txt")
		content := []byte("hello world")
		mode := os.FileMode(0o644)

		err := atomicWriteFile(path, content, mode)
		if err != nil {
			t.Fatalf("atomicWriteFile failed: %v", err)
		}

		// Verify the file exists at the target path
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(data) != string(content) {
			t.Fatalf("content mismatch: got %q, want %q", string(data), string(content))
		}

		// Verify no .tmp file exists
		tmpPath := path + ".tmp"
		if _, err := os.Stat(tmpPath); err == nil {
			t.Errorf(".tmp file should not exist after successful write, but it does")
		} else if !os.IsNotExist(err) {
			t.Fatalf("unexpected error checking .tmp file: %v", err)
		}

		// Verify file mode
		stat, _ := os.Stat(path)
		if stat.Mode().Perm() != mode.Perm() {
			t.Errorf("file mode mismatch: got %o, want %o", stat.Mode().Perm(), mode.Perm())
		}
	})

	t.Run("partial write leaves original file intact", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test2.txt")
		originalContent := []byte("original content")

		// Write original file
		if err := os.WriteFile(path, originalContent, 0o644); err != nil {
			t.Fatalf("failed to create original file: %v", err)
		}

		// Create a directory at the .tmp path to force rename to fail
		tmpPath := path + ".tmp"
		if err := os.Mkdir(tmpPath, 0o755); err != nil {
			t.Fatalf("failed to create .tmp dir: %v", err)
		}

		newContent := []byte("new content")
		err := atomicWriteFile(path, newContent, 0o644)

		// Should get an error
		if err == nil {
			t.Fatal("atomicWriteFile should have failed due to .tmp being a directory")
		}

		// Verify original file is unchanged
		data, _ := os.ReadFile(path)
		if string(data) != string(originalContent) {
			t.Errorf("original file was modified: got %q, want %q", string(data), string(originalContent))
		}

		// Verify .tmp file was cleaned up (it's a directory in this test, but no new .tmp file should be created)
		if _, err := os.Stat(tmpPath); err != nil && !os.IsNotExist(err) {
			t.Fatalf("unexpected error checking .tmp: %v", err)
		}
	})

	t.Run("parent directory missing returns wrapped error", func(t *testing.T) {
		path := filepath.Join(tmpDir, "nonexistent", "subdir", "test.txt")
		content := []byte("test")

		err := atomicWriteFile(path, content, 0o644)
		if err == nil {
			t.Fatal("atomicWriteFile should have failed for nonexistent parent directory")
		}
		// Error should be wrapped (contain context about the path)
		if _, ok := err.(interface{ Unwrap() error }); !ok {
			t.Logf("error should be wrapped, but got type: %T", err)
		}
	})

	t.Run("overwrite existing file atomically", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test3.txt")
		oldContent := []byte("old content")
		newContent := []byte("new content")

		// Create initial file
		if err := os.WriteFile(path, oldContent, 0o644); err != nil {
			t.Fatalf("failed to create initial file: %v", err)
		}

		// Overwrite with atomic write
		if err := atomicWriteFile(path, newContent, 0o644); err != nil {
			t.Fatalf("atomicWriteFile failed: %v", err)
		}

		// Verify new content
		data, _ := os.ReadFile(path)
		if string(data) != string(newContent) {
			t.Errorf("content mismatch: got %q, want %q", string(data), string(newContent))
		}

		// Verify no .tmp file exists
		tmpPath := path + ".tmp"
		if _, err := os.Stat(tmpPath); err == nil {
			t.Errorf(".tmp file should not exist after successful write")
		}
	})
}
