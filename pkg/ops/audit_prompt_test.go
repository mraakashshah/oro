package ops //nolint:testpackage // internal test needs access to audit orchestration helpers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditSectionFanout(t *testing.T) {
	worktree := testReviewRepo(t)
	t.Chdir(worktree)
	writePersonaAgentConfig(t, worktree)
	writeFile(t, filepath.Join(worktree, "pkg", "worker.go"), "package pkg\n\nfunc Worker() string { return \"changed\" }\n")

	sections := []string{
		"code-quality",
		"tests-safety",
		"data-migrations",
		"security-static",
		"perf-patterns",
		"dx-deps-docs",
	}
	spawner := &recordingReviewSpawner{
		stdout: structuredReviewOutput(t, ReviewReport{Reviewer: "auditor", Verdict: VerdictApproved}),
	}
	s := NewSpawner(spawner)

	result := waitResult(t, s.Audit(context.Background(), AuditOpts{
		Worktree:     worktree,
		MaxReviewers: 2,
	}))

	if result.Type != OpsAudit {
		t.Fatalf("Audit result type = %q, want %q", result.Type, OpsAudit)
	}
	if result.Verdict != VerdictApproved {
		t.Fatalf("Audit verdict = %q, want approved; feedback=%s err=%v", result.Verdict, result.Feedback, result.Err)
	}

	var feedback struct {
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(result.Feedback), &feedback); err != nil {
		t.Fatalf("Audit feedback is not merged findings JSON: %v\n%s", err, result.Feedback)
	}
	if len(feedback.Findings) != 0 {
		t.Fatalf("Audit findings = %#v, want none", feedback.Findings)
	}

	calls := spawner.getCalls()
	if len(calls) != len(sections) {
		t.Fatalf("audit spawn calls = %d, want %d", len(calls), len(sections))
	}
	seen := make(map[string]bool, len(sections))
	for _, call := range calls {
		if !strings.Contains(call.prompt, "## Whole-Repository Audit") {
			t.Fatalf("audit prompt omitted shared audit base:\n%s", call.prompt)
		}
		section := ""
		for _, candidate := range sections {
			if strings.Contains(call.prompt, "## Audit Section: "+candidate) {
				if section != "" {
					t.Fatalf("audit prompt included multiple section fragments:\n%s", call.prompt)
				}
				section = candidate
			}
		}
		if section == "" {
			t.Fatalf("audit prompt omitted a section fragment:\n%s", call.prompt)
		}
		seen[section] = true
	}
	for _, section := range sections {
		if !seen[section] {
			t.Fatalf("missing prompt for section %q", section)
		}
	}
}

func TestAuditAllUnparseableReportsFailClosed(t *testing.T) {
	worktree := testReviewRepo(t)
	t.Chdir(worktree)
	writePersonaAgentConfig(t, worktree)
	writeFile(t, filepath.Join(worktree, "pkg", "worker.go"), "package pkg\n\nfunc Worker() string { return \"changed\" }\n")

	s := NewSpawner(&recordingReviewSpawner{stdout: "not a structured review report"})
	result := waitResult(t, s.Audit(context.Background(), AuditOpts{Worktree: worktree}))

	if result.Verdict != VerdictFailed {
		t.Fatalf("Audit verdict = %q, want failed for unparseable section reports; feedback=%s err=%v", result.Verdict, result.Feedback, result.Err)
	}
}
