package dispatcher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

const (
	defaultQGFingerprintInputBytes = 64 * 1024
	maxQGFailureSummaryBytes       = 512
)

// QGFailureRecord captures the stable identity and concise summary of a
// quality-gate failure.
type QGFailureRecord struct {
	Fingerprint string
	Summary     string
	Output      string
}

// QGFingerprintOptions configures quality-gate failure normalization.
type QGFingerprintOptions struct {
	MaxInputBytes int
}

var (
	qgTimestampRE      = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ][0-9:.]+(?:Z|[+-]\d{2}:?\d{2})?\b`)
	qgWorkerIDRE       = regexp.MustCompile(`\bworker-[A-Za-z0-9_-]+\b`)
	qgPIDRE            = regexp.MustCompile(`\b(?:pid|PID|process)\s*[=: ]\s*\d+\b`)
	qgPortRE           = regexp.MustCompile(`\bport\s*[=: ]\s*\d+\b`)
	qgElapsedRE        = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ns|µs|us|ms|s|m|h)\b`)
	qgGoLineRE         = regexp.MustCompile(`(\.go):\d+\b`)
	qgTempPathRE       = regexp.MustCompile(`(?:/private)?/(?:var/folders|tmp|T)/[^\s:]+`)
	qgWorktreePathRE   = regexp.MustCompile(`/[^\s:"]*/\.worktrees/[^\s:"]+`)
	qgHexTokenRE       = regexp.MustCompile(`\b[0-9a-fA-F]{12,}\b`)
	qgWhitespaceRE     = regexp.MustCompile(`[ \t]+`)
	qgSummaryMarkersRE = regexp.MustCompile(`(?i)(FAIL|panic:|fatal|error|exit status|signal:)`)
)

// FingerprintQGFailure returns a stable fingerprint and human-readable summary
// for quality-gate output after normalizing volatile run-specific details.
func FingerprintQGFailure(output string, opts QGFingerprintOptions) (fingerprint, summary string) {
	normalized := normalizeQGFailureOutput(output, opts)
	if normalized == "" {
		normalized = "unknown qg failure"
	}

	sum := sha256.Sum256([]byte(normalized))
	fingerprint = "qg:" + hex.EncodeToString(sum[:])[:24]
	summary = summarizeQGFailure(normalized)
	return fingerprint, summary
}

func normalizeQGFailureOutput(output string, opts QGFingerprintOptions) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}

	limit := opts.MaxInputBytes
	if limit <= 0 {
		limit = defaultQGFingerprintInputBytes
	}
	if len(output) > limit {
		output = output[:limit]
	}

	replacements := []struct {
		re   *regexp.Regexp
		with string
	}{
		{qgTimestampRE, "<timestamp>"},
		{qgWorkerIDRE, "<worker>"},
		{qgPIDRE, "pid=<pid>"},
		{qgPortRE, "port <port>"},
		{qgElapsedRE, "<duration>"},
		{qgGoLineRE, "$1:<line>"},
		{qgWorktreePathRE, "<worktree>"},
		{qgTempPathRE, "<worktree>"},
		{qgHexTokenRE, "<hex>"},
		{qgWhitespaceRE, " "},
	}
	for _, repl := range replacements {
		output = repl.re.ReplaceAllString(output, repl.with)
	}
	return strings.TrimSpace(output)
}

func summarizeQGFailure(normalized string) string {
	if normalized == "" {
		return "unknown qg failure"
	}

	lines := strings.Split(normalized, "\n")
	parts := make([]string, 0, 4)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if qgSummaryMarkersRE.MatchString(line) {
			parts = append(parts, line)
		}
		if len(parts) == 5 {
			break
		}
	}
	if len(parts) == 0 {
		parts = append(parts, lines[0])
	}

	summary := strings.Join(parts, " | ")
	if len(summary) > maxQGFailureSummaryBytes {
		return fmt.Sprintf("%s...", summary[:maxQGFailureSummaryBytes-3])
	}
	return summary
}
