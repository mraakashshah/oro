package ops

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parseReviewReport(raw string) (ReviewReport, []DroppedFinding) {
	outcome, err := parseStructuredReviewReport(raw)
	if err == nil {
		return reviewReportFromOutcome(outcome), nil
	}
	report, dropped, err := parseLegacyStructuredReviewReport(raw)
	if err == nil {
		return report, dropped
	}
	verdict, feedback := parseReviewOutput(raw)
	return ReviewReport{Verdict: verdict, Raw: feedback}, nil
}

// parseStructuredReviewReport decodes a typed review outcome and reduces its
// decision from validated findings, blockers, and process execution.
func parseStructuredReviewReport(raw string) (ReviewOutcome, error) {
	text := reviewOutputText(raw)
	block, ok := reviewJSONBlock(text)
	if !ok {
		return ReviewOutcome{}, fmt.Errorf("structured review JSON is required")
	}

	var outcome ReviewOutcome
	if err := json.Unmarshal([]byte(block), &outcome); err != nil {
		return ReviewOutcome{}, fmt.Errorf("parse typed review outcome: %w", err)
	}
	outcome.Decision = reduceReviewDecision(outcome)
	if err := ValidateReviewOutcome(outcome); err != nil {
		return ReviewOutcome{}, err
	}
	return outcome, nil
}

func reviewReportFromOutcome(outcome ReviewOutcome) ReviewReport {
	verdict := VerdictFailed
	switch outcome.Decision {
	case ReviewApproved:
		verdict = VerdictApproved
	case ReviewRejected:
		verdict = VerdictRejected
	case ReviewBlocked, ReviewFailed:
		verdict = VerdictFailed
	default:
		verdict = VerdictFailed
	}
	return ReviewReport{Findings: outcome.Findings, Verdict: verdict, Raw: outcome.Summary}
}

type reviewReportEnvelope struct {
	Reviewer string            `json:"reviewer"`
	Findings []json.RawMessage `json:"findings"`
	Verdict  Verdict           `json:"verdict"`
}

func parseLegacyStructuredReviewReport(raw string) (ReviewReport, []DroppedFinding, error) {
	text := reviewOutputText(raw)
	block, ok := reviewJSONBlock(text)
	if !ok {
		return ReviewReport{}, nil, fmt.Errorf("structured review JSON is required")
	}
	return parseReviewReportLayered(block, text)
}

func parseReviewReportLayered(raw, text string) (ReviewReport, []DroppedFinding, error) {
	var envelope reviewReportEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return ReviewReport{}, nil, fmt.Errorf("parse review report envelope: %w", err)
	}
	if err := validateStructuredReviewVerdict(envelope.Verdict); err != nil {
		return ReviewReport{}, nil, err
	}

	report := ReviewReport{
		Reviewer: envelope.Reviewer,
		Verdict:  envelope.Verdict,
	}
	var dropped []DroppedFinding
	for _, item := range envelope.Findings {
		var finding Finding
		if err := json.Unmarshal(item, &finding); err != nil {
			dropped = append(dropped, DroppedFinding{
				Layer:  "schema",
				Reason: err.Error(),
			})
			continue
		}
		report.Findings = append(report.Findings, finding)
	}
	report.Raw = text
	return report, dropped, nil
}

func validateStructuredReviewVerdict(verdict Verdict) error {
	switch verdict {
	case VerdictApproved, VerdictRejected, VerdictFailed:
		return nil
	default:
		return fmt.Errorf("invalid structured review verdict %q", verdict)
	}
}

func reviewJSONBlock(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") {
		return trimmed, true
	}

	start := strings.Index(trimmed, "```json")
	if start < 0 {
		start = strings.Index(trimmed, "```")
	}
	if start < 0 {
		return "", false
	}
	blockStart := strings.Index(trimmed[start:], "\n")
	if blockStart < 0 {
		return "", false
	}
	contentStart := start + blockStart + 1
	end := strings.Index(trimmed[contentStart:], "```")
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(trimmed[contentStart : contentStart+end]), true
}
