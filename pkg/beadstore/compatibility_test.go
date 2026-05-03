package beadstore_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// openTestSQLiteStore creates a temporary SQLiteStore for use in external tests.
// The database file lives under t.TempDir() and is cleaned up automatically.
func openTestSQLiteStore(t *testing.T) *beadstore.SQLiteStore {
	t.Helper()
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "beads.db")
	store, err := beadstore.OpenSQLiteStore(ctx, db)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	return store
}

// TestSQLiteStoreDependencyRoundTrip verifies that dependencies persisted via
// AddDependency are returned as protocol.Dependency values when the bead is
// shown, keeping the bead-shaped dependency API stable.
func TestSQLiteStoreDependencyRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestSQLiteStore(t)

	_, err := store.Create(ctx, beadstore.CreateParams{ID: "oro-parent", Title: "parent task", Type: "task"})
	if err != nil {
		t.Fatalf("Create parent: %v", err)
	}
	_, err = store.Create(ctx, beadstore.CreateParams{ID: "oro-child", Title: "child task", Type: "task"})
	if err != nil {
		t.Fatalf("Create child: %v", err)
	}

	if err := store.AddDependency(ctx, "oro-child", "oro-parent", "blocks"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	child, err := store.Show(ctx, "oro-child")
	if err != nil {
		t.Fatalf("Show child: %v", err)
	}
	if child == nil {
		t.Fatal("Show returned nil for existing bead")
	}

	if len(child.Dependencies) != 1 {
		t.Fatalf("expected 1 dependency, got %d: %v", len(child.Dependencies), child.Dependencies)
	}
	dep := child.Dependencies[0]
	if dep.IssueID != "oro-child" {
		t.Errorf("Dependency.IssueID = %q, want %q", dep.IssueID, "oro-child")
	}
	if dep.DependsOnID != "oro-parent" {
		t.Errorf("Dependency.DependsOnID = %q, want %q", dep.DependsOnID, "oro-parent")
	}
	if dep.Type != "blocks" {
		t.Errorf("Dependency.Type = %q, want %q", dep.Type, "blocks")
	}
}

// TestSQLiteStoreBeadAPIReturnShape verifies that SQLiteStore.Create and
// SQLiteStore.Show return protocol.Bead values with the expected shape, so
// callers that depend on bead-shaped responses stay compatible.
func TestSQLiteStoreBeadAPIReturnShape(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestSQLiteStore(t)

	created, err := store.Create(ctx, beadstore.CreateParams{
		ID:                 "oro-shape",
		Title:              "shape check",
		Type:               "task",
		Priority:           1,
		Description:        "verify bead-shaped return",
		AcceptanceCriteria: "Test: shape | Assert: PASS",
		Tags:               []string{"compat"},
		Labels:             []string{"guard"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if created.ID != "oro-shape" {
		t.Errorf("Created.ID = %q, want %q", created.ID, "oro-shape")
	}
	if created.Status != "open" {
		t.Errorf("Created.Status = %q, want open", created.Status)
	}

	// Verify the returned value is protocol.Bead and JSON-round-trips cleanly.
	data, err := json.Marshal(created)
	if err != nil {
		t.Fatalf("marshal Created bead: %v", err)
	}
	var got protocol.Bead
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal bead from JSON: %v", err)
	}
	if got.ID != "oro-shape" {
		t.Errorf("JSON round-trip ID = %q, want %q", got.ID, "oro-shape")
	}
	if got.AcceptanceCriteria != "Test: shape | Assert: PASS" {
		t.Errorf("JSON round-trip AcceptanceCriteria = %q", got.AcceptanceCriteria)
	}

	shown, err := store.Show(ctx, "oro-shape")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if shown == nil {
		t.Fatal("Show returned nil for existing bead")
	}
	if shown.ID != "oro-shape" {
		t.Errorf("Shown.ID = %q, want %q", shown.ID, "oro-shape")
	}
}

// TestBeadstoreBeadSchemaCompatibility verifies that the SQLite database opened
// by OpenSQLiteStore has the beads and bead_* tables so schema renames are
// caught at the beadstore level.
func TestBeadstoreBeadSchemaCompatibility(t *testing.T) {
	t.Parallel()

	store := openTestSQLiteStore(t)

	// Exercise the schema by creating a bead and verifying the store works end-to-end.
	ctx := context.Background()
	_, err := store.Create(ctx, beadstore.CreateParams{
		ID:    "oro-schema-check",
		Title: "schema compatibility guard",
	})
	if err != nil {
		t.Fatalf("Create failed — schema may be incompatible: %v", err)
	}

	bead, err := store.Show(ctx, "oro-schema-check")
	if err != nil {
		t.Fatalf("Show failed — schema may be incompatible: %v", err)
	}
	if bead == nil {
		t.Fatal("Show returned nil — beads table missing or broken")
	}
}
