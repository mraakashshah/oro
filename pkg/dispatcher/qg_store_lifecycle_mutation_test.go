package dispatcher //nolint:testpackage // focused mutation owner exercises private QG store lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

type qgStoreLifecycleMutationRunner struct {
	output []byte
	err    error
	name   string
	args   []string
}

func (r *qgStoreLifecycleMutationRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return r.output, r.err
}

func qgStoreLifecycleMutationConfig(t *testing.T, repoRoot, beadsDir string) Config {
	t.Helper()
	return Config{
		SocketPath: filepath.Join(t.TempDir(), "dispatcher.sock"),
		RepoRoot:   repoRoot,
		BeadsDir:   beadsDir,
		MaxWorkers: 1,
	}
}

func qgStoreLifecycleMutationStore() DeferredStore {
	return beadstore.NewFakeStore()
}

func newQGStoreLifecycleMutationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open QG store lifecycle database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), protocol.SchemaDDL); err != nil {
		t.Fatalf("create QG store lifecycle schema: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE review_checkpoints (
		head_sha TEXT,
		target_sha TEXT,
		qg_evidence_path TEXT,
		qg_evidence_sha256 TEXT
	)`); err != nil {
		t.Fatalf("create QG store lifecycle review schema: %v", err)
	}
	return db
}

func newQGStoreLifecycleMutationDispatcher(
	t *testing.T,
	db *sql.DB,
	runner *qgStoreLifecycleMutationRunner,
) *Dispatcher {
	t.Helper()
	d, err := New(
		qgStoreLifecycleMutationConfig(t, t.TempDir(), protocol.BeadsDir),
		db,
		nil,
		nil,
		qgStoreLifecycleMutationStore(),
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("construct QG store lifecycle dispatcher: %v", err)
	}
	d.shutdownRunner = runner
	return d
}

func qgStoreLifecycleMutationWorker(id, worktree, targetSHA string) *trackedWorker {
	return &trackedWorker{
		id:           id,
		beadID:       "mutation-bead",
		assignmentID: 71,
		state:        protocol.WorkerBusy,
		worktree:     worktree,
		targetSHA:    targetSHA,
	}
}

func qgStoreLifecycleMutationAttributionWithin(
	t *testing.T,
	d *Dispatcher,
	workerID string,
	record QGFailureRecord,
) QGFailureAttribution {
	t.Helper()
	result := make(chan QGFailureAttribution, 1)
	go func() {
		result <- d.qgFailureAttribution(context.Background(), workerID, record)
	}()
	select {
	case attribution := <-result:
		return attribution
	case <-time.After(2 * time.Second):
		t.Fatal("qgFailureAttribution did not return; possible local mutex deadlock")
		return QGFailureAttribution{}
	}
}

func qgStoreLifecycleMutationRecordPassWithin(t *testing.T, d *Dispatcher, targetSHA string) {
	t.Helper()
	done := make(chan struct{}, 1)
	go func() {
		d.recordQGTargetPass(targetSHA)
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recordQGTargetPass did not return; possible local mutex deadlock")
	}
}

func qgStoreLifecycleMutationRecordFailureWithin(
	t *testing.T,
	d *Dispatcher,
	targetSHA string,
	fingerprint string,
) {
	t.Helper()
	done := make(chan struct{}, 1)
	go func() {
		d.recordQGTargetFailure(targetSHA, fingerprint)
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recordQGTargetFailure did not return; possible local mutex deadlock")
	}
}

func TestQGStoreLifecycleMutationOwner(t *testing.T) {
	t.Setenv("ORO_BEADSOURCE_MODE", "cli")

	t.Run("constructor", func(t *testing.T) {
		t.Run("invalid config", func(t *testing.T) {
			d, err := New(
				Config{MaxWorkers: -1},
				newQGStoreLifecycleMutationDB(t),
				nil,
				nil,
				qgStoreLifecycleMutationStore(),
				nil,
				nil,
				nil,
			)
			if err == nil || d != nil {
				t.Fatalf("New(invalid config) = (%v, %v), want nil dispatcher and error", d, err)
			}
		})

		t.Run("default paths", func(t *testing.T) {
			db := newQGStoreLifecycleMutationDB(t)
			d, err := New(
				qgStoreLifecycleMutationConfig(t, "", ""),
				db,
				nil,
				nil,
				qgStoreLifecycleMutationStore(),
				nil,
				nil,
				nil,
			)
			if err != nil {
				t.Fatalf("New(default paths): %v", err)
			}
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatalf("get working directory: %v", err)
			}
			if d.repoRoot != cwd || d.beadsDir != protocol.BeadsDir {
				t.Fatalf("New(default paths) = repoRoot %q, beadsDir %q; want %q, %q",
					d.repoRoot, d.beadsDir, cwd, protocol.BeadsDir)
			}
			if d.remoteGates == nil || d.beads == nil || d.workers == nil || d.qgTargetObservations == nil {
				t.Fatalf("New(default paths) omitted initialized stores or maps: %+v", d)
			}
		})

		t.Run("explicit paths", func(t *testing.T) {
			repoRoot := t.TempDir()
			d, err := New(
				qgStoreLifecycleMutationConfig(t, repoRoot, ".tasks"),
				newQGStoreLifecycleMutationDB(t),
				nil,
				nil,
				qgStoreLifecycleMutationStore(),
				nil,
				nil,
				nil,
			)
			if err != nil {
				t.Fatalf("New(explicit paths): %v", err)
			}
			if d.repoRoot != repoRoot || d.beadsDir != ".tasks" || d.cfg.MaxWorkers != 1 {
				t.Fatalf("New(explicit paths) resolved %+v, want repo=%q beads=.tasks workers=1",
					d.cfg, repoRoot)
			}
		})
	})

	t.Run("classification history merge", func(t *testing.T) {
		t.Run("nil database", func(t *testing.T) {
			d := &Dispatcher{}
			got := d.classifyQGFailureWithAttribution(
				context.Background(),
				QGFailureRecord{Output: "revive failed: builtinShadow"},
				QGFailureHistory{RetryExhausted: true},
				QGFailureAttribution{},
			)
			if got.Class != QGFailureClassWorkerDeterministic || got.Decision != QGFailureDecisionReopenOriginal {
				t.Fatalf("nil-DB classification = %+v, want deterministic reopen", got)
			}
		})

		db := newQGStoreLifecycleMutationDB(t)
		d := newQGStoreLifecycleMutationDispatcher(t, db, &qgStoreLifecycleMutationRunner{})
		if _, err := db.Exec(`INSERT INTO qg_failure_incidents
			(id, fingerprint, class, decision, confidence, reason, summary)
			VALUES (1, 'qg:history', 'flaky', 'backoff_retry', 'high', 'known flaky', 'summary');
			INSERT INTO qg_failure_occurrences
			(id, incident_id, bead_id, output_hash)
			VALUES ('occ-history', 1, 'bead-history', 'hash')`); err != nil {
			t.Fatalf("insert QG history fixture: %v", err)
		}

		tests := []struct {
			name         string
			record       QGFailureRecord
			override     QGFailureHistory
			wantClass    QGFailureClass
			wantDecision QGFailureDecision
		}{
			{
				name: "stored flaky history",
				record: QGFailureRecord{
					Fingerprint: "qg:history", BeadID: "bead-history", Output: "unrecognized output",
				},
				wantClass: QGFailureClassFlaky, wantDecision: QGFailureDecisionBackoffRetry,
			},
			{
				name: "affected bead override",
				record: QGFailureRecord{
					Fingerprint: "qg:history", BeadID: "bead-history", Output: "unrecognized output",
				},
				override:  QGFailureHistory{AffectedBeads: 3},
				wantClass: QGFailureClassSystemic, wantDecision: QGFailureDecisionCreateOrReuseInfra,
			},
			{
				name:      "known flaky override",
				record:    QGFailureRecord{Fingerprint: "qg:new-flaky", Output: "unrecognized output"},
				override:  QGFailureHistory{KnownFlaky: true},
				wantClass: QGFailureClassFlaky, wantDecision: QGFailureDecisionBackoffRetry,
			},
			{
				name:      "rerun passed override",
				record:    QGFailureRecord{Fingerprint: "qg:new-rerun", Output: "unrecognized output"},
				override:  QGFailureHistory{RerunPassed: true},
				wantClass: QGFailureClassFlaky, wantDecision: QGFailureDecisionBackoffRetry,
			},
			{
				name:      "retry exhausted override",
				record:    QGFailureRecord{Fingerprint: "qg:new-retry", Output: "revive failed: builtinShadow"},
				override:  QGFailureHistory{RetryExhausted: true},
				wantClass: QGFailureClassWorkerDeterministic, wantDecision: QGFailureDecisionReopenOriginal,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := d.classifyQGFailureWithAttribution(
					context.Background(), tt.record, tt.override, QGFailureAttribution{},
				)
				if got.Class != tt.wantClass || got.Decision != tt.wantDecision {
					t.Fatalf("classification = %+v, want class=%q decision=%q",
						got, tt.wantClass, tt.wantDecision)
				}
			})
		}
	})

	t.Run("failure attribution", func(t *testing.T) {
		const (
			workerID    = "store-mutation-worker"
			worktree    = "/tmp/qg-store-lifecycle-mutation"
			targetSHA   = "target-sha"
			candidate   = "candidate-sha"
			fingerprint = "qg:store-lifecycle"
		)
		record := QGFailureRecord{Fingerprint: fingerprint}

		t.Run("missing worker", func(t *testing.T) {
			d := newQGStoreLifecycleMutationDispatcher(
				t, newQGStoreLifecycleMutationDB(t), &qgStoreLifecycleMutationRunner{},
			)
			if got := qgStoreLifecycleMutationAttributionWithin(t, d, workerID, record); got != (QGFailureAttribution{}) {
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
				runner := &qgStoreLifecycleMutationRunner{output: []byte(candidate + "\n")}
				d := newQGStoreLifecycleMutationDispatcher(t, newQGStoreLifecycleMutationDB(t), runner)
				d.workers[workerID] = qgStoreLifecycleMutationWorker(workerID, tt.worktree, tt.targetSHA)
				if got := qgStoreLifecycleMutationAttributionWithin(t, d, workerID, record); got != (QGFailureAttribution{}) {
					t.Fatalf("incomplete-worker attribution = %+v, want empty", got)
				}
				if runner.name != "" {
					t.Fatalf("incomplete worker invoked %q, want no git command", runner.name)
				}
			})
		}

		t.Run("git failure", func(t *testing.T) {
			runner := &qgStoreLifecycleMutationRunner{err: errors.New("rev-parse failed")}
			d := newQGStoreLifecycleMutationDispatcher(t, newQGStoreLifecycleMutationDB(t), runner)
			d.workers[workerID] = qgStoreLifecycleMutationWorker(workerID, worktree, targetSHA)
			if got := qgStoreLifecycleMutationAttributionWithin(t, d, workerID, record); got != (QGFailureAttribution{}) {
				t.Fatalf("git-failure attribution = %+v, want empty", got)
			}
		})

		t.Run("empty candidate", func(t *testing.T) {
			runner := &qgStoreLifecycleMutationRunner{output: []byte(" \n")}
			d := newQGStoreLifecycleMutationDispatcher(t, newQGStoreLifecycleMutationDB(t), runner)
			d.workers[workerID] = qgStoreLifecycleMutationWorker(workerID, worktree, targetSHA)
			want := QGFailureAttribution{TargetSHA: targetSHA}
			if got := qgStoreLifecycleMutationAttributionWithin(t, d, workerID, record); got != want {
				t.Fatalf("empty-candidate attribution = %+v, want %+v", got, want)
			}
		})

		t.Run("candidate is target", func(t *testing.T) {
			runner := &qgStoreLifecycleMutationRunner{output: []byte("  " + targetSHA + "\n")}
			d := newQGStoreLifecycleMutationDispatcher(t, newQGStoreLifecycleMutationDB(t), runner)
			d.qgTargetObservations = nil
			d.workers[workerID] = qgStoreLifecycleMutationWorker(workerID, worktree, targetSHA)
			want := QGFailureAttribution{
				CandidateSHA: targetSHA, TargetSHA: targetSHA,
				TargetFingerprint: fingerprint, TargetKnown: true,
			}
			if got := qgStoreLifecycleMutationAttributionWithin(t, d, workerID, record); got != want {
				t.Fatalf("exact-target attribution = %+v, want %+v", got, want)
			}
			if _, ok := d.qgTargetObservations[targetSHA].failureFingerprints[fingerprint]; !ok {
				t.Fatalf("exact target did not retain failure fingerprint: %+v", d.qgTargetObservations)
			}
			if runner.name != "git" || !reflect.DeepEqual(runner.args, []string{"-C", worktree, "rev-parse", "HEAD"}) {
				t.Fatalf("git command = %q %v, want git -C %s rev-parse HEAD", runner.name, runner.args, worktree)
			}
		})

		t.Run("unknown distinct candidate", func(t *testing.T) {
			runner := &qgStoreLifecycleMutationRunner{output: []byte(candidate + "\n")}
			d := newQGStoreLifecycleMutationDispatcher(t, newQGStoreLifecycleMutationDB(t), runner)
			d.workers[workerID] = qgStoreLifecycleMutationWorker(workerID, worktree, targetSHA)
			want := QGFailureAttribution{CandidateSHA: candidate, TargetSHA: targetSHA}
			if got := qgStoreLifecycleMutationAttributionWithin(t, d, workerID, record); got != want {
				t.Fatalf("unknown-target attribution = %+v, want %+v", got, want)
			}
		})

		t.Run("cached target observations", func(t *testing.T) {
			runner := &qgStoreLifecycleMutationRunner{output: []byte(candidate + "\n")}
			d := newQGStoreLifecycleMutationDispatcher(t, newQGStoreLifecycleMutationDB(t), runner)
			d.workers[workerID] = qgStoreLifecycleMutationWorker(workerID, worktree, targetSHA)
			d.qgTargetObservations[targetSHA] = qgTargetObservation{
				passed: true, failureFingerprints: map[string]struct{}{fingerprint: {}},
			}
			want := QGFailureAttribution{
				CandidateSHA: candidate, TargetSHA: targetSHA,
				TargetFingerprint: fingerprint, TargetKnown: true, TargetPassed: true,
			}
			if got := qgStoreLifecycleMutationAttributionWithin(t, d, workerID, record); got != want {
				t.Fatalf("cached-target attribution = %+v, want %+v", got, want)
			}
		})

		t.Run("accepted target pass", func(t *testing.T) {
			db := newQGStoreLifecycleMutationDB(t)
			if _, err := db.Exec(`INSERT INTO review_checkpoints
				(head_sha, target_sha, qg_evidence_path, qg_evidence_sha256)
				VALUES (?, ?, 'evidence.json', 'sha256')`, targetSHA, targetSHA); err != nil {
				t.Fatalf("insert accepted target checkpoint: %v", err)
			}
			runner := &qgStoreLifecycleMutationRunner{output: []byte(candidate + "\n")}
			d := newQGStoreLifecycleMutationDispatcher(t, db, runner)
			d.workers[workerID] = qgStoreLifecycleMutationWorker(workerID, worktree, targetSHA)
			want := QGFailureAttribution{
				CandidateSHA: candidate, TargetSHA: targetSHA, TargetKnown: true, TargetPassed: true,
			}
			if got := qgStoreLifecycleMutationAttributionWithin(t, d, workerID, record); got != want {
				t.Fatalf("accepted-target attribution = %+v, want %+v", got, want)
			}
			if !d.qgTargetObservations[targetSHA].passed {
				t.Fatal("accepted target pass was not cached")
			}
		})
	})

	t.Run("target observations", func(t *testing.T) {
		d := &Dispatcher{}
		qgStoreLifecycleMutationRecordPassWithin(t, d, "")
		qgStoreLifecycleMutationRecordFailureWithin(t, d, "", "fingerprint")
		qgStoreLifecycleMutationRecordFailureWithin(t, d, "target", "")
		if d.qgTargetObservations != nil {
			t.Fatalf("empty target or fingerprint initialized observations: %+v", d.qgTargetObservations)
		}

		qgStoreLifecycleMutationRecordPassWithin(t, d, "target")
		qgStoreLifecycleMutationRecordFailureWithin(t, d, "target", "fingerprint")
		qgStoreLifecycleMutationRecordFailureWithin(t, d, "target", "fingerprint")
		observation := d.qgTargetObservations["target"]
		if !observation.passed || len(observation.failureFingerprints) != 1 {
			t.Fatalf("recorded target observation = %+v, want pass and one deduped failure", observation)
		}
	})
}
