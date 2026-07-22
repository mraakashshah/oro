package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"oro/pkg/janitor"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"oro/pkg/storage"
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
	_ = d.appendJanitorJourney(ctx, roleBeadID, "note", map[string]string{
		"kind":  "janitor_cycle_failed",
		"error": err.Error(),
	})
	return err
}

func (d *Dispatcher) runJanitorInWorktree(ctx context.Context, roleBeadID, worktree string) error {
	candidates, ran, skipped, projectScript, err := scanJanitorDetectors(ctx, worktree, d.cfg.DefaultBranch, d.cfg.StorageCatalogPath)
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
	return d.fileJanitorTriage(ctx, roleBeadID, result.Feedback, candidates, worktree, suppressed, ran, skipped, projectScript)
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
	worktree string,
	suppressed []ops.Finding,
	ran, skipped []string,
	projectScript bool,
) error {
	findings, err := parseJanitorTriage(feedback, candidates, worktree)
	if err != nil {
		return err
	}
	for i := range findings {
		if findingSuppressed(findings[i], suppressed) {
			findings[i].Status = "wont-fix"
		}
	}
	payload, err := json.Marshal(janitorResultPayload{
		Findings:      findings,
		RanDetectors:  ran,
		Skipped:       skipped,
		ProjectScript: projectScript,
	})
	if err != nil {
		return fmt.Errorf("marshal janitor findings: %w", err)
	}
	return d.handleJanitorResult(ctx, ops.Result{
		Type:     ops.OpsJanitor,
		BeadID:   roleBeadID,
		Feedback: string(payload),
	})
}

func scanJanitorDetectors(ctx context.Context, worktree, targetBranch, catalogPath string) (
	candidates []janitor.Candidate,
	ran, skipped []string,
	projectScript bool,
	err error,
) {
	options, closeCatalog, err := janitorRunOptions(ctx, worktree, catalogPath)
	if err != nil {
		return nil, nil, nil, false, err
	}
	defer closeCatalog()
	candidates, skippedLines, found, err := janitor.RunDetectScript(ctx, worktree, options...)
	if err != nil {
		return nil, nil, nil, false, fmt.Errorf("run detector script: %w", err)
	}
	if !found {
		candidates, ran, skipped, err = janitor.RunBuiltins(ctx, worktree, targetBranch, options...)
		if err != nil {
			return nil, nil, nil, false, fmt.Errorf("run built-in detectors: %w", err)
		}
		return candidates, ran, skipped, false, nil
	}
	ran = make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ran = append(ran, candidate.Detector)
	}
	return candidates, uniqueStrings(ran), skippedLines, true, nil
}

func janitorRunOptions(ctx context.Context, worktree, catalogPath string) ([]janitor.RunOption, func(), error) {
	if catalogPath == "" {
		return nil, nil, fmt.Errorf("janitor runtime catalog path is required")
	}
	catalog, err := storage.OpenCatalog(ctx, catalogPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open janitor runtime catalog: %w", err)
	}
	now := time.Now().UTC()
	runtime := storage.RuntimeRequest{
		Catalog: catalog,
		Lease: storage.LeaseRequest{
			ControllerID: "dispatcher",
			OwnerID:      "janitor-detector",
			PID:          os.Getpid(),
			ProcessStart: now,
			AcquiredAt:   now,
			HeartbeatAt:  now,
		},
		Env:     os.Environ(),
		Workdir: worktree,
		Policy: storage.StoragePolicy{
			ProjectID:      filepath.Base(worktree),
			RepositoryRoot: worktree,
		},
	}
	return []janitor.RunOption{janitor.WithRuntime(runtime)}, func() { _ = catalog.Close() }, nil
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

func parseJanitorTriage(feedback string, candidates []janitor.Candidate, worktree string) ([]ops.Finding, error) {
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
		if err := validateJanitorTriageFinding(*finding, availableSources, candidates, worktree); err != nil {
			return nil, fmt.Errorf("validate janitor triage finding %d: %w", i, err)
		}
		finding.ID = ops.FindingID("", *finding)
	}
	return findings, nil
}

func validateJanitorTriageFinding(
	finding ops.Finding,
	availableSources map[string]bool,
	candidates []janitor.Candidate,
	worktree string,
) error {
	switch finding.Severity {
	case ops.SevCritical, ops.SevImportant, ops.SevMinor:
	default:
		return fmt.Errorf("invalid severity %q", finding.Severity)
	}
	if strings.TrimSpace(finding.Category) == "" || strings.TrimSpace(finding.Title) == "" || strings.TrimSpace(finding.Detail) == "" {
		return fmt.Errorf("category, title, and detail are required")
	}
	if finding.Status != "" || len(finding.History) != 0 {
		return fmt.Errorf("status and history are dispatcher-managed")
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
	return validateJanitorTriageEvidence(finding, candidates, worktree)
}

func validateJanitorTriageEvidence(finding ops.Finding, candidates []janitor.Candidate, worktree string) error {
	sources := make(map[string]bool, len(finding.Sources))
	for _, source := range finding.Sources {
		sources[source] = true
	}
	for _, evidence := range finding.Evidence {
		matched, err := janitorEvidenceMatchesCandidate(evidence, candidates, sources, worktree)
		if err != nil {
			return err
		}
		if !matched {
			return fmt.Errorf("evidence does not match a cited detector candidate: %s", evidence.File)
		}
	}
	return nil
}

func janitorEvidenceMatchesCandidate(
	evidence ops.Evidence,
	candidates []janitor.Candidate,
	sources map[string]bool,
	worktree string,
) (bool, error) {
	evidenceFile, err := normalizeJanitorEvidencePath(evidence.File)
	if err != nil {
		return false, err
	}
	for _, candidate := range candidates {
		if !sources[candidate.Detector] {
			continue
		}
		candidateFile, candidateErr := normalizeJanitorEvidencePath(candidate.File)
		if candidateErr != nil || candidateFile != evidenceFile {
			continue
		}
		matched, matchErr := janitorEvidenceMatchesLocation(evidence, candidate, worktree, evidenceFile)
		if matchErr != nil {
			return false, matchErr
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func janitorEvidenceMatchesLocation(
	evidence ops.Evidence,
	candidate janitor.Candidate,
	worktree, evidenceFile string,
) (bool, error) {
	if candidate.Line == 0 {
		return evidence.LineStart == 0 && evidence.LineEnd == 0 && evidence.Quote == "", nil
	}
	if candidate.Line < 0 || evidence.LineStart <= 0 || evidence.LineEnd < evidence.LineStart ||
		candidate.Line < evidence.LineStart || candidate.Line > evidence.LineEnd || strings.TrimSpace(evidence.Quote) == "" {
		return false, nil
	}
	text, err := janitorEvidenceLineText(worktree, evidenceFile, evidence.LineStart, evidence.LineEnd)
	if err != nil {
		return false, err
	}
	return strings.Contains(text, evidence.Quote), nil
}

func normalizeJanitorEvidencePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("evidence path is empty")
	}
	localPath := filepath.FromSlash(path)
	if filepath.IsAbs(localPath) {
		return "", fmt.Errorf("evidence path must be relative: %s", path)
	}
	clean := filepath.ToSlash(filepath.Clean(localPath))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("evidence path escapes scan worktree: %s", path)
	}
	return clean, nil
}

func janitorEvidenceLineText(worktree, file string, start, end int) (string, error) {
	root, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return "", fmt.Errorf("resolve scan worktree: %w", err)
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		return "", fmt.Errorf("resolve evidence file: %w", err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("evidence path escapes scan worktree: %s", file)
	}
	data, err := os.ReadFile(path) //nolint:gosec // evaluated path is contained by the isolated scan worktree.
	if err != nil {
		return "", fmt.Errorf("read evidence file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	if end > len(lines) {
		return "", fmt.Errorf("evidence line outside file: %s:%d", file, end)
	}
	return strings.Join(lines[start-1:end], "\n"), nil
}
