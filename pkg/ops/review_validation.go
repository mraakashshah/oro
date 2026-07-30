package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"oro/pkg/reviewcontract"
)

// PromptManifest records the file line ranges included in a review prompt.
type PromptManifest struct {
	Shown map[string][][2]int
}

// DroppedFinding records a structured finding discarded before gating.
type DroppedFinding struct {
	Finding Finding
	Layer   string
	Reason  string
}

// ValidateReviewOutcome rejects incomplete or internally inconsistent typed
// review results. Callers must not classify prose or malformed JSON as an
// approved review.
func ValidateReviewOutcome(out ReviewOutcome) error {
	if err := validateReviewOutcomeSchema(out); err != nil {
		return err
	}
	if out.Decision != reduceReviewDecision(out) {
		return fmt.Errorf("review decision %q conflicts with typed outcome", out.Decision)
	}
	return nil
}

func validateReviewOutcomeSchema(out ReviewOutcome) error {
	if err := validateReviewDecision(out.Decision); err != nil {
		return err
	}
	if strings.TrimSpace(out.Summary) == "" {
		return fmt.Errorf("review summary is required")
	}
	if strings.TrimSpace(out.Artifact.SHA256) == "" {
		return fmt.Errorf("artifact sha256 is required")
	}
	if out.Artifact.Bytes < 0 {
		return fmt.Errorf("artifact bytes must not be negative")
	}
	if err := validateReviewVerification(out.Verification); err != nil {
		return err
	}
	if err := validateReviewExecution(out.Execution); err != nil {
		return err
	}
	for i, blocker := range out.Blockers {
		if err := validateReviewBlocker(blocker); err != nil {
			return fmt.Errorf("blocker %d: %w", i, err)
		}
	}
	for i, finding := range out.Findings {
		if err := validateOutcomeFinding(finding); err != nil {
			return fmt.Errorf("finding %d: %w", i, err)
		}
	}
	return nil
}

func validateReviewDecision(decision ReviewDecision) error {
	switch decision {
	case ReviewApproved, ReviewRejected, ReviewBlocked, ReviewFailed:
		return nil
	default:
		return fmt.Errorf("invalid review decision %q", decision)
	}
}

func validateReviewVerification(verification ReviewVerification) error {
	switch verification.AcceptanceStatus {
	case "passed", "failed", "not_run", "blocked":
		return nil
	default:
		return fmt.Errorf("invalid acceptance status %q", verification.AcceptanceStatus)
	}
}

func validateReviewExecution(execution ReviewExecution) error {
	switch execution.Kind {
	case ReviewExecSucceeded, ReviewExecSpawnError, ReviewExecExitError, ReviewExecTimeout, ReviewExecIdle, ReviewExecCancelled:
		return nil
	default:
		return fmt.Errorf("invalid review execution kind %q", execution.Kind)
	}
}

func validateReviewBlocker(blocker ReviewBlocker) error {
	if blocker.Class != "environment" && blocker.Class != "infrastructure" {
		return fmt.Errorf("invalid blocker class %q", blocker.Class)
	}
	switch blocker.Scope {
	case "acceptance", "broader_verification", "runtime":
	default:
		return fmt.Errorf("invalid blocker scope %q", blocker.Scope)
	}
	if strings.TrimSpace(blocker.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	return nil
}

func validateOutcomeFinding(finding Finding) error {
	requiredFields := [...]string{
		finding.ID,
		finding.Category,
		finding.Title,
		finding.Detail,
		finding.Origin,
		finding.RequiredAction,
	}
	for _, field := range requiredFields {
		if strings.TrimSpace(field) == "" {
			return fmt.Errorf("required finding field is empty")
		}
	}
	if finding.Confidence < 0 || finding.Confidence > 100 {
		return fmt.Errorf("confidence must be between 0 and 100")
	}
	if len(finding.Evidence) == 0 || len(finding.Sources) == 0 {
		return fmt.Errorf("evidence and sources are required")
	}
	switch finding.Severity {
	case reviewcontract.SevCritical, reviewcontract.SevImportant, reviewcontract.SevMinor:
	default:
		return fmt.Errorf("invalid severity %q", finding.Severity)
	}
	switch finding.ContractImpact {
	case reviewcontract.ContractImplementationFix, reviewcontract.ContractAcceptanceGap:
	default:
		return fmt.Errorf("invalid contract impact %q", finding.ContractImpact)
	}
	for _, evidence := range finding.Evidence {
		if strings.TrimSpace(evidence.File) == "" || evidence.LineStart <= 0 || evidence.LineEnd < evidence.LineStart {
			return fmt.Errorf("invalid evidence")
		}
	}
	return nil
}

func reduceReviewDecision(out ReviewOutcome) ReviewDecision {
	for _, finding := range out.Findings {
		if finding.Severity == reviewcontract.SevCritical || finding.Severity == reviewcontract.SevImportant {
			return ReviewRejected
		}
	}
	if out.Execution.Kind != ReviewExecSucceeded || !out.Execution.Complete || out.Execution.ExitCode != 0 {
		return ReviewFailed
	}
	if len(out.Blockers) > 0 {
		return ReviewBlocked
	}
	if out.Decision == ReviewApproved {
		return ReviewApproved
	}
	return ReviewFailed
}

// ValidateFinding rejects evidence that was not shown in the prompt manifest.
//
//oro:testonly — wired into production by subsequent structured-review phases.
func ValidateFinding(m PromptManifest, repoRoot string, f Finding) error {
	for _, ev := range f.Evidence {
		if err := validateEvidence(m, repoRoot, ev); err != nil {
			return err
		}
	}
	return nil
}

// PartitionFindings keeps valid findings and drops invalid ones with validation metadata.
//
//oro:testonly — wired into production by subsequent structured-review phases.
func PartitionFindings(m PromptManifest, repoRoot string, in []Finding) (kept []Finding, dropped []DroppedFinding) {
	for _, f := range in {
		if err := ValidateFinding(m, repoRoot, f); err != nil {
			dropped = append(dropped, DroppedFinding{
				Finding: f,
				Layer:   "validation",
				Reason:  err.Error(),
			})
			continue
		}
		kept = append(kept, f)
	}
	return kept, dropped
}

func validateEvidence(m PromptManifest, repoRoot string, ev Evidence) error {
	file, err := normalizeManifestPath(ev.File)
	if err != nil {
		return err
	}
	ranges, ok := m.Shown[file]
	if !ok {
		return fmt.Errorf("evidence path not in manifest: %s", file)
	}
	if ev.LineStart == 0 && ev.Quote == "" {
		return nil
	}
	if !rangeShown(ranges, ev.LineStart, ev.LineEnd) {
		return fmt.Errorf("evidence lines outside manifest range: %s:%d-%d", file, ev.LineStart, ev.LineEnd)
	}
	if ev.Quote == "" {
		return nil
	}
	return validateLiteralQuote(repoRoot, file, ev)
}

func normalizeManifestPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("evidence path is empty")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("evidence path must be relative: %s", path)
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("evidence path escapes manifest: %s", path)
	}
	return clean, nil
}

func rangeShown(ranges [][2]int, start, end int) bool {
	if start <= 0 || end < start {
		return false
	}
	for _, r := range ranges {
		if start >= r[0] && end <= r[1] {
			return true
		}
	}
	return false
}

func validateLiteralQuote(repoRoot, file string, ev Evidence) error {
	text, err := evidenceLineText(repoRoot, file, ev.LineStart, ev.LineEnd)
	if err != nil {
		return err
	}
	if !strings.Contains(text, ev.Quote) {
		return fmt.Errorf("evidence quote not literal: %s:%d-%d", file, ev.LineStart, ev.LineEnd)
	}
	return nil
}

func evidenceLineText(repoRoot, file string, start, end int) (string, error) {
	path := filepath.Join(repoRoot, filepath.FromSlash(file))
	cleanRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repo root: %w", err)
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve evidence path: %w", err)
	}
	if cleanPath != cleanRoot && !strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("evidence path escapes repo root: %s", file)
	}

	data, err := os.ReadFile(cleanPath) //nolint:gosec // path was normalized against repoRoot and manifest-relative input.
	if err != nil {
		return "", fmt.Errorf("read evidence file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	if start > len(lines) {
		return "", fmt.Errorf("evidence line outside file: %s:%d", file, start)
	}
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start-1:end], "\n"), nil
}
