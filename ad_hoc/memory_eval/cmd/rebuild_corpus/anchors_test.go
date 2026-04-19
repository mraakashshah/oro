//go:build cgo && darwin

package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestFilterByTokenCountDropsOverLimit(t *testing.T) {
	dense := strings.Repeat("a ", 600) // 600 tokens in 1200 chars
	if tc := countTokens(dense); tc != 600 {
		t.Fatalf("countTokens(dense) = %d, want 600", tc)
	}
	anchors := []CorpusAnchor{
		{ID: 1, Type: "lesson", Content: "short lesson content"},
		{ID: 2, Type: "gotcha", Content: dense},
		{ID: 3, Type: "pattern", Content: "another short pattern content"},
	}
	got := filterByTokenCount(anchors, 512)
	if len(got) != 2 {
		t.Fatalf("want 2 anchors after filter, got %d", len(got))
	}
	for _, a := range got {
		if a.ID == 2 {
			t.Errorf("anchor id=2 with %d tokens not dropped by filter", countTokens(a.Content))
		}
	}
}

func TestCountTokensBoundary(t *testing.T) {
	// Exactly at the 512 boundary — should pass.
	at := strings.Repeat("x ", 512)
	if tc := countTokens(at); tc != 512 {
		t.Fatalf("countTokens(512 tokens) = %d, want 512", tc)
	}
	kept := filterByTokenCount([]CorpusAnchor{{ID: 1, Content: at}}, 512)
	if len(kept) != 1 {
		t.Errorf("boundary anchor (512 tokens) was dropped; want kept")
	}
	// One over — should be dropped.
	over := strings.Repeat("x ", 513)
	dropped := filterByTokenCount([]CorpusAnchor{{ID: 1, Content: over}}, 512)
	if len(dropped) != 0 {
		t.Errorf("over-boundary anchor (513 tokens) was kept; want dropped")
	}
}

func TestWriteCorpusAnchorsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corpus_anchors.jsonl")
	anchors := []CorpusAnchor{
		{ID: 1, Type: "lesson", Content: "first anchor content"},
		{ID: 2, Type: "gotcha", Content: "second anchor content"},
		{ID: 3, Type: "pattern", Content: "third anchor content"},
	}

	if err := WriteCorpusAnchors(path, anchors); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Atomicity: tmp file must not exist after successful rename.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file %q exists after rename; want removed (err=%v)", path+".tmp", err)
	}

	f, err := os.Open(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("open written file: %v", err)
	}
	defer func() { _ = f.Close() }()

	var got []CorpusAnchor
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var a CorpusAnchor
		if err := json.Unmarshal(scanner.Bytes(), &a); err != nil {
			t.Fatalf("unmarshal line: %v", err)
		}
		got = append(got, a)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(got) != len(anchors) {
		t.Fatalf("roundtrip: want %d anchors, got %d", len(anchors), len(got))
	}
	for i := range anchors {
		if got[i] != anchors[i] {
			t.Errorf("roundtrip line %d: got %+v, want %+v", i, got[i], anchors[i])
		}
	}
}

func TestWriteCorpusAnchorsRenameFailureCleansTmp(t *testing.T) {
	dir := t.TempDir()
	// Make the destination path a directory so os.Rename fails.
	path := filepath.Join(dir, "out.jsonl")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err := WriteCorpusAnchors(path, []CorpusAnchor{{ID: 1, Type: "t", Content: "c"}})
	if err == nil {
		t.Fatal("want error when destination is a directory, got nil")
	}
	if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
		t.Errorf("tmp file %q not cleaned up after rename failure (err=%v)", path+".tmp", statErr)
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
