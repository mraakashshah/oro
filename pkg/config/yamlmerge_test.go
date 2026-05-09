package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/config"
)

func TestYAMLMergePreservesUserEdits(t *testing.T) {
	t.Run("replaces only the specified key", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")

		initial := `# top-level comment
languages:
  go:
    test_cmd: go test ./...
memory:
  semantic:
    enabled: true
project: myproject
`
		if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := config.MergeKey(path, "project", "newproject"); err != nil {
			t.Fatalf("MergeKey returned error: %v", err)
		}

		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(out)

		// The specified key must be updated
		if !strings.Contains(s, "project: newproject") {
			t.Errorf("expected project: newproject in output; got:\n%s", s)
		}
		// Other top-level keys must be preserved
		if !strings.Contains(s, "languages:") {
			t.Errorf("expected languages key preserved; got:\n%s", s)
		}
		if !strings.Contains(s, "memory:") {
			t.Errorf("expected memory key preserved; got:\n%s", s)
		}
		// Nested content must survive
		if !strings.Contains(s, "go test ./...") {
			t.Errorf("expected nested test_cmd preserved; got:\n%s", s)
		}
		if !strings.Contains(s, "enabled: true") {
			t.Errorf("expected nested memory.semantic.enabled preserved; got:\n%s", s)
		}
		// Old value must be gone
		if strings.Contains(s, "project: myproject") {
			t.Errorf("old value should be gone; got:\n%s", s)
		}
	})

	t.Run("comments preserved best-effort", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")

		initial := `# top-level comment
project: old
`
		if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := config.MergeKey(path, "project", "new"); err != nil {
			t.Fatalf("MergeKey returned error: %v", err)
		}

		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// Comment preservation is best-effort (NOT byte-identical required),
		// but the document-level comment should survive.
		if !strings.Contains(string(out), "# top-level comment") {
			t.Errorf("expected top-level comment preserved; got:\n%s", string(out))
		}
	})

	t.Run("missing file creates file with key only", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "newconfig.yaml")

		if err := config.MergeKey(path, "project", "created"); err != nil {
			t.Fatalf("MergeKey returned error: %v", err)
		}

		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("file was not created: %v", err)
		}
		if !strings.Contains(string(out), "project: created") {
			t.Errorf("expected project: created in new file; got:\n%s", string(out))
		}
	})

	t.Run("malformed yaml returns error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.yaml")

		if err := os.WriteFile(path, []byte("key: [unclosed"), 0o600); err != nil {
			t.Fatal(err)
		}

		err := config.MergeKey(path, "project", "x")
		if err == nil {
			t.Error("expected error for malformed yaml, got nil")
		}
	})

	t.Run("no key match inserts new key", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")

		initial := `languages:
  go:
    test_cmd: go test ./...
`
		if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := config.MergeKey(path, "project", "inserted"); err != nil {
			t.Fatalf("MergeKey returned error: %v", err)
		}

		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(out)

		if !strings.Contains(s, "project: inserted") {
			t.Errorf("expected project: inserted; got:\n%s", s)
		}
		if !strings.Contains(s, "languages:") {
			t.Errorf("expected languages preserved; got:\n%s", s)
		}
	})

	t.Run("replaces structured value (map)", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")

		initial := `project: old
languages:
  go:
    test_cmd: old
`
		if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
			t.Fatal(err)
		}

		newLang := map[string]any{
			"go": map[string]any{"test_cmd": "go test -race ./..."},
		}
		if err := config.MergeKey(path, "languages", newLang); err != nil {
			t.Fatalf("MergeKey returned error: %v", err)
		}

		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(out)

		if !strings.Contains(s, "go test -race") {
			t.Errorf("expected updated test_cmd; got:\n%s", s)
		}
		if !strings.Contains(s, "project: old") {
			t.Errorf("expected project preserved; got:\n%s", s)
		}
	})
}
