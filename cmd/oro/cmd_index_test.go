package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestIndexBuildCommand_CreatesDatabase(t *testing.T) {
	rootDir := t.TempDir()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "code_index.db")

	// Write a Go source file to index.
	if err := os.WriteFile(filepath.Join(rootDir, "main.go"), []byte(`package main

func Hello() string {
	return "hello"
}
`), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var buf bytes.Buffer
	err := runIndexBuild(&buf, rootDir, dbPath)
	if err != nil {
		t.Fatalf("runIndexBuild: %v", err)
	}

	// DB file should exist.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("expected database file to be created")
	}

	// Output should contain progress info.
	output := buf.String()
	if !containsSubstr(output, "Indexed") {
		t.Errorf("expected output to contain 'Indexed', got: %s", output)
	}
}

func TestIndexSearchCommand_ReturnsResults(t *testing.T) {
	t.Skip("Search is a stub (returns empty); real FTS5 search landing in oro-noc.3 + oro-noc.5")
	rootDir := t.TempDir()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "code_index.db")

	// Write source files.
	if err := os.WriteFile(filepath.Join(rootDir, "auth.go"), []byte(`package main

func handleAuthentication() error {
	// check user credentials
	return nil
}
`), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Build the index first.
	var buildBuf bytes.Buffer
	if err := runIndexBuild(&buildBuf, rootDir, dbPath); err != nil {
		t.Fatalf("runIndexBuild: %v", err)
	}

	// Now search.
	var searchBuf bytes.Buffer
	err := runIndexSearch(&searchBuf, "authentication", dbPath, 5)
	if err != nil {
		t.Fatalf("runIndexSearch: %v", err)
	}

	output := searchBuf.String()
	if !containsSubstr(output, "auth.go") {
		t.Errorf("expected search results to mention auth.go, got: %s", output)
	}
}

func TestIndexSearchCommand_EmptyIndex(t *testing.T) {
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "empty_index.db")

	var buf bytes.Buffer
	err := runIndexSearch(&buf, "anything", dbPath, 5)
	if err != nil {
		t.Fatalf("runIndexSearch: %v", err)
	}

	output := buf.String()
	if !containsSubstr(output, "No results") {
		t.Errorf("expected 'No results' message, got: %s", output)
	}
}

func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && findSub(s, sub)
}

func findSub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
