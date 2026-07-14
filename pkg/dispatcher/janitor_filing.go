package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
)

const (
	janitorFindingMetadataKey = "meta_finding_id"
	janitorRoleActor          = "ops_janitor"
	janitorTopFindings        = 5
)

type janitorResultPayload struct {
	Findings      []ops.Finding `json:"findings"`
	RanDetectors  []string      `json:"ran_detectors"`
	Skipped       []string      `json:"skipped"`
	ProjectScript bool          `json:"project_script"`
}

// handleJanitorResult files the highest-severity findings from one janitor
// cycle and keeps the complete finding set in the janitor role-bead journey.
func (d *Dispatcher) handleJanitorResult(ctx context.Context, result ops.Result) error {
	payload, err := parseJanitorResult(result.Feedback)
	if err != nil {
		noteErr := d.appendJanitorJourney(ctx, result.BeadID, "note", map[string]string{
			"kind":  "malformed_janitor_result",
			"error": err.Error(),
		})
		_ = d.logEvent(ctx, "janitor_result_malformed", janitorRoleActor, result.BeadID, "", err.Error())
		return errors.Join(err, noteErr)
	}
	payload.Findings = uniqueJanitorFindings(payload.Findings)
	eligible, err := d.filterJanitorFindings(ctx, result.BeadID, payload.Findings)
	if err != nil {
		noteErr := d.appendJanitorJourney(ctx, result.BeadID, "note", map[string]string{
			"kind":  "suppression_derivation_failed",
			"error": err.Error(),
		})
		_ = d.logEvent(ctx, "janitor_suppression_failed", janitorRoleActor, result.BeadID, "", err.Error())
		return errors.Join(err, noteErr)
	}

	var errs []error
	persisted := make(map[string]bool, len(payload.Findings))
	for _, finding := range payload.Findings {
		if err := d.persistJanitorFinding(ctx, result.BeadID, finding); err != nil {
			errs = append(errs, err)
			continue
		}
		persisted[finding.ID] = true
	}

	selected := janitorTopFindingsBySeverity(eligible, d.cfg.JanitorTopK)
	filed := 0
	for _, finding := range selected {
		if !persisted[finding.ID] {
			continue
		}
		params := janitorFindingCreateParams(finding, payload.RanDetectors, payload.ProjectScript, d.cfg.DefaultBranch)
		if _, createErr := d.beads.Create(ctx, params); createErr != nil {
			_ = d.logEvent(ctx, "janitor_finding_create_failed", janitorRoleActor, result.BeadID, "", createErr.Error())
			errs = append(errs, fmt.Errorf("create janitor finding %s: %w", finding.ID, createErr))
			continue
		}
		filed++
	}
	if err := d.appendJanitorJourney(ctx, result.BeadID, "janitor_cycle", map[string]any{
		"findings": len(payload.Findings),
		"filed":    filed,
		"skipped":  payload.Skipped,
	}); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func uniqueJanitorFindings(findings []ops.Finding) []ops.Finding {
	seen := make(map[string]bool, len(findings))
	unique := make([]ops.Finding, 0, len(findings))
	for _, finding := range findings {
		key := ops.FindingID("", finding)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, finding)
	}
	return unique
}

func (d *Dispatcher) filterJanitorFindings(
	ctx context.Context,
	roleBeadID string,
	findings []ops.Finding,
) ([]ops.Finding, error) {
	roleBeadIDs, err := d.cleanlinessRoleBeadIDs(ctx, roleBeadID)
	if err != nil {
		return nil, err
	}
	suppressed, err := d.deriveSuppressed(ctx, roleBeadIDs)
	if err != nil {
		return nil, err
	}
	active, err := d.deriveActiveFindings(ctx, roleBeadIDs)
	if err != nil {
		return nil, err
	}
	eligible := make([]ops.Finding, 0, len(findings))
	for _, finding := range findings {
		if findingSuppressed(finding, active) || findingSuppressed(finding, suppressed) {
			continue
		}
		eligible = append(eligible, finding)
	}
	return eligible, nil
}

func parseJanitorResult(feedback string) (janitorResultPayload, error) {
	var payload janitorResultPayload
	if err := json.Unmarshal([]byte(feedback), &payload); err != nil {
		return janitorResultPayload{}, fmt.Errorf("parse janitor findings: %w", err)
	}
	return payload, nil
}

func (d *Dispatcher) persistJanitorFinding(ctx context.Context, roleBeadID string, finding ops.Finding) error {
	payload, err := json.Marshal(finding)
	if err != nil {
		_ = d.logEvent(ctx, "janitor_finding_persist_failed", janitorRoleActor, roleBeadID, "", err.Error())
		return fmt.Errorf("marshal janitor finding %s: %w", finding.ID, err)
	}
	if err := d.beads.AppendJourney(ctx, roleBeadID, beadstore.JourneyEvent{
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Actor:   janitorRoleActor,
		Event:   "janitor_finding",
		Payload: string(payload),
	}); err != nil {
		_ = d.logEvent(ctx, "janitor_finding_persist_failed", janitorRoleActor, roleBeadID, "", err.Error())
		return fmt.Errorf("persist janitor finding %s: %w", finding.ID, err)
	}
	return nil
}

func (d *Dispatcher) appendJanitorJourney(ctx context.Context, roleBeadID, event string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal janitor %s journey: %w", event, err)
	}
	if err := d.beads.AppendJourney(ctx, roleBeadID, beadstore.JourneyEvent{
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Actor:   janitorRoleActor,
		Event:   event,
		Payload: string(encoded),
	}); err != nil {
		_ = d.logEvent(ctx, "janitor_journey_append_failed", janitorRoleActor, roleBeadID, "", err.Error())
		return fmt.Errorf("append janitor %s journey: %w", event, err)
	}
	return nil
}

func janitorTopFindingsBySeverity(findings []ops.Finding, limit int) []ops.Finding {
	ordered := make([]ops.Finding, 0, len(findings))
	for _, finding := range findings {
		if finding.Status == "wont-fix" {
			continue
		}
		ordered = append(ordered, finding)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return janitorSeverityRank(ordered[i].Severity) > janitorSeverityRank(ordered[j].Severity)
	})
	if limit == 0 {
		limit = janitorTopFindings
	}
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered
}

func janitorFindingCreateParams(
	finding ops.Finding,
	ranDetectors []string,
	projectScript bool,
	targetBranch string,
) beadstore.CreateParams {
	return beadstore.CreateParams{
		Title:              finding.Title,
		Type:               "task",
		Priority:           2,
		Description:        janitorFindingDescription(finding),
		AcceptanceCriteria: janitorFindingAcceptance(finding, ranDetectors, projectScript, targetBranch),
		Metadata:           map[string]string{janitorFindingMetadataKey: finding.ID},
	}
}

func janitorFindingDescription(finding ops.Finding) string {
	return strings.TrimSpace(fmt.Sprintf(`%s

Suppression contract: close with a reason beginning "wont-fix:" to mark this finding intentional and prevent refiling. The first close reason is immutable; reopen this bead before closing again to change that reason.`, finding.Detail))
}

func janitorFindingAcceptance(finding ops.Finding, ranDetectors []string, projectScript bool, targetBranch string) string {
	ran := make(map[string]bool, len(ranDetectors))
	for _, detector := range ranDetectors {
		ran[detector] = true
	}
	var commands []string
	for _, detector := range finding.Sources {
		if ran[detector] && (projectScript || detector != "ci") {
			commands = append(commands, janitorDetectorRerunCommand(detector, projectScript, targetBranch))
		}
	}
	commands = append(commands, "./scripts/quality_gate.sh")
	return fmt.Sprintf("Test: janitor finding %s\nCmd: %s\nAssert: finding is gone and the quality gate passes", finding.ID, strings.Join(uniqueStrings(commands), " && "))
}

func janitorDetectorRerunCommand(detector string, projectScript bool, targetBranch string) string {
	prefix, detectorArg := shellAcceptanceArgument("janitor_detector", detector)
	parts := make([]string, 0, 3)
	if prefix != "" {
		parts = append(parts, prefix)
	}
	if projectScript {
		parts = append(parts, fmt.Sprintf("oro janitor:detect --project-script --detector %s", detectorArg))
		return strings.Join(parts, " && ")
	}
	command := fmt.Sprintf("oro janitor:detect --detector %s", detectorArg)
	if detector == "ci" && targetBranch != "" {
		branchPrefix, branchArg := shellAcceptanceArgument("janitor_target_branch", targetBranch)
		if branchPrefix != "" {
			parts = append(parts, branchPrefix)
		}
		command += " --target-branch " + branchArg
	}
	return strings.Join(append(parts, command), " && ")
}

func shellAcceptanceArgument(variable, value string) (prefix, argument string) {
	if !strings.ContainsAny(value, "\r\n") {
		return "", shellSingleQuote(value)
	}
	var encoded strings.Builder
	for _, valueByte := range []byte(value) {
		_, _ = fmt.Fprintf(&encoded, `\0%03o`, valueByte)
	}
	encoded.WriteString(`\0137`)
	prefix = variable + "=$(printf '%b' " + shellSingleQuote(encoded.String()) + ") && " +
		variable + "=${" + variable + "%_}"
	return prefix, `"$` + variable + `"`
}

func janitorSeverityRank(severity ops.Severity) int {
	switch severity {
	case ops.SevCritical:
		return 3
	case ops.SevImportant:
		return 2
	default:
		return 1
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
