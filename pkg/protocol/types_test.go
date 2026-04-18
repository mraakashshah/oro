package protocol_test

import (
	"encoding/json"
	"strings"
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

func TestCountDistinctModules(t *testing.T) {
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
			name:       "single module path",
			acceptance: "Read: pkg/ops/foo.go",
			expected:   1,
		},
		{
			name:       "two paths same module",
			acceptance: "Read: pkg/ops/foo.go\nRead: pkg/ops/bar.go",
			expected:   1,
		},
		{
			name:       "three distinct modules",
			acceptance: "Read: pkg/ops/foo.go\nRead: pkg/dispatcher/bar.go\nRead: pkg/protocol/baz.go",
			expected:   3,
		},
		{
			name:       "comma-separated paths in single Read: line same module",
			acceptance: "Read: pkg/dispatcher/dispatcher.go:510, pkg/dispatcher/quality_gate_gen.go:ProjectPaths",
			expected:   1,
		},
		{
			name:       "comma-separated paths including non-pkg module",
			acceptance: "Read: pkg/dispatcher/dispatcher.go:510, pkg/ops/review_prompt.go:128, langprofile/detect.go:38",
			expected:   3,
		},
		{
			name:       "malformed path no slash counts as own module",
			acceptance: "Read: foo.go",
			expected:   1,
		},
		{
			name:       "paths in Cmd line only — no Read lines",
			acceptance: "Cmd: go test ./pkg/ops/... -run TestFoo\nCmd: go test ./pkg/dispatcher/... -run TestBar",
			expected:   0,
		},
		{
			name:       "mixed Read and Cmd lines — only Read contributes",
			acceptance: "Read: pkg/ops/foo.go\nCmd: go test ./pkg/dispatcher/...\nAssert: pass",
			expected:   1,
		},
		{
			name:       "no recognizable paths",
			acceptance: "Assert: tests pass\nCheck: lint clean",
			expected:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protocol.CountDistinctModules(tt.acceptance)
			if got != tt.expected {
				t.Errorf("CountDistinctModules() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestCountDistinctModulesLineNumbers(t *testing.T) {
	tests := []struct {
		name       string
		acceptance string
		expected   int
	}{
		{
			name:       "bare line numbers after comma are skipped",
			acceptance: "Read: pkg/a/x.go:13,26,51, pkg/b/y.go",
			expected:   2,
		},
		{
			name:       "file with line suffix and symbol ref in same module",
			acceptance: "Read: pkg/dispatcher/dispatcher.go:510, pkg/dispatcher/quality_gate_gen.go:ProjectPaths",
			expected:   1,
		},
		{
			name:       "file alone no suffix",
			acceptance: "Read: pkg/protocol/types.go, pkg/protocol/types_test.go",
			expected:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protocol.CountDistinctModules(tt.acceptance)
			if got != tt.expected {
				t.Errorf("CountDistinctModules(%q) = %d, want %d", tt.acceptance, got, tt.expected)
			}
		})
	}
}

func TestCountDistinctModulesSymbols(t *testing.T) {
	tests := []struct {
		name       string
		acceptance string
		expected   int
	}{
		{
			name:       "bare symbol names after colon are skipped",
			acceptance: "Read: pkg/memory/embed.go:Embedder,Embed,ExportVocab",
			expected:   1,
		},
		{
			name:       "multiple bare symbols same module",
			acceptance: "Read: pkg/dispatcher/foo.go:FuncA,FuncB,FuncC",
			expected:   1,
		},
		{
			name:       "mix of files and bare symbols",
			acceptance: "Read: pkg/ops/bar.go,FuncName,pkg/ops/baz.go:Symbol",
			expected:   1,
		},
		{
			name:       "root file still counts",
			acceptance: "Read: main.go",
			expected:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protocol.CountDistinctModules(tt.acceptance)
			if got != tt.expected {
				t.Errorf("CountDistinctModules(%q) = %d, want %d", tt.acceptance, got, tt.expected)
			}
		})
	}
}

func TestCountDistinctModulesParenthetical(t *testing.T) {
	tests := []struct {
		name       string
		acceptance string
		expected   int
	}{
		{
			name:       "exact case from oro-ot51 swarm stall",
			acceptance: "Read: pkg/memory/memory.go:25,36,43,61-103 (Store struct field + SetEmbedder), pkg/memory/embed.go:Embedder (from bead 1.1)",
			expected:   1,
		},
		{
			name:       "parenthetical with dotted annotation does not contribute",
			acceptance: "Read: pkg/a/foo.go, (type.Go annotation here), pkg/a/bar.go",
			expected:   1,
		},
		{
			name:       "parenthetical with version dot does not contribute",
			acceptance: "Read: pkg/a/foo.go, (from bead 1.1), pkg/a/bar.go",
			expected:   1,
		},
		{
			name:       "semicolons treated as additional separators",
			acceptance: "Read: pkg/a/foo.go; pkg/b/bar.go",
			expected:   2,
		},
		{
			name:       "semicolons and commas both work as separators",
			acceptance: "Read: pkg/a/foo.go; pkg/b/bar.go, pkg/c/baz.go",
			expected:   3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protocol.CountDistinctModules(tt.acceptance)
			if got != tt.expected {
				t.Errorf("CountDistinctModules(%q) = %d, want %d", tt.acceptance, got, tt.expected)
			}
		})
	}
}

func TestBeadUnmarshalNewFields(t *testing.T) {
	tests := []struct {
		name            string
		jsonStr         string
		wantID          string
		wantTitle       string
		wantStatus      string
		wantPriority    int
		wantClosedAt    string
		wantCreatedAt   string
		wantDescription string
		wantCloseReason string
		wantOwner       string
	}{
		{
			name: "Bead with all new fields",
			jsonStr: `{
				"id": "oro-123",
				"title": "Test Bead",
				"status": "closed",
				"priority": 1,
				"closed_at": "2025-02-27T10:00:00Z",
				"created_at": "2025-02-20T08:00:00Z",
				"description": "Test description",
				"close_reason": "Completed",
				"owner": "agent-1"
			}`,
			wantID:          "oro-123",
			wantTitle:       "Test Bead",
			wantStatus:      "closed",
			wantPriority:    1,
			wantClosedAt:    "2025-02-27T10:00:00Z",
			wantCreatedAt:   "2025-02-20T08:00:00Z",
			wantDescription: "Test description",
			wantCloseReason: "Completed",
			wantOwner:       "agent-1",
		},
		{
			name: "Bead with partial new fields",
			jsonStr: `{
				"id": "oro-456",
				"title": "Another Bead",
				"priority": 2,
				"created_at": "2025-02-25T12:00:00Z",
				"owner": "agent-2"
			}`,
			wantID:        "oro-456",
			wantTitle:     "Another Bead",
			wantPriority:  2,
			wantCreatedAt: "2025-02-25T12:00:00Z",
			wantOwner:     "agent-2",
		},
		{
			name: "Bead without new fields (backwards compatibility)",
			jsonStr: `{
				"id": "oro-789",
				"title": "Old Bead",
				"priority": 0
			}`,
			wantID:       "oro-789",
			wantTitle:    "Old Bead",
			wantPriority: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got protocol.Bead
			if err := json.Unmarshal([]byte(tt.jsonStr), &got); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if got.ID != tt.wantID {
				t.Errorf("ID = %s, want %s", got.ID, tt.wantID)
			}
			if got.Title != tt.wantTitle {
				t.Errorf("Title = %s, want %s", got.Title, tt.wantTitle)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %s, want %s", got.Status, tt.wantStatus)
			}
			if got.Priority != tt.wantPriority {
				t.Errorf("Priority = %d, want %d", got.Priority, tt.wantPriority)
			}
			if got.ClosedAt != tt.wantClosedAt {
				t.Errorf("ClosedAt = %s, want %s", got.ClosedAt, tt.wantClosedAt)
			}
			if got.CreatedAt != tt.wantCreatedAt {
				t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, tt.wantCreatedAt)
			}
			if got.Description != tt.wantDescription {
				t.Errorf("Description = %s, want %s", got.Description, tt.wantDescription)
			}
			if got.CloseReason != tt.wantCloseReason {
				t.Errorf("CloseReason = %s, want %s", got.CloseReason, tt.wantCloseReason)
			}
			if got.Owner != tt.wantOwner {
				t.Errorf("Owner = %s, want %s", got.Owner, tt.wantOwner)
			}
		})
	}
}

func TestBeadDetailUnmarshalOwner(t *testing.T) {
	jsonStr := `{
		"id": "oro-detail-123",
		"title": "Detail Test",
		"acceptance_criteria": "Test AC",
		"owner": "agent-3"
	}`

	var got protocol.BeadDetail
	if err := json.Unmarshal([]byte(jsonStr), &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if got.ID != "oro-detail-123" {
		t.Errorf("ID = %s, want %s", got.ID, "oro-detail-123")
	}
	if got.Title != "Detail Test" {
		t.Errorf("Title = %s, want %s", got.Title, "Detail Test")
	}
	if got.AcceptanceCriteria != "Test AC" {
		t.Errorf("AcceptanceCriteria = %s, want %s", got.AcceptanceCriteria, "Test AC")
	}
	if got.Owner != "agent-3" {
		t.Errorf("Owner = %s, want %s", got.Owner, "agent-3")
	}
}

func TestBeadJSONRoundTrip_MetadataAndLabels(t *testing.T) {
	tests := []struct {
		name        string
		bead        protocol.Bead
		wantJSON    string
		checkFields func(t *testing.T, got protocol.Bead)
	}{
		{
			name: "Bead with metadata and labels round-trips",
			bead: protocol.Bead{
				ID:       "oro-1",
				Title:    "Test",
				Priority: 1,
				Metadata: map[string]any{
					"version": "1.0",
					"count":   42,
					"active":  true,
				},
				Labels: []string{"urgent", "client-facing"},
			},
			checkFields: func(t *testing.T, got protocol.Bead) {
				t.Helper()
				if got.ID != "oro-1" {
					t.Errorf("ID = %s, want oro-1", got.ID)
				}
				if len(got.Metadata) == 0 {
					t.Errorf("Metadata is empty, want populated")
				}
				if v, ok := got.Metadata["count"]; !ok || v.(float64) != 42 {
					t.Errorf("Metadata count = %v, want 42", v)
				}
				if len(got.Labels) != 2 || got.Labels[0] != "urgent" {
					t.Errorf("Labels = %v, want [urgent client-facing]", got.Labels)
				}
			},
		},
		{
			name: "Bead with nil metadata and labels omits fields (omitempty)",
			bead: protocol.Bead{
				ID:       "oro-2",
				Title:    "Test",
				Priority: 2,
				Metadata: nil,
				Labels:   nil,
			},
			checkFields: func(t *testing.T, got protocol.Bead) {
				t.Helper()
				jsonBytes, _ := json.Marshal(got)
				jsonStr := string(jsonBytes)
				if strings.Contains(jsonStr, "\"metadata\"") {
					t.Errorf("JSON should not contain 'metadata' field when nil: %s", jsonStr)
				}
				if strings.Contains(jsonStr, "\"labels\"") {
					t.Errorf("JSON should not contain 'labels' field when nil: %s", jsonStr)
				}
			},
		},
		{
			name: "Bead without metadata/labels for backwards compatibility",
			bead: protocol.Bead{
				ID:       "oro-3",
				Title:    "Old Bead",
				Priority: 3,
			},
			checkFields: func(t *testing.T, got protocol.Bead) {
				t.Helper()
				if got.ID != "oro-3" {
					t.Errorf("ID = %s, want oro-3", got.ID)
				}
				if len(got.Metadata) > 0 {
					t.Errorf("Metadata should be empty or nil, got %v", got.Metadata)
				}
				if len(got.Labels) > 0 {
					t.Errorf("Labels should be empty or nil, got %v", got.Labels)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Encode to JSON
			jsonBytes, err := json.Marshal(tt.bead)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}

			// Decode back from JSON
			var got protocol.Bead
			if err := json.Unmarshal(jsonBytes, &got); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}

			// Check round-trip
			tt.checkFields(t, got)
		})
	}
}

func TestBeadDetailJSONRoundTrip_MetadataAndLabels(t *testing.T) {
	tests := []struct {
		name        string
		detail      protocol.BeadDetail
		checkFields func(t *testing.T, got protocol.BeadDetail)
	}{
		{
			name: "BeadDetail with metadata and labels round-trips",
			detail: protocol.BeadDetail{
				ID:       "oro-d1",
				Title:    "Detail Test",
				Metadata: map[string]any{"env": "prod"},
				Labels:   []string{"feature"},
			},
			checkFields: func(t *testing.T, got protocol.BeadDetail) {
				t.Helper()
				if got.Metadata["env"] != "prod" {
					t.Errorf("Metadata env = %v, want prod", got.Metadata["env"])
				}
				if len(got.Labels) != 1 || got.Labels[0] != "feature" {
					t.Errorf("Labels = %v, want [feature]", got.Labels)
				}
			},
		},
		{
			name: "BeadDetail with nil metadata/labels omits fields",
			detail: protocol.BeadDetail{
				ID:    "oro-d2",
				Title: "Test",
			},
			checkFields: func(t *testing.T, got protocol.BeadDetail) {
				t.Helper()
				jsonBytes, _ := json.Marshal(got)
				jsonStr := string(jsonBytes)
				if strings.Contains(jsonStr, "\"metadata\"") {
					t.Errorf("JSON should not contain 'metadata': %s", jsonStr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonBytes, err := json.Marshal(tt.detail)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}

			var got protocol.BeadDetail
			if err := json.Unmarshal(jsonBytes, &got); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}

			tt.checkFields(t, got)
		})
	}
}

func TestEmbedMessageTypeConstants(t *testing.T) {
	t.Parallel()

	if string(protocol.MsgEmbedRequest) != "EMBED_REQUEST" {
		t.Errorf("MsgEmbedRequest = %q, want %q", protocol.MsgEmbedRequest, "EMBED_REQUEST")
	}
	if string(protocol.MsgEmbedResponse) != "EMBED_RESPONSE" {
		t.Errorf("MsgEmbedResponse = %q, want %q", protocol.MsgEmbedResponse, "EMBED_RESPONSE")
	}
}

func TestEmbedRequestResponseRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  protocol.Message
	}{
		{
			name: "EmbedRequest with text",
			msg: protocol.Message{
				Type: protocol.MsgEmbedRequest,
				Embed: &protocol.EmbedRequest{
					Text: "hello world",
				},
			},
		},
		{
			name: "EmbedRequest empty text",
			msg: protocol.Message{
				Type: protocol.MsgEmbedRequest,
				Embed: &protocol.EmbedRequest{
					Text: "",
				},
			},
		},
		{
			name: "EmbedResponse with vec",
			msg: protocol.Message{
				Type: protocol.MsgEmbedResponse,
				EmbedResp: &protocol.EmbedResponse{
					Vec: make([]float32, 384),
				},
			},
		},
		{
			name: "EmbedResponse with error",
			msg: protocol.Message{
				Type: protocol.MsgEmbedResponse,
				EmbedResp: &protocol.EmbedResponse{
					Vec: nil,
					Err: "embedding failed",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(tc.msg)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}

			var got protocol.Message
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.name, err)
			}

			wantJSON, _ := json.Marshal(tc.msg)
			gotJSON, _ := json.Marshal(got)

			if string(wantJSON) != string(gotJSON) {
				t.Errorf("round-trip mismatch for %s:\n  want: %s\n  got:  %s", tc.name, wantJSON, gotJSON)
			}
		})
	}
}

func TestEmbedResponseMarshalUnderMaxSize(t *testing.T) {
	t.Parallel()

	resp := protocol.EmbedResponse{
		Vec: make([]float32, 384),
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal EmbedResponse: %v", err)
	}

	size := len(data)
	if size >= protocol.MaxMessageSize {
		t.Errorf("EmbedResponse with 384-element vec marshals to %d bytes, want < %d", size, protocol.MaxMessageSize)
	}

	msg := protocol.Message{
		Type: protocol.MsgEmbedResponse,
		EmbedResp: &protocol.EmbedResponse{
			Vec: make([]float32, 384),
		},
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal Message with EmbedResponse: %v", err)
	}

	msgSize := len(msgData)
	if msgSize >= protocol.MaxMessageSize {
		t.Errorf("Message with EmbedResponse marshals to %d bytes, want < %d", msgSize, protocol.MaxMessageSize)
	}
}
