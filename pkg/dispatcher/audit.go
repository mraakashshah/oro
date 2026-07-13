package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
)

const auditFindingMetadataKey = "meta_finding_id"

// runAudit executes the whole-repository audit in an isolated scan worktree.
// Failed section reports are recorded as audit events; audits never escalate.
func (d *Dispatcher) runAudit(ctx context.Context, roleBeadID string) error {
	return d.withScanWorktree(ctx, func(worktree string) error {
		result := <-d.ops.Audit(ctx, ops.AuditOpts{BeadID: roleBeadID, Worktree: worktree})
		d.handleAuditResult(ctx, result)
		return nil
	})
}

func (d *Dispatcher) handleAuditResult(ctx context.Context, result ops.Result) {
	if result.Err != nil || result.Verdict == ops.VerdictFailed {
		detail := auditFailureDetail(result)
		d.appendAuditNote(ctx, result.BeadID, "all_sections_failed", detail)
		_ = d.logEvent(ctx, "audit_failed", auditRoleActor, result.BeadID, "", detail)
		return
	}

	findings, err := auditFindings(result.Feedback)
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
	for _, finding := range findings {
		if finding.ID == "" {
			continue
		}
		if err := d.appendAuditFinding(ctx, result.BeadID, finding); err != nil {
			_ = d.logEvent(ctx, "audit_finding_persist_failed", auditRoleActor, result.BeadID, "", err.Error())
			continue
		}
		if findingSuppressed(finding, active) || findingSuppressed(finding, suppressed) {
			continue
		}
		if _, err := d.beads.Create(ctx, auditFindingCreateParams(finding)); err != nil {
			_ = d.logEvent(ctx, "audit_finding_create_failed", auditRoleActor, result.BeadID, "", err.Error())
			continue
		}
		_ = d.logEvent(ctx, "audit_finding_created", auditRoleActor, result.BeadID, "", finding.ID)
	}
	coveragePayload, err := auditCoveragePayload()
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

func auditFindings(feedback string) ([]ops.Finding, error) {
	var payload struct {
		Findings []ops.Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(feedback), &payload); err != nil {
		return nil, fmt.Errorf("parse audit findings: %w", err)
	}
	return payload.Findings, nil
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

func auditFindingCreateParams(finding ops.Finding) beadstore.CreateParams {
	return beadstore.CreateParams{
		Title:       finding.Title,
		Type:        "task",
		Priority:    auditFindingPriority(finding.Severity),
		Description: auditFindingDescription(finding),
		Metadata:    map[string]string{auditFindingMetadataKey: finding.ID},
	}
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

func auditCoveragePayload() (string, error) {
	payload, err := json.Marshal(struct {
		CoveredSections []string `json:"covered_sections"`
		NotCovered      []string `json:"not_covered"`
	}{
		CoveredSections: ops.AuditSectionIDs(),
		NotCovered: []string{
			"product-correctness-live",
			"reliability-injection",
			"integrations-live",
			"deploy-observability",
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal audit coverage: %w", err)
	}
	return string(payload), nil
}
