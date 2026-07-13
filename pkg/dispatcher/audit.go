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
	suppressed, err := d.suppressedFindingIDs(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "audit_suppression_failed", "ops_audit", "", "", err.Error())
		return
	}
	for _, finding := range findings {
		if finding.ID == "" || suppressed[finding.ID] {
			continue
		}
		if _, err := d.beads.Create(ctx, auditFindingCreateParams(finding)); err != nil {
			_ = d.logEvent(ctx, "audit_finding_create_failed", "ops_audit", "", "", err.Error())
			continue
		}
		_ = d.logEvent(ctx, "audit_finding_created", "ops_audit", "", "", finding.ID)
	}
	_ = d.logEvent(ctx, "audit_coverage", "ops_audit", "", "", auditCoveragePayload)
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

func (d *Dispatcher) suppressedFindingIDs(ctx context.Context) (map[string]bool, error) {
	beads, err := d.beads.FindByMetadataKey(ctx, auditFindingMetadataKey)
	if err != nil {
		return nil, fmt.Errorf("find suppressed audit findings: %w", err)
	}
	suppressed := make(map[string]bool)
	for _, bead := range beads {
		if bead == nil || bead.Status != "closed" {
			continue
		}
		if findingID, ok := bead.Metadata[auditFindingMetadataKey].(string); ok && findingID != "" {
			suppressed[findingID] = true
		}
	}
	return suppressed, nil
}

func auditFindingCreateParams(finding ops.Finding) beadstore.CreateParams {
	return beadstore.CreateParams{
		Title:       finding.Title,
		Type:        "task",
		Priority:    auditFindingPriority(finding.Severity),
		Description: strings.TrimSpace(finding.Detail),
		Metadata:    map[string]string{auditFindingMetadataKey: finding.ID},
	}
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

const auditCoveragePayload = `{"covered_sections":["code-quality","tests-safety","data-migrations","security-static","perf-patterns","dx-deps-docs"],"not_covered":["product-correctness-live","reliability-injection","integrations-live","deploy-observability"]}`
