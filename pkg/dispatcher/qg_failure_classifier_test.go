package dispatcher //nolint:testpackage // white-box: target attribution requires private durable-state fixtures

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"oro/pkg/protocol"
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

	fpA, summaryA := FingerprintQGFailure(outputA, QGFingerprintOptions{})
	fpB, summaryB := FingerprintQGFailure(outputB, QGFingerprintOptions{})
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

	emptyA, emptySummaryA := FingerprintQGFailure("", QGFingerprintOptions{})
	emptyB, emptySummaryB := FingerprintQGFailure(" \n\t", QGFingerprintOptions{})
	if emptyA == "" || emptyA != emptyB || emptySummaryA != emptySummaryB {
		t.Fatalf("empty output fingerprint unstable: %q/%q vs %q/%q", emptyA, emptySummaryA, emptyB, emptySummaryB)
	}
	if !strings.Contains(emptySummaryA, "unknown") {
		t.Fatalf("empty summary = %q, want unknown", emptySummaryA)
	}

	hugeOutput := strings.Repeat("noise /tmp/worktree/pkg/foo.go:123 pid=1234 elapsed 55.2s\n", 10_000)
	hugeFP, hugeSummary := FingerprintQGFailure(hugeOutput, QGFingerprintOptions{MaxInputBytes: 1024})
	if hugeFP == "" || len(hugeSummary) > 512 {
		t.Fatalf("huge output produced fingerprint=%q summary length=%d", hugeFP, len(hugeSummary))
	}

	rawA, _ := FingerprintQGFailure("unstructured failure /tmp/a/file.go:12 pid=1", QGFingerprintOptions{})
	rawB, _ := FingerprintQGFailure("unstructured failure /tmp/b/file.go:99 pid=2", QGFingerprintOptions{})
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

	fpWith, summaryWith := FingerprintQGFailure(withANSI, QGFingerprintOptions{})
	fpWithout, _ := FingerprintQGFailure(withoutANSI, QGFingerprintOptions{})

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
		record       QGFailureRecord
		history      QGFailureHistory
		wantClass    QGFailureClass
		wantDecision QGFailureDecision
	}{
		{
			name: "deterministic retries original",
			record: QGFailureRecord{
				Output:  "--- FAIL: TestAcceptance\npkg/worker/foo_test.go:42: got false want true",
				Summary: "FAIL pkg/worker TestAcceptance",
			},
			wantClass:    QGFailureClassWorkerDeterministic,
			wantDecision: QGFailureDecisionRetryOriginal,
		},
		{
			name: "deterministic exhausted reopens original",
			record: QGFailureRecord{
				Output: "golangci-lint failed: pkg/foo/foo.go:12: unused variable",
			},
			history:      QGFailureHistory{RetryExhausted: true},
			wantClass:    QGFailureClassWorkerDeterministic,
			wantDecision: QGFailureDecisionReopenOriginal,
		},
		{
			name: "systemic cross bead creates infra",
			record: QGFailureRecord{
				Fingerprint: "qg:loader",
				Output:      "package loader failure: cannot load stdlib",
			},
			history:      QGFailureHistory{AffectedBeads: 3},
			wantClass:    QGFailureClassSystemic,
			wantDecision: QGFailureDecisionCreateOrReuseInfra,
		},
		{
			name: "memory telemetry schema failure creates infra",
			record: QGFailureRecord{
				Output: "memory: telemetry write failed: insert memory_search_events: SQL logic error: no such table: memory_search_events",
			},
			wantClass:    QGFailureClassSystemic,
			wantDecision: QGFailureDecisionCreateOrReuseInfra,
		},
		{
			name: "known flaky backs off",
			record: QGFailureRecord{
				Output: "race detected under parallel load",
			},
			history:      QGFailureHistory{KnownFlaky: true},
			wantClass:    QGFailureClassFlaky,
			wantDecision: QGFailureDecisionBackoffRetry,
		},
		{
			name: "transient backs off",
			record: QGFailureRecord{
				Output: "network timeout while downloading module",
			},
			wantClass:    QGFailureClassTransient,
			wantDecision: QGFailureDecisionBackoffRetry,
		},
		{
			name: "impossible bumps original",
			record: QGFailureRecord{
				Output: "missing acceptance criteria: no Cmd field",
			},
			wantClass:    QGFailureClassImpossible,
			wantDecision: QGFailureDecisionBumpOriginal,
		},
		{
			name: "unknown stops for triage",
			record: QGFailureRecord{
				Output: "something odd happened",
			},
			wantClass:    QGFailureClassUnknown,
			wantDecision: QGFailureDecisionStopForTriage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyQGFailure(tt.record, tt.history, QGFailureAttribution{})
			if got.Class != tt.wantClass || got.Decision != tt.wantDecision {
				t.Fatalf("ClassifyQGFailure() = class=%q decision=%q, want class=%q decision=%q; reason=%q",
					got.Class, got.Decision, tt.wantClass, tt.wantDecision, got.Reason)
			}
			if got.Confidence == "" || got.Reason == "" {
				t.Fatalf("classification missing confidence/reason: %+v", got)
			}
			if got.Decision == QGFailureDecisionStopForTriage && got.Confidence != QGFailureConfidenceLow {
				t.Fatalf("triage confidence = %q, want low", got.Confidence)
			}
		})
	}
}

func TestClassifyQGFailureTargetBaselineAttribution(t *testing.T) {
	reviveFailure := "▶ revive                         ✗ FAIL\n" +
		"pkg/remotegate/types_test.go:130:6: avoid package-level name `delete` (builtinShadow)"
	fingerprint, _ := FingerprintQGFailure(reviveFailure, QGFingerprintOptions{})
	t.Run("omitted attribution preserves conservative API", func(t *testing.T) {
		got := ClassifyQGFailure(
			QGFailureRecord{Fingerprint: fingerprint, Output: reviveFailure},
			QGFailureHistory{AffectedBeads: 3},
		)
		if got.Class != QGFailureClassSystemic || got.Decision != QGFailureDecisionCreateOrReuseInfra {
			t.Fatalf("ClassifyQGFailure() = class=%q decision=%q reason=%q, want systemic/create_or_reuse_infra",
				got.Class, got.Decision, got.Reason)
		}
	})

	tests := []struct {
		name         string
		record       QGFailureRecord
		history      QGFailureHistory
		attribution  QGFailureAttribution
		wantClass    QGFailureClass
		wantDecision QGFailureDecision
	}{
		{
			name: "candidate is target baseline",
			record: QGFailureRecord{
				Fingerprint: fingerprint,
				Output:      reviveFailure,
			},
			attribution: QGFailureAttribution{
				CandidateSHA: "target-sha",
				TargetSHA:    "target-sha",
				TargetKnown:  true,
			},
			wantClass:    QGFailureClassSystemic,
			wantDecision: QGFailureDecisionCreateOrReuseInfra,
		},
		{
			name: "candidate matches target failure fingerprint",
			record: QGFailureRecord{
				Fingerprint: fingerprint,
				Output:      reviveFailure,
			},
			attribution: QGFailureAttribution{
				CandidateSHA:      "candidate-sha",
				TargetSHA:         "target-sha",
				TargetFingerprint: fingerprint,
				TargetKnown:       true,
			},
			wantClass:    QGFailureClassSystemic,
			wantDecision: QGFailureDecisionCreateOrReuseInfra,
		},
		{
			name: "candidate-only deterministic failure overrides cross-bead history",
			record: QGFailureRecord{
				Fingerprint: fingerprint,
				Output:      reviveFailure,
			},
			history: QGFailureHistory{AffectedBeads: 3},
			attribution: QGFailureAttribution{
				CandidateSHA: "candidate-sha",
				TargetSHA:    "target-sha",
				TargetKnown:  true,
				TargetPassed: true,
			},
			wantClass:    QGFailureClassWorkerDeterministic,
			wantDecision: QGFailureDecisionRetryOriginal,
		},
		{
			name: "unknown target evidence preserves conservative policy",
			record: QGFailureRecord{
				Fingerprint: fingerprint,
				Output:      reviveFailure,
			},
			history:      QGFailureHistory{AffectedBeads: 3},
			wantClass:    QGFailureClassSystemic,
			wantDecision: QGFailureDecisionCreateOrReuseInfra,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyQGFailure(tt.record, tt.history, tt.attribution)
			if got.Class != tt.wantClass || got.Decision != tt.wantDecision {
				t.Fatalf("ClassifyQGFailure() = class=%q decision=%q reason=%q, want class=%q decision=%q",
					got.Class, got.Decision, got.Reason, tt.wantClass, tt.wantDecision)
			}
		})
	}

	t.Run("accepted exact target pass reaches evaluation", func(t *testing.T) {
		ctx := context.Background()
		d, ready, readyWorkerID, _, _ := newCanonicalReadyAdmissionTest(t, "")
		worktrees := d.worktrees.(*mockWorktreeManager)
		worktrees.branchHeadFn = func(string) (string, error) {
			return ready.TargetSHA, nil
		}
		if _, accepted := d.acceptReadyEvidence(ctx, readyWorkerID, &ready); !accepted {
			t.Fatal("canonical READY was not accepted")
		}
		if _, err := d.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state = ?
WHERE head_sha = ? AND target_sha = ?`,
			ReviewCheckpointStateIntegrated, ready.TargetSHA, ready.TargetSHA); err != nil {
			t.Fatalf("advance accepted target checkpoint lifecycle: %v", err)
		}
		d.mu.Lock()
		d.qgTargetObservations = make(map[string]qgTargetObservation)
		d.mu.Unlock()

		seedCrossBeadQGHistory(t, d, fingerprint, reviveFailure)

		const candidateWorkerID = "candidate-worker"
		d.mu.Lock()
		d.workers[candidateWorkerID] = &trackedWorker{
			id:        candidateWorkerID,
			state:     protocol.WorkerBusy,
			beadID:    "candidate-bead",
			worktree:  t.TempDir(),
			targetSHA: ready.TargetSHA,
		}
		d.mu.Unlock()
		d.shutdownRunner = &mockCommandRunner{output: []byte("candidate-sha\n")}
		d.qgRunner = &mockQGRunner{}

		evaluation := d.evaluateQGFailure(ctx, candidateWorkerID, "candidate-bead", reviveFailure)
		if evaluation.classification.Class != QGFailureClassWorkerDeterministic ||
			evaluation.classification.Decision != QGFailureDecisionRetryOriginal {
			t.Fatalf("evaluation classification = %+v, want worker_deterministic/retry_original from accepted target pass",
				evaluation.classification)
		}
		if got := len(d.qgRunner.(*mockQGRunner).calls); got != 0 {
			t.Fatalf("evaluation reran target QG %d times, want 0", got)
		}
	})

	t.Run("mismatched ready head preserves conservative policy", func(t *testing.T) {
		ctx := context.Background()
		d, ready, readyWorkerID, _, _ := newCanonicalReadyAdmissionTest(t, "")
		worktrees := d.worktrees.(*mockWorktreeManager)
		worktrees.branchHeadFn = func(string) (string, error) {
			return "different-ready-head", nil
		}
		if _, accepted := d.acceptReadyEvidence(ctx, readyWorkerID, &ready); !accepted {
			t.Fatal("canonical READY was not accepted")
		}
		seedCrossBeadQGHistory(t, d, fingerprint, reviveFailure)

		const candidateWorkerID = "mismatched-ready-candidate"
		d.mu.Lock()
		d.workers[candidateWorkerID] = &trackedWorker{
			id:        candidateWorkerID,
			state:     protocol.WorkerBusy,
			beadID:    "mismatched-ready-bead",
			worktree:  t.TempDir(),
			targetSHA: ready.TargetSHA,
		}
		d.mu.Unlock()
		d.shutdownRunner = &mockCommandRunner{output: []byte("candidate-sha\n")}
		d.qgRunner = &mockQGRunner{}

		evaluation := d.evaluateQGFailure(ctx, candidateWorkerID, "mismatched-ready-bead", reviveFailure)
		if evaluation.classification.Class != QGFailureClassSystemic ||
			evaluation.classification.Decision != QGFailureDecisionCreateOrReuseInfra {
			t.Fatalf("evaluation classification = %+v, want conservative systemic/create_or_reuse_infra",
				evaluation.classification)
		}
		if got := len(d.qgRunner.(*mockQGRunner).calls); got != 0 {
			t.Fatalf("evaluation reran target QG %d times, want 0", got)
		}
	})

	t.Run("exact target failure fingerprint is reused", func(t *testing.T) {
		ctx := context.Background()
		d, _, _, _, _, _ := newTestDispatcher(t)
		const (
			workerID  = "target-failure-worker"
			targetSHA = "target-failure-sha"
		)
		d.workers[workerID] = &trackedWorker{
			id: workerID, state: protocol.WorkerBusy, beadID: "target-failure-bead",
			worktree: t.TempDir(), targetSHA: targetSHA,
		}
		runner := &mockCommandRunner{output: []byte(targetSHA + "\n")}
		d.shutdownRunner = runner
		d.qgRunner = &mockQGRunner{}

		first := d.evaluateQGFailure(ctx, workerID, "target-failure-bead", reviveFailure)
		if first.classification.Class != QGFailureClassSystemic {
			t.Fatalf("exact-target classification = %+v, want systemic", first.classification)
		}
		runner.output = []byte("distinct-candidate-sha\n")
		second := d.evaluateQGFailure(ctx, workerID, "target-failure-bead", reviveFailure)
		if second.classification.Class != QGFailureClassSystemic ||
			second.classification.Decision != QGFailureDecisionCreateOrReuseInfra {
			t.Fatalf("reused target fingerprint classification = %+v, want systemic/create_or_reuse_infra",
				second.classification)
		}
		if got := len(d.qgRunner.(*mockQGRunner).calls); got != 0 {
			t.Fatalf("evaluation reran target QG %d times, want 0", got)
		}
	})

	t.Run("exact target failure bypasses retry reservation", func(t *testing.T) {
		ctx := context.Background()
		d, beadSource, _, _, _, _ := newTestDispatcher(t)
		const (
			beadID    = "exact-target-no-retry"
			workerID  = "exact-target-worker"
			targetSHA = "exact-target-sha"
		)
		worktree := t.TempDir()
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
		beadSource.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		conn := newMockConn()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id: workerID, conn: conn, encoder: json.NewEncoder(conn),
			state: protocol.WorkerBusy, beadID: beadID, assignmentID: assignmentID,
			worktree: worktree, targetSHA: targetSHA,
		}
		d.mu.Unlock()
		d.shutdownRunner = &mockCommandRunner{output: []byte(targetSHA + "\n")}

		d.handleDone(ctx, workerID, protocol.Message{Done: &protocol.DonePayload{
			BeadID: beadID, WorkerID: workerID, QualityGatePassed: false, QGOutput: reviveFailure,
		}})

		d.mu.Lock()
		attempts := d.attemptCounts[beadID]
		d.mu.Unlock()
		conn.mu.Lock()
		writes := len(conn.written)
		conn.mu.Unlock()
		if attempts != 0 || writes != 0 {
			t.Fatalf("exact-target retry side effects = attempts %d, ASSIGN writes %d; want 0/0", attempts, writes)
		}
		var assignmentStatus string
		if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
			t.Fatalf("read assignment status: %v", err)
		}
		if assignmentStatus != "completed" {
			t.Fatalf("assignment status = %q, want completed by infra routing", assignmentStatus)
		}
	})

	t.Run("passing target candidate still reserves retry", func(t *testing.T) {
		ctx := context.Background()
		d, ready, readyWorkerID, _, _ := newCanonicalReadyAdmissionTest(t, "")
		worktrees := d.worktrees.(*mockWorktreeManager)
		worktrees.branchHeadFn = func(string) (string, error) {
			return ready.TargetSHA, nil
		}
		if _, accepted := d.acceptReadyEvidence(ctx, readyWorkerID, &ready); !accepted {
			t.Fatal("canonical READY was not accepted")
		}
		seedCrossBeadQGHistory(t, d, fingerprint, reviveFailure)

		const (
			beadID   = "passing-target-retry"
			workerID = "passing-target-worker"
		)
		worktree := t.TempDir()
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
		beadSource := d.beads.(*fakeBeadStore)
		beadSource.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		conn := newMockConn()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id: workerID, conn: conn, encoder: json.NewEncoder(conn),
			state: protocol.WorkerBusy, beadID: beadID, assignmentID: assignmentID,
			worktree: worktree, targetSHA: ready.TargetSHA,
		}
		d.mu.Unlock()
		d.shutdownRunner = &mockCommandRunner{output: []byte("distinct-candidate-sha\n")}

		d.handleDone(ctx, workerID, protocol.Message{Done: &protocol.DonePayload{
			BeadID: beadID, WorkerID: workerID, QualityGatePassed: false, QGOutput: reviveFailure,
		}})

		d.mu.Lock()
		attempts := d.attemptCounts[beadID]
		d.mu.Unlock()
		conn.mu.Lock()
		writes := len(conn.written)
		conn.mu.Unlock()
		if attempts != 1 || writes != 1 {
			t.Fatalf("candidate retry side effects = attempts %d, ASSIGN writes %d; want 1/1", attempts, writes)
		}
	})
}

func seedCrossBeadQGHistory(t *testing.T, d *Dispatcher, fingerprint, output string) {
	t.Helper()
	classification := qgClassification(
		QGFailureClassSystemic,
		QGFailureDecisionCreateOrReuseInfra,
		QGFailureConfidenceHigh,
		"prior cross-bead observation",
	)
	for _, priorBeadID := range []string{"prior-bead-a", "prior-bead-b"} {
		if _, err := RecordQGFailureOccurrence(context.Background(), d.db, QGFailureRecord{
			ID:          "occurrence-" + priorBeadID,
			BeadID:      priorBeadID,
			WorkerID:    "prior-worker",
			Fingerprint: fingerprint,
			Output:      output,
		}, classification); err != nil {
			t.Fatalf("record prior QG failure for %s: %v", priorBeadID, err)
		}
	}
}

func TestClassifyNilAwaySourceDiagnosticDeterministic(t *testing.T) {
	output := `▶ nilaway                       ✗ FAIL
pkg/dispatcher/presubmit.go:141:24: Potential nil panic detected`

	got := ClassifyQGFailure(QGFailureRecord{Output: output}, QGFailureHistory{}, QGFailureAttribution{})
	if got.Class != QGFailureClassWorkerDeterministic ||
		got.Decision != QGFailureDecisionRetryOriginal ||
		got.Confidence != QGFailureConfidenceHigh {
		t.Fatalf("classification = %+v, want worker_deterministic/retry_original/high", got)
	}
}

func TestClassifyNilAwayWithoutSourceDiagnosticPreservesExistingRules(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		wantClass    QGFailureClass
		wantDecision QGFailureDecision
	}{
		{
			name:         "tool-only summary remains unknown",
			output:       "▶ nilaway ✗ FAIL\nQuality gate FAILED",
			wantClass:    QGFailureClassUnknown,
			wantDecision: QGFailureDecisionStopForTriage,
		},
		{
			name:         "infrastructure failure remains systemic",
			output:       "▶ nilaway ✗ FAIL\npackage loader: cannot load stdlib",
			wantClass:    QGFailureClassSystemic,
			wantDecision: QGFailureDecisionCreateOrReuseInfra,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyQGFailure(QGFailureRecord{Output: tt.output}, QGFailureHistory{}, QGFailureAttribution{})
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
		wantClass    QGFailureClass
		wantDecision QGFailureDecision
	}{
		{
			name: "go test failure with timeout-bearing name and flag is deterministic",
			output: `▶ go test + coverage ✗ FAIL
--- FAIL: TestProgressTimeoutReapsWedgedNonBusyWorker (60.00s)
    progress_timeout_test.go:87: worker remained tracked after the deadline
FAIL	oro/pkg/dispatcher	60.123s
go test ./pkg/dispatcher -run TestProgressTimeoutReapsWedgedNonBusyWorker -timeout 60s`,
			wantClass:    QGFailureClassWorkerDeterministic,
			wantDecision: QGFailureDecisionRetryOriginal,
		},
		{
			name: "network timeout remains transient despite formatter tool pass lines",
			output: `▶ gofumpt ✓ PASS
▶ goimports ✓ PASS
▶ go test + coverage ✗ FAIL
go: downloading example.com/module v1.2.3
Get "https://proxy.example.com/example.com/module/@v/v1.2.3.zip": net/http: TLS handshake timeout`,
			wantClass:    QGFailureClassTransient,
			wantDecision: QGFailureDecisionBackoffRetry,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyQGFailure(QGFailureRecord{Output: tt.output}, QGFailureHistory{}, QGFailureAttribution{})
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
		history      QGFailureHistory
		wantClass    QGFailureClass
		wantDecision QGFailureDecision
	}{
		{
			name: "priority contention repeated across beads is systemic",
			output: `--- FAIL: TestPriorityContention (30.00s)
dispatcher_test.go:123: timed out waiting for worker under contention
FAIL oro/pkg/dispatcher`,
			history:      QGFailureHistory{AffectedBeads: 2},
			wantClass:    QGFailureClassSystemic,
			wantDecision: QGFailureDecisionCreateOrReuseInfra,
		},
		{
			name: "consolidation repeated under load is flaky",
			output: `--- FAIL: TestConsolidation (15.00s)
dispatcher_test.go:456: throughput consolidation failed under parallel load
FAIL oro/pkg/dispatcher`,
			wantClass:    QGFailureClassFlaky,
			wantDecision: QGFailureDecisionBackoffRetry,
		},
		{
			name: "tmp-test yamllint is source scoped infrastructure",
			output: `.tmp-test/session/generated.yaml
  1:1       error    too many blank lines  (empty-lines)
yamllint failed while scanning .tmp-test generated artifacts`,
			wantClass:    QGFailureClassSystemic,
			wantDecision: QGFailureDecisionCreateOrReuseInfra,
		},
		{
			name: "gofumpt remains worker deterministic",
			output: `gofumpt failed:
pkg/dispatcher/foo.go
Run gofumpt -w pkg/dispatcher/foo.go`,
			wantClass:    QGFailureClassWorkerDeterministic,
			wantDecision: QGFailureDecisionRetryOriginal,
		},
		{
			name: "goimports remains worker deterministic",
			output: `goimports failed:
pkg/dispatcher/foo.go imports are not sorted`,
			wantClass:    QGFailureClassWorkerDeterministic,
			wantDecision: QGFailureDecisionRetryOriginal,
		},
		{
			name: "unexpected subprocess death retries original even when repeated",
			output: `reason: subprocess_died
runtime: claude
model: sonnet
exit_code: 137
signal: killed`,
			history:      QGFailureHistory{AffectedBeads: 3},
			wantClass:    QGFailureClass("worker_transient"),
			wantDecision: QGFailureDecisionRetryOriginal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyQGFailure(QGFailureRecord{Output: tt.output}, tt.history, QGFailureAttribution{})
			if got.Class != tt.wantClass || got.Decision != tt.wantDecision {
				t.Fatalf("ClassifyQGFailure() = class=%q decision=%q reason=%q, want class=%q decision=%q",
					got.Class, got.Decision, got.Reason, tt.wantClass, tt.wantDecision)
			}
		})
	}
}
