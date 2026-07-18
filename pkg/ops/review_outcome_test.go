package ops //nolint:testpackage // white-box test exercises the required unexported parser signature

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypedReviewOutcomeParsing(t *testing.T) {
	t.Parallel()

	validFinding := `{
  "id": "fnd_auth",
  "severity": "important",
  "category": "correctness",
  "title": "authorization bypass",
  "detail": "request handling misses authorization",
  "evidence": [{"file": "pkg/auth/check.go", "line_start": 12, "line_end": 14}],
  "confidence": 90,
  "sources": ["correctness"],
  "origin": "review",
  "contract_impact": "implementation_fix",
  "required_action": "add authorization enforcement"
}`

	tests := []struct {
		name         string
		raw          string
		wantDecision ReviewDecision
		wantErr      string
	}{
		{
			name: "schema-valid approval",
			raw: `{
  "decision": "approved",
  "findings": [],
  "blockers": [],
  "verification": {"acceptance_status": "passed"},
  "execution": {"kind": "succeeded", "complete": true},
  "summary": "review complete",
  "artifact": {"sha256": "abc", "bytes": 3}
}`,
			wantDecision: ReviewApproved,
		},
		{
			name: "gating finding outranks blocker",
			raw: `{
  "decision": "blocked",
  "findings": [` + validFinding + `],
  "blockers": [{"class": "environment", "scope": "acceptance", "summary": "sandbox unavailable"}],
  "verification": {"acceptance_status": "blocked"},
  "execution": {"kind": "succeeded", "complete": true},
  "summary": "review complete",
  "artifact": {"sha256": "abc", "bytes": 3}
}`,
			wantDecision: ReviewRejected,
		},
		{
			name: "process error outranks approval",
			raw: `{
  "decision": "approved",
  "findings": [],
  "blockers": [],
  "verification": {"acceptance_status": "not_run"},
  "execution": {"kind": "exit_error", "error_code": "exit_1", "complete": false},
  "summary": "review interrupted",
  "artifact": {"sha256": "abc", "bytes": 3}
}`,
			wantDecision: ReviewFailed,
		},
		{
			name: "nonzero exit outranks approval",
			raw: `{
  "decision": "approved",
  "findings": [],
  "blockers": [],
  "verification": {"acceptance_status": "passed"},
  "execution": {"kind": "succeeded", "exit_code": 1, "complete": true},
  "summary": "review complete",
  "artifact": {"sha256": "abc", "bytes": 3}
}`,
			wantDecision: ReviewFailed,
		},
		{
			name: "missing decision fails closed",
			raw: `{
  "findings": [],
  "blockers": [],
  "verification": {"acceptance_status": "passed"},
  "execution": {"kind": "succeeded", "complete": true},
  "summary": "review complete",
  "artifact": {"sha256": "abc", "bytes": 3}
}`,
			wantErr: "invalid review decision",
		},
		{
			name: "unknown decision fails closed",
			raw: `{
  "decision": "indeterminate",
  "findings": [],
  "blockers": [],
  "verification": {"acceptance_status": "passed"},
  "execution": {"kind": "succeeded", "complete": true},
  "summary": "review complete",
  "artifact": {"sha256": "abc", "bytes": 3}
}`,
			wantErr: "invalid review decision",
		},
		{
			name:    "ambiguous prose fails closed",
			raw:     "The change looks good, probably approved.",
			wantErr: "structured review JSON is required",
		},
		{
			name: "malformed schema fails closed",
			raw: `{
  "decision": "approved",
  "verification": {"acceptance_status": "passed"},
  "execution": {"kind": "succeeded", "complete": true},
  "summary": "review complete",
  "artifact": {"sha256": "", "bytes": 3}
}`,
			wantErr: "artifact sha256 is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := parseStructuredReviewReport(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseStructuredReviewReport() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStructuredReviewReport() error = %v", err)
			}
			if out.Decision != tt.wantDecision {
				t.Fatalf("decision = %q, want %q", out.Decision, tt.wantDecision)
			}
			if err := ValidateReviewOutcome(out); err != nil {
				t.Fatalf("ValidateReviewOutcome() error = %v", err)
			}
		})
	}
}

func TestDocsOnlyTypedReviewOutcome(t *testing.T) {
	worktree := initReviewTestRepo(t)
	if err := os.MkdirAll(filepath.Join(worktree, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "docs", "guide.md"), []byte("# Guide\n"), 0o644); err != nil {
		t.Fatalf("write docs change: %v", err)
	}

	mock := &mockBatchSpawner{process: newReadyMockProcess("VERDICT: APPROVED", nil)}
	result := waitResult(t, NewSpawner(mock).Review(context.Background(), ReviewOpts{
		BeadID:     "oro-docs",
		Worktree:   worktree,
		BaseBranch: "main",
	}))

	if result.Verdict != VerdictApproved {
		t.Fatalf("docs-only review verdict = %q, want approved", result.Verdict)
	}
	outcome, err := parseStructuredReviewReport(result.Feedback)
	if err != nil {
		t.Fatalf("parseStructuredReviewReport() error = %v", err)
	}
	if err := ValidateReviewOutcome(outcome); err != nil {
		t.Fatalf("ValidateReviewOutcome() error = %v", err)
	}
	if outcome.Decision != ReviewApproved || outcome.Execution.Kind != ReviewExecSucceeded || !outcome.Execution.Complete {
		t.Fatalf("docs-only outcome = %#v, want complete succeeded approval", outcome)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.Feedback), &raw); err != nil {
		t.Fatalf("unmarshal docs-only feedback: %v", err)
	}
	if len(raw["policy_hash"]) == 0 || string(raw["policy_hash"]) == `""` {
		t.Fatalf("docs-only outcome missing explicit policy_hash: %s", result.Feedback)
	}
	if calls := mock.getCalls(); len(calls) != 0 {
		t.Fatalf("docs-only review should not spawn ops process, got %d calls", len(calls))
	}
}

func TestDocsOnlyReviewWithoutPolicyFallsBack(t *testing.T) {
	worktree := initReviewTestRepo(t)
	if err := os.MkdirAll(filepath.Join(worktree, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "docs", "guide.md"), []byte("# Guide\n"), 0o644); err != nil {
		t.Fatalf("write docs change: %v", err)
	}

	mock := &mockBatchSpawner{process: newReadyMockProcess("VERDICT: APPROVED", nil)}
	result := waitResult(t, NewSpawner(mock).Review(context.Background(), ReviewOpts{
		BeadID:       "oro-docs",
		Worktree:     worktree,
		BaseBranch:   "main",
		ReviewPolicy: &ReviewPolicy{},
	}))

	if result.Verdict != VerdictFailed {
		t.Fatalf("review without policy verdict = %q, want failed typed review", result.Verdict)
	}
	if result.Err == nil {
		t.Fatal("review without policy unexpectedly accepted raw prose")
	}
	if calls := mock.getCalls(); len(calls) != 1 {
		t.Fatalf("review without policy should invoke reviewer once, got %d calls", len(calls))
	} else if !strings.Contains(calls[0].prompt, "Structured Review Output") {
		t.Fatal("review without policy did not use the typed review prompt")
	}
}
