package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"oro/pkg/beadstore"
	"oro/pkg/janitor"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

const (
	janitorRoleMetadataKey = "meta_role"
	janitorRoleMetadata    = "janitor"
)

// runJanitor scans an isolated checkout, derives suppression from persisted
// close reasons, and records the complete detector outcome on its role bead.
func (d *Dispatcher) runJanitor(ctx context.Context) error {
	role, err := d.janitorRole(ctx)
	if err != nil {
		return err
	}
	return d.withScanWorktree(ctx, func(worktree string) error {
		candidates, ran, skipped, err := scanJanitorDetectors(ctx, worktree, d.cfg.DefaultBranch)
		if err != nil {
			return fmt.Errorf("run janitor detectors: %w", err)
		}
		suppressed, err := d.janitorSuppressedFindingIDs(ctx)
		if err != nil {
			return err
		}
		findings := janitorFindings(candidates, suppressed)
		feedback, err := json.Marshal(janitorResultPayload{
			Findings:     findings,
			RanDetectors: ran,
			Skipped:      skipped,
		})
		if err != nil {
			return fmt.Errorf("marshal janitor findings: %w", err)
		}
		d.handleJanitorResult(ctx, ops.Result{Type: ops.OpsJanitor, BeadID: role.ID, Feedback: string(feedback)})
		return nil
	})
}

func scanJanitorDetectors(ctx context.Context, worktree, targetBranch string) (candidates []janitor.Candidate, ran, skipped []string, err error) {
	candidates, skippedLines, found, err := janitor.RunDetectScript(ctx, worktree)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("run detector script: %w", err)
	}
	if !found {
		candidates, ran, skipped, err = janitor.RunBuiltins(ctx, worktree, targetBranch)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("run built-in detectors: %w", err)
		}
		return candidates, ran, skipped, nil
	}
	ran = make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ran = append(ran, candidate.Detector)
	}
	return candidates, uniqueStrings(ran), skippedLines, nil
}

func (d *Dispatcher) janitorRole(ctx context.Context) (*protocol.Bead, error) {
	beads, err := d.beads.FindByMetadataKey(ctx, janitorRoleMetadataKey)
	if err != nil {
		return nil, fmt.Errorf("find janitor role: %w", err)
	}
	for _, bead := range beads {
		if bead != nil && bead.Metadata[janitorRoleMetadataKey] == janitorRoleMetadata {
			return bead, nil
		}
	}
	role, err := d.beads.Create(ctx, beadstore.CreateParams{
		Title:    "Janitor findings",
		Type:     "task",
		Priority: 2,
		Status:   "closed",
		Metadata: map[string]string{janitorRoleMetadataKey: janitorRoleMetadata},
	})
	if err != nil {
		return nil, fmt.Errorf("create janitor role: %w", err)
	}
	return role, nil
}

func (d *Dispatcher) janitorSuppressedFindingIDs(ctx context.Context) (map[string]bool, error) {
	beads, err := d.beads.FindByMetadataKey(ctx, janitorFindingMetadataKey)
	if err != nil {
		return nil, fmt.Errorf("find janitor findings: %w", err)
	}
	suppressed := make(map[string]bool)
	for _, bead := range beads {
		if bead == nil || bead.Status != "closed" || !janitorWontFix(bead.CloseReason) {
			continue
		}
		findingID, _ := bead.Metadata[janitorFindingMetadataKey].(string)
		if findingID != "" {
			suppressed[findingID] = true
		}
	}
	return suppressed, nil
}

func janitorWontFix(reason string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(reason)), "wont-fix")
}

func janitorFindings(candidates []janitor.Candidate, suppressed map[string]bool) []ops.Finding {
	findings := make([]ops.Finding, 0, len(candidates))
	for _, candidate := range candidates {
		finding := ops.Finding{
			Severity:   ops.SevMinor,
			Category:   candidate.Detector,
			Title:      candidate.Title,
			Detail:     candidate.Detail,
			Evidence:   []ops.Evidence{{File: candidate.File, LineStart: candidate.Line, LineEnd: candidate.Line, Quote: candidate.Detail}},
			Confidence: 100,
			Sources:    []string{candidate.Detector},
			Origin:     "pre_existing",
		}
		finding.ID = ops.FindingID("", finding)
		if suppressed[finding.ID] {
			finding.Status = "wont-fix"
		}
		findings = append(findings, finding)
	}
	return findings
}
