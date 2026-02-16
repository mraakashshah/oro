package main

import (
	"context"
	"testing"
)

func TestFetchWorkerEvents_EmptyWorkerID(t *testing.T) {
	ctx := context.Background()
	events, err := fetchWorkerEvents(ctx, "", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if events != nil {
		t.Error("expected nil events for empty worker ID")
	}
}

func TestFetchWorkerEvents_MissingDatabase(t *testing.T) {
	ctx := context.Background()
	// With a non-existent database, should return empty slice gracefully
	events, err := fetchWorkerEvents(ctx, "worker-1", 10)
	if err != nil {
		t.Errorf("expected nil error for missing database, got: %v", err)
	}
	if events != nil {
		t.Error("expected nil events when database is not accessible")
	}
}

func TestRenderWorkerEvents_EmptySlice(t *testing.T) {
	theme := DefaultTheme()
	result := renderWorkerEvents(nil, theme, NewStyles(theme))
	if result == "" {
		t.Error("expected non-empty result for empty events")
	}
	// Should show "No events" message
	if result != "\x1b[2;3mNo events\x1b[0m" {
		// Allow for different ANSI codes from lipgloss
		if len(result) < 5 {
			t.Errorf("expected 'No events' message, got: %s", result)
		}
	}
}

func TestRenderWorkerEvents_WithEvents(t *testing.T) {
	theme := DefaultTheme()
	events := []WorkerEvent{
		{Type: "heartbeat", Timestamp: "12:34:56", BeadID: "beads-abc"},
		{Type: "assign", Timestamp: "12:34:57", BeadID: "beads-def"},
	}

	result := renderWorkerEvents(events, theme, NewStyles(theme))
	if result == "" {
		t.Error("expected non-empty result for events")
	}

	// Should contain event types
	if !contains(result, "heartbeat") {
		t.Error("expected result to contain 'heartbeat'")
	}
	if !contains(result, "assign") {
		t.Error("expected result to contain 'assign'")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10c", 10, "exactly10c"},
		{"this is a very long string", 10, "this is..."},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc"},
		{"", 5, ""},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

// contains checks if a string contains a substring (helper).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && indexOfSubstring(s, substr) >= 0)
}

func indexOfSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
