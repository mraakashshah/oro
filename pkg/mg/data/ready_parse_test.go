package data

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestParseIssuesJSONAcceptsOroBeadReadyOutput(t *testing.T) {
	tests := []struct {
		name  string
		count int
	}{
		{name: "empty result", count: 0},
		{name: "single bead", count: 1},
		{name: "large ready queue", count: 101},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues, err := ParseIssuesJSON(oroReadyJSON(t, tt.count), "")
			if err != nil {
				t.Fatalf("ParseIssuesJSON() error = %v", err)
			}
			if len(issues) != tt.count {
				t.Fatalf("len(issues) = %d, want %d", len(issues), tt.count)
			}
			if tt.count == 0 {
				return
			}

			byID := BuildIssueMap(issues)
			first := byID["oro-ready-001"]
			if first == nil {
				t.Fatalf("oro-ready-001 not found in parsed issues")
			}
			if first.IssueType != TypeTask {
				t.Fatalf("IssueType = %q, want %q", first.IssueType, TypeTask)
			}
			if first.AcceptanceCriteria != "Cmd: go test ./... | Assert: PASS" {
				t.Fatalf("AcceptanceCriteria = %q", first.AcceptanceCriteria)
			}
		})
	}
}

func oroReadyJSON(t *testing.T, count int) []byte {
	t.Helper()

	beads := make([]map[string]any, 0, count)
	for i := 1; i <= count; i++ {
		beads = append(beads, map[string]any{
			"id":                  fmt.Sprintf("oro-ready-%03d", i),
			"title":               fmt.Sprintf("Ready bead %03d", i),
			"status":              "open",
			"priority":            i % 5,
			"parent_id":           "oro-parent",
			"type":                "task",
			"model":               nil,
			"tier":                nil,
			"worker_id":           nil,
			"context_percent":     nil,
			"last_heartbeat":      nil,
			"git_diff":            nil,
			"memory":              nil,
			"estimated_minutes":   nil,
			"acceptance_criteria": "Cmd: go test ./... | Assert: PASS",
			"dependencies":        nil,
			"updated_at":          nil,
			"closed_at":           nil,
			"created_at":          nil,
			"description":         nil,
			"close_reason":        nil,
			"owner":               nil,
			"notes":               nil,
			"tags":                nil,
			"metadata":            nil,
			"labels":              nil,
		})
	}

	out, err := json.Marshal(beads)
	if err != nil {
		t.Fatalf("marshal ready JSON fixture: %v", err)
	}
	return out
}
