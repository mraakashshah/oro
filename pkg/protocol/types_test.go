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
			name:     "epic type routes to Opus",
			bead:     protocol.Bead{Type: "epic"},
			expected: protocol.ModelOpus,
		},
		{
			name:     "feature type routes to Opus",
			bead:     protocol.Bead{Type: "feature"},
			expected: protocol.ModelOpus,
		},
		{
			name:     "task type routes to Sonnet",
			bead:     protocol.Bead{Type: "task"},
			expected: protocol.ModelSonnet,
		},
		{
			name:     "bug type routes to Sonnet",
			bead:     protocol.Bead{Type: "bug"},
			expected: protocol.ModelSonnet,
		},
		{
			name:     "unknown type uses default",
			bead:     protocol.Bead{Type: "unknown"},
			expected: protocol.DefaultModel,
		},
		{
			name:     "empty type uses default",
			bead:     protocol.Bead{},
			expected: protocol.DefaultModel,
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
