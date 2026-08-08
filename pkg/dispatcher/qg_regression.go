package dispatcher

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

type qgBaseline map[string]qgBaselineEntry

type qgBaselineEntry struct {
	HeadSHA     string
	SuitePassed bool
	Outcomes    map[string]bool
}

type qgRegression struct {
	TestName       string
	BaselinePassed bool
	CurrentPassed  bool
}

var (
	ansiEscapeRE    = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	goTestOutcomeRE = regexp.MustCompile(`^--- (PASS|FAIL): ([^\s(]+)`)
)

func (d *Dispatcher) captureQGBaseline(ctx context.Context, beadID, worktree, mutationBase string) (qgBaseline, error) {
	headOut, err := d.commandRunner().Run(ctx, "git", "-C", worktree, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("capture qg baseline head: %w", err)
	}
	headSHA := strings.TrimSpace(string(headOut))

	if cached, ok := d.cachedQGBaseline(headSHA); ok {
		return cached, nil
	}

	passed, output, err := d.qgRunner.Run(ctx, worktree, !d.cfg.MutationTesting, mutationBase)
	if err != nil {
		return nil, fmt.Errorf("capture qg baseline run: %w", err)
	}
	fingerprint := ""
	if !passed {
		fingerprint, _ = FingerprintQGFailure(output, QGFingerprintOptions{})
	}
	baseline := qgBaseline{
		beadID: {
			HeadSHA:     headSHA,
			SuitePassed: passed,
			Outcomes:    parseTestOutcomes(output),
		},
	}
	return d.storeQGBaseline(headSHA, baseline, fingerprint), nil
}

// seedQGBaselineFromFailure records the worker's failed QG result as the
// retry baseline. The worker has already run QG on this exact HEAD, so running
// it again would only delay delivering retry feedback.
func (d *Dispatcher) seedQGBaselineFromFailure(ctx context.Context, beadID, worktree, qgOutput string) (qgBaseline, error) {
	headOut, err := d.commandRunner().Run(ctx, "git", "-C", worktree, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("seed qg baseline head: %w", err)
	}
	headSHA := strings.TrimSpace(string(headOut))

	if cached, ok := d.cachedQGBaseline(headSHA); ok {
		return cached, nil
	}

	fingerprint, _ := FingerprintQGFailure(qgOutput, QGFingerprintOptions{})
	baseline := qgBaseline{
		beadID: {
			HeadSHA:     headSHA,
			SuitePassed: false,
			Outcomes:    parseTestOutcomes(qgOutput),
		},
	}
	return d.storeQGBaseline(headSHA, baseline, fingerprint), nil
}

func (d *Dispatcher) qgFailureAttribution(ctx context.Context, workerID string, record QGFailureRecord) QGFailureAttribution {
	d.mu.Lock()
	worker := d.workers[workerID]
	if worker == nil {
		d.mu.Unlock()
		return QGFailureAttribution{}
	}
	worktree, targetSHA := worker.worktree, worker.targetSHA
	d.mu.Unlock()
	if worktree == "" || targetSHA == "" {
		return QGFailureAttribution{}
	}

	headOut, err := d.commandRunner().Run(ctx, "git", "-C", worktree, "rev-parse", "HEAD")
	if err != nil {
		return QGFailureAttribution{}
	}
	candidateSHA := strings.TrimSpace(string(headOut))
	attribution := QGFailureAttribution{CandidateSHA: candidateSHA, TargetSHA: targetSHA}
	if candidateSHA == "" {
		return attribution
	}
	if candidateSHA == targetSHA {
		attribution.TargetKnown = true
		attribution.TargetFingerprint = record.Fingerprint
		return attribution
	}

	baseline, ok := d.cachedQGBaseline(targetSHA)
	if !ok {
		return attribution
	}
	allPassed := true
	targetFingerprint := d.cachedQGBaselineFingerprint(targetSHA)
	for _, entry := range baseline {
		if entry.HeadSHA != targetSHA {
			continue
		}
		attribution.TargetKnown = true
		allPassed = allPassed && entry.SuitePassed
	}
	if record.Fingerprint != "" && record.Fingerprint == targetFingerprint {
		attribution.TargetFingerprint = targetFingerprint
	}
	if attribution.TargetKnown {
		attribution.TargetPassed = allPassed
	}
	return attribution
}

func (d *Dispatcher) detectQGRegression(ctx context.Context, base qgBaseline, worktree, mutationBase string) (qgRegression, error) {
	passed, output, err := d.qgRunner.Run(ctx, worktree, !d.cfg.MutationTesting, mutationBase)
	if err != nil {
		return qgRegression{}, fmt.Errorf("detect qg regression run: %w", err)
	}
	current := parseTestOutcomes(output)
	for beadID := range base {
		regressions := compareQGRegressionBaseline(base, beadID, current)
		if len(regressions) > 0 {
			return regressions[0], nil
		}
		if regression, ok := detectCoarseQGRegression(base[beadID], passed, current); ok {
			return regression, nil
		}
	}
	return qgRegression{}, nil
}

func (d *Dispatcher) revertRegressedRetry(ctx context.Context, base qgBaseline, worktree string) error {
	headSHA := ""
	for _, entry := range base {
		headSHA = entry.HeadSHA
		break
	}
	if headSHA == "" {
		return fmt.Errorf("revert regressed retry: missing baseline head")
	}
	if _, err := (&ExecCommandRunner{Dir: worktree}).Run(ctx, "git", "reset", "--hard", headSHA); err != nil {
		return fmt.Errorf("revert regressed retry: %w", err)
	}
	return nil
}

func (d *Dispatcher) cachedQGBaseline(headSHA string) (qgBaseline, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.qgBaselineCache == nil {
		return nil, false
	}
	baseline, ok := d.qgBaselineCache[headSHA]
	return cloneQGBaseline(baseline), ok
}

func (d *Dispatcher) cachedQGBaselineFingerprint(headSHA string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.qgBaselineFingerprints[headSHA]
}

func (d *Dispatcher) storeQGBaseline(headSHA string, baseline qgBaseline, fingerprint string) qgBaseline {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.qgBaselineCache == nil {
		d.qgBaselineCache = make(map[string]qgBaseline)
	}
	if d.qgBaselineFingerprints == nil {
		d.qgBaselineFingerprints = make(map[string]string)
	}
	if cached, ok := d.qgBaselineCache[headSHA]; ok {
		return cloneQGBaseline(cached)
	}
	d.qgBaselineCache[headSHA] = cloneQGBaseline(baseline)
	if fingerprint != "" {
		d.qgBaselineFingerprints[headSHA] = fingerprint
	}
	return cloneQGBaseline(baseline)
}

func (d *Dispatcher) takeQGBaselineForBead(beadID string) (qgBaseline, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.qgBaselineCache == nil {
		return nil, false
	}
	for headSHA, baseline := range d.qgBaselineCache {
		entry, ok := baseline[beadID]
		if !ok {
			continue
		}
		delete(d.qgBaselineCache, headSHA)
		delete(d.qgBaselineFingerprints, headSHA)
		return cloneQGBaseline(qgBaseline{beadID: entry}), true
	}
	return nil, false
}

func cloneQGBaseline(baseline qgBaseline) qgBaseline {
	if baseline == nil {
		return nil
	}
	cloned := make(qgBaseline, len(baseline))
	for beadID, entry := range baseline {
		outcomes := make(map[string]bool, len(entry.Outcomes))
		for name, passed := range entry.Outcomes {
			outcomes[name] = passed
		}
		cloned[beadID] = qgBaselineEntry{
			HeadSHA:     entry.HeadSHA,
			SuitePassed: entry.SuitePassed,
			Outcomes:    outcomes,
		}
	}
	return cloned
}

func detectCoarseQGRegression(entry qgBaselineEntry, currentPassed bool, current map[string]bool) (qgRegression, bool) {
	if len(entry.Outcomes) != 0 || len(current) != 0 || !entry.SuitePassed || currentPassed {
		return qgRegression{}, false
	}
	return qgRegression{
		TestName:       "quality_gate",
		BaselinePassed: true,
		CurrentPassed:  false,
	}, true
}

func compareQGRegressionBaseline(baseline qgBaseline, beadID string, current map[string]bool) []qgRegression {
	entry, ok := baseline[beadID]
	if !ok {
		return nil
	}
	regressions := make([]qgRegression, 0)
	for testName, baselinePassed := range entry.Outcomes {
		currentPassed, exists := current[testName]
		if !baselinePassed || !exists || currentPassed {
			continue
		}
		regressions = append(regressions, qgRegression{
			TestName:       testName,
			BaselinePassed: baselinePassed,
			CurrentPassed:  currentPassed,
		})
	}
	return regressions
}

func parseTestOutcomes(output string) map[string]bool {
	outcomes := make(map[string]bool)
	normalized := ansiEscapeRE.ReplaceAllString(output, "")
	for _, line := range strings.Split(normalized, "\n") {
		parseGoTestOutcome(line, outcomes)
		parsePytestOutcome(line, outcomes)
	}
	return outcomes
}

func parseGoTestOutcome(line string, outcomes map[string]bool) {
	matches := goTestOutcomeRE.FindStringSubmatch(strings.TrimSpace(line))
	if len(matches) != 3 {
		return
	}
	outcomes[matches[2]] = matches[1] == "PASS"
}

func parsePytestOutcome(line string, outcomes map[string]bool) {
	fields := strings.Fields(line)
	for i, field := range fields {
		if i == 0 {
			continue
		}
		switch field {
		case "PASSED":
			outcomes[pytestTestName(fields[i-1])] = true
		case "FAILED":
			outcomes[pytestTestName(fields[i-1])] = false
		default:
			continue
		}
		return
	}
}

func pytestTestName(name string) string {
	if _, testName, ok := strings.Cut(name, "::"); ok {
		return testName
	}
	return name
}
