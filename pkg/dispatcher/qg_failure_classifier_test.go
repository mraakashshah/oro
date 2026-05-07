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
