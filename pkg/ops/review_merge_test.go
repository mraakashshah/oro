package ops //nolint:testpackage // internal test needs access to unexported mergeReports

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMergeAuditReportsCoverage(t *testing.T) {
	reports := []ReviewReport{
		{Reviewer: "dx-deps-docs", Verdict: VerdictApproved},
		{Reviewer: "unknown-section", Verdict: VerdictApproved},
		{Reviewer: "perf-patterns", Verdict: VerdictRejected},
		{Reviewer: "code-quality", Verdict: VerdictApproved},
		{Reviewer: "perf-patterns", Verdict: VerdictApproved},
		{Reviewer: "tests-safety", Verdict: VerdictFailed},
	}

	result := mergeAuditReports(reports, reviewMergeManifest(), ReviewOpts{BeadID: "oro-audit"})
	var auditFeedback struct {
		Findings        []Finding `json:"findings"`
		CoveredSections []string  `json:"covered_sections"`
	}
	if err := json.Unmarshal([]byte(result.Feedback), &auditFeedback); err != nil {
		t.Fatalf("audit feedback is not structured JSON: %v\n%s", err, result.Feedback)
	}
	wantSections := []string{"code-quality", "perf-patterns", "dx-deps-docs"}
	if !reflect.DeepEqual(auditFeedback.CoveredSections, wantSections) {
		t.Fatalf("covered_sections = %#v, want %#v", auditFeedback.CoveredSections, wantSections)
	}

	normalResult := mergeReportResults(reports, reviewMergeManifest(), ReviewOpts{BeadID: "oro-review"})
	var normalFeedback map[string]json.RawMessage
	if err := json.Unmarshal([]byte(normalResult.Feedback), &normalFeedback); err != nil {
		t.Fatalf("normal review feedback is not structured JSON: %v\n%s", err, normalResult.Feedback)
	}
	if _, ok := normalFeedback["covered_sections"]; ok {
		t.Fatalf("normal review feedback gained covered_sections: %s", normalResult.Feedback)
	}

	failedReports := []ReviewReport{
		{Reviewer: "code-quality", Verdict: VerdictFailed},
		{Reviewer: "tests-safety", Verdict: VerdictFailed},
	}
	if !allReviewReportsFailed(failedReports) {
		t.Fatal("allReviewReportsFailed() = false, want true for zero successful reports")
	}
}

func TestAuditMergeAllFindings(t *testing.T) {
	survivor := reviewMergeFinding("pkg/ops/finding.go", 10, "survivor", SevImportant, 80, "code-quality")
	belowGate := reviewMergeFinding("pkg/ops/finding.go", 30, "below gate", SevMinor, 40, "code-quality")
	invalid := reviewMergeFinding("missing.go", 1, "invalid", SevCritical, 100, "code-quality")
	reports := []ReviewReport{{
		Reviewer: "code-quality", Verdict: VerdictRejected, Findings: []Finding{survivor, belowGate, invalid},
	}}

	result := mergeAuditReports(reports, reviewMergeManifest(), ReviewOpts{BeadID: "oro-audit"})
	var feedback struct {
		Findings    []Finding `json:"findings"`
		AllFindings []Finding `json:"all_findings"`
	}
	if err := json.Unmarshal([]byte(result.Feedback), &feedback); err != nil {
		t.Fatalf("parse audit feedback: %v\n%s", err, result.Feedback)
	}
	if got, want := reviewMergeTitles(feedback.Findings), []string{"survivor"}; !equalStrings(got, want) {
		t.Fatalf("survivors = %#v, want %#v", got, want)
	}
	if got, want := reviewMergeTitles(feedback.AllFindings), []string{"survivor", "below gate"}; !equalStrings(got, want) {
		t.Fatalf("all findings = %#v, want validated findings %#v", got, want)
	}
	if feedback.AllFindings[1].Status != "below_gate" {
		t.Fatalf("below-gate status = %q, want below_gate", feedback.AllFindings[1].Status)
	}

	normal := mergeReportResults(reports, reviewMergeManifest(), ReviewOpts{BeadID: "oro-review"})
	var normalFeedback map[string]json.RawMessage
	if err := json.Unmarshal([]byte(normal.Feedback), &normalFeedback); err != nil {
		t.Fatalf("parse normal feedback: %v", err)
	}
	if _, ok := normalFeedback["all_findings"]; ok {
		t.Fatalf("normal review feedback gained all_findings: %s", normal.Feedback)
	}
}

func TestAuditIgnoresModelFindingIDs(t *testing.T) {
	const beadID = "oro-audit"
	first := reviewMergeFinding("pkg/ops/finding.go", 10, "first distinct issue", SevImportant, 80, "code-quality")
	first.ID = "model-duplicate"
	second := reviewMergeFinding("pkg/ops/finding.go", 30, "second distinct issue", SevImportant, 80, "code-quality")
	second.ID = "model-duplicate"
	identical := first
	identical.ID = "another-model-id"
	identical.Sources = []string{"tests-safety"}

	result := mergeAuditReports([]ReviewReport{
		{Reviewer: "code-quality", Verdict: VerdictRejected, Findings: []Finding{first, second}},
		{Reviewer: "tests-safety", Verdict: VerdictRejected, Findings: []Finding{identical}},
	}, reviewMergeManifest(), ReviewOpts{BeadID: beadID})
	var feedback struct {
		Findings    []Finding `json:"findings"`
		AllFindings []Finding `json:"all_findings"`
	}
	if err := json.Unmarshal([]byte(result.Feedback), &feedback); err != nil {
		t.Fatalf("parse audit feedback: %v\n%s", err, result.Feedback)
	}
	if got, want := len(feedback.AllFindings), 2; got != want {
		t.Fatalf("all findings = %d, want %d distinct content findings: %#v", got, want, feedback.AllFindings)
	}
	if got, want := len(feedback.Findings), 2; got != want {
		t.Fatalf("survivors = %d, want %d distinct content findings: %#v", got, want, feedback.Findings)
	}

	ids := make(map[string]string, len(feedback.AllFindings))
	for _, finding := range feedback.AllFindings {
		wantID := FindingID(beadID, finding)
		if finding.ID != wantID {
			t.Errorf("finding %q id = %q, want trusted content id %q", finding.Title, finding.ID, wantID)
		}
		if priorTitle, duplicate := ids[finding.ID]; duplicate {
			t.Errorf("distinct findings %q and %q share id %q", priorTitle, finding.Title, finding.ID)
		}
		ids[finding.ID] = finding.Title
	}
	if got, want := feedback.AllFindings[0].Sources, []string{"code-quality", "tests-safety"}; !equalStrings(got, want) {
		t.Fatalf("identical finding sources = %#v, want merged sources %#v", got, want)
	}
}

func TestDedupAndUnionSources(t *testing.T) {
	reports := []ReviewReport{
		{
			Reviewer: "a",
			Verdict:  VerdictRejected,
			Findings: []Finding{
				reviewMergeFinding("pkg/ops/finding.go", 10, "Cache Key Drifts!", SevImportant, 75, "a"),
			},
		},
		{
			Reviewer: "b",
			Verdict:  VerdictRejected,
			Findings: []Finding{
				reviewMergeFinding("pkg/ops/finding.go", 13, "cache key drifts", SevImportant, 75, "b"),
			},
		},
	}

	result := mergeReportResults(reports, reviewMergeManifest(), ReviewOpts{BeadID: "oro-test"})
	findings := reviewMergeFeedbackFindings(t, result)

	if result.Verdict != VerdictRejected {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictRejected)
	}
	if len(findings) != 1 {
		t.Fatalf("findings len = %d, want 1: %#v", len(findings), findings)
	}
	if got, want := findings[0].Sources, []string{"a", "b"}; !equalStrings(got, want) {
		t.Fatalf("sources = %#v, want %#v", got, want)
	}
}

func TestPromote(t *testing.T) {
	reports := []ReviewReport{
		{
			Reviewer: "a",
			Verdict:  VerdictRejected,
			Findings: []Finding{
				reviewMergeFinding("pkg/ops/finding.go", 10, "shared confidence", SevImportant, 50, "a"),
			},
		},
		{
			Reviewer: "b",
			Verdict:  VerdictRejected,
			Findings: []Finding{
				reviewMergeFinding("pkg/ops/finding.go", 11, "shared confidence", SevImportant, 50, "b"),
			},
		},
	}

	result := mergeReportResults(reports, reviewMergeManifest(), ReviewOpts{BeadID: "oro-test"})
	findings := reviewMergeFeedbackFindings(t, result)

	if len(findings) != 1 {
		t.Fatalf("findings len = %d, want 1: %#v", len(findings), findings)
	}
	if findings[0].Confidence != 75 {
		t.Fatalf("confidence = %d, want promoted 75", findings[0].Confidence)
	}
}

func TestGateRunsLast(t *testing.T) {
	reports := []ReviewReport{
		{
			Reviewer: "a",
			Verdict:  VerdictRejected,
			Findings: []Finding{
				reviewMergeFinding("pkg/ops/finding.go", 10, "design confidence fifty", SevImportant, 50, "a"),
				reviewMergeFinding("pkg/ops/finding.go", 20, "critical confidence fifty", SevCritical, 50, "a"),
				reviewMergeFinding("pkg/ops/finding.go", 30, "promoted survivor", SevImportant, 50, "a"),
			},
		},
		{
			Reviewer: "b",
			Verdict:  VerdictRejected,
			Findings: []Finding{
				reviewMergeFinding("pkg/ops/finding.go", 32, "promoted survivor", SevImportant, 50, "b"),
			},
		},
	}

	result := mergeReportResults(reports, reviewMergeManifest(), ReviewOpts{BeadID: "oro-test"})
	findings := reviewMergeFeedbackFindings(t, result)

	if got, want := reviewMergeTitles(findings), []string{"critical confidence fifty", "promoted survivor"}; !equalStrings(got, want) {
		t.Fatalf("survivor titles = %#v, want %#v", got, want)
	}
	if findings[1].Confidence != 75 {
		t.Fatalf("promoted confidence = %d, want 75", findings[1].Confidence)
	}
}

func TestOnePersonaErrors_MergesSurvivors(t *testing.T) {
	reports := []ReviewReport{
		{
			Reviewer: "a",
			Verdict:  VerdictFailed,
			Raw:      "reviewer timed out",
		},
		{
			Reviewer: "b",
			Verdict:  VerdictRejected,
			Findings: []Finding{
				reviewMergeFinding("pkg/ops/finding.go", 10, "surviving finding", SevImportant, 75, "b"),
			},
		},
	}

	result := mergeReportResults(reports, reviewMergeManifest(), ReviewOpts{BeadID: "oro-test"})
	findings := reviewMergeFeedbackFindings(t, result)

	if result.Verdict != VerdictRejected {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictRejected)
	}
	if got, want := reviewMergeTitles(findings), []string{"surviving finding"}; !equalStrings(got, want) {
		t.Fatalf("survivor titles = %#v, want %#v", got, want)
	}
}

func TestCheapGate(t *testing.T) {
	tests := []struct {
		name       string
		finding    Finding
		wantStatus string
		wantCount  int
	}{
		{
			name: "score 50 single source advances",
			finding: Finding{
				Title:      "high enough",
				Confidence: 50,
				Sources:    []string{"correctness"},
			},
			wantCount: 1,
		},
		{
			name: "score 30 with two source families advances",
			finding: Finding{
				Title:          "cross sourced",
				Confidence:     30,
				SourceFamilies: []string{"correctness", "security"},
			},
			wantCount: 1,
		},
		{
			name: "score 30 single source is below gate",
			finding: Finding{
				Title:      "too weak",
				Confidence: 30,
				Sources:    []string{"correctness"},
			},
			wantStatus: "below_gate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := []Finding{tt.finding}

			survivors := cheapGate(candidates)

			if len(survivors) != tt.wantCount {
				t.Fatalf("survivors len = %d, want %d: %#v", len(survivors), tt.wantCount, survivors)
			}
			if tt.wantStatus != "" && candidates[0].Status != tt.wantStatus {
				t.Fatalf("candidate status = %q, want %q", candidates[0].Status, tt.wantStatus)
			}
		})
	}
}

func reviewMergeFinding(file string, line int, title string, severity Severity, confidence int, source string) Finding {
	return Finding{
		Severity:   severity,
		Category:   "correctness",
		Title:      title,
		Detail:     "detail",
		Evidence:   []Evidence{{File: file, LineStart: line, LineEnd: line}},
		Confidence: confidence,
		Sources:    []string{source},
		Origin:     "introduced",
	}
}

func reviewMergeManifest() PromptManifest {
	return PromptManifest{
		Shown: map[string][][2]int{
			"pkg/ops/finding.go": {{1, 200}},
		},
	}
}

func reviewMergeFeedbackFindings(t *testing.T, result Result) []Finding {
	t.Helper()
	var feedback struct {
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(result.Feedback), &feedback); err != nil {
		t.Fatalf("feedback is not structured JSON: %v\n%s", err, result.Feedback)
	}
	return feedback.Findings
}

func reviewMergeTitles(findings []Finding) []string {
	titles := make([]string, 0, len(findings))
	for _, finding := range findings {
		titles = append(titles, finding.Title)
	}
	return titles
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
