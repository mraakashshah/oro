package dispatcher //nolint:testpackage // needs access to ExtractLearnings (package-level function)

import (
	"testing"

	"oro/pkg/memory"
)

func TestExtractLearnings(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		beadID  string
		wantLen int
		checks  func(t *testing.T, results []memory.InsertParams)
	}{
		{
			name:    "I_learned_that_pattern",
			text:    "I learned that Go interfaces are implicitly satisfied.",
			beadID:  "bead-ex1",
			wantLen: 1,
			checks: func(t *testing.T, results []memory.InsertParams) {
				t.Helper()
				r := results[0]
				if r.Content != "Go interfaces are implicitly satisfied" {
					t.Errorf("content = %q, want match for Go interfaces", r.Content)
				}
				if r.Type != "lesson" {
					t.Errorf("type = %q, want lesson", r.Type)
				}
				if r.Source != "daemon_extracted" {
					t.Errorf("source = %q, want daemon_extracted", r.Source)
				}
				if r.BeadID != "bead-ex1" {
					t.Errorf("bead_id = %q, want bead-ex1", r.BeadID)
				}
				if r.Confidence != 0.6 {
					t.Errorf("confidence = %f, want 0.6", r.Confidence)
				}
			},
		},
		{
			name:    "Note_pattern",
			text:    "Note: Always run tests before committing.",
			beadID:  "bead-ex2",
			wantLen: 1,
			checks: func(t *testing.T, results []memory.InsertParams) {
				t.Helper()
				r := results[0]
				if r.Content != "Always run tests before committing" {
					t.Errorf("content = %q", r.Content)
				}
				if r.Type != "lesson" {
					t.Errorf("type = %q, want lesson", r.Type)
				}
			},
		},
		{
			name:    "Gotcha_pattern",
			text:    "Gotcha: SQLite WAL mode requires shared-cache for concurrent writers.",
			beadID:  "bead-ex3",
			wantLen: 1,
			checks: func(t *testing.T, results []memory.InsertParams) {
				t.Helper()
				r := results[0]
				if r.Type != "gotcha" {
					t.Errorf("type = %q, want gotcha", r.Type)
				}
			},
		},
		{
			name:    "Important_pattern",
			text:    "Important: Never push directly to main.",
			beadID:  "bead-ex4",
			wantLen: 1,
			checks: func(t *testing.T, results []memory.InsertParams) {
				t.Helper()
				if results[0].Type != "lesson" {
					t.Errorf("type = %q, want lesson", results[0].Type)
				}
			},
		},
		{
			name:    "TIL_pattern",
			text:    "TIL: rg supports multiline matching with -U flag.",
			beadID:  "bead-ex5",
			wantLen: 1,
			checks: func(t *testing.T, results []memory.InsertParams) {
				t.Helper()
				r := results[0]
				if r.Content != "rg supports multiline matching with -U flag" {
					t.Errorf("content = %q", r.Content)
				}
				if r.Type != "lesson" {
					t.Errorf("type = %q, want lesson", r.Type)
				}
			},
		},
		{
			name:    "This_doesnt_work_because_pattern",
			text:    "This doesn't work because the socket path exceeds 108 chars on macOS.",
			beadID:  "bead-ex6",
			wantLen: 1,
			checks: func(t *testing.T, results []memory.InsertParams) {
				t.Helper()
				r := results[0]
				if r.Type != "gotcha" {
					t.Errorf("type = %q, want gotcha", r.Type)
				}
			},
		},
		{
			name:    "The_fix_was_pattern",
			text:    "The fix was to use a shorter temp directory for the UDS path.",
			beadID:  "bead-ex7",
			wantLen: 1,
			checks: func(t *testing.T, results []memory.InsertParams) {
				t.Helper()
				r := results[0]
				if r.Type != "lesson" {
					t.Errorf("type = %q, want lesson", r.Type)
				}
			},
		},
		{
			name:    "multiple_patterns_in_text",
			text:    "Some preamble text.\nI learned that Go interfaces are implicitly satisfied.\nGotcha: SQLite WAL mode requires shared-cache.\nTIL: rg supports multiline matching.\nIrrelevant line.",
			beadID:  "bead-multi",
			wantLen: 3,
			checks: func(t *testing.T, results []memory.InsertParams) {
				t.Helper()
				for _, r := range results {
					if r.BeadID != "bead-multi" {
						t.Errorf("bead_id = %q, want bead-multi", r.BeadID)
					}
					if r.Source != "daemon_extracted" {
						t.Errorf("source = %q, want daemon_extracted", r.Source)
					}
					if r.Confidence != 0.6 {
						t.Errorf("confidence = %f, want 0.6", r.Confidence)
					}
				}
			},
		},
		{
			name:    "Decision_pattern",
			text:    "Decision: Use FTS5 instead of trigram indexing.",
			beadID:  "bead-dec",
			wantLen: 1,
			checks: func(t *testing.T, results []memory.InsertParams) {
				t.Helper()
				if results[0].Type != "decision" {
					t.Errorf("type = %q, want decision", results[0].Type)
				}
			},
		},
		{
			name:    "Pattern_pattern",
			text:    "Pattern: Two-phase locking for reservation-based assignment.",
			beadID:  "bead-pat",
			wantLen: 1,
			checks: func(t *testing.T, results []memory.InsertParams) {
				t.Helper()
				if results[0].Type != "pattern" {
					t.Errorf("type = %q, want pattern", results[0].Type)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := ExtractLearnings(tt.text, tt.beadID)
			if len(results) != tt.wantLen {
				t.Fatalf("len(results) = %d, want %d; results: %+v", len(results), tt.wantLen, results)
			}
			if tt.checks != nil {
				tt.checks(t, results)
			}
		})
	}
}

func TestExtractLearnings_EmptyContent(t *testing.T) {
	results := ExtractLearnings("", "bead-empty")
	if len(results) != 0 {
		t.Fatalf("expected empty slice for empty content, got %d entries", len(results))
	}

	results = ExtractLearnings("no patterns here at all\njust regular text", "bead-none")
	if len(results) != 0 {
		t.Fatalf("expected empty slice for text with no patterns, got %d entries", len(results))
	}
}

func TestExtractLearnings_EmptyMatch(t *testing.T) {
	// These lines match the regex prefix but have empty/whitespace-only content after.
	inputs := []string{
		"Note:   ",
		"Gotcha: ",
		"TIL: ",
		"Important: ",
		"I learned that ",
		"I learned that    .",
	}
	for _, input := range inputs {
		results := ExtractLearnings(input, "bead-blank")
		if len(results) != 0 {
			t.Errorf("input %q: expected 0 results, got %d: %+v", input, len(results), results)
		}
	}
}

func TestExtractLearnings_NoDuplicates(t *testing.T) {
	// Same pattern repeated on multiple lines — each line should produce exactly one entry,
	// but duplicate content on separate lines should still each be returned (dedup is at insert time).
	text := "I learned that X is important.\nI learned that X is important."
	results := ExtractLearnings(text, "bead-dup")

	// ExtractLearnings itself does content-level dedup to avoid flooding the memory store.
	if len(results) != 1 {
		t.Fatalf("expected 1 (deduped) result, got %d", len(results))
	}
}
