package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
)

const auditFindingMetadataKey = "meta_finding_id"

// runAudit executes the whole-repository audit in an isolated scan worktree.
// Failed section reports are recorded as audit events; audits never escalate.
func (d *Dispatcher) runAudit(ctx context.Context, roleBeadID string) error {
	err := d.withScanWorktree(ctx, func(worktree string) error {
		result := d.waitAuditResult(ctx, ops.AuditOpts{BeadID: roleBeadID, Worktree: worktree})
		return d.handleAuditResultInWorktree(ctx, result, worktree)
	})
	if err != nil {
		d.appendAuditNote(ctx, roleBeadID, "audit_cycle_failed", err.Error())
	}
	return err
}

func (d *Dispatcher) waitAuditResult(ctx context.Context, opts ops.AuditOpts) ops.Result {
	if d.auditResultFn != nil {
		return d.auditResultFn(ctx, opts)
	}
	return <-d.ops.Audit(ctx, opts)
}

func (d *Dispatcher) handleAuditResult(ctx context.Context, result ops.Result) error {
	return d.handleAuditResultInWorktree(ctx, result, d.repoRoot)
}

func (d *Dispatcher) handleAuditResultInWorktree(ctx context.Context, result ops.Result, worktree string) error {
	if result.Err != nil || result.Verdict == ops.VerdictFailed {
		detail := auditFailureDetail(result)
		_ = d.logEvent(ctx, "audit_failed", auditRoleActor, result.BeadID, "", detail)
		if result.Err != nil {
			return fmt.Errorf("audit result: %w", result.Err)
		}
		return fmt.Errorf("audit result failed: %s", detail)
	}

	payload, err := parseAuditResult(result.Feedback)
	if err != nil {
		_ = d.logEvent(ctx, "audit_failed", auditRoleActor, result.BeadID, "", err.Error())
		return err
	}
	roleBeadIDs, err := d.cleanlinessRoleBeadIDs(ctx, result.BeadID)
	if err != nil {
		_ = d.logEvent(ctx, "audit_suppression_failed", auditRoleActor, result.BeadID, "", err.Error())
		return err
	}
	suppressed, err := d.deriveSuppressed(ctx, roleBeadIDs)
	if err != nil {
		_ = d.logEvent(ctx, "audit_suppression_failed", auditRoleActor, result.BeadID, "", err.Error())
		return err
	}
	active, err := d.deriveActiveFindings(ctx, roleBeadIDs)
	if err != nil {
		_ = d.logEvent(ctx, "audit_suppression_failed", auditRoleActor, result.BeadID, "", err.Error())
		return err
	}
	if err := d.fileAuditFindingsInWorktree(
		ctx,
		result.BeadID,
		payload.AllFindings,
		payload.Findings,
		active,
		suppressed,
		worktree,
	); err != nil {
		return err
	}

	coveragePayload, err := auditCoveragePayload(payload.CoveredSections)
	if err != nil {
		_ = d.logEvent(ctx, "audit_coverage_failed", auditRoleActor, result.BeadID, "", err.Error())
		return err
	}
	if err := d.appendAuditJourney(ctx, result.BeadID, "audit_coverage", coveragePayload); err != nil {
		_ = d.logEvent(ctx, "audit_coverage_failed", auditRoleActor, result.BeadID, "", err.Error())
		return err
	}
	_ = d.logEvent(ctx, "audit_coverage", auditRoleActor, result.BeadID, "", coveragePayload)
	return nil
}

func (d *Dispatcher) fileAuditFindings(
	ctx context.Context,
	roleBeadID string,
	findings, active, suppressed []ops.Finding,
) error {
	return d.fileAuditFindingsInWorktree(ctx, roleBeadID, findings, findings, active, suppressed, d.repoRoot)
}

func (d *Dispatcher) fileAuditFindingsInWorktree(
	ctx context.Context,
	roleBeadID string,
	allFindings, survivors, active, suppressed []ops.Finding,
	worktree string,
) error {
	var errs []error
	persisted := make(map[string]bool, len(allFindings))
	for _, finding := range allFindings {
		if finding.ID == "" || persisted[finding.ID] {
			continue
		}
		if err := d.appendAuditFinding(ctx, roleBeadID, finding); err != nil {
			_ = d.logEvent(ctx, "audit_finding_persist_failed", auditRoleActor, roleBeadID, "", err.Error())
			errs = append(errs, fmt.Errorf("persist audit finding %s: %w", finding.ID, err))
			continue
		}
		persisted[finding.ID] = true
	}

	filed := make(map[string]bool, len(survivors))
	for _, finding := range survivors {
		if finding.ID == "" || filed[finding.ID] || !persisted[finding.ID] {
			continue
		}
		filed[finding.ID] = true
		if findingSuppressed(finding, active) || findingSuppressed(finding, suppressed) {
			continue
		}
		params, err := auditFindingCreateParams(finding, worktree)
		if err != nil {
			_ = d.logEvent(ctx, "audit_finding_acceptance_failed", auditRoleActor, roleBeadID, "", err.Error())
			errs = append(errs, fmt.Errorf("prepare audit finding %s: %w", finding.ID, err))
			continue
		}
		if _, err := d.beads.Create(ctx, params); err != nil {
			_ = d.logEvent(ctx, "audit_finding_create_failed", auditRoleActor, roleBeadID, "", err.Error())
			errs = append(errs, fmt.Errorf("create audit finding %s: %w", finding.ID, err))
			continue
		}
		_ = d.logEvent(ctx, "audit_finding_created", auditRoleActor, roleBeadID, "", finding.ID)
	}
	return errors.Join(errs...)
}

type auditResultPayload struct {
	Findings        []ops.Finding `json:"findings"`
	AllFindings     []ops.Finding `json:"all_findings,omitempty"`
	CoveredSections []string      `json:"covered_sections"`
}

func parseAuditResult(feedback string) (auditResultPayload, error) {
	var payload auditResultPayload
	if err := json.Unmarshal([]byte(feedback), &payload); err != nil {
		return auditResultPayload{}, fmt.Errorf("parse audit findings: %w", err)
	}
	if payload.AllFindings == nil {
		payload.AllFindings = append([]ops.Finding(nil), payload.Findings...)
	}
	return payload, nil
}

func (d *Dispatcher) appendAuditFinding(ctx context.Context, roleBeadID string, finding ops.Finding) error {
	payload, err := json.Marshal(finding)
	if err != nil {
		return fmt.Errorf("marshal audit finding: %w", err)
	}
	return d.appendAuditJourney(ctx, roleBeadID, "audit_finding", string(payload))
}

func (d *Dispatcher) appendAuditNote(ctx context.Context, roleBeadID, kind, detail string) {
	payload, err := json.Marshal(map[string]string{"kind": kind, "error": detail})
	if err != nil {
		return
	}
	if err := d.appendAuditJourney(ctx, roleBeadID, "note", string(payload)); err != nil {
		_ = d.logEvent(ctx, "audit_journey_append_failed", auditRoleActor, roleBeadID, "", err.Error())
	}
}

func (d *Dispatcher) appendAuditJourney(ctx context.Context, roleBeadID, event, payload string) error {
	if err := d.beads.AppendJourney(ctx, roleBeadID, beadstore.JourneyEvent{
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Actor:   auditRoleActor,
		Event:   event,
		Payload: payload,
	}); err != nil {
		return fmt.Errorf("append audit %s journey: %w", event, err)
	}
	return nil
}

func isWontFixReason(reason string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(reason)), "wont-fix")
}

func auditFindingCreateParams(finding ops.Finding, worktree string) (beadstore.CreateParams, error) {
	command, err := auditFindingAcceptanceCommand(finding, worktree)
	if err != nil {
		return beadstore.CreateParams{}, err
	}
	return beadstore.CreateParams{
		Title:              finding.Title,
		Type:               "task",
		Priority:           auditFindingPriority(finding.Severity),
		Description:        auditFindingDescription(finding),
		AcceptanceCriteria: fmt.Sprintf("Test: audit finding %s\nCmd: %s\nAssert: audit finding %s is resolved and the quality gate passes", finding.ID, command, finding.ID),
		Metadata:           map[string]string{auditFindingMetadataKey: finding.ID},
	}, nil
}

func auditFindingAcceptanceCommand(finding ops.Finding, worktree string) (string, error) {
	if len(finding.Evidence) == 0 {
		return "", fmt.Errorf("audit finding %s has no evidence", finding.ID)
	}
	unique := make(map[string]struct{}, len(finding.Evidence))
	for _, evidence := range finding.Evidence {
		check, err := auditEvidenceAcceptanceCheck(evidence, worktree)
		if err != nil {
			return "", fmt.Errorf("audit finding %s evidence: %w", finding.ID, err)
		}
		unique[check] = struct{}{}
	}
	checks := make([]string, 0, len(unique))
	for check := range unique {
		checks = append(checks, check)
	}
	sort.Strings(checks)
	return strings.Join(append(checks, "./scripts/quality_gate.sh"), " && "), nil
}

func auditEvidenceAcceptanceCheck(evidence ops.Evidence, worktree string) (string, error) {
	file, err := normalizeAuditEvidencePath(evidence.File)
	if err != nil {
		return "", err
	}
	if err := validateAuditEvidenceRange(evidence); err != nil {
		return "", err
	}
	if evidence.Quote != "" {
		if strings.ContainsAny(evidence.Quote, "\r\n") {
			return "", fmt.Errorf("evidence quote must be a single line")
		}
		return fmt.Sprintf("! grep -Fq -- %s %s", shellSingleQuote(evidence.Quote), shellSingleQuote(file)), nil
	}
	if evidence.LineStart > 0 {
		baseline, rangeErr := auditEvidenceRangeHash(worktree, file, evidence.LineStart, evidence.LineEnd)
		if rangeErr != nil {
			return "", rangeErr
		}
		sedRange := fmt.Sprintf("%d,%dp", evidence.LineStart, evidence.LineEnd)
		return fmt.Sprintf(
			"test \"$(sed -n %s < %s 2>/dev/null | git hash-object --stdin)\" != %s",
			shellSingleQuote(sedRange),
			shellSingleQuote(file),
			shellSingleQuote(baseline),
		), nil
	}
	baseline, err := auditEvidenceFileHash(worktree, file)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"test \"$(git hash-object -- %s 2>/dev/null)\" != %s",
		shellSingleQuote(file),
		shellSingleQuote(baseline),
	), nil
}

func validateAuditEvidenceRange(evidence ops.Evidence) error {
	if evidence.LineStart == 0 && evidence.LineEnd == 0 {
		if evidence.Quote != "" {
			return fmt.Errorf("quoted evidence requires a positive line range")
		}
		return nil
	}
	if evidence.LineStart <= 0 || evidence.LineEnd < evidence.LineStart {
		return fmt.Errorf("invalid evidence line range: %d-%d", evidence.LineStart, evidence.LineEnd)
	}
	return nil
}

func auditEvidenceRangeHash(worktree, file string, start, end int) (string, error) {
	data, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(file))) //nolint:gosec // file is normalized and worktree-relative.
	if err != nil {
		return "", fmt.Errorf("read line evidence %s: %w", file, err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if start > len(lines) || end > len(lines) {
		return "", fmt.Errorf("line evidence outside file %s: %d-%d", file, start, end)
	}
	cmd := exec.CommandContext(context.Background(), "git", "hash-object", "--stdin") //nolint:gosec // fixed git hash command.
	cmd.Dir = worktree
	cmd.Stdin = bytes.NewBufferString(strings.Join(lines[start-1:end], ""))
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("hash line evidence %s:%d-%d: %w", file, start, end, err)
	}
	return strings.TrimSpace(string(output)), nil
}

func normalizeAuditEvidencePath(path string) (string, error) {
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return "", fmt.Errorf("evidence path is empty or contains a newline")
	}
	local := filepath.FromSlash(path)
	if filepath.IsAbs(local) {
		return "", fmt.Errorf("evidence path must be relative: %s", path)
	}
	clean := filepath.ToSlash(filepath.Clean(local))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("evidence path escapes repository: %s", path)
	}
	return clean, nil
}

func auditEvidenceFileHash(worktree, file string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", "hash-object", "--", file) //nolint:gosec // fixed git subcommand with a normalized relative path.
	cmd.Dir = worktree
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("hash file-only evidence %s: %w", file, err)
	}
	baseline := strings.TrimSpace(string(output))
	if baseline == "" {
		return "", fmt.Errorf("hash file-only evidence %s: empty hash", file)
	}
	return baseline, nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func auditFindingDescription(finding ops.Finding) string {
	return strings.TrimSpace(fmt.Sprintf(`%s

Suppression contract: close with a reason beginning "wont-fix:" to mark this finding intentional and prevent refiling. The first close reason is immutable; reopen this bead before closing again to change that reason.`, finding.Detail))
}

func auditFindingPriority(severity ops.Severity) int {
	switch severity {
	case ops.SevCritical:
		return 0
	case ops.SevImportant:
		return 1
	default:
		return 2
	}
}

func auditFailureDetail(result ops.Result) string {
	if result.Err != nil {
		return result.Err.Error()
	}
	return result.Feedback
}

func auditCoveragePayload(reported []string) (string, error) {
	reportedSet := make(map[string]bool, len(reported))
	for _, section := range reported {
		reportedSet[section] = true
	}
	covered := make([]string, 0, len(reportedSet))
	notCovered := make([]string, 0, len(ops.AuditSectionIDs())+4)
	for _, section := range ops.AuditSectionIDs() {
		if reportedSet[section] {
			covered = append(covered, section)
			continue
		}
		notCovered = append(notCovered, section)
	}
	notCovered = append(notCovered,
		"product-correctness-live",
		"reliability-injection",
		"integrations-live",
		"deploy-observability",
	)
	payload, err := json.Marshal(struct {
		CoveredSections []string `json:"covered_sections"`
		NotCovered      []string `json:"not_covered"`
	}{
		CoveredSections: covered,
		NotCovered:      notCovered,
	})
	if err != nil {
		return "", fmt.Errorf("marshal audit coverage: %w", err)
	}
	return string(payload), nil
}
