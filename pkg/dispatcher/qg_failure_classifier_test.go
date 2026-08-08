package dispatcher_test

import (
	"strings"
	"testing"

	"oro/pkg/dispatcher"
)

func TestQGFailureFingerprintNormalizesVolatileOutput(t *testing.T) {
	outputA := `2026-05-07T02:28:13.123Z worker-1778063187957098000-0 pid=48123 port 54321
FAIL	oro/pkg/dispatcher	12.345s
--- FAIL: TestCriticalFlow (0.42s)
    /Users/as21/codehouse/oro/.worktrees/oro-a1b2/pkg/dispatcher/critical_test.go:123: panic: nil pointer
exit status 2
signal: killed
`
	outputB := `2026-05-07T03:29:44.999Z worker-9999999999999999999-7 pid=99123 port 61234
FAIL	oro/pkg/dispatcher	98.765s
--- FAIL: TestCriticalFlow (9.87s)
    /private/var/folders/x/y/T/oro-z9y8/pkg/dispatcher/critical_test.go:987: panic: nil pointer
exit status 2
signal: killed
`

	fpA, summaryA := dispatcher.FingerprintQGFailure(outputA, dispatcher.QGFingerprintOptions{})
	fpB, summaryB := dispatcher.FingerprintQGFailure(outputB, dispatcher.QGFingerprintOptions{})
	if fpA == "" {
		t.Fatal("fingerprint is empty")
	}
	if fpA != fpB {
		t.Fatalf("fingerprints differ after volatile normalization:\nA=%s %q\nB=%s %q", fpA, summaryA, fpB, summaryB)
	}
	for _, want := range []string{"oro/pkg/dispatcher", "TestCriticalFlow", "panic:", "exit status 2", "signal: killed"} {
		if !strings.Contains(summaryA, want) {
			t.Fatalf("summary %q missing stable marker %q", summaryA, want)
		}
	}
	for _, volatile := range []string{"1778063187957098000", "48123", "54321", "12.345s", ":123"} {
		if strings.Contains(summaryA, volatile) {
			t.Fatalf("summary %q still contains volatile marker %q", summaryA, volatile)
		}
	}

	emptyA, emptySummaryA := dispatcher.FingerprintQGFailure("", dispatcher.QGFingerprintOptions{})
	emptyB, emptySummaryB := dispatcher.FingerprintQGFailure(" \n\t", dispatcher.QGFingerprintOptions{})
	if emptyA == "" || emptyA != emptyB || emptySummaryA != emptySummaryB {
		t.Fatalf("empty output fingerprint unstable: %q/%q vs %q/%q", emptyA, emptySummaryA, emptyB, emptySummaryB)
	}
	if !strings.Contains(emptySummaryA, "unknown") {
		t.Fatalf("empty summary = %q, want unknown", emptySummaryA)
	}

	hugeOutput := strings.Repeat("noise /tmp/worktree/pkg/foo.go:123 pid=1234 elapsed 55.2s\n", 10_000)
	hugeFP, hugeSummary := dispatcher.FingerprintQGFailure(hugeOutput, dispatcher.QGFingerprintOptions{MaxInputBytes: 1024})
	if hugeFP == "" || len(hugeSummary) > 512 {
		t.Fatalf("huge output produced fingerprint=%q summary length=%d", hugeFP, len(hugeSummary))
	}

	rawA, _ := dispatcher.FingerprintQGFailure("unstructured failure /tmp/a/file.go:12 pid=1", dispatcher.QGFingerprintOptions{})
	rawB, _ := dispatcher.FingerprintQGFailure("unstructured failure /tmp/b/file.go:99 pid=2", dispatcher.QGFingerprintOptions{})
	if rawA != rawB {
		t.Fatalf("unparsable output should hash normalized output: %q != %q", rawA, rawB)
	}
}

func TestQGFailureFingerprintStripsANSIEscapeCodes(t *testing.T) {
	// QG output as it appears in a terminal: ANSI color codes wrapping labels.
	// The elapsed-time normalization regex (`\b\d+m\b`) would corrupt ANSI
	// sequences like \033[0;34m if ANSI is not stripped first, producing
	// different fingerprints for identical logical content.
	withANSI := "\033[0;34m\xe2\x96\xb6\033[0m golangci-lint                  \033[0;31m\xe2\x9c\x97 FAIL\033[0m\n" +
		"\033[0;34m\xe2\x96\xb6\033[0m pytest                         \033[0;31m\xe2\x9c\x97 FAIL\033[0m\n" +
		"\033[0;31mFailed:\033[0m 3\n" +
		"\033[0;31mQuality gate FAILED\033[0m\n"

	withoutANSI := "▶ golangci-lint                  ✗ FAIL\n" +
		"▶ pytest                         ✗ FAIL\n" +
		"Failed: 3\n" +
		"Quality gate FAILED\n"

	fpWith, summaryWith := dispatcher.FingerprintQGFailure(withANSI, dispatcher.QGFingerprintOptions{})
	fpWithout, _ := dispatcher.FingerprintQGFailure(withoutANSI, dispatcher.QGFingerprintOptions{})

	if fpWith == "" {
		t.Fatal("fingerprint is empty for ANSI output")
	}
	if fpWith != fpWithout {
		t.Fatalf("fingerprints differ with vs without ANSI escape codes:\nwith    = %q\nwithout = %q", fpWith, fpWithout)
	}
	if strings.ContainsRune(summaryWith, '\033') {
		t.Fatalf("summary contains raw ESC byte: %q", summaryWith)
	}
}

func TestClassifyQGFailureDecisionMatrix(t *testing.T) {
	tests := []struct {
		name         string
		record       dispatcher.QGFailureRecord
		history      dispatcher.QGFailureHistory
		wantClass    dispatcher.QGFailureClass
		wantDecision dispatcher.QGFailureDecision
	}{
		{
			name: "deterministic retries original",
			record: dispatcher.QGFailureRecord{
				Output:  "--- FAIL: TestAcceptance\npkg/worker/foo_test.go:42: got false want true",
				Summary: "FAIL pkg/worker TestAcceptance",
			},
			wantClass:    dispatcher.QGFailureClassWorkerDeterministic,
			wantDecision: dispatcher.QGFailureDecisionRetryOriginal,
		},
		{
			name: "deterministic exhausted reopens original",
			record: dispatcher.QGFailureRecord{
				Output: "golangci-lint failed: pkg/foo/foo.go:12: unused variable",
			},
			history:      dispatcher.QGFailureHistory{RetryExhausted: true},
			wantClass:    dispatcher.QGFailureClassWorkerDeterministic,
			wantDecision: dispatcher.QGFailureDecisionReopenOriginal,
		},
		{
			name: "systemic cross bead creates infra",
			record: dispatcher.QGFailureRecord{
				Fingerprint: "qg:loader",
				Output:      "package loader failure: cannot load stdlib",
			},
			history:      dispatcher.QGFailureHistory{AffectedBeads: 3},
			wantClass:    dispatcher.QGFailureClassSystemic,
			wantDecision: dispatcher.QGFailureDecisionCreateOrReuseInfra,
		},
		{
			name: "memory telemetry schema failure creates infra",
			record: dispatcher.QGFailureRecord{
				Output: "memory: telemetry write failed: insert memory_search_events: SQL logic error: no such table: memory_search_events",
			},
			wantClass:    dispatcher.QGFailureClassSystemic,
			wantDecision: dispatcher.QGFailureDecisionCreateOrReuseInfra,
		},
		{
			name: "known flaky backs off",
			record: dispatcher.QGFailureRecord{
				Output: "race detected under parallel load",
			},
			history:      dispatcher.QGFailureHistory{KnownFlaky: true},
			wantClass:    dispatcher.QGFailureClassFlaky,
			wantDecision: dispatcher.QGFailureDecisionBackoffRetry,
		},
		{
			name: "transient backs off",
			record: dispatcher.QGFailureRecord{
				Output: "network timeout while downloading module",
			},
			wantClass:    dispatcher.QGFailureClassTransient,
			wantDecision: dispatcher.QGFailureDecisionBackoffRetry,
		},
		{
			name: "impossible bumps original",
			record: dispatcher.QGFailureRecord{
				Output: "missing acceptance criteria: no Cmd field",
			},
			wantClass:    dispatcher.QGFailureClassImpossible,
			wantDecision: dispatcher.QGFailureDecisionBumpOriginal,
		},
		{
			name: "unknown stops for triage",
			record: dispatcher.QGFailureRecord{
				Output: "something odd happened",
			},
			wantClass:    dispatcher.QGFailureClassUnknown,
			wantDecision: dispatcher.QGFailureDecisionStopForTriage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dispatcher.ClassifyQGFailure(tt.record, tt.history, dispatcher.QGFailureAttribution{})
			if got.Class != tt.wantClass || got.Decision != tt.wantDecision {
				t.Fatalf("ClassifyQGFailure() = class=%q decision=%q, want class=%q decision=%q; reason=%q",
					got.Class, got.Decision, tt.wantClass, tt.wantDecision, got.Reason)
			}
			if got.Confidence == "" || got.Reason == "" {
				t.Fatalf("classification missing confidence/reason: %+v", got)
			}
			if got.Decision == dispatcher.QGFailureDecisionStopForTriage && got.Confidence != dispatcher.QGFailureConfidenceLow {
				t.Fatalf("triage confidence = %q, want low", got.Confidence)
			}
		})
	}
}

func TestClassifyQGFailureTargetBaselineAttribution(t *testing.T) {
	reviveFailure := "▶ revive                         ✗ FAIL\n" +
		"pkg/remotegate/types_test.go:130:6: avoid package-level name `delete` (builtinShadow)"
	fingerprint, _ := dispatcher.FingerprintQGFailure(reviveFailure, dispatcher.QGFingerprintOptions{})

	tests := []struct {
		name         string
		record       dispatcher.QGFailureRecord
		history      dispatcher.QGFailureHistory
		attribution  dispatcher.QGFailureAttribution
		wantClass    dispatcher.QGFailureClass
		wantDecision dispatcher.QGFailureDecision
	}{
		{
			name: "candidate is target baseline",
			record: dispatcher.QGFailureRecord{
				Fingerprint: fingerprint,
				Output:      reviveFailure,
			},
			attribution: dispatcher.QGFailureAttribution{
				CandidateSHA: "target-sha",
				TargetSHA:    "target-sha",
				TargetKnown:  true,
			},
			wantClass:    dispatcher.QGFailureClassSystemic,
			wantDecision: dispatcher.QGFailureDecisionCreateOrReuseInfra,
		},
		{
			name: "candidate matches target failure fingerprint",
			record: dispatcher.QGFailureRecord{
				Fingerprint: fingerprint,
				Output:      reviveFailure,
			},
			attribution: dispatcher.QGFailureAttribution{
				CandidateSHA:      "candidate-sha",
				TargetSHA:         "target-sha",
				TargetFingerprint: fingerprint,
				TargetKnown:       true,
			},
			wantClass:    dispatcher.QGFailureClassSystemic,
			wantDecision: dispatcher.QGFailureDecisionCreateOrReuseInfra,
		},
		{
			name: "candidate-only deterministic failure overrides cross-bead history",
			record: dispatcher.QGFailureRecord{
				Fingerprint: fingerprint,
				Output:      reviveFailure,
			},
			history: dispatcher.QGFailureHistory{AffectedBeads: 3},
			attribution: dispatcher.QGFailureAttribution{
				CandidateSHA: "candidate-sha",
				TargetSHA:    "target-sha",
				TargetKnown:  true,
				TargetPassed: true,
			},
			wantClass:    dispatcher.QGFailureClassWorkerDeterministic,
			wantDecision: dispatcher.QGFailureDecisionRetryOriginal,
		},
		{
			name: "unknown target evidence preserves conservative policy",
			record: dispatcher.QGFailureRecord{
				Fingerprint: fingerprint,
				Output:      reviveFailure,
			},
			history:      dispatcher.QGFailureHistory{AffectedBeads: 3},
			wantClass:    dispatcher.QGFailureClassSystemic,
			wantDecision: dispatcher.QGFailureDecisionCreateOrReuseInfra,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dispatcher.ClassifyQGFailure(tt.record, tt.history, tt.attribution)
			if got.Class != tt.wantClass || got.Decision != tt.wantDecision {
				t.Fatalf("ClassifyQGFailure() = class=%q decision=%q reason=%q, want class=%q decision=%q",
					got.Class, got.Decision, got.Reason, tt.wantClass, tt.wantDecision)
			}
		})
	}
}

func TestClassifyNilAwaySourceDiagnosticDeterministic(t *testing.T) {
	output := `▶ nilaway                       ✗ FAIL
pkg/dispatcher/presubmit.go:141:24: Potential nil panic detected`

	got := dispatcher.ClassifyQGFailure(dispatcher.QGFailureRecord{Output: output}, dispatcher.QGFailureHistory{}, dispatcher.QGFailureAttribution{})
	if got.Class != dispatcher.QGFailureClassWorkerDeterministic ||
		got.Decision != dispatcher.QGFailureDecisionRetryOriginal ||
		got.Confidence != dispatcher.QGFailureConfidenceHigh {
		t.Fatalf("classification = %+v, want worker_deterministic/retry_original/high", got)
	}
}

func TestClassifyNilAwayWithoutSourceDiagnosticPreservesExistingRules(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		wantClass    dispatcher.QGFailureClass
		wantDecision dispatcher.QGFailureDecision
	}{
		{
			name:         "tool-only summary remains unknown",
			output:       "▶ nilaway ✗ FAIL\nQuality gate FAILED",
			wantClass:    dispatcher.QGFailureClassUnknown,
			wantDecision: dispatcher.QGFailureDecisionStopForTriage,
		},
		{
			name:         "infrastructure failure remains systemic",
			output:       "▶ nilaway ✗ FAIL\npackage loader: cannot load stdlib",
			wantClass:    dispatcher.QGFailureClassSystemic,
			wantDecision: dispatcher.QGFailureDecisionCreateOrReuseInfra,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dispatcher.ClassifyQGFailure(dispatcher.QGFailureRecord{Output: tt.output}, dispatcher.QGFailureHistory{}, dispatcher.QGFailureAttribution{})
			if got.Class != tt.wantClass || got.Decision != tt.wantDecision {
				t.Fatalf("classification = %+v, want class=%q decision=%q", got, tt.wantClass, tt.wantDecision)
			}
		})
	}
}

func TestClassifyQGFailureDeterministicMarkerWinsOverTimeoutText(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		wantClass    dispatcher.QGFailureClass
		wantDecision dispatcher.QGFailureDecision
	}{
		{
			name: "go test failure with timeout-bearing name and flag is deterministic",
			output: `▶ go test + coverage ✗ FAIL
--- FAIL: TestProgressTimeoutReapsWedgedNonBusyWorker (60.00s)
    progress_timeout_test.go:87: worker remained tracked after the deadline
FAIL	oro/pkg/dispatcher	60.123s
go test ./pkg/dispatcher -run TestProgressTimeoutReapsWedgedNonBusyWorker -timeout 60s`,
			wantClass:    dispatcher.QGFailureClassWorkerDeterministic,
			wantDecision: dispatcher.QGFailureDecisionRetryOriginal,
		},
		{
			name: "network timeout remains transient despite formatter tool pass lines",
			output: `▶ gofumpt ✓ PASS
▶ goimports ✓ PASS
▶ go test + coverage ✗ FAIL
go: downloading example.com/module v1.2.3
Get "https://proxy.example.com/example.com/module/@v/v1.2.3.zip": net/http: TLS handshake timeout`,
			wantClass:    dispatcher.QGFailureClassTransient,
			wantDecision: dispatcher.QGFailureDecisionBackoffRetry,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dispatcher.ClassifyQGFailure(dispatcher.QGFailureRecord{Output: tt.output}, dispatcher.QGFailureHistory{}, dispatcher.QGFailureAttribution{})
			if got.Class != tt.wantClass || got.Decision != tt.wantDecision {
				t.Fatalf("ClassifyQGFailure() = class=%q decision=%q reason=%q, want class=%q decision=%q",
					got.Class, got.Decision, got.Reason, tt.wantClass, tt.wantDecision)
			}
		})
	}
}

func TestClassifyRepeatedQGPatternsFromThroughputRun(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		history      dispatcher.QGFailureHistory
		wantClass    dispatcher.QGFailureClass
		wantDecision dispatcher.QGFailureDecision
	}{
		{
			name: "priority contention repeated across beads is systemic",
			output: `--- FAIL: TestPriorityContention (30.00s)
dispatcher_test.go:123: timed out waiting for worker under contention
FAIL oro/pkg/dispatcher`,
			history:      dispatcher.QGFailureHistory{AffectedBeads: 2},
			wantClass:    dispatcher.QGFailureClassSystemic,
			wantDecision: dispatcher.QGFailureDecisionCreateOrReuseInfra,
		},
		{
			name: "consolidation repeated under load is flaky",
			output: `--- FAIL: TestConsolidation (15.00s)
dispatcher_test.go:456: throughput consolidation failed under parallel load
FAIL oro/pkg/dispatcher`,
			wantClass:    dispatcher.QGFailureClassFlaky,
			wantDecision: dispatcher.QGFailureDecisionBackoffRetry,
		},
		{
			name: "tmp-test yamllint is source scoped infrastructure",
			output: `.tmp-test/session/generated.yaml
  1:1       error    too many blank lines  (empty-lines)
yamllint failed while scanning .tmp-test generated artifacts`,
			wantClass:    dispatcher.QGFailureClassSystemic,
			wantDecision: dispatcher.QGFailureDecisionCreateOrReuseInfra,
		},
		{
			name: "gofumpt remains worker deterministic",
			output: `gofumpt failed:
pkg/dispatcher/foo.go
Run gofumpt -w pkg/dispatcher/foo.go`,
			wantClass:    dispatcher.QGFailureClassWorkerDeterministic,
			wantDecision: dispatcher.QGFailureDecisionRetryOriginal,
		},
		{
			name: "goimports remains worker deterministic",
			output: `goimports failed:
pkg/dispatcher/foo.go imports are not sorted`,
			wantClass:    dispatcher.QGFailureClassWorkerDeterministic,
			wantDecision: dispatcher.QGFailureDecisionRetryOriginal,
		},
		{
			name: "unexpected subprocess death retries original even when repeated",
			output: `reason: subprocess_died
runtime: claude
model: sonnet
exit_code: 137
signal: killed`,
			history:      dispatcher.QGFailureHistory{AffectedBeads: 3},
			wantClass:    dispatcher.QGFailureClass("worker_transient"),
			wantDecision: dispatcher.QGFailureDecisionRetryOriginal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dispatcher.ClassifyQGFailure(dispatcher.QGFailureRecord{Output: tt.output}, tt.history, dispatcher.QGFailureAttribution{})
			if got.Class != tt.wantClass || got.Decision != tt.wantDecision {
				t.Fatalf("ClassifyQGFailure() = class=%q decision=%q reason=%q, want class=%q decision=%q",
					got.Class, got.Decision, got.Reason, tt.wantClass, tt.wantDecision)
			}
		})
	}
}
