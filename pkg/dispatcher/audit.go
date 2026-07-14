package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
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
	resultFailureRecorded := false
	err := d.withScanWorktree(ctx, func(worktree string) error {
		result := <-d.ops.Audit(ctx, ops.AuditOpts{BeadID: roleBeadID, Worktree: worktree})
		d.handleAuditResultInWorktree(ctx, result, worktree)
		if result.Err != nil {
			resultFailureRecorded = true
			return fmt.Errorf("audit result: %w", result.Err)
		}
		if result.Verdict == ops.VerdictFailed {
			resultFailureRecorded = true
			return fmt.Errorf("audit result failed: %s", result.Feedback)
		}
		return nil
	})
	if err != nil && !resultFailureRecorded {
		d.appendAuditNote(ctx, roleBeadID, "audit_scan_failed", err.Error())
	}
	return err
}

func (d *Dispatcher) handleAuditResult(ctx context.Context, result ops.Result) {
	d.handleAuditResultInWorktree(ctx, result, d.repoRoot)
}

func (d *Dispatcher) handleAuditResultInWorktree(ctx context.Context, result ops.Result, worktree string) {
	if result.Err != nil || result.Verdict == ops.VerdictFailed {
		detail := auditFailureDetail(result)
		d.appendAuditNote(ctx, result.BeadID, "all_sections_failed", detail)
		_ = d.logEvent(ctx, "audit_failed", auditRoleActor, result.BeadID, "", detail)
		return
	}

	payload, err := parseAuditResult(result.Feedback)
	if err != nil {
		_ = d.logEvent(ctx, "audit_failed", "ops_audit", "", "", err.Error())
		return
	}
	roleBeadIDs, err := d.cleanlinessRoleBeadIDs(ctx, result.BeadID)
	if err != nil {
		_ = d.logEvent(ctx, "audit_suppression_failed", auditRoleActor, result.BeadID, "", err.Error())
		return
	}
	suppressed, err := d.deriveSuppressed(ctx, roleBeadIDs)
	if err != nil {
		_ = d.logEvent(ctx, "audit_suppression_failed", auditRoleActor, result.BeadID, "", err.Error())
		return
	}
	active, err := d.deriveActiveFindings(ctx, roleBeadIDs)
	if err != nil {
		_ = d.logEvent(ctx, "audit_suppression_failed", auditRoleActor, result.BeadID, "", err.Error())
		return
	}
	d.fileAuditFindingsInWorktree(ctx, result.BeadID, payload.Findings, active, suppressed, worktree)

	coveragePayload, err := auditCoveragePayload(payload.CoveredSections)
	if err != nil {
		_ = d.logEvent(ctx, "audit_coverage_failed", auditRoleActor, result.BeadID, "", err.Error())
		return
	}
	if err := d.appendAuditJourney(ctx, result.BeadID, "audit_coverage", coveragePayload); err != nil {
		_ = d.logEvent(ctx, "audit_coverage_failed", auditRoleActor, result.BeadID, "", err.Error())
		return
	}
	_ = d.logEvent(ctx, "audit_coverage", auditRoleActor, result.BeadID, "", coveragePayload)
}

func (d *Dispatcher) fileAuditFindings(
	ctx context.Context,
	roleBeadID string,
	findings, active, suppressed []ops.Finding,
) {
	d.fileAuditFindingsInWorktree(ctx, roleBeadID, findings, active, suppressed, d.repoRoot)
}

func (d *Dispatcher) fileAuditFindingsInWorktree(
	ctx context.Context,
	roleBeadID string,
	findings, active, suppressed []ops.Finding,
	worktree string,
) {
	for _, finding := range findings {
		if finding.ID == "" {
			continue
		}
		if err := d.appendAuditFinding(ctx, roleBeadID, finding); err != nil {
			_ = d.logEvent(ctx, "audit_finding_persist_failed", auditRoleActor, roleBeadID, "", err.Error())
			continue
		}
		if findingSuppressed(finding, active) || findingSuppressed(finding, suppressed) {
			continue
		}
		params, err := auditFindingCreateParams(finding, worktree)
		if err != nil {
			_ = d.logEvent(ctx, "audit_finding_acceptance_failed", auditRoleActor, roleBeadID, "", err.Error())
			continue
		}
		if _, err := d.beads.Create(ctx, params); err != nil {
			_ = d.logEvent(ctx, "audit_finding_create_failed", auditRoleActor, roleBeadID, "", err.Error())
			continue
		}
		_ = d.logEvent(ctx, "audit_finding_created", auditRoleActor, roleBeadID, "", finding.ID)
	}
}

type auditResultPayload struct {
	Findings        []ops.Finding `json:"findings"`
	CoveredSections []string      `json:"covered_sections"`
}

func parseAuditResult(feedback string) (auditResultPayload, error) {
	var payload auditResultPayload
	if err := json.Unmarshal([]byte(feedback), &payload); err != nil {
		return auditResultPayload{}, fmt.Errorf("parse audit findings: %w", err)
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
	if evidence.Quote != "" {
		if strings.ContainsAny(evidence.Quote, "\r\n") {
			return "", fmt.Errorf("evidence quote must be a single line")
		}
		return fmt.Sprintf("! grep -Fq -- %s %s", shellSingleQuote(evidence.Quote), shellSingleQuote(file)), nil
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
	cmd := exec.Command("git", "hash-object", "--", file) //nolint:gosec // fixed git subcommand with a normalized relative path.
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
