package dispatcher //nolint:testpackage // focused mutation owners exercise private target-attribution state

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

type qgTargetAttributionMutationRunner struct {
	output []byte
	err    error
	name   string
	args   []string
}

func (r *qgTargetAttributionMutationRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return r.output, r.err
}

func newQGTargetAttributionMutationDispatcher(
	workerID, beadID, worktree, targetSHA string,
	runner *qgTargetAttributionMutationRunner,
) *Dispatcher {
	return &Dispatcher{
		shutdownRunner:       runner,
		qgTargetObservations: make(map[string]qgTargetObservation),
		WorkerPool: WorkerPool{workers: map[string]*trackedWorker{
			workerID: {
				id:           workerID,
				beadID:       beadID,
				assignmentID: 41,
				state:        protocol.WorkerBusy,
				worktree:     worktree,
				targetSHA:    targetSHA,
			},
		}},
	}
}

func TestQGIsDeterministicMutationOwner(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "go test marker", text: "--- fail: TestBroken", want: true},
		{name: "package fail marker", text: "header\nfail\toro/pkg/dispatcher", want: true},
		{name: "nilaway source diagnostic", text: "nilaway\npkg/example.go:12:3: potential nil panic detected", want: true},
		{name: "nilaway summary only", text: "nilaway failed without a source diagnostic", want: false},
		{name: "gofumpt failure", text: "gofumpt failed: file is not formatted", want: true},
		{name: "goimports error", text: "goimports error: imports are unsorted", want: true},
		{name: "golangci lint failure", text: "golangci-lint failed: revive", want: true},
		{name: "revive error", text: "revive error: builtinShadow", want: true},
		{name: "compile error", text: "compile error in package", want: true},
		{name: "compilation failed", text: "compilation failed for command", want: true},
		{name: "build failed", text: "build failed for binary", want: true},
		{name: "unused variable", text: "pkg/example.go: unused variable value", want: true},
		{name: "tool pass lines", text: "gofumpt pass\ngoimports pass\ngolangci-lint pass\nrevive pass", want: false},
		{name: "unrelated failure word", text: "prefailure marker", want: false},
		{name: "empty", text: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDeterministicQGFailure(tt.text); got != tt.want {
				t.Fatalf("isDeterministicQGFailure(%q) = %t, want %t", tt.text, got, tt.want)
			}
		})
	}
}

func TestQGFailureAttributionMutationOwner(t *testing.T) {
	const (
		workerID    = "mutation-worker"
		beadID      = "mutation-bead"
		worktree    = "/tmp/qg-target-attribution-mutation"
		targetSHA   = "target-sha"
		candidate   = "candidate-sha"
		fingerprint = "qg:target-attribution"
	)
	record := QGFailureRecord{Fingerprint: fingerprint}

	t.Run("missing worker", func(t *testing.T) {
		d := &Dispatcher{WorkerPool: WorkerPool{workers: make(map[string]*trackedWorker)}}
		if got := d.qgFailureAttribution(context.Background(), workerID, record); got != (QGFailureAttribution{}) {
			t.Fatalf("missing-worker attribution = %+v, want empty", got)
		}
	})

	for _, tt := range []struct {
		name      string
		worktree  string
		targetSHA string
	}{
		{name: "missing worktree", targetSHA: targetSHA},
		{name: "missing target", worktree: worktree},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &qgTargetAttributionMutationRunner{output: []byte(candidate + "\n")}
			d := newQGTargetAttributionMutationDispatcher(workerID, beadID, tt.worktree, tt.targetSHA, runner)
			if got := d.qgFailureAttribution(context.Background(), workerID, record); got != (QGFailureAttribution{}) {
				t.Fatalf("incomplete-worker attribution = %+v, want empty", got)
			}
			if runner.name != "" {
				t.Fatalf("incomplete worker invoked %q, want no git call", runner.name)
			}
		})
	}

	t.Run("git failure", func(t *testing.T) {
		runner := &qgTargetAttributionMutationRunner{err: errors.New("rev-parse failed")}
		d := newQGTargetAttributionMutationDispatcher(workerID, beadID, worktree, targetSHA, runner)
		if got := d.qgFailureAttribution(context.Background(), workerID, record); got != (QGFailureAttribution{}) {
			t.Fatalf("git-failure attribution = %+v, want empty", got)
		}
	})

	t.Run("empty candidate", func(t *testing.T) {
		runner := &qgTargetAttributionMutationRunner{output: []byte(" \n")}
		d := newQGTargetAttributionMutationDispatcher(workerID, beadID, worktree, targetSHA, runner)
		want := QGFailureAttribution{TargetSHA: targetSHA}
		if got := d.qgFailureAttribution(context.Background(), workerID, record); got != want {
			t.Fatalf("empty-candidate attribution = %+v, want %+v", got, want)
		}
	})

	t.Run("candidate is target", func(t *testing.T) {
		runner := &qgTargetAttributionMutationRunner{output: []byte("  " + targetSHA + "\n")}
		d := newQGTargetAttributionMutationDispatcher(workerID, beadID, worktree, targetSHA, runner)
		want := QGFailureAttribution{
			CandidateSHA: targetSHA, TargetSHA: targetSHA,
			TargetFingerprint: fingerprint, TargetKnown: true,
		}
		if got := d.qgFailureAttribution(context.Background(), workerID, record); got != want {
			t.Fatalf("exact-target attribution = %+v, want %+v", got, want)
		}
		observation := d.qgTargetObservations[targetSHA]
		if _, ok := observation.failureFingerprints[fingerprint]; !ok {
			t.Fatalf("exact target did not retain failure fingerprint: %+v", observation)
		}
		if runner.name != "git" || !reflect.DeepEqual(runner.args, []string{"-C", worktree, "rev-parse", "HEAD"}) {
			t.Fatalf("git call = %q %v, want git -C %s rev-parse HEAD", runner.name, runner.args, worktree)
		}
	})

	t.Run("unknown distinct candidate", func(t *testing.T) {
		runner := &qgTargetAttributionMutationRunner{output: []byte(candidate + "\n")}
		d := newQGTargetAttributionMutationDispatcher(workerID, beadID, worktree, targetSHA, runner)
		want := QGFailureAttribution{CandidateSHA: candidate, TargetSHA: targetSHA}
		if got := d.qgFailureAttribution(context.Background(), workerID, record); got != want {
			t.Fatalf("unknown-target attribution = %+v, want %+v", got, want)
		}
	})

	t.Run("cached passing target", func(t *testing.T) {
		runner := &qgTargetAttributionMutationRunner{output: []byte(candidate + "\n")}
		d := newQGTargetAttributionMutationDispatcher(workerID, beadID, worktree, targetSHA, runner)
		d.qgTargetObservations[targetSHA] = qgTargetObservation{
			passed:              true,
			failureFingerprints: map[string]struct{}{fingerprint: {}},
		}
		want := QGFailureAttribution{
			CandidateSHA: candidate, TargetSHA: targetSHA,
			TargetFingerprint: fingerprint, TargetKnown: true, TargetPassed: true,
		}
		if got := d.qgFailureAttribution(context.Background(), workerID, record); got != want {
			t.Fatalf("cached-target attribution = %+v, want %+v", got, want)
		}
	})

	t.Run("accepted target pass", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(`CREATE TABLE review_checkpoints (
			head_sha TEXT, target_sha TEXT, qg_evidence_path TEXT, qg_evidence_sha256 TEXT
		)`); err != nil {
			t.Fatalf("create review checkpoints: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO review_checkpoints VALUES (?, ?, ?, ?)`,
			targetSHA, targetSHA, "evidence.json", "sha256"); err != nil {
			t.Fatalf("insert accepted target evidence: %v", err)
		}
		runner := &qgTargetAttributionMutationRunner{output: []byte(candidate + "\n")}
		d := newQGTargetAttributionMutationDispatcher(workerID, beadID, worktree, targetSHA, runner)
		d.db = db
		want := QGFailureAttribution{
			CandidateSHA: candidate, TargetSHA: targetSHA, TargetKnown: true, TargetPassed: true,
		}
		if got := d.qgFailureAttribution(context.Background(), workerID, record); got != want {
			t.Fatalf("accepted-target attribution = %+v, want %+v", got, want)
		}
		if !d.qgTargetObservations[targetSHA].passed {
			t.Fatal("accepted target pass was not cached")
		}
	})
}

func TestQGEvaluateFailureMutationOwner(t *testing.T) {
	const (
		workerID  = "flow-worker"
		beadID    = "flow-bead"
		worktree  = "/tmp/qg-target-attribution-flow"
		targetSHA = "flow-target"
	)
	qgOutput := "revive failed: pkg/example.go:12: builtinShadow"

	tests := []struct {
		name         string
		candidateSHA string
		configure    func(*Dispatcher)
		wantClass    QGFailureClass
		wantDecision QGFailureDecision
		wantKnown    bool
		wantPassed   bool
		wantBaseline bool
	}{
		{
			name: "candidate is exact target", candidateSHA: targetSHA,
			wantClass: QGFailureClassSystemic, wantDecision: QGFailureDecisionCreateOrReuseInfra,
			wantKnown: true, wantBaseline: true,
		},
		{
			name: "candidate absent from passing target", candidateSHA: "flow-candidate",
			configure: func(d *Dispatcher) {
				d.qgTargetObservations[targetSHA] = qgTargetObservation{passed: true}
			},
			wantClass: QGFailureClassWorkerDeterministic, wantDecision: QGFailureDecisionRetryOriginal,
			wantKnown: true, wantPassed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &qgTargetAttributionMutationRunner{output: []byte(tt.candidateSHA + "\n")}
			d := newQGTargetAttributionMutationDispatcher(workerID, beadID, worktree, targetSHA, runner)
			if tt.configure != nil {
				tt.configure(d)
			}
			result := make(chan qgFailureEvaluation, 1)
			go func() {
				result <- d.evaluateQGFailure(context.Background(), workerID, beadID, qgOutput)
			}()
			var got qgFailureEvaluation
			select {
			case got = <-result:
			case <-time.After(2 * time.Second):
				t.Fatal("evaluateQGFailure did not return; possible local mutex deadlock")
			}
			if got.err == nil || got.err.BeadID != beadID || got.err.WorkerID != workerID || got.err.Output != qgOutput {
				t.Fatalf("quality-gate error = %+v, want worker/bead/output preserved", got.err)
			}
			if got.record.BeadID != beadID || got.record.WorkerID != workerID ||
				got.record.AssignmentID != 41 || got.record.Fingerprint == "" || got.record.Output != qgOutput {
				t.Fatalf("failure record = %+v, want exact assignment and QG output", got.record)
			}
			if got.attribution.CandidateSHA != tt.candidateSHA || got.attribution.TargetSHA != targetSHA ||
				got.attribution.TargetKnown != tt.wantKnown || got.attribution.TargetPassed != tt.wantPassed {
				t.Fatalf("attribution = %+v, want candidate=%q target=%q known=%t passed=%t",
					got.attribution, tt.candidateSHA, targetSHA, tt.wantKnown, tt.wantPassed)
			}
			if got.classification.Class != tt.wantClass || got.classification.Decision != tt.wantDecision {
				t.Fatalf("classification = %+v, want class=%q decision=%q",
					got.classification, tt.wantClass, tt.wantDecision)
			}
			if got.targetBaselineFailure() != tt.wantBaseline {
				t.Fatalf("targetBaselineFailure() = %t, want %t", got.targetBaselineFailure(), tt.wantBaseline)
			}
		})
	}
}
