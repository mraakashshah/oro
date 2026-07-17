package ops //nolint:testpackage // white-box test exercises the required unexported parser signature

import (
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
