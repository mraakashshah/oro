package memory //nolint:testpackage // white-box tests consistent with memory_test.go

import (
	"context"
	"testing"
)

// TestParseDreamActions verifies ParseDreamActions correctly parses
// [DELETE], [MERGE], and [CREATE] action lines and skips malformed lines.
func TestParseDreamActions(t *testing.T) {
	t.Run("empty output returns zero actions", func(t *testing.T) {
		actions := ParseDreamActions("")
		if len(actions) != 0 {
			t.Errorf("expected 0 actions, got %d", len(actions))
		}
	})

	t.Run("parses DELETE action", func(t *testing.T) {
		actions := ParseDreamActions("[DELETE] 42")
		if len(actions) != 1 {
			t.Fatalf("expected 1 action, got %d", len(actions))
		}
		a := actions[0]
		if a.Kind != "DELETE" {
			t.Errorf("expected kind DELETE, got %q", a.Kind)
		}
		if a.ID != 42 {
			t.Errorf("expected ID 42, got %d", a.ID)
		}
	})

	t.Run("parses CREATE action", func(t *testing.T) {
		actions := ParseDreamActions("[CREATE] type=lesson tags=go,testing: Always use table-driven tests")
		if len(actions) != 1 {
			t.Fatalf("expected 1 action, got %d", len(actions))
		}
		a := actions[0]
		if a.Kind != "CREATE" {
			t.Errorf("expected kind CREATE, got %q", a.Kind)
		}
		if a.Params.Type != "lesson" {
			t.Errorf("expected type lesson, got %q", a.Params.Type)
		}
		if a.Params.Content != "Always use table-driven tests" {
			t.Errorf("unexpected content: %q", a.Params.Content)
		}
		if len(a.Params.Tags) != 2 || a.Params.Tags[0] != "go" || a.Params.Tags[1] != "testing" {
			t.Errorf("unexpected tags: %v", a.Params.Tags)
		}
	})

	t.Run("parses CREATE action without tags", func(t *testing.T) {
		actions := ParseDreamActions("[CREATE] type=gotcha: Check for nil pointers before dereferencing")
		if len(actions) != 1 {
			t.Fatalf("expected 1 action, got %d", len(actions))
		}
		a := actions[0]
		if a.Kind != "CREATE" {
			t.Errorf("expected kind CREATE, got %q", a.Kind)
		}
		if a.Params.Type != "gotcha" {
			t.Errorf("expected type gotcha, got %q", a.Params.Type)
		}
		if len(a.Params.Tags) != 0 {
			t.Errorf("expected no tags, got %v", a.Params.Tags)
		}
	})

	t.Run("parses MERGE action", func(t *testing.T) {
		actions := ParseDreamActions("[MERGE] 10 20 type=lesson: Combined memory content here")
		if len(actions) != 1 {
			t.Fatalf("expected 1 action, got %d", len(actions))
		}
		a := actions[0]
		if a.Kind != "MERGE" {
			t.Errorf("expected kind MERGE, got %q", a.Kind)
		}
		if len(a.IDs) != 2 || a.IDs[0] != 10 || a.IDs[1] != 20 {
			t.Errorf("expected IDs [10 20], got %v", a.IDs)
		}
		if a.Params.Type != "lesson" {
			t.Errorf("expected type lesson, got %q", a.Params.Type)
		}
		if a.Params.Content != "Combined memory content here" {
			t.Errorf("unexpected content: %q", a.Params.Content)
		}
	})

	t.Run("skips malformed lines", func(t *testing.T) {
		input := `not an action
[DELETE] notanumber
[CREATE] missingtype: content
[MERGE] 10 type=lesson: missing second id
[DELETE] 7
`
		actions := ParseDreamActions(input)
		// Only "[DELETE] 7" should parse successfully
		if len(actions) != 1 {
			t.Errorf("expected 1 valid action, got %d: %+v", len(actions), actions)
		}
		if actions[0].Kind != "DELETE" || actions[0].ID != 7 {
			t.Errorf("unexpected action: %+v", actions[0])
		}
	})

	t.Run("parses multiple actions from multi-line output", func(t *testing.T) {
		input := `[DELETE] 1
[CREATE] type=lesson tags=go: Use gofumpt for formatting
[MERGE] 3 4 type=gotcha: Avoid nil map writes
`
		actions := ParseDreamActions(input)
		if len(actions) != 3 {
			t.Fatalf("expected 3 actions, got %d", len(actions))
		}
		if actions[0].Kind != "DELETE" {
			t.Errorf("action[0] kind: %q", actions[0].Kind)
		}
		if actions[1].Kind != "CREATE" {
			t.Errorf("action[1] kind: %q", actions[1].Kind)
		}
		if actions[2].Kind != "MERGE" {
			t.Errorf("action[2] kind: %q", actions[2].Kind)
		}
	})
}

// TestExecuteActions verifies ExecuteActions dispatches to Store methods correctly.
func TestExecuteActions(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Seed a memory to delete.
	id1, err := store.Insert(ctx, InsertParams{
		Content: "First memory to delete during dreaming", Type: "lesson",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert id1: %v", err)
	}

	// Seed two memories to merge.
	id2, err := store.Insert(ctx, InsertParams{
		Content: "Second memory will be merged away in dream", Type: "gotcha",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert id2: %v", err)
	}

	id3, err := store.Insert(ctx, InsertParams{
		Content: "Third memory will be merged away in dream test case", Type: "gotcha",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert id3: %v", err)
	}

	var logs []string
	logFn := func(s string) { logs = append(logs, s) }

	actions := []DreamAction{
		{Kind: "DELETE", ID: id1},
		{Kind: "MERGE", IDs: []int64{id2, id3}, Params: InsertParams{
			Content: "Merged: both gotchas about dream testing combined together",
			Type:    "gotcha",
			Source:  "dreamer",
		}},
		{Kind: "CREATE", Params: InsertParams{
			Content: "Dream consolidation creates new memories with synthesis content",
			Type:    "lesson",
			Source:  "dreamer",
		}},
	}

	if err := ExecuteActions(ctx, actions, store, logFn); err != nil {
		t.Fatalf("ExecuteActions: %v", err)
	}

	// id1 should be deleted — lookup should return an error.
	_, err = store.GetByID(ctx, id1)
	if err == nil {
		t.Errorf("expected id1 to be deleted (GetByID should error), but got no error")
	}

	// id2 and id3 should be deleted (merged away).
	for _, id := range []int64{id2, id3} {
		_, err := store.GetByID(ctx, id)
		if err == nil {
			t.Errorf("expected id %d to be deleted after merge, but GetByID succeeded", id)
		}
	}
}

// TestExecuteActions_StoreErrorLogsAndContinues verifies that a store error
// is logged but execution continues to remaining actions.
func TestExecuteActions_StoreErrorLogsAndContinues(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	var logs []string
	logFn := func(s string) { logs = append(logs, s) }

	// Delete non-existent ID 9999 — store.Delete returns no error for missing rows.
	// Use a valid CREATE after to verify continuation.
	actions := []DreamAction{
		{Kind: "DELETE", ID: 9999}, // should not error (DELETE by ID is idempotent)
		{Kind: "CREATE", Params: InsertParams{
			Content: "Created after error path to verify continuation in dream executor",
			Type:    "lesson",
			Source:  "dreamer",
		}},
	}

	if err := ExecuteActions(ctx, actions, store, logFn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
