package ops //nolint:testpackage // internal test needs access to audit orchestration helpers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAuditSectionIDs(t *testing.T) {
	want := []string{
		"code-quality",
		"tests-safety",
		"data-migrations",
		"security-static",
		"perf-patterns",
		"dx-deps-docs",
	}
	if got := AuditSectionIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("AuditSectionIDs() = %#v, want %#v", got, want)
	}
}

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

func TestAuditMalformedSectionFailsClosed(t *testing.T) {
	personas := []Persona{
		{ID: "code-quality", Role: "ops_audit_code_quality"},
		{ID: "tests-safety", Role: "ops_audit_tests_safety"},
		{ID: "data-migrations", Role: "ops_audit_data_migrations"},
	}
	spawner := &recordingReviewSpawner{outputs: []string{
		"legacy-only report\nVERDICT: APPROVED\n",
		"```json\n{malformed\n```\nVERDICT: REJECTED\n",
		structuredReviewOutput(t, ReviewReport{Reviewer: "model-spoof", Verdict: VerdictApproved}),
	}}

	reports := NewSpawner(spawner).collectPersonaReviews(
		context.Background(), OpsAudit, ReviewOpts{MaxReviewers: 1}, personas, "audit prompt",
	)

	if len(reports) != len(personas) {
		t.Fatalf("reports = %d, want %d", len(reports), len(personas))
	}
	for i := range 2 {
		if reports[i].Verdict != VerdictFailed {
			t.Errorf("reports[%d].Verdict = %q, want failed for malformed structured output", i, reports[i].Verdict)
		}
	}
	if reports[2].Reviewer != "data-migrations" || reports[2].Verdict != VerdictApproved {
		t.Fatalf("valid report = %#v, want attributed approved data-migrations report", reports[2])
	}
	if got, want := coveredAuditSections(reports), []string{"data-migrations"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("coveredAuditSections() = %#v, want %#v", got, want)
	}
}

func TestAuditCoverageUsesAssignedPersona(t *testing.T) {
	t.Run("audit coverage ignores model reviewer values", func(t *testing.T) {
		personas := auditSections()
		spawner := &recordingReviewSpawner{outputs: []string{
			structuredReviewOutput(t, ReviewReport{Reviewer: "tests-safety", Verdict: VerdictApproved}),
			structuredReviewOutput(t, ReviewReport{Reviewer: "code-quality", Verdict: VerdictFailed}),
			structuredReviewOutput(t, ReviewReport{Reviewer: "unknown-section", Verdict: VerdictApproved}),
			structuredReviewOutput(t, ReviewReport{Reviewer: "code-quality", Verdict: VerdictApproved}),
			structuredReviewOutput(t, ReviewReport{Reviewer: "", Verdict: VerdictApproved}),
			structuredReviewOutput(t, ReviewReport{Reviewer: "tests-safety", Verdict: VerdictApproved}),
		}}
		s := NewSpawner(spawner)

		reports := s.collectPersonaReviews(
			context.Background(),
			OpsAudit,
			ReviewOpts{MaxReviewers: 1},
			personas,
			"audit prompt",
		)

		want := []string{"code-quality", "data-migrations", "security-static", "perf-patterns", "dx-deps-docs"}
		if got := coveredAuditSections(reports); !reflect.DeepEqual(got, want) {
			t.Fatalf("coveredAuditSections() = %#v, want assigned successful personas %#v", got, want)
		}
	})

	t.Run("regular review attribution ignores model reviewer values", func(t *testing.T) {
		personas := []Persona{
			{ID: "correctness", Role: "ops_review_correctness"},
			{ID: "security", Role: "ops_review_security"},
		}
		spawner := &recordingReviewSpawner{outputs: []string{
			structuredReviewOutput(t, ReviewReport{Reviewer: "security", Verdict: VerdictApproved}),
			structuredReviewOutput(t, ReviewReport{Reviewer: "", Verdict: VerdictApproved}),
		}}
		s := NewSpawner(spawner)

		reports := s.collectPersonaReviews(
			context.Background(),
			OpsReview,
			ReviewOpts{MaxReviewers: 1},
			personas,
			"review prompt",
		)

		got := []string{reports[0].Reviewer, reports[1].Reviewer}
		want := []string{"correctness", "security"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("reviewer attribution = %#v, want assigned personas %#v", got, want)
		}
	})
}

func TestAuditFindingSourcesUseAssignedPersona(t *testing.T) {
	t.Run("untrusted model sources cannot promote one persona", func(t *testing.T) {
		testCases := []struct {
			name    string
			sources []string
		}{
			{name: "empty"},
			{name: "spoofed", sources: []string{"tests-safety"}},
			{name: "duplicate", sources: []string{"code-quality", "code-quality"}},
			{name: "unknown", sources: []string{"unknown-section"}},
		}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				reports := collectSourceReports(t, OpsAudit, []Persona{{
					ID: "code-quality", Role: "ops_audit_code_quality",
				}}, []Finding{auditSourceFinding(tc.sources)})

				if got, want := reports[0].Findings[0].Sources, []string{"code-quality"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("finding sources = %#v, want assigned persona %#v", got, want)
				}
				merged := []Finding{mergeFindingGroup(flattenReviewReports(reports), "oro-source-binding")}
				promoteFindings(merged)
				if merged[0].Confidence != 50 || len(gateFindings(merged)) != 0 {
					t.Fatalf("one assigned persona manufactured promotion: %#v", merged[0])
				}
			})
		}
	})

	t.Run("independent assigned personas still union after merge", func(t *testing.T) {
		personas := []Persona{
			{ID: "code-quality", Role: "ops_audit_code_quality"},
			{ID: "tests-safety", Role: "ops_audit_tests_safety"},
		}
		reports := collectSourceReports(t, OpsAudit, personas, []Finding{
			auditSourceFinding([]string{"model-spoof"}),
			auditSourceFinding([]string{"model-spoof"}),
		})

		group := flattenReviewReports(reports)
		merged := []Finding{mergeFindingGroup(group, "oro-source-binding")}
		promoteFindings(merged)
		if got, want := merged[0].Sources, []string{"code-quality", "tests-safety"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("merged sources = %#v, want independent personas %#v", got, want)
		}
		if merged[0].Confidence != 75 || len(gateFindings(merged)) != 1 {
			t.Fatalf("independent persona corroboration was not promoted: %#v", merged[0])
		}
	})

	t.Run("regular review uses the same assigned persona boundary", func(t *testing.T) {
		reports := collectSourceReports(t, OpsReview, []Persona{{
			ID: "correctness", Role: "ops_review_correctness",
		}}, []Finding{auditSourceFinding([]string{"security", "unknown"})})

		if got, want := reports[0].Findings[0].Sources, []string{"correctness"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("regular review sources = %#v, want assigned persona %#v", got, want)
		}
	})
}

func collectSourceReports(t *testing.T, opsType Type, personas []Persona, findings []Finding) []ReviewReport {
	t.Helper()
	outputs := make([]string, 0, len(findings))
	for _, finding := range findings {
		outputs = append(outputs, structuredReviewOutput(t, ReviewReport{
			Reviewer: "model-claimed-reviewer",
			Verdict:  VerdictRejected,
			Findings: []Finding{finding},
		}))
	}
	return NewSpawner(&recordingReviewSpawner{outputs: outputs}).collectPersonaReviews(
		context.Background(), opsType, ReviewOpts{MaxReviewers: 1}, personas, "prompt",
	)
}

func auditSourceFinding(sources []string) Finding {
	finding := reviewMergeFinding("pkg/ops/finding.go", 10, "source integrity", SevImportant, 50, "model")
	finding.Sources = sources
	return finding
}
