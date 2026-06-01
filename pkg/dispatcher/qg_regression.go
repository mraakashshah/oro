package dispatcher

import (
	"regexp"
	"strings"
)

var (
	ansiEscapeRE    = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	goTestOutcomeRE = regexp.MustCompile(`^--- (PASS|FAIL): ([^\s(]+)`)
)

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
