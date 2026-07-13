package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
)

const auditFindingMetadataKey = "meta_finding_id"

// runAudit executes the whole-repository audit in an isolated scan worktree.
// Failed section reports are recorded as audit events; audits never escalate.
func (d *Dispatcher) runAudit(ctx context.Context) error {
	return d.withScanWorktree(ctx, func(worktree string) error {
		result := <-d.ops.Audit(ctx, ops.AuditOpts{Worktree: worktree})
		d.handleAuditResult(ctx, result)
		return nil
	})
}

func (d *Dispatcher) handleAuditResult(ctx context.Context, result ops.Result) {
	if result.Err != nil || result.Verdict == ops.VerdictFailed {
		_ = d.logEvent(ctx, "audit_failed", "ops_audit", "", "", auditFailureDetail(result))
		return
	}

	findings, err := auditFindings(result.Feedback)
	if err != nil {
		_ = d.logEvent(ctx, "audit_failed", "ops_audit", "", "", err.Error())
		return
	}
	blocked, err := d.blockedAuditFindingIDs(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "audit_suppression_failed", "ops_audit", "", "", err.Error())
		return
	}
	for _, finding := range findings {
		if finding.ID == "" || blocked[finding.ID] {
			continue
		}
		if _, err := d.beads.Create(ctx, auditFindingCreateParams(finding)); err != nil {
			_ = d.logEvent(ctx, "audit_finding_create_failed", "ops_audit", "", "", err.Error())
			continue
		}
		_ = d.logEvent(ctx, "audit_finding_created", "ops_audit", "", "", finding.ID)
	}
	coveragePayload, err := auditCoveragePayload()
	if err != nil {
		_ = d.logEvent(ctx, "audit_coverage_failed", "ops_audit", "", "", err.Error())
		return
	}
	_ = d.logEvent(ctx, "audit_coverage", "ops_audit", "", "", coveragePayload)
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

func (d *Dispatcher) blockedAuditFindingIDs(ctx context.Context) (map[string]bool, error) {
	beads, err := d.beads.FindByMetadataKey(ctx, auditFindingMetadataKey)
	if err != nil {
		return nil, fmt.Errorf("find existing audit findings: %w", err)
	}
	blocked := make(map[string]bool)
	for _, bead := range beads {
		if bead == nil {
			continue
		}
		findingID, ok := bead.Metadata[auditFindingMetadataKey].(string)
		if !ok || findingID == "" {
			continue
		}
		if bead.Status != "closed" || isWontFixReason(bead.CloseReason) {
			blocked[findingID] = true
		}
	}
	return blocked, nil
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
