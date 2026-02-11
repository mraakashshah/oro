package protocol_test

import (
	"testing"

	"oro/pkg/protocol"
)

func TestBeadResolveModel(t *testing.T) {
	tests := []struct {
		name     string
		bead     protocol.Bead
		expected string
	}{
		{
			name:     "explicit model override",
			bead:     protocol.Bead{Model: "custom-model"},
			expected: "custom-model",
		},
		{
			name:     "EstimatedMinutes=3 routes to Haiku",
			bead:     protocol.Bead{EstimatedMinutes: 3},
			expected: protocol.ModelHaiku,
		},
		{
			name:     "EstimatedMinutes=5 routes to Haiku",
			bead:     protocol.Bead{EstimatedMinutes: 5},
			expected: protocol.ModelHaiku,
		},
		{
			name:     "EstimatedMinutes=6 routes to Sonnet",
			bead:     protocol.Bead{EstimatedMinutes: 6},
			expected: protocol.ModelSonnet,
		},
		{
			name:     "EstimatedMinutes=0 (unset) routes to Sonnet",
			bead:     protocol.Bead{EstimatedMinutes: 0},
			expected: protocol.ModelSonnet,
		},
		{
			name:     "no fields set routes to Sonnet",
			bead:     protocol.Bead{},
			expected: protocol.ModelSonnet,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.bead.ResolveModel()
			if got != tt.expected {
				t.Errorf("ResolveModel() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFormatEscalation(t *testing.T) {
	tests := []struct {
		name     string
		typ      protocol.EscalationType
		beadID   string
		summary  string
		details  string
		expected string
	}{
		{
			name:     "with details",
			typ:      protocol.EscStuck,
			beadID:   "oro-123",
			summary:  "worker stuck",
			details:  "QG failed 3 times",
			expected: "[ORO-DISPATCH] STUCK: oro-123 — worker stuck. QG failed 3 times.",
		},
		{
			name:     "without details",
			typ:      protocol.EscMergeConflict,
			beadID:   "oro-456",
			summary:  "merge conflict in main.go",
			details:  "",
			expected: "[ORO-DISPATCH] MERGE_CONFLICT: oro-456 — merge conflict in main.go.",
		},
		{
			name:     "worker crash",
			typ:      protocol.EscWorkerCrash,
			beadID:   "oro-789",
			summary:  "heartbeat timeout",
			details:  "",
			expected: "[ORO-DISPATCH] WORKER_CRASH: oro-789 — heartbeat timeout.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protocol.FormatEscalation(tt.typ, tt.beadID, tt.summary, tt.details)
			if got != tt.expected {
				t.Errorf("protocol.FormatEscalation() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestCountReadFiles(t *testing.T) {
	tests := []struct {
		name       string
		acceptance string
		expected   int
	}{
		{
			name:       "empty string",
			acceptance: "",
			expected:   0,
		},
		{
			name:       "single Read: line",
			acceptance: "Read: foo.go",
			expected:   1,
		},
		{
			name:       "multi-line with 3 Read: lines",
			acceptance: "Read: foo.go\nRead: bar.go\nCheck tests pass\nRead: baz.go",
			expected:   3,
		},
		{
			name:       "Read: without prefix space still counts",
			acceptance: "Read:foo.go",
			expected:   1,
		},
		{
			name:       "lines that don't start with Read:",
			acceptance: "Check tests pass\nVerify build\nDeploy to staging",
			expected:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protocol.CountReadFiles(tt.acceptance)
			if got != tt.expected {
				t.Errorf("CountReadFiles() = %d, want %d", got, tt.expected)
			}
		})
	}
}
