package ops

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parseReviewReport(raw string) (ReviewReport, []DroppedFinding) {
	report, dropped, err := parseStructuredReviewReport(raw)
	if err == nil {
		return report, dropped
	}
	verdict, feedback := parseReviewOutput(raw)
	return ReviewReport{Verdict: verdict, Raw: feedback}, nil
}

func parseStructuredReviewReport(raw string) (ReviewReport, []DroppedFinding, error) {
	text := reviewOutputText(raw)
	block, ok := reviewJSONBlock(text)
	if !ok {
		return ReviewReport{}, nil, fmt.Errorf("structured review JSON is required")
	}

	var report ReviewReport
	if err := json.Unmarshal([]byte(block), &report); err == nil {
		if err := validateStructuredReviewVerdict(report.Verdict); err != nil {
			return ReviewReport{}, nil, err
		}
		report.Raw = text
		return report, nil, nil
	}

	report, dropped, err := parseReviewReportLayered(block)
	if err != nil {
		return ReviewReport{}, nil, err
	}
	if err := validateStructuredReviewVerdict(report.Verdict); err != nil {
		return ReviewReport{}, nil, err
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

type reviewReportEnvelope struct {
	Reviewer string            `json:"reviewer"`
	Findings []json.RawMessage `json:"findings"`
	Verdict  Verdict           `json:"verdict"`
}

func parseReviewReportLayered(raw string) (ReviewReport, []DroppedFinding, error) {
	var envelope reviewReportEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return ReviewReport{}, nil, fmt.Errorf("parse review report envelope: %w", err)
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
	return report, dropped, nil
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
