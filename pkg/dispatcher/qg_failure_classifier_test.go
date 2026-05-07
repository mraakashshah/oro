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
			got := dispatcher.ClassifyQGFailure(tt.record, tt.history)
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
