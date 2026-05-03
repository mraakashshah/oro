package protocol_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"oro/pkg/protocol"
)

// TestBeadJSONFieldNames pins the json tag names on protocol.Bead so that
// accidental renames break this test before they reach production.
func TestBeadJSONFieldNames(t *testing.T) {
	t.Parallel()

	required := map[string]string{
		"ID":                 "id",
		"Title":              "title",
		"AcceptanceCriteria": "acceptance_criteria,omitempty",
		"Description":        "description,omitempty",
		"Type":               "issue_type,omitempty",
		"Epic":               "parent,omitempty",
		"Dependencies":       "dependencies,omitempty",
	}

	beadType := reflect.TypeOf(protocol.Bead{})
	for fieldName, wantTag := range required {
		f, ok := beadType.FieldByName(fieldName)
		if !ok {
			t.Errorf("protocol.Bead missing field %q", fieldName)
			continue
		}
		got := f.Tag.Get("json")
		if got != wantTag {
			t.Errorf("protocol.Bead.%s json tag = %q, want %q", fieldName, got, wantTag)
		}
	}
}

// TestBeadDependencyJSONFields pins the JSON serialization of protocol.Dependency
// so that the issue_id / depends_on_id field names stay stable.
func TestBeadDependencyJSONFields(t *testing.T) {
	t.Parallel()

	dep := protocol.Dependency{
		IssueID:     "oro-child",
		DependsOnID: "oro-parent",
		Type:        "blocks",
	}

	data, err := json.Marshal(dep)
	if err != nil {
		t.Fatalf("marshal Dependency: %v", err)
	}

	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal Dependency to map: %v", err)
	}

	if got := raw["issue_id"]; got != "oro-child" {
		t.Errorf("Dependency.IssueID must serialize as json key 'issue_id', got map: %v", raw)
	}
	if got := raw["depends_on_id"]; got != "oro-parent" {
		t.Errorf("Dependency.DependsOnID must serialize as json key 'depends_on_id', got map: %v", raw)
	}
	if got := raw["type"]; got != "blocks" {
		t.Errorf("Dependency.Type must serialize as json key 'type', got map: %v", raw)
	}

	var got protocol.Dependency
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal Dependency: %v", err)
	}
	if got != dep {
		t.Errorf("Dependency round-trip mismatch: want %+v, got %+v", dep, got)
	}
}

// TestAssignmentPayloadBeadIDJSONField pins that AssignPayload.BeadID is
// serialized as the JSON key "bead_id".
func TestAssignmentPayloadBeadIDJSONField(t *testing.T) {
	t.Parallel()

	p := protocol.AssignPayload{BeadID: "oro-test", Worktree: "/tmp/wt"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal AssignPayload: %v", err)
	}
	if !strings.Contains(string(data), `"bead_id":"oro-test"`) {
		t.Errorf("AssignPayload must serialize BeadID as bead_id, got: %s", data)
	}
}

// TestEventJSONBeadIDField pins that protocol.Event.BeadID is serialized as
// the JSON key "bead_id".
func TestEventJSONBeadIDField(t *testing.T) {
	t.Parallel()

	e := protocol.Event{
		BeadID:    "oro-ev",
		Type:      "assign",
		Source:    "dispatcher",
		CreatedAt: "2026-01-01T00:00:00Z",
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal Event: %v", err)
	}
	if !strings.Contains(string(data), `"bead_id":"oro-ev"`) {
		t.Errorf("Event must serialize BeadID as bead_id, got: %s", data)
	}
}

// TestMemoryJSONBeadIDField pins that protocol.Memory.BeadID is serialized as
// the JSON key "bead_id".
func TestMemoryJSONBeadIDField(t *testing.T) {
	t.Parallel()

	m := protocol.Memory{
		BeadID:  "oro-mem",
		Content: "test learning",
		Type:    "lesson",
		Source:  "worker-1",
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal Memory: %v", err)
	}
	if !strings.Contains(string(data), `"bead_id":"oro-mem"`) {
		t.Errorf("Memory must serialize BeadID as bead_id, got: %s", data)
	}
}

// TestBeadSchemaBeadsTableColumns pins that the beads SQLite table has the
// expected column names so that schema renames are caught early.
func TestBeadSchemaBeadsTableColumns(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}

	required := []string{
		"id", "title", "description", "acceptance_criteria",
		"status", "priority", "type", "parent_id",
		"close_reason", "created_at", "updated_at", "closed_at",
	}
	for _, col := range required {
		var name string
		if err := db.QueryRow(
			"SELECT name FROM pragma_table_info('beads') WHERE name=?", col,
		).Scan(&name); err != nil {
			t.Errorf("beads table missing column %q: %v", col, err)
		}
	}
}

// TestBeadSchemaBead_StarTablesExist pins that the bead_* child tables exist
// after MigrateBeadSchema so their names are caught if renamed.
func TestBeadSchemaBead_StarTablesExist(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}

	for _, table := range []string{
		"beads",
		"bead_deps",
		"bead_tags",
		"bead_labels",
		"bead_metadata",
		"bead_notes",
	} {
		var name string
		if err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name); err != nil {
			t.Errorf("expected bead table %q not found: %v", table, err)
		}
	}
}

// TestBeadMigrateBeadSchemaAssignmentsBeadIDColumn asserts that the
// assignments table created by MigrateBeadSchema has a bead_id column.
func TestBeadMigrateBeadSchemaAssignmentsBeadIDColumn(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}

	var name string
	if err := db.QueryRow(
		"SELECT name FROM pragma_table_info('assignments') WHERE name='bead_id'",
	).Scan(&name); err != nil {
		t.Errorf("assignments table must have a bead_id column: %v", err)
	}
}
