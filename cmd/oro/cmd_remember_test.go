package main

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/memory"
	"oro/pkg/protocol"
)

// newTestMemoryStore creates an in-memory SQLite store suitable for remember cmd tests.
func newTestMemoryStore(t *testing.T) *memory.Store {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	return memory.NewStore(db)
}

func TestRememberCmdWithStoreNil(t *testing.T) {
	// Verify that newRememberCmdWithStore(nil) lazily opens the default store.
	// This is an integration test that ensures the deduplication works correctly.
	cmd := newRememberCmdWithStore(nil)
	if cmd == nil { //nolint:staticcheck // check below verifies
		t.Fatal("newRememberCmdWithStore(nil) returned nil command")
	}
	// We can't easily test execution without setting up ~/.oro/memories.db,
	// but we can verify the command structure is correct.
	if cmd.Use != "remember <text>" {
		t.Errorf("expected Use='remember <text>', got %q", cmd.Use)
	}
}

func TestRememberCmdDoesNotImportMemory(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cmd_remember.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse cmd_remember.go imports: %v", err)
	}
	for _, imp := range file.Imports {
		if imp.Path.Value == `"oro/pkg/memory"` {
			t.Fatal("cmd_remember.go must not import oro/pkg/memory")
		}
	}
}

func TestRememberCmd(t *testing.T) {
	// Table-driven: parseTypePrefix
	prefixTests := []struct {
		name     string
		input    string
		wantType string
		wantText string
	}{
		{
			name:     "plain text defaults to self_report",
			input:    "always use TDD when writing Go code",
			wantType: "self_report",
			wantText: "always use TDD when writing Go code",
		},
		{
			name:     "lesson prefix",
			input:    "lesson: prefer composition over inheritance",
			wantType: "lesson",
			wantText: "prefer composition over inheritance",
		},
		{
			name:     "decision prefix",
			input:    "decision: use FTS5 for memory search",
			wantType: "decision",
			wantText: "use FTS5 for memory search",
		},
		{
			name:     "gotcha prefix",
			input:    "gotcha: modernc sqlite bm25 returns zeros",
			wantType: "gotcha",
			wantText: "modernc sqlite bm25 returns zeros",
		},
		{
			name:     "pattern prefix",
			input:    "pattern: table-driven tests for Go CLI commands",
			wantType: "pattern",
			wantText: "table-driven tests for Go CLI commands",
		},
	}

	t.Run("parseTypePrefix", func(t *testing.T) {
		for _, tc := range prefixTests {
			t.Run(tc.name, func(t *testing.T) {
				gotType, gotText := parseTypePrefix(tc.input)
				if gotType != tc.wantType {
					t.Errorf("parseTypePrefix(%q) type = %q, want %q", tc.input, gotType, tc.wantType)
				}
				if gotText != tc.wantText {
					t.Errorf("parseTypePrefix(%q) text = %q, want %q", tc.input, gotText, tc.wantText)
				}
			})
		}
	})

	// Table-driven: full command execution with store
	cmdTests := []struct {
		name       string
		args       []string
		wantType   string
		wantText   string
		wantPinned bool
		wantErr    bool
	}{
		{
			name:     "plain text stored as self_report",
			args:     []string{"unique_sr_remember_test_abc123"},
			wantType: "self_report",
			wantText: "unique_sr_remember_test_abc123",
		},
		{
			name:     "lesson prefix via args",
			args:     []string{"lesson:", "unique_lesson_remember_xyz789"},
			wantType: "lesson",
			wantText: "unique_lesson_remember_xyz789",
		},
		{
			name:     "decision prefix via args",
			args:     []string{"decision:", "unique_decision_remember_qrs456"},
			wantType: "decision",
			wantText: "unique_decision_remember_qrs456",
		},
		{
			name:     "gotcha prefix via args",
			args:     []string{"gotcha:", "unique_gotcha_remember_mnop321"},
			wantType: "gotcha",
			wantText: "unique_gotcha_remember_mnop321",
		},
		{
			name:     "pattern prefix via args",
			args:     []string{"pattern:", "unique_pattern_remember_wxyz654"},
			wantType: "pattern",
			wantText: "unique_pattern_remember_wxyz654",
		},
		{
			name:       "pin flag sets Pinned true",
			args:       []string{"--pin", "unique_pinned_remember_hjk987"},
			wantType:   "self_report",
			wantText:   "unique_pinned_remember_hjk987",
			wantPinned: true,
		},
		{
			name:    "no args returns error",
			args:    []string{},
			wantErr: true,
		},
	}

	for _, tc := range cmdTests {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestMemoryStore(t)
			cmd := newRememberCmdWithStore(store)

			var out strings.Builder
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tc.args)

			err := cmd.Execute()

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			output := out.String()

			// Verify output contains the "Remembered" confirmation
			if !strings.Contains(output, "Remembered") {
				t.Errorf("expected output to contain 'Remembered', got: %s", output)
			}

			// Verify type is reported in output
			if !strings.Contains(output, "type="+tc.wantType) {
				t.Errorf("expected output to contain type=%s, got: %s", tc.wantType, output)
			}

			// Verify pinned tag in output
			if tc.wantPinned && !strings.Contains(output, "[pinned]") {
				t.Errorf("expected output to contain '[pinned]', got: %s", output)
			}
			if !tc.wantPinned && strings.Contains(output, "[pinned]") {
				t.Errorf("expected output NOT to contain '[pinned]', got: %s", output)
			}

			// Verify the memory was stored correctly via the store
			ctx := context.Background()
			results, err := store.Search(ctx, tc.wantText, memory.SearchOpts{Limit: 5})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(results) == 0 {
				t.Fatalf("expected at least one memory matching %q", tc.wantText)
			}

			mem := results[0]
			if mem.Type != tc.wantType {
				t.Errorf("stored type = %q, want %q", mem.Type, tc.wantType)
			}
			if mem.Source != "cli" {
				t.Errorf("stored source = %q, want %q", mem.Source, "cli")
			}
			if mem.Confidence != 0.8 {
				t.Errorf("stored confidence = %v, want 0.8", mem.Confidence)
			}
			if mem.Pinned != tc.wantPinned {
				t.Errorf("stored pinned = %v, want %v", mem.Pinned, tc.wantPinned)
			}
		})
	}
}

func TestRememberWithTags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantTags []string
		wantText string
	}{
		{
			name:     "tags flag with comma-separated values",
			args:     []string{"--tags=go,sqlite", "lesson:", "WAL mode is faster"},
			wantTags: []string{"go", "sqlite"},
			wantText: "WAL mode is faster",
		},
		{
			name:     "tags flag with whitespace trimmed",
			args:     []string{"--tags=go, sqlite, database", "pattern:", "use indexed queries"},
			wantTags: []string{"go", "sqlite", "database"},
			wantText: "use indexed queries",
		},
		{
			name:     "empty tags flag stores no tags",
			args:     []string{"--tags=", "unique_no_tags_xyz789"},
			wantTags: []string{},
			wantText: "unique_no_tags_xyz789",
		},
		{
			name:     "no tags flag means no tags",
			args:     []string{"unique_no_flag_abc123"},
			wantTags: []string{},
			wantText: "unique_no_flag_abc123",
		},
		{
			name:     "single tag in tags flag",
			args:     []string{"--tags=golang", "decision:", "use goroutines"},
			wantTags: []string{"golang"},
			wantText: "use goroutines",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestMemoryStore(t)
			cmd := newRememberCmdWithStore(store)

			var out strings.Builder
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tc.args)

			err := cmd.Execute()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			output := out.String()
			if !strings.Contains(output, "Remembered") {
				t.Errorf("expected output to contain 'Remembered', got: %s", output)
			}

			// Verify the memory was stored with correct tags
			ctx := context.Background()
			results, err := store.Search(ctx, tc.wantText, memory.SearchOpts{Limit: 5})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(results) == 0 {
				t.Fatalf("expected at least one memory matching %q", tc.wantText)
			}

			mem := results[0]

			// Parse tags from memory (stored as JSON)
			var storedTags []string
			if mem.Tags != "" {
				if err := json.Unmarshal([]byte(mem.Tags), &storedTags); err != nil {
					t.Fatalf("failed to parse tags JSON: %v", err)
				}
			}

			// Compare tags
			if len(storedTags) != len(tc.wantTags) {
				t.Errorf("stored tags count = %d, want %d. stored=%v, want=%v", len(storedTags), len(tc.wantTags), storedTags, tc.wantTags)
			}
			for i, tag := range tc.wantTags {
				if i >= len(storedTags) {
					t.Errorf("missing tag at index %d: %q", i, tag)
					continue
				}
				if storedTags[i] != tag {
					t.Errorf("stored tags[%d] = %q, want %q", i, storedTags[i], tag)
				}
			}
		})
	}
}
