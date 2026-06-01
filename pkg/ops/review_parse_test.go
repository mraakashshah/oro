package ops

import "testing"

func TestTwoLayerParse(t *testing.T) {
	t.Run("whole_doc_valid_json_keeps_all_findings", func(t *testing.T) {
		raw := `{
			"reviewer": "ops_review",
			"verdict": "rejected",
			"findings": [
				{
					"severity": "critical",
					"category": "correctness",
					"title": "first",
					"detail": "first detail",
					"evidence": [{"file": "pkg/ops/ops.go", "line_start": 1, "line_end": 1}],
					"confidence": 90,
					"sources": ["review"],
					"origin": "ops_review"
				},
				{
					"severity": "minor",
					"category": "tests",
					"title": "second",
					"detail": "second detail",
					"evidence": [{"file": "pkg/ops/ops.go", "line_start": 2, "line_end": 2}],
					"confidence": 60,
					"sources": ["review"],
					"origin": "ops_review"
				}
			]
		}`

		report, dropped := parseReviewReport(raw)

		if len(dropped) != 0 {
			t.Fatalf("dropped = %#v, want none", dropped)
		}
		if report.Verdict != VerdictRejected {
			t.Fatalf("verdict = %q, want %q", report.Verdict, VerdictRejected)
		}
		if len(report.Findings) != 2 {
			t.Fatalf("findings len = %d, want 2: %#v", len(report.Findings), report.Findings)
		}
		if report.Findings[0].Title != "first" || report.Findings[1].Title != "second" {
			t.Fatalf("findings = %#v, want first and second preserved", report.Findings)
		}
	})

	t.Run("malformed_finding_element_dropped_siblings_kept", func(t *testing.T) {
		raw := `{
			"reviewer": "ops_review",
			"verdict": "rejected",
			"findings": [
				{
					"severity": "critical",
					"category": "correctness",
					"title": "kept before",
					"detail": "first detail",
					"evidence": [{"file": "pkg/ops/ops.go", "line_start": 1, "line_end": 1}],
					"confidence": 90,
					"sources": ["review"],
					"origin": "ops_review"
				},
				{
					"severity": "important",
					"category": "correctness",
					"title": 17,
					"detail": "bad title type",
					"evidence": [{"file": "pkg/ops/ops.go", "line_start": 2, "line_end": 2}],
					"confidence": 80,
					"sources": ["review"],
					"origin": "ops_review"
				},
				{
					"severity": "minor",
					"category": "tests",
					"title": "kept after",
					"detail": "second detail",
					"evidence": [{"file": "pkg/ops/ops.go", "line_start": 3, "line_end": 3}],
					"confidence": 60,
					"sources": ["review"],
					"origin": "ops_review"
				}
			]
		}`

		report, dropped := parseReviewReport(raw)

		if report.Verdict != VerdictRejected {
			t.Fatalf("verdict = %q, want %q", report.Verdict, VerdictRejected)
		}
		if len(report.Findings) != 2 {
			t.Fatalf("findings len = %d, want 2: %#v", len(report.Findings), report.Findings)
		}
		if report.Findings[0].Title != "kept before" || report.Findings[1].Title != "kept after" {
			t.Fatalf("findings = %#v, want malformed middle element dropped", report.Findings)
		}
		if len(dropped) != 1 {
			t.Fatalf("dropped len = %d, want 1: %#v", len(dropped), dropped)
		}
		if dropped[0].Layer != "schema" {
			t.Fatalf("dropped layer = %q, want schema", dropped[0].Layer)
		}
	})

	t.Run("no_json_block_falls_back_to_legacy_verdict", func(t *testing.T) {
		raw := "Looks good.\n\nVERDICT: APPROVED\n"

		report, dropped := parseReviewReport(raw)

		if len(dropped) != 0 {
			t.Fatalf("dropped = %#v, want none", dropped)
		}
		if report.Verdict != VerdictApproved {
			t.Fatalf("verdict = %q, want %q", report.Verdict, VerdictApproved)
		}
		if report.Raw != "Looks good.\n\nVERDICT: APPROVED" {
			t.Fatalf("raw feedback = %q, want legacy fallback feedback", report.Raw)
		}
		if len(report.Findings) != 0 {
			t.Fatalf("findings = %#v, want none", report.Findings)
		}
	})
}
