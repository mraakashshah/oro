//go:build cgo && darwin

package main

import (
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE memories (
		id      INTEGER PRIMARY KEY,
		content TEXT    NOT NULL,
		type    TEXT    NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

// insertDiverseMemories inserts 70 rows with 5 distinct types and content
// lengths between 50 and 400 characters, all with token counts well under 512.
func insertDiverseMemories(t *testing.T, db *sql.DB) {
	t.Helper()
	types := []string{"gotcha", "lesson", "pattern", "decision", "self_report"}
	base := "This is a test memory entry that has enough content to pass the length filter for type %s number %d in the eval corpus anchor selection test"
	for i := 0; i < 70; i++ {
		memType := types[i%len(types)]
		content := fmt.Sprintf(base, memType, i+1)
		// Verify content is in [50,400] char range.
		if len(content) < 50 || len(content) > 400 {
			t.Fatalf("fixture content length %d out of [50,400] for i=%d", len(content), i)
		}
		_, err := db.Exec(`INSERT INTO memories (id, content, type) VALUES (?, ?, ?)`,
			i+1, content, memType)
		if err != nil {
			t.Fatalf("insert memory %d: %v", i+1, err)
		}
	}
}

func TestSelectAnchorsDeterministic(t *testing.T) {
	db := openTestDB(t)
	insertDiverseMemories(t, db)

	got1, err := SelectAnchors(db, 42, 50)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(got1) != 50 {
		t.Fatalf("want 50 anchors, got %d", len(got1))
	}

	got2, err := SelectAnchors(db, 42, 50)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	// Assert determinism: same IDs in same order.
	for i := range got1 {
		if got1[i].ID != got2[i].ID {
			t.Errorf("position %d: ID mismatch: %d vs %d", i, got1[i].ID, got2[i].ID)
		}
	}

	// Assert content length filter [50, 400].
	for _, a := range got1 {
		if l := len(a.Content); l < 50 || l > 400 {
			t.Errorf("content length %d out of [50,400]: id=%d", l, a.ID)
		}
	}

	// Assert token count <= 512 (Go-side check).
	for _, a := range got1 {
		if tc := countTokens(a.Content); tc > 512 {
			t.Errorf("token count %d > 512 for id=%d", tc, a.ID)
		}
	}

	// Assert at least 3 distinct types in the 50-anchor result.
	types := make(map[string]struct{})
	for _, a := range got1 {
		types[a.Type] = struct{}{}
	}
	if len(types) < 3 {
		t.Errorf("want >= 3 distinct types, got %d: %v", len(types), types)
	}
}

func TestSelectAnchorsEmptyDB(t *testing.T) {
	db := openTestDB(t)
	_, err := SelectAnchors(db, 42, 50)
	if err == nil {
		t.Fatal("want error on empty DB, got nil")
	}
}

func TestSelectAnchorsInsufficientCandidates(t *testing.T) {
	db := openTestDB(t)
	// Insert only 10 rows — fewer than the requested 50.
	for i := 0; i < 10; i++ {
		content := fmt.Sprintf("This is memory %d with sufficient length to pass the fifty char filter", i+1)
		_, err := db.Exec(`INSERT INTO memories (id, content, type) VALUES (?, ?, ?)`,
			i+1, content, "lesson")
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	_, err := SelectAnchors(db, 42, 50)
	if err == nil {
		t.Fatal("want error when candidates < count, got nil")
	}
}
