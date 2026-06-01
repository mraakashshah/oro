package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"oro/pkg/beadstore"
)

const cheapGateScore = 45

type reviewMergeFeedback struct {
	Findings []Finding `json:"findings"`
}

func mergeReports(reports []ReviewReport, m PromptManifest, opts ReviewOpts) Result {
	findings := flattenReviewReports(reports)
	findings, _ = PartitionFindings(m, opts.Worktree, findings)
	priorFindings, err := priorReviewFindings(context.Background(), opts)
	if err != nil {
		return Result{
			Type:     OpsReview,
			BeadID:   opts.BeadID,
			Verdict:  VerdictFailed,
			Feedback: err.Error(),
			Err:      err,
		}
	}

	groups := dedupFindings(findings)
	merged := make([]Finding, 0, len(groups))
	for _, group := range groups {
		finding := mergeFindingGroup(group, opts.BeadID)
		if prior, ok := priorFindings[finding.ID]; ok {
			finding = mergeFinding(prior, finding)
		}
		if finding.Origin == "pre_existing" {
			continue
		}
		merged = append(merged, finding)
	}

	promoteFindings(merged)
	if err := persistReviewFindings(context.Background(), opts, merged); err != nil {
		return Result{
			Type:     OpsReview,
			BeadID:   opts.BeadID,
			Verdict:  VerdictFailed,
			Feedback: err.Error(),
			Err:      err,
		}
	}
	survivors := gateFindings(merged)
	feedback, err := json.Marshal(reviewMergeFeedback{Findings: survivors})
	if err != nil {
		return Result{
			Type:     OpsReview,
			BeadID:   opts.BeadID,
			Verdict:  VerdictFailed,
			Feedback: err.Error(),
			Err:      err,
		}
	}

	return Result{
		Type:     OpsReview,
		BeadID:   opts.BeadID,
		Verdict:  verdictForFindings(survivors),
		Feedback: string(feedback),
	}
}

func priorReviewFindings(ctx context.Context, opts ReviewOpts) (map[string]Finding, error) {
	if opts.BeadStore == nil {
		return nil, nil
	}
	events, err := opts.BeadStore.Journey(ctx, opts.BeadID, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("load prior review findings: %w", err)
	}
	prior := make(map[string]Finding)
	for _, event := range events {
		if event.Actor != "ops_review" || event.Event != "review_finding" || event.Payload == "" {
			continue
		}
		var finding Finding
		if err := json.Unmarshal([]byte(event.Payload), &finding); err != nil {
			return nil, fmt.Errorf("parse prior review finding: %w", err)
		}
		if finding.ID != "" {
			prior[finding.ID] = finding
		}
	}
	return prior, nil
}

func persistReviewFindings(ctx context.Context, opts ReviewOpts, findings []Finding) error {
	if !opts.PersistFindings || opts.BeadStore == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, finding := range findings {
		payload, err := json.Marshal(finding)
		if err != nil {
			return fmt.Errorf("marshal review finding: %w", err)
		}
		err = opts.BeadStore.AppendJourney(ctx, opts.BeadID, beadstore.JourneyEvent{
			Ts:      now,
			Actor:   "ops_review",
			Event:   "review_finding",
			Payload: string(payload),
		})
		if err != nil {
			return fmt.Errorf("append review finding journey: %w", err)
		}
	}
	return nil
}

func flattenReviewReports(reports []ReviewReport) []Finding {
	var findings []Finding
	for _, report := range reports {
		if report.Verdict == VerdictFailed {
			continue
		}
		for _, finding := range report.Findings {
			if len(finding.Sources) == 0 && report.Reviewer != "" {
				finding.Sources = []string{report.Reviewer}
			}
			findings = append(findings, finding)
		}
	}
	return findings
}

func dedupFindings(findings []Finding) [][]Finding {
	groups := make([][]Finding, 0, len(findings))
	for _, finding := range findings {
		index := matchingFindingGroup(groups, finding)
		if index == -1 {
			groups = append(groups, []Finding{finding})
			continue
		}
		groups[index] = append(groups[index], finding)
	}
	return groups
}

func matchingFindingGroup(groups [][]Finding, finding Finding) int {
	for i, group := range groups {
		for _, existing := range group {
			if sameFindingBucket(existing, finding) {
				return i
			}
		}
	}
	return -1
}

func sameFindingBucket(a, b Finding) bool {
	aEvidence, aOK := primaryEvidence(a)
	bEvidence, bOK := primaryEvidence(b)
	if !aOK || !bOK {
		return normalizeTitle(a.Title) == normalizeTitle(b.Title)
	}
	return aEvidence.File == bEvidence.File &&
		lineDistance(aEvidence.LineStart, bEvidence.LineStart) <= 3 &&
		normalizeTitle(a.Title) == normalizeTitle(b.Title)
}

func primaryEvidence(f Finding) (Evidence, bool) {
	if len(f.Evidence) == 0 {
		return Evidence{}, false
	}
	return f.Evidence[0], true
}

func lineDistance(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

func mergeFindingGroup(group []Finding, beadID string) Finding {
	merged := group[0]
	for _, finding := range group[1:] {
		if finding.Confidence > merged.Confidence {
			merged.Confidence = finding.Confidence
		}
		if severityRank(finding.Severity) > severityRank(merged.Severity) {
			merged.Severity = finding.Severity
		}
	}
	merged.Sources = unionFindingSources(group)
	if merged.ID == "" {
		merged.ID = FindingID(beadID, merged)
	}
	return merged
}

func mergeFinding(prior, incoming Finding) Finding {
	merged := incoming
	merged.Status = prior.Status
	merged.History = append([]FindingHistoryEntry(nil), prior.History...)
	return merged
}

func unionFindingSources(group []Finding) []string {
	seen := make(map[string]struct{})
	for _, finding := range group {
		for _, source := range finding.Sources {
			if source != "" {
				seen[source] = struct{}{}
			}
		}
	}
	sources := make([]string, 0, len(seen))
	for source := range seen {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	return sources
}

func promoteFindings(findings []Finding) {
	for i := range findings {
		if len(findings[i].Sources) >= 2 && findings[i].Confidence < 100 {
			findings[i].Confidence += 25
		}
	}
}

func gateFindings(findings []Finding) []Finding {
	survivors := make([]Finding, 0, len(findings))
	for _, finding := range findings {
		if !findingBlocksGate(finding) {
			continue
		}
		if finding.Confidence >= 75 || finding.Severity == SevCritical && finding.Confidence >= 50 {
			survivors = append(survivors, finding)
		}
	}
	return survivors
}

func findingBlocksGate(finding Finding) bool {
	switch finding.Status {
	case "", "open", "uncertain":
		return true
	case "false-positive", "fixed", "wont-fix":
		return false
	default:
		return true
	}
}

func cheapGate(candidates []Finding) (survivors []Finding) {
	for i := range candidates {
		if candidates[i].Confidence >= cheapGateScore || sourceFamilyCount(candidates[i]) >= 2 {
			survivors = append(survivors, candidates[i])
			continue
		}
		candidates[i].Status = "below_gate"
	}
	return survivors
}

func sourceFamilyCount(finding Finding) int {
	seen := make(map[string]struct{})
	for _, source := range finding.Sources {
		family := sourceFamily(source)
		if family != "" {
			seen[family] = struct{}{}
		}
	}
	for _, family := range finding.SourceFamilies {
		family = strings.TrimSpace(family)
		if family != "" {
			seen[family] = struct{}{}
		}
	}
	return len(seen)
}

func sourceFamily(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if family, _, ok := strings.Cut(source, ":"); ok {
		return strings.TrimSpace(family)
	}
	return source
}

func verdictForFindings(findings []Finding) Verdict {
	for _, finding := range findings {
		if finding.Severity == SevCritical || finding.Severity == SevImportant {
			return VerdictRejected
		}
	}
	return VerdictApproved
}

func severityRank(severity Severity) int {
	switch severity {
	case SevCritical:
		return 3
	case SevImportant:
		return 2
	case SevMinor:
		return 1
	default:
		return 0
	}
}
