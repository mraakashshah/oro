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
	ID           string
	BeadID       string
	WorkerID     string
	AssignmentID int64
	Component    string
	Fingerprint  string
	Summary      string
	Output       string
	OutputHash   string
}

// QGFailureHistory summarizes prior observations for the same or related
// quality-gate failure fingerprint.
type QGFailureHistory struct {
	AffectedBeads  int
	KnownFlaky     bool
	RerunPassed    bool
	RetryExhausted bool
}

// QGFailureAttribution describes evidence from the exact target revision that
// the candidate was assigned against. TargetKnown is false when no
// SHA-matched target evidence is available.
type QGFailureAttribution struct {
	CandidateSHA      string
	TargetSHA         string
	TargetFingerprint string
	TargetKnown       bool
	TargetPassed      bool
}

// QGFailureClass is the broad cause category assigned to a QG failure.
type QGFailureClass string

const (
	// QGFailureClassWorkerDeterministic means the failure is tied to worker changes.
	QGFailureClassWorkerDeterministic QGFailureClass = "worker_deterministic"
	// QGFailureClassWorkerTransient means worker runtime infrastructure failed before QG completed.
	QGFailureClassWorkerTransient QGFailureClass = "worker_transient"
	// QGFailureClassSystemic means the failure appears shared across beads or infrastructure.
	QGFailureClassSystemic QGFailureClass = "systemic"
	// QGFailureClassFlaky means the failure matches known flaky or rerun-sensitive behavior.
	QGFailureClassFlaky QGFailureClass = "flaky"
	// QGFailureClassTransient means the failure appears temporary or environmental.
	QGFailureClassTransient QGFailureClass = "transient"
	// QGFailureClassImpossible means the bead acceptance or state is impossible to satisfy as written.
	QGFailureClassImpossible QGFailureClass = "impossible"
	// QGFailureClassUnknown means the classifier has low confidence.
	QGFailureClassUnknown QGFailureClass = "unknown"
)

// QGFailureDecision is the dispatcher policy selected for a classified QG
// failure.
type QGFailureDecision string

const (
	// QGFailureDecisionRetryOriginal retries the same original bead with feedback.
	QGFailureDecisionRetryOriginal QGFailureDecision = "retry_original"
	// QGFailureDecisionReopenOriginal reopens the original bead after retry exhaustion.
	QGFailureDecisionReopenOriginal QGFailureDecision = "reopen_original"
	// QGFailureDecisionCreateOrReuseInfra creates or reuses an infra incident.
	QGFailureDecisionCreateOrReuseInfra QGFailureDecision = "create_or_reuse_infra"
	// QGFailureDecisionBackoffRetry retries after backoff without burning worker-fix attempts.
	QGFailureDecisionBackoffRetry QGFailureDecision = "backoff_retry"
	// QGFailureDecisionBumpOriginal updates or bumps the original bead.
	QGFailureDecisionBumpOriginal QGFailureDecision = "bump_original"
	// QGFailureDecisionStopForTriage stops automated handling for human or ops triage.
	QGFailureDecisionStopForTriage QGFailureDecision = "stop_for_triage"
)

// QGFailureConfidence is the classifier's confidence in its selected policy.
type QGFailureConfidence string

const (
	// QGFailureConfidenceHigh means the classifier has strong evidence.
	QGFailureConfidenceHigh QGFailureConfidence = "high"
	// QGFailureConfidenceMedium means the classifier has useful but incomplete evidence.
	QGFailureConfidenceMedium QGFailureConfidence = "medium"
	// QGFailureConfidenceLow means automation should stop for triage.
	QGFailureConfidenceLow QGFailureConfidence = "low"
)

// QGFailureClassification is the classifier result for one QG failure.
type QGFailureClassification struct {
	Class      QGFailureClass
	Decision   QGFailureDecision
	Confidence QGFailureConfidence
	Reason     string
}

// QGFingerprintOptions configures quality-gate failure normalization.
type QGFingerprintOptions struct {
	MaxInputBytes int
}

var (
	qgANSIEscapeRE     = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
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
	qgNilAwaySourceRE  = regexp.MustCompile(`\b[^\s:]+\.go:\d+:\d+.*potential nil panic detected`)
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

// ClassifyQGFailure maps a QG failure record plus prior history to the
// dispatcher policy that should handle it. Callers without exact target
// evidence may omit attribution to preserve the conservative policy.
func ClassifyQGFailure(record QGFailureRecord, history QGFailureHistory, attributions ...QGFailureAttribution) QGFailureClassification {
	var attribution QGFailureAttribution
	if len(attributions) > 0 {
		attribution = attributions[0]
	}
	text := strings.ToLower(record.Output + "\n" + record.Summary)

	switch {
	case targetBaselineHasFailure(record, attribution):
		return qgClassification(QGFailureClassSystemic, QGFailureDecisionCreateOrReuseInfra, QGFailureConfidenceHigh,
			"failure is present on the exact target baseline")
	case isImpossibleQGFailure(text):
		return qgClassification(QGFailureClassImpossible, QGFailureDecisionBumpOriginal, QGFailureConfidenceHigh,
			"failure indicates missing or impossible task acceptance")
	case isWorkerTransientQGFailure(text):
		return qgClassification(QGFailureClassWorkerTransient, QGFailureDecisionRetryOriginal, QGFailureConfidenceHigh,
			"worker subprocess exited before completing; retry original with preserved diagnostics")
	case candidateOnlyDeterministicFailure(text, attribution):
		if history.RetryExhausted {
			return qgClassification(QGFailureClassWorkerDeterministic, QGFailureDecisionReopenOriginal, QGFailureConfidenceHigh,
				"candidate-only deterministic failure exhausted retry budget")
		}
		return qgClassification(QGFailureClassWorkerDeterministic, QGFailureDecisionRetryOriginal, QGFailureConfidenceHigh,
			"deterministic failure is absent from the passing target baseline")
	case history.AffectedBeads > 1 || isSystemicQGFailure(text):
		return qgClassification(QGFailureClassSystemic, QGFailureDecisionCreateOrReuseInfra, QGFailureConfidenceHigh,
			"failure appears systemic across beads or shared infrastructure")
	case history.KnownFlaky || history.RerunPassed || isFlakyQGFailure(text):
		return qgClassification(QGFailureClassFlaky, QGFailureDecisionBackoffRetry, QGFailureConfidenceHigh,
			"failure matches known flaky or rerun-sensitive pattern")
	case isDeterministicQGFailure(text):
		if history.RetryExhausted {
			return qgClassification(QGFailureClassWorkerDeterministic, QGFailureDecisionReopenOriginal, QGFailureConfidenceHigh,
				"deterministic worker failure exhausted retry budget")
		}
		return qgClassification(QGFailureClassWorkerDeterministic, QGFailureDecisionRetryOriginal, QGFailureConfidenceHigh,
			"failure is tied to deterministic test, compile, or lint output")
	case isTransientQGFailure(text):
		return qgClassification(QGFailureClassTransient, QGFailureDecisionBackoffRetry, QGFailureConfidenceMedium,
			"failure appears transient or environmental")
	default:
		return qgClassification(QGFailureClassUnknown, QGFailureDecisionStopForTriage, QGFailureConfidenceLow,
			"could not classify QG failure with enough confidence")
	}
}

func targetBaselineHasFailure(record QGFailureRecord, attribution QGFailureAttribution) bool {
	if !attribution.TargetKnown {
		return false
	}
	if attribution.CandidateSHA != "" && attribution.CandidateSHA == attribution.TargetSHA {
		return true
	}
	return record.Fingerprint != "" && record.Fingerprint == attribution.TargetFingerprint
}

func candidateOnlyDeterministicFailure(text string, attribution QGFailureAttribution) bool {
	return attribution.TargetKnown && attribution.TargetPassed &&
		attribution.CandidateSHA != "" && attribution.TargetSHA != "" &&
		attribution.CandidateSHA != attribution.TargetSHA && isDeterministicQGFailure(text)
}

func qgClassification(class QGFailureClass, decision QGFailureDecision, confidence QGFailureConfidence, reason string) QGFailureClassification {
	return QGFailureClassification{Class: class, Decision: decision, Confidence: confidence, Reason: reason}
}

func isDeterministicQGFailure(text string) bool {
	return strings.Contains(text, "--- fail:") ||
		strings.Contains(text, "\nfail") ||
		(strings.Contains(text, "nilaway") && qgNilAwaySourceRE.MatchString(text)) ||
		toolFailure(text, "gofumpt") ||
		toolFailure(text, "goimports") ||
		toolFailure(text, "golangci-lint") ||
		toolFailure(text, "revive") ||
		strings.Contains(text, "compile error") ||
		strings.Contains(text, "compilation failed") ||
		strings.Contains(text, "build failed") ||
		strings.Contains(text, "unused variable")
}

func toolFailure(text, tool string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, tool) && (strings.Contains(line, "fail") || strings.Contains(line, "error")) {
			return true
		}
	}
	return false
}

func isSystemicQGFailure(text string) bool {
	return strings.Contains(text, "package loader") ||
		strings.Contains(text, "quality_gate.sh") ||
		(strings.Contains(text, ".tmp-test") && strings.Contains(text, "yamllint")) ||
		strings.Contains(text, "out of memory") ||
		strings.Contains(text, " oom") ||
		strings.Contains(text, "no such table") ||
		strings.Contains(text, "database panic") ||
		strings.Contains(text, "cannot load stdlib")
}

func isWorkerTransientQGFailure(text string) bool {
	return strings.Contains(text, "reason: subprocess_died") ||
		strings.Contains(text, "subprocess_died") ||
		strings.Contains(text, "subprocess died unexpectedly")
}

func isFlakyQGFailure(text string) bool {
	return strings.Contains(text, "flaky") ||
		strings.Contains(text, "race detected") ||
		strings.Contains(text, "parallel load") ||
		strings.Contains(text, "rerun passes")
}

func isTransientQGFailure(text string) bool {
	return strings.Contains(text, "network") ||
		strings.Contains(text, "timeout") ||
		strings.Contains(text, "temporary") ||
		strings.Contains(text, "database is locked") ||
		strings.Contains(text, "context canceled") ||
		strings.Contains(text, "shutdown")
}

func isImpossibleQGFailure(text string) bool {
	return strings.Contains(text, "missing acceptance") ||
		strings.Contains(text, "no cmd field") ||
		strings.Contains(text, "impossible command") ||
		strings.Contains(text, "contradictory") ||
		strings.Contains(text, "required dependency absent")
}

func normalizeQGFailureOutput(output string, opts QGFingerprintOptions) string {
	output = qgANSIEscapeRE.ReplaceAllString(output, "")
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
