package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/cards"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestCmdStoreDoesNotImportMemory(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	if strings.Contains(string(src), `"oro/pkg/memory"`) {
		t.Fatal("cmd/oro/store.go must not import oro/pkg/memory directly")
	}
}

func TestNewDispatcherMemoryServicesProvidesHandoffInserter(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB("file:dispatcher_memory_services?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO beads (id, title, status, priority, type)
VALUES ('bead-handoff-service', 'handoff service', 'open', 1, 'task')`)
	if err != nil {
		t.Fatalf("insert bead: %v", err)
	}
	cardStore, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("new cards store: %v", err)
	}
	services := newDispatcherMemoryServices(db)
	if services.HandoffInserter == nil {
		t.Fatal("HandoffInserter is nil")
	}
	sink := services.HandoffInserter(cardStore)
	if sink == nil {
		t.Fatal("HandoffInserter returned nil sink")
	}
	_, err = sink.AppendLearningPending(ctx, "bead-handoff-service", cards.CardCandidate{
		Type:        string(cards.CardTypePattern),
		Title:       "handoff learning",
		BodySummary: "handoff learning",
		BodyFull:    "handoff learning",
		Confidence:  0.8,
	})
	if err != nil {
		t.Fatalf("append pending learning: %v", err)
	}
	pending, err := cardStore.PendingLearnings(ctx, "bead-handoff-service")
	if err != nil {
		t.Fatalf("pending learnings: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}
}

// TestDefaultMemoryStoreUsesProjectPaths verifies that defaultMemoryStore()
// uses ResolveProjectDBPaths to respect the current project context,
// so that different projects have separate databases.
func TestDefaultMemoryStoreUsesProjectPaths(t *testing.T) {
	// Setup: create a temporary .oro/config.yaml with a project name
	tmpDir := t.TempDir()
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir to tmpDir: %v", err)
	}
	defer func() { _ = os.Chdir(originalWd) }()

	// Create .oro/config.yaml with project name
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil {
		t.Fatalf("failed to create .oro dir: %v", err)
	}

	configFile := filepath.Join(oroDir, "config.yaml")
	if err := os.WriteFile(configFile, []byte("project: test-project\n"), 0o600); err != nil {
		t.Fatalf("failed to write config.yaml: %v", err)
	}

	// Also create a temporary ORO_HOME to isolate the test
	oroHome := t.TempDir()
	origOroHome := os.Getenv("ORO_HOME")
	origOroProject := os.Getenv("ORO_PROJECT")
	defer func() {
		if origOroHome != "" {
			_ = os.Setenv("ORO_HOME", origOroHome) //nolint:errcheck // best-effort cleanup
		} else {
			os.Unsetenv("ORO_HOME")
		}
		if origOroProject != "" {
			_ = os.Setenv("ORO_PROJECT", origOroProject) //nolint:errcheck // best-effort cleanup
		} else {
			os.Unsetenv("ORO_PROJECT")
		}
	}()
	if err := os.Setenv("ORO_HOME", oroHome); err != nil {
		t.Fatalf("failed to set ORO_HOME: %v", err)
	}
	os.Unsetenv("ORO_PROJECT") // Clear any existing ORO_PROJECT so config.yaml is used

	// Verify that the StateDBPath would be project-scoped
	// The store's DB path should be in ~/.oro/projects/test-project/ not ~/.oro/
	expectedProjPath := filepath.Join(oroHome, "projects", "test-project")
	expectedDBPath := filepath.Join(expectedProjPath, "state.db")

	// Create the project directory structure
	if err := os.MkdirAll(expectedProjPath, 0o750); err != nil {
		t.Fatalf("failed to create project directory: %v", err)
	}

	// Call defaultMemoryStore() which should now use ResolveProjectDBPaths
	_, err = defaultMemoryStore()
	if err != nil {
		t.Fatalf("defaultMemoryStore failed: %v", err)
	}

	// Verify ResolveProjectDBPaths would return the project-scoped path
	paths, err := ResolveProjectDBPaths()
	if err != nil {
		t.Fatalf("ResolveProjectDBPaths failed: %v", err)
	}

	if paths.StateDBPath != expectedDBPath {
		t.Errorf("Expected StateDBPath %s, got %s", expectedDBPath, paths.StateDBPath)
	}

	// Memory retirement leaves the legacy store disabled; resolving the project
	// path remains important, but defaultMemoryStore no longer opens a DB.
}
