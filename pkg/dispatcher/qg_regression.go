package dispatcher

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

type qgBaseline map[string]qgBaselineEntry

type qgBaselineEntry struct {
	HeadSHA  string
	Outcomes map[string]bool
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

func (d *Dispatcher) captureQGBaseline(ctx context.Context, beadID, worktree string) (qgBaseline, error) {
	headOut, err := d.shutdownRunner.Run(ctx, "git", "-C", worktree, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("capture qg baseline head: %w", err)
	}
	headSHA := strings.TrimSpace(string(headOut))

	if cached, ok := d.cachedQGBaseline(headSHA); ok {
		return cached, nil
	}

	_, output, err := d.qgRunner.Run(ctx, worktree, !d.cfg.MutationTesting)
	if err != nil {
		return nil, fmt.Errorf("capture qg baseline run: %w", err)
	}
	baseline := qgBaseline{
		beadID: {
			HeadSHA:  headSHA,
			Outcomes: parseTestOutcomes(output),
		},
	}
	return d.storeQGBaseline(headSHA, baseline), nil
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

func (d *Dispatcher) storeQGBaseline(headSHA string, baseline qgBaseline) qgBaseline {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.qgBaselineCache == nil {
		d.qgBaselineCache = make(map[string]qgBaseline)
	}
	if cached, ok := d.qgBaselineCache[headSHA]; ok {
		return cloneQGBaseline(cached)
	}
	d.qgBaselineCache[headSHA] = cloneQGBaseline(baseline)
	return cloneQGBaseline(baseline)
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
			HeadSHA:  entry.HeadSHA,
			Outcomes: outcomes,
		}
	}
	return cloned
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
