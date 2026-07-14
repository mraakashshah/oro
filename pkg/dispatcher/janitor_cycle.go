package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"oro/pkg/janitor"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

const janitorRoleMetadataKey = cleanlinessRoleMetadataKey

// runJanitor scans an isolated checkout, derives persisted suppression, asks
// one cheap ops agent to triage the deterministic candidates, and files only
// the structured findings returned by that triage.
func (d *Dispatcher) runJanitor(ctx context.Context) error {
	roleBeadID, err := d.ensureRoleBead(ctx, "janitor")
	if err != nil {
		return err
	}

	err = d.withScanWorktree(ctx, func(worktree string) error {
		return d.runJanitorInWorktree(ctx, roleBeadID, worktree)
	})
	if err == nil {
		return nil
	}
	d.appendJanitorJourney(ctx, roleBeadID, "note", map[string]string{
		"kind":  "janitor_cycle_failed",
		"error": err.Error(),
	})
	return err
}

func (d *Dispatcher) runJanitorInWorktree(ctx context.Context, roleBeadID, worktree string) error {
	candidates, ran, skipped, err := scanJanitorDetectors(ctx, worktree, d.cfg.DefaultBranch)
	if err != nil {
		return fmt.Errorf("run janitor detectors: %w", err)
	}
	suppressed, err := d.janitorSuppressedFindings(ctx, roleBeadID)
	if err != nil {
		return err
	}
	openTitles, err := d.janitorOpenTitles(ctx)
	if err != nil {
		return err
	}
	result, err := d.waitJanitorTriage(ctx, ops.JanitorOpts{
		Candidates: candidates,
		Suppressed: suppressed,
		OpenTitles: openTitles,
		Worktree:   worktree,
	})
	if err != nil {
		return err
	}
	return d.fileJanitorTriage(ctx, roleBeadID, result.Feedback, candidates, suppressed, ran, skipped)
}

func (d *Dispatcher) janitorSuppressedFindings(ctx context.Context, roleBeadID string) ([]ops.Finding, error) {
	roleBeadIDs, err := d.cleanlinessRoleBeadIDs(ctx, roleBeadID)
	if err != nil {
		return nil, err
	}
	return d.deriveSuppressed(ctx, roleBeadIDs)
}

func (d *Dispatcher) waitJanitorTriage(ctx context.Context, opts ops.JanitorOpts) (ops.Result, error) {
	select {
	case <-ctx.Done():
		return ops.Result{}, fmt.Errorf("wait for janitor triage: %w", ctx.Err())
	case result := <-d.ops.Janitor(ctx, opts):
		if result.Err != nil {
			return ops.Result{}, fmt.Errorf("run janitor triage: %w", result.Err)
		}
		if result.Verdict == ops.VerdictFailed {
			return ops.Result{}, fmt.Errorf("run janitor triage: %s", strings.TrimSpace(result.Feedback))
		}
		return result, nil
	}
}

func (d *Dispatcher) fileJanitorTriage(
	ctx context.Context,
	roleBeadID, feedback string,
	candidates []janitor.Candidate,
	suppressed []ops.Finding,
	ran, skipped []string,
) error {
	findings, err := parseJanitorTriage(feedback, candidates)
	if err != nil {
		return err
	}
	for i := range findings {
		if findingSuppressed(findings[i], suppressed) {
			findings[i].Status = "wont-fix"
		}
	}
	payload, err := json.Marshal(janitorResultPayload{
		Findings:     findings,
		RanDetectors: ran,
		Skipped:      skipped,
	})
	if err != nil {
		return fmt.Errorf("marshal janitor findings: %w", err)
	}
	d.handleJanitorResult(ctx, ops.Result{
		Type:     ops.OpsJanitor,
		BeadID:   roleBeadID,
		Feedback: string(payload),
	})
	return nil
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

func (d *Dispatcher) janitorOpenTitles(ctx context.Context) ([]string, error) {
	ready, err := d.beads.Ready(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ready beads for janitor triage: %w", err)
	}
	inProgress, err := d.beads.InProgress(ctx)
	if err != nil {
		return nil, fmt.Errorf("list in-progress beads for janitor triage: %w", err)
	}
	blocked, err := d.beads.Blocked(ctx)
	if err != nil {
		return nil, fmt.Errorf("list blocked beads for janitor triage: %w", err)
	}

	titles := make([]string, 0, len(ready)+len(inProgress)+len(blocked))
	for _, beads := range [][]protocol.Bead{ready, inProgress, blocked} {
		for _, bead := range beads {
			title := strings.TrimSpace(bead.Title)
			if title != "" {
				titles = append(titles, title)
			}
		}
	}
	sort.Strings(titles)
	return uniqueStrings(titles), nil
}

func parseJanitorTriage(feedback string, candidates []janitor.Candidate) ([]ops.Finding, error) {
	var findings []ops.Finding
	if err := json.Unmarshal([]byte(strings.TrimSpace(feedback)), &findings); err != nil {
		return nil, fmt.Errorf("parse janitor triage findings: %w", err)
	}
	if findings == nil {
		findings = []ops.Finding{}
	}
	availableSources := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if candidate.Detector != "" {
			availableSources[candidate.Detector] = true
		}
	}
	for i := range findings {
		finding := &findings[i]
		if err := validateJanitorTriageFinding(*finding, availableSources); err != nil {
			return nil, fmt.Errorf("validate janitor triage finding %d: %w", i, err)
		}
		finding.ID = ops.FindingID("", *finding)
	}
	return findings, nil
}

func validateJanitorTriageFinding(finding ops.Finding, availableSources map[string]bool) error {
	switch finding.Severity {
	case ops.SevCritical, ops.SevImportant, ops.SevMinor:
	default:
		return fmt.Errorf("invalid severity %q", finding.Severity)
	}
	if strings.TrimSpace(finding.Category) == "" || strings.TrimSpace(finding.Title) == "" || strings.TrimSpace(finding.Detail) == "" {
		return fmt.Errorf("category, title, and detail are required")
	}
	if finding.Confidence < 0 || finding.Confidence > 100 {
		return fmt.Errorf("confidence %d is outside 0..100", finding.Confidence)
	}
	if len(finding.Evidence) == 0 {
		return fmt.Errorf("candidate-backed evidence is required")
	}
	if len(finding.Sources) == 0 {
		return fmt.Errorf("a detector source is required")
	}
	for _, source := range finding.Sources {
		if !availableSources[source] {
			return fmt.Errorf("source %q did not produce a candidate", source)
		}
	}
	if finding.Origin != "pre_existing" {
		return fmt.Errorf("origin must be pre_existing")
	}
	return nil
}
