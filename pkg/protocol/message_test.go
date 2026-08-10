package protocol_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"oro/pkg/cards"
	"oro/pkg/protocol"
)

func TestMessageTypes(t *testing.T) {
	t.Parallel()

	// All expected message type constants must be defined.
	types := []protocol.MessageType{
		protocol.MsgAssign,
		protocol.MsgShutdown,
		protocol.MsgHeartbeat,
		protocol.MsgStatus,
		protocol.MsgHandoff,
		protocol.MsgDone,
		protocol.MsgReadyForReview,
		protocol.MsgReconnect,
		protocol.MsgReviewResult,
	}

	expected := []string{
		"ASSIGN",
		"SHUTDOWN",
		"HEARTBEAT",
		"STATUS",
		"HANDOFF",
		"DONE",
		"READY_FOR_REVIEW",
		"RECONNECT",
		"REVIEW_RESULT",
	}

	for i, mt := range types {
		if string(mt) != expected[i] {
			t.Errorf("expected %q, got %q", expected[i], mt)
		}
	}
}

func TestAssignPayload_CodeSearchContext(t *testing.T) {
	t.Parallel()

	// Verify AssignPayload includes CodeSearchContext field and it
	// marshals/unmarshals correctly through JSON round-trip.
	original := protocol.AssignPayload{
		BeadID:             "oro-test",
		Worktree:           "/tmp/worktree",
		Model:              "sonnet",
		MemoryContext:      "some memory context",
		CodeSearchContext:  "## Relevant Code\n\n### file.go:10-20\n```go\nfunc Example() {}\n```",
		Title:              "Test Bead",
		Description:        "Test description",
		AcceptanceCriteria: "Test AC",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded protocol.AssignPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.CodeSearchContext != original.CodeSearchContext {
		t.Errorf("CodeSearchContext mismatch: got %q, want %q", decoded.CodeSearchContext, original.CodeSearchContext)
	}
}

func TestAssignPayloadCardsContextRoundTrip(t *testing.T) {
	t.Parallel()

	if _, ok := reflect.TypeOf(cards.DeckCard{}).FieldByName("BodyFull"); ok {
		t.Fatal("DeckCard must not expose BodyFull")
	}
	if _, ok := reflect.TypeOf(cards.InlinedCard{}).FieldByName("BodyFull"); !ok {
		t.Fatal("InlinedCard must expose BodyFull")
	}

	original := protocol.AssignPayload{
		BeadID:   "oro-cards-1",
		Worktree: "/tmp/worktree",
		Cards: cards.RelevantCards{
			Deck: []cards.DeckCard{
				{
					ID:          "card-deck-1",
					Type:        cards.CardTypePattern,
					Title:       "Run targeted tests first",
					BodySummary: "Use the task acceptance command before the full gate.",
					Score:       12.5,
					Tags:        []string{"go", "tests"},
				},
			},
			Inlined: []cards.InlinedCard{
				{
					ID:          "card-inline-1",
					Type:        cards.CardTypeDecision,
					Title:       "Render cards instead of memory",
					BodySummary: "Worker prompts consume cards.",
					BodyFull:    "Worker assignments should carry cards through the protocol and render them in ## Cards.",
					Score:       22.25,
					Tags:        []string{"prompt"},
				},
			},
		},
		MemoryContext: "legacy memory remains wire-compatible",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal AssignPayload: %v", err)
	}
	if !strings.Contains(string(data), `"cards"`) {
		t.Fatalf("AssignPayload JSON missing cards: %s", string(data))
	}

	var decoded protocol.AssignPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal AssignPayload: %v", err)
	}

	if len(decoded.Cards.Deck) != 1 {
		t.Fatalf("decoded Cards.Deck len = %d, want 1", len(decoded.Cards.Deck))
	}
	if got, want := decoded.Cards.Deck[0].Title, original.Cards.Deck[0].Title; got != want {
		t.Fatalf("decoded deck title = %q, want %q", got, want)
	}
	if len(decoded.Cards.Inlined) != 1 {
		t.Fatalf("decoded Cards.Inlined len = %d, want 1", len(decoded.Cards.Inlined))
	}
	if got, want := decoded.Cards.Inlined[0].BodyFull, original.Cards.Inlined[0].BodyFull; got != want {
		t.Fatalf("decoded inline body = %q, want %q", got, want)
	}
	if decoded.MemoryContext != original.MemoryContext {
		t.Fatalf("decoded MemoryContext = %q, want %q", decoded.MemoryContext, original.MemoryContext)
	}

	var empty protocol.AssignPayload
	if err := json.Unmarshal([]byte(`{"bead_id":"oro-empty","worktree":"/tmp/wt"}`), &empty); err != nil {
		t.Fatalf("unmarshal empty cards AssignPayload: %v", err)
	}
	if len(empty.Cards.Deck) != 0 || len(empty.Cards.Inlined) != 0 {
		t.Fatalf("empty Cards decoded as %#v, want zero value", empty.Cards)
	}

	rawWithDeckBody := []byte(`{
		"bead_id": "oro-extra",
		"worktree": "/tmp/wt",
		"cards": {
			"Deck": [{
				"ID": "card-deck-extra",
				"Type": "pattern",
				"Title": "deck title",
				"BodySummary": "deck summary",
				"BodyFull": "ignored deck body",
				"Score": 1.5,
				"Tags": ["deck"]
			}],
			"Inlined": [{
				"ID": "card-inline-extra",
				"Type": "decision",
				"Title": "inline title",
				"BodySummary": "inline summary",
				"BodyFull": "kept inline body",
				"Score": 2.5,
				"Tags": ["inline"]
			}]
		}
	}`)
	var extra protocol.AssignPayload
	if err := json.Unmarshal(rawWithDeckBody, &extra); err != nil {
		t.Fatalf("unmarshal extra BodyFull deck payload: %v", err)
	}
	if len(extra.Cards.Deck) != 1 || len(extra.Cards.Inlined) != 1 {
		t.Fatalf("extra Cards decoded as %#v, want one deck and one inline", extra.Cards)
	}
	if extra.Cards.Inlined[0].BodyFull != "kept inline body" {
		t.Fatalf("inline BodyFull = %q, want kept inline body", extra.Cards.Inlined[0].BodyFull)
	}
}

func TestHandoffPayloadFields(t *testing.T) {
	t.Parallel()

	// Verify HandoffPayload includes the new typed context fields and they
	// marshal/unmarshal correctly through JSON round-trip.
	original := protocol.HandoffPayload{
		BeadID:         "bead-ctx-1",
		WorkerID:       "worker-ctx",
		Learnings:      []string{"ruff must run before pyright", "SQLite WAL requires single-writer"},
		Decisions:      []string{"use table-driven tests"},
		FilesModified:  []string{"pkg/protocol/message.go", "pkg/worker/worker.go"},
		ContextSummary: "Extended HandoffPayload with typed context fields for cross-session memory",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal HandoffPayload: %v", err)
	}

	var decoded protocol.HandoffPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal HandoffPayload: %v", err)
	}

	// Verify all fields survived round-trip
	if decoded.BeadID != original.BeadID {
		t.Errorf("BeadID: got %q, want %q", decoded.BeadID, original.BeadID)
	}
	if decoded.WorkerID != original.WorkerID {
		t.Errorf("WorkerID: got %q, want %q", decoded.WorkerID, original.WorkerID)
	}
	if len(decoded.Learnings) != len(original.Learnings) {
		t.Fatalf("Learnings len: got %d, want %d", len(decoded.Learnings), len(original.Learnings))
	}
	for i, l := range decoded.Learnings {
		if l != original.Learnings[i] {
			t.Errorf("Learnings[%d]: got %q, want %q", i, l, original.Learnings[i])
		}
	}
	if len(decoded.Decisions) != len(original.Decisions) {
		t.Fatalf("Decisions len: got %d, want %d", len(decoded.Decisions), len(original.Decisions))
	}
	for i, d := range decoded.Decisions {
		if d != original.Decisions[i] {
			t.Errorf("Decisions[%d]: got %q, want %q", i, d, original.Decisions[i])
		}
	}
	if len(decoded.FilesModified) != len(original.FilesModified) {
		t.Fatalf("FilesModified len: got %d, want %d", len(decoded.FilesModified), len(original.FilesModified))
	}
	for i, f := range decoded.FilesModified {
		if f != original.FilesModified[i] {
			t.Errorf("FilesModified[%d]: got %q, want %q", i, f, original.FilesModified[i])
		}
	}
	if decoded.ContextSummary != original.ContextSummary {
		t.Errorf("ContextSummary: got %q, want %q", decoded.ContextSummary, original.ContextSummary)
	}

	// Verify omitempty: empty slices and empty string should not appear in JSON
	emptyPayload := protocol.HandoffPayload{
		BeadID:   "bead-empty",
		WorkerID: "worker-empty",
	}
	emptyData, err := json.Marshal(emptyPayload)
	if err != nil {
		t.Fatalf("marshal empty payload: %v", err)
	}
	emptyJSON := string(emptyData)
	for _, field := range []string{"learnings", "decisions", "files_modified", "context_summary"} {
		if contains(emptyJSON, field) {
			t.Errorf("expected omitted field %q in JSON of empty payload, got: %s", field, emptyJSON)
		}
	}
}

// TestAssignPayloadMemoryContext verifies the new MemoryContext field on AssignPayload.
func TestAssignPayloadMemoryContext(t *testing.T) {
	t.Parallel()

	original := protocol.AssignPayload{
		BeadID:        "bead-mem-1",
		Worktree:      "/tmp/wt-mem",
		MemoryContext: "## Relevant Memories\n- [gotcha] ruff must run before pyright",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded protocol.AssignPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.MemoryContext != original.MemoryContext {
		t.Errorf("MemoryContext: got %q, want %q", decoded.MemoryContext, original.MemoryContext)
	}

	// Verify omitempty: empty MemoryContext should not appear
	emptyAssign := protocol.AssignPayload{BeadID: "b1", Worktree: "/tmp/wt"}
	emptyData, err := json.Marshal(emptyAssign)
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if contains(string(emptyData), "memory_context") {
		t.Errorf("expected omitted memory_context in empty payload, got: %s", string(emptyData))
	}
}

func TestAssignPayloadHasRuntime(t *testing.T) {
	t.Parallel()

	original := protocol.AssignPayload{
		BeadID:   "oro-runtime",
		Worktree: "/tmp/worktree",
		Runtime:  "codex",
		Model:    "gpt-5.5",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal AssignPayload: %v", err)
	}
	if !strings.Contains(string(data), `"runtime":"codex"`) {
		t.Fatalf("AssignPayload JSON missing runtime: %s", string(data))
	}

	var decoded protocol.AssignPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal AssignPayload: %v", err)
	}
	if decoded.Runtime != original.Runtime {
		t.Fatalf("Runtime = %q, want %q", decoded.Runtime, original.Runtime)
	}
}

// contains checks if substr exists in s (simple helper to avoid importing strings).
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestMessageJSON(t *testing.T) { //nolint:funlen // table-driven test with 8 message types
	t.Parallel()

	tests := []struct {
		name string
		msg  protocol.Message
	}{
		{
			name: "ASSIGN",
			msg: protocol.Message{
				Type: protocol.MsgAssign,
				Assign: &protocol.AssignPayload{
					BeadID:   "bead-123",
					Worktree: "/tmp/worktree",
				},
			},
		},
		{
			name: "SHUTDOWN",
			msg: protocol.Message{
				Type: protocol.MsgShutdown,
			},
		},
		{
			name: "HEARTBEAT",
			msg: protocol.Message{
				Type: protocol.MsgHeartbeat,
				Heartbeat: &protocol.HeartbeatPayload{
					BeadID:     "bead-456",
					WorkerID:   "worker-1",
					ContextPct: 42,
				},
			},
		},
		{
			name: "STATUS",
			msg: protocol.Message{
				Type: protocol.MsgStatus,
				Status: &protocol.StatusPayload{
					BeadID:   "bead-789",
					WorkerID: "worker-2",
					State:    "running",
					Result:   "ok",
				},
			},
		},
		{
			name: "HANDOFF",
			msg: protocol.Message{
				Type: protocol.MsgHandoff,
				Handoff: &protocol.HandoffPayload{
					BeadID:         "bead-abc",
					WorkerID:       "worker-3",
					Learnings:      []string{"learned something"},
					Decisions:      []string{"decided something"},
					FilesModified:  []string{"file.go"},
					ContextSummary: "summary of work",
				},
			},
		},
		{
			name: "DONE",
			msg: protocol.Message{
				Type: protocol.MsgDone,
				Done: &protocol.DonePayload{
					BeadID:   "bead-def",
					WorkerID: "worker-4",
				},
			},
		},
		{
			name: "DONE_with_quality_gate",
			msg: protocol.Message{
				Type: protocol.MsgDone,
				Done: &protocol.DonePayload{
					BeadID:            "bead-qg",
					WorkerID:          "worker-qg",
					QualityGatePassed: true,
				},
			},
		},
		{
			name: "READY_FOR_REVIEW",
			msg: protocol.Message{
				Type: protocol.MsgReadyForReview,
				ReadyForReview: &protocol.ReadyForReviewPayload{
					BeadID:   "bead-ghi",
					WorkerID: "worker-5",
				},
			},
		},
		{
			name: "RECONNECT",
			msg: protocol.Message{
				Type: protocol.MsgReconnect,
				Reconnect: &protocol.ReconnectPayload{
					WorkerID:   "worker-6",
					BeadID:     "bead-jkl",
					State:      "paused",
					ContextPct: 75,
					BufferedEvents: []protocol.Message{
						{
							Type: protocol.MsgHeartbeat,
							Heartbeat: &protocol.HeartbeatPayload{
								BeadID:     "bead-jkl",
								WorkerID:   "worker-6",
								ContextPct: 70,
							},
						},
					},
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

			// Re-marshal both and compare JSON to verify round-trip equality.
			wantJSON, _ := json.Marshal(tc.msg)
			gotJSON, _ := json.Marshal(got)

			if string(wantJSON) != string(gotJSON) {
				t.Errorf("round-trip mismatch for %s:\n  want: %s\n  got:  %s", tc.name, wantJSON, gotJSON)
			}
		})
	}
}

func TestDonePayload_QualityGatePassed_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		passed bool
	}{
		{"gate_passed", true},
		{"gate_failed", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			msg := protocol.Message{
				Type: protocol.MsgDone,
				Done: &protocol.DonePayload{
					BeadID:            "bead-qg",
					WorkerID:          "worker-qg",
					QualityGatePassed: tc.passed,
				},
			}

			data, err := json.Marshal(msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var got protocol.Message
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if got.Done == nil {
				t.Fatal("expected Done payload to be non-nil")
			}
			if got.Done.QualityGatePassed != tc.passed {
				t.Errorf("QualityGatePassed: got %v, want %v", got.Done.QualityGatePassed, tc.passed)
			}
		})
	}
}

func TestReadyForReviewEvidenceContract(t *testing.T) {
	t.Parallel()

	trustedEvidence := &protocol.QGEvidence{
		RunID:        "run-17",
		AssignmentID: 17,
		BeadID:       "oro-ready",
		WorkerID:     "worker-ready",
		HeadSHA:      strings.Repeat("2", 40),
		TargetBranch: "main",
		TargetSHA:    strings.Repeat("1", 40),
		ScriptHash:   strings.Repeat("a", 64),
		Mode:         "worker",
		Passed:       true,
		OutputHash:   strings.Repeat("b", 64),
		StartedAt:    "2026-08-10T03:00:00Z",
		FinishedAt:   "2026-08-10T03:01:00Z",
	}
	trustedRef := &protocol.QGEvidenceRef{
		RunID:  "run-17",
		Path:   "/var/tmp/oro/evidence/oro-ready/17/1.json",
		SHA256: strings.Repeat("c", 64),
	}
	want := protocol.ReadyForReviewPayload{
		BeadID:         "oro-ready",
		WorkerID:       "worker-ready",
		AssignmentID:   17,
		Worktree:       "/tmp/oro-ready",
		QGEvidencePath: trustedRef.Path,
		TargetSHA:      trustedEvidence.TargetSHA,
		ReadyAttempt:   "1",
		QGEvidence:     trustedEvidence,
		QGEvidenceRef:  trustedRef,
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal durable READY evidence: %v", err)
	}
	var got protocol.ReadyForReviewPayload
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal durable READY evidence: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("durable READY evidence round-trip = %#v, want %#v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("trusted evidence validation: %v", err)
	}

	legacyJSON := []byte(`{"bead_id":"oro-ready","worker_id":"worker-ready","assignment_id":17,"worktree":"/tmp/oro-ready","qg_evidence_path":"/var/tmp/oro/evidence/oro-ready/17/1.json","target_sha":"1111111111111111111111111111111111111111"}`)
	var legacy protocol.ReadyForReviewPayload
	if err := json.Unmarshal(legacyJSON, &legacy); err != nil {
		t.Fatalf("unmarshal legacy READY payload: %v", err)
	}
	if legacy.QGEvidence != nil || legacy.QGEvidenceRef != nil {
		t.Fatal("legacy READY payload unexpectedly acquired durable evidence pointers")
	}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy READY payload validation: %v", err)
	}
	legacyBaseCases := []struct {
		name   string
		mutate func(*protocol.ReadyForReviewPayload)
	}{
		{name: "missing bead ID", mutate: func(p *protocol.ReadyForReviewPayload) { p.BeadID = "" }},
		{name: "missing worker ID", mutate: func(p *protocol.ReadyForReviewPayload) { p.WorkerID = "" }},
		{name: "missing assignment ID", mutate: func(p *protocol.ReadyForReviewPayload) { p.AssignmentID = 0 }},
		{name: "missing worktree", mutate: func(p *protocol.ReadyForReviewPayload) { p.Worktree = "" }},
		{name: "missing evidence path", mutate: func(p *protocol.ReadyForReviewPayload) { p.QGEvidencePath = "" }},
		{name: "missing target SHA", mutate: func(p *protocol.ReadyForReviewPayload) { p.TargetSHA = "" }},
	}
	for _, tc := range legacyBaseCases {
		t.Run("legacy "+tc.name, func(t *testing.T) {
			candidate := legacy
			tc.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("legacy validation unexpectedly accepted %s", tc.name)
			}
		})
	}

	cases := []struct {
		name   string
		mutate func(*protocol.ReadyForReviewPayload)
	}{
		{
			name: "missing evidence pointer",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidence = nil
			},
		},
		{
			name: "missing reference pointer",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidenceRef = nil
			},
		},
		{
			name: "empty required evidence field",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidence = &protocol.QGEvidence{}
			},
		},
		{
			name: "invalid head SHA",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidence.HeadSHA = "not-a-commit"
			},
		},
		{
			name: "invalid target SHA",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidence.TargetSHA = "not-a-target"
			},
		},
		{
			name: "invalid hash",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidence.ScriptHash = "not-a-hash"
			},
		},
		{
			name: "invalid output hash",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidence.OutputHash = "not-an-output-hash"
			},
		},
		{
			name: "uppercase hash",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidence.ScriptHash = strings.Repeat("A", 64)
			},
		},
		{
			name: "reference run mismatch",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidenceRef.RunID = "run-other"
			},
		},
		{
			name: "empty reference run ID",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidenceRef.RunID = ""
			},
		},
		{
			name: "empty reference path",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidenceRef.Path = ""
			},
		},
		{
			name: "relative reference path",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidenceRef.Path = "evidence/oro-ready/17/1.json"
			},
		},
		{
			name: "unclean reference path",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidenceRef.Path = "/var/tmp/oro/evidence/oro-ready/17/../17/1.json"
			},
		},
		{
			name: "invalid reference hash",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidenceRef.SHA256 = strings.Repeat("d", 63)
			},
		},
		{
			name: "uppercase reference hash",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidenceRef.SHA256 = strings.Repeat("C", 64)
			},
		},
		{
			name: "wrong mode",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidence.Mode = "dispatcher"
			},
		},
		{
			name: "failed evidence",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidence.Passed = false
			},
		},
		{
			name: "finish precedes start",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.QGEvidence.FinishedAt = "2026-08-10T02:59:00Z"
			},
		},
		{
			name: "empty ready attempt",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.ReadyAttempt = ""
			},
		},
		{
			name: "empty ready worker ID",
			mutate: func(p *protocol.ReadyForReviewPayload) {
				p.WorkerID = ""
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := want
			candidate.QGEvidence = trustedEvidenceCopy(trustedEvidence)
			candidate.QGEvidenceRef = &protocol.QGEvidenceRef{
				RunID:  trustedRef.RunID,
				Path:   trustedRef.Path,
				SHA256: trustedRef.SHA256,
			}
			tc.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("validation unexpectedly accepted %s", tc.name)
			}
		})
	}
}

func trustedEvidenceCopy(evidence *protocol.QGEvidence) *protocol.QGEvidence {
	cloned := *evidence
	return &cloned
}

func TestAssignPayload_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload protocol.AssignPayload
		wantErr bool
	}{
		{
			name: "valid_payload",
			payload: protocol.AssignPayload{
				BeadID:   "oro-1nf",
				Worktree: "/tmp/worktree",
			},
			wantErr: false,
		},
		{
			name: "valid_with_all_fields",
			payload: protocol.AssignPayload{
				BeadID:             "oro-1nf.1",
				Worktree:           "/tmp/worktree",
				Model:              "sonnet",
				MemoryContext:      "context",
				CodeSearchContext:  "code",
				Feedback:           "feedback",
				Title:              "title",
				Description:        "desc",
				AcceptanceCriteria: "ac",
				Attempt:            1,
			},
			wantErr: false,
		},
		{
			name: "empty_bead_id",
			payload: protocol.AssignPayload{
				BeadID:   "",
				Worktree: "/tmp/worktree",
			},
			wantErr: true,
		},
		{
			name: "empty_worktree",
			payload: protocol.AssignPayload{
				BeadID:   "oro-1nf",
				Worktree: "",
			},
			wantErr: true,
		},
		{
			name: "both_empty",
			payload: protocol.AssignPayload{
				BeadID:   "",
				Worktree: "",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.payload.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestAssignPayloadEpicDecomposition(t *testing.T) {
	t.Parallel()

	t.Run("round_trip_true", func(t *testing.T) {
		t.Parallel()
		original := protocol.AssignPayload{
			BeadID:              "oro-epic",
			Worktree:            "/repo",
			IsEpicDecomposition: true,
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		var decoded protocol.AssignPayload
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !decoded.IsEpicDecomposition {
			t.Error("IsEpicDecomposition: got false, want true")
		}
	})

	t.Run("omitempty_when_false", func(t *testing.T) {
		t.Parallel()
		original := protocol.AssignPayload{
			BeadID:   "oro-task",
			Worktree: "/tmp/wt",
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		// Field should not appear in JSON when false (omitempty).
		if strings.Contains(string(data), "is_epic_decomposition") {
			t.Error("expected is_epic_decomposition to be omitted when false")
		}
		var decoded protocol.AssignPayload
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.IsEpicDecomposition {
			t.Error("IsEpicDecomposition: got true, want false (default)")
		}
	})
}

func TestValidateBeadID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		beadID  string
		wantErr bool
	}{
		// Valid IDs
		{"valid_simple", "oro-1nf", false},
		{"valid_with_dot", "oro-1nf.1", false},
		{"valid_with_multiple_dots", "oro-3e0.2.1", false},
		{"valid_dfe", "oro-dfe.3", false},
		{"valid_lowercase_letters", "abc-123", false},
		{"valid_with_hyphens", "oro-test-1", false},
		{"valid_with_underscores", "oro_test_1", false},
		{"valid_mixed", "oro-1nf_2.3-test", false},
		{"valid_min_length", "a1", false},
		{"valid_max_length", "a12345678901234567890123456789012345678901234567890123456789012", false}, // 63 chars total

		// Invalid IDs - path traversal
		{"invalid_parent_dir", "../etc", true},
		{"invalid_parent_with_valid_start", "oro-1nf/../etc", true},
		{"invalid_double_parent", "../../etc", true},
		{"invalid_absolute_path", "/etc/passwd", true},
		{"invalid_absolute_with_valid_prefix", "/oro-1nf", true},

		// Invalid IDs - special characters
		{"invalid_backslash", "oro\\test", true},
		{"invalid_null_byte", "oro\x00test", true},
		{"invalid_space", "oro test", true},
		{"invalid_special_chars", "oro@test", true},
		{"invalid_parentheses", "oro(test)", true},
		{"invalid_brackets", "oro[test]", true},
		{"invalid_braces", "oro{test}", true},

		// Invalid IDs - format violations
		{"invalid_empty", "", true},
		{"invalid_too_short", "a", true},
		{"invalid_starts_with_hyphen", "-oro", true},
		{"invalid_starts_with_dot", ".oro", true},
		{"invalid_starts_with_underscore", "_oro", true},
		{"invalid_uppercase", "ORO-1NF", true},
		{"invalid_ends_with_hyphen", "oro-", true},
		{"invalid_too_long", "a1234567890123456789012345678901234567890123456789012345678901234", true}, // 64 chars
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := protocol.ValidateBeadID(tc.beadID)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateBeadID(%q) = nil, want error", tc.beadID)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateBeadID(%q) = %v, want nil", tc.beadID, err)
			}
		})
	}
}

// TestAssignPayloadValidate_NewFields verifies GitLog and WorkerProgram fields
// serialize/deserialize correctly via JSON round-trip with omitempty behavior.
func TestAssignPayloadValidate_NewFields(t *testing.T) {
	t.Parallel()

	// Test with both new fields populated
	t.Run("with_new_fields", func(t *testing.T) {
		t.Parallel()
		original := protocol.AssignPayload{
			BeadID:        "oro-kjje",
			Worktree:      "/tmp/worktree",
			GitLog:        "commit abc123\nAuthor: Test <test@example.com>\nDate: 2026-03-19\nMessage: test commit",
			WorkerProgram: "claude --model opus -p /path/to/prompt.md",
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}

		var decoded protocol.AssignPayload
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		if decoded.GitLog != original.GitLog {
			t.Errorf("GitLog mismatch: got %q, want %q", decoded.GitLog, original.GitLog)
		}
		if decoded.WorkerProgram != original.WorkerProgram {
			t.Errorf("WorkerProgram mismatch: got %q, want %q", decoded.WorkerProgram, original.WorkerProgram)
		}
	})

	// Test omitempty: empty fields should not appear in JSON
	t.Run("omitempty_behavior", func(t *testing.T) {
		t.Parallel()
		original := protocol.AssignPayload{
			BeadID:   "oro-test",
			Worktree: "/tmp/wt",
			// GitLog and WorkerProgram are empty
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}

		jsonStr := string(data)
		if strings.Contains(jsonStr, "git_log") {
			t.Errorf("expected git_log to be omitted when empty, got: %s", jsonStr)
		}
		if strings.Contains(jsonStr, "worker_program") {
			t.Errorf("expected worker_program to be omitted when empty, got: %s", jsonStr)
		}
	})

	// Test partial: only GitLog populated
	t.Run("only_git_log", func(t *testing.T) {
		t.Parallel()
		original := protocol.AssignPayload{
			BeadID:   "oro-1",
			Worktree: "/tmp/wt",
			GitLog:   "commit xyz",
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		var decoded protocol.AssignPayload
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if decoded.GitLog != "commit xyz" {
			t.Errorf("GitLog: got %q, want %q", decoded.GitLog, "commit xyz")
		}
		if decoded.WorkerProgram != "" {
			t.Errorf("WorkerProgram: got %q, want empty", decoded.WorkerProgram)
		}
	})
}
