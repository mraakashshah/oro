package ops

import (
	"encoding/json"
	"testing"
)

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

	result := mergeReports(reports, reviewMergeManifest(), ReviewOpts{BeadID: "oro-test"})
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

	result := mergeReports(reports, reviewMergeManifest(), ReviewOpts{BeadID: "oro-test"})
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

	result := mergeReports(reports, reviewMergeManifest(), ReviewOpts{BeadID: "oro-test"})
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

	result := mergeReports(reports, reviewMergeManifest(), ReviewOpts{BeadID: "oro-test"})
	findings := reviewMergeFeedbackFindings(t, result)

	if result.Verdict != VerdictRejected {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictRejected)
	}
	if got, want := reviewMergeTitles(findings), []string{"surviving finding"}; !equalStrings(got, want) {
		t.Fatalf("survivor titles = %#v, want %#v", got, want)
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
