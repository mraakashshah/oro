package dispatcher //nolint:testpackage // focused mutation owner exercises private classifier decisions

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type qgClassifierDecisionMutationRunner struct {
	output []byte
	err    error
}

func (r *qgClassifierDecisionMutationRunner) Run(
	_ context.Context,
	_ string,
	_ ...string,
) ([]byte, error) {
	return r.output, r.err
}

func newQGClassifierDecisionMutationDispatcher(t *testing.T, withReviewTable bool) *Dispatcher {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open classifier decision mutation database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if withReviewTable {
		if _, err := db.Exec(`CREATE TABLE review_checkpoints (
			head_sha TEXT,
			target_sha TEXT,
			qg_evidence_path TEXT,
			qg_evidence_sha256 TEXT
		)`); err != nil {
			t.Fatalf("create classifier decision review checkpoints: %v", err)
		}
	}
	return &Dispatcher{
		db:             db,
		shutdownRunner: &qgClassifierDecisionMutationRunner{},
	}
}

func qgClassifierDecisionAcceptedWithin(
	t *testing.T,
	d *Dispatcher,
	ctx context.Context,
	targetSHA string,
) bool {
	t.Helper()
	result := make(chan bool, 1)
	go func() {
		result <- d.acceptedQGTargetPassed(ctx, targetSHA)
	}()
	select {
	case accepted := <-result:
		return accepted
	case <-time.After(2 * time.Second):
		t.Fatal("acceptedQGTargetPassed did not return")
		return false
	}
}

func TestQGClassifierDecisionMutationOwner(t *testing.T) {
	t.Run("deterministic markers", func(t *testing.T) {
		tests := []struct {
			name string
			text string
			want bool
		}{
			{name: "go test", text: "--- fail: TestBroken", want: true},
			{name: "package fail", text: "header\nfail\toro/pkg/dispatcher", want: true},
			{name: "nilaway source", text: "nilaway pkg/example.go:12:3: potential nil panic detected", want: true},
			{name: "nilaway summary", text: "nilaway failed without a source diagnostic", want: false},
			{name: "gofumpt", text: "gofumpt failed: file is not formatted", want: true},
			{name: "goimports", text: "goimports error: imports are unsorted", want: true},
			{name: "golangci", text: "golangci-lint failed: revive", want: true},
			{name: "golangci without another tool marker", text: "golangci-lint failed: errcheck", want: true},
			{name: "revive", text: "revive error: builtinShadow", want: true},
			{name: "compile", text: "compile error in package", want: true},
			{name: "compilation", text: "compilation failed for command", want: true},
			{name: "build", text: "build failed for binary", want: true},
			{name: "unused", text: "pkg/example.go: unused variable value", want: true},
			{name: "passing tools", text: "gofumpt pass\ngoimports pass\ngolangci-lint pass\nrevive pass", want: false},
			{name: "nil source diagnostic without nilaway", text: "pkg/example.go:12:3: potential nil panic detected", want: false},
			{name: "word fragment", text: "prefailure marker", want: false},
			{name: "empty", text: "", want: false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := isDeterministicQGFailure(tt.text); got != tt.want {
					t.Fatalf("isDeterministicQGFailure(%q) = %t, want %t", tt.text, got, tt.want)
				}
			})
		}
	})

	t.Run("target baseline evidence", func(t *testing.T) {
		tests := []struct {
			name        string
			record      QGFailureRecord
			attribution QGFailureAttribution
			want        bool
		}{
			{name: "unknown target", record: QGFailureRecord{Fingerprint: "failure"}, want: false},
			{
				name: "unknown target with matching revisions",
				attribution: QGFailureAttribution{
					CandidateSHA: "target", TargetSHA: "target",
				},
				want: false,
			},
			{
				name:        "known target with empty revisions",
				attribution: QGFailureAttribution{TargetKnown: true},
				want:        false,
			},
			{
				name: "candidate is target",
				attribution: QGFailureAttribution{
					CandidateSHA: "target", TargetSHA: "target", TargetKnown: true,
				},
				want: true,
			},
			{
				name:   "matching fingerprint",
				record: QGFailureRecord{Fingerprint: "failure"},
				attribution: QGFailureAttribution{
					CandidateSHA: "candidate", TargetSHA: "target", TargetKnown: true,
					TargetFingerprint: "failure",
				},
				want: true,
			},
			{
				name: "empty fingerprint",
				attribution: QGFailureAttribution{
					CandidateSHA: "candidate", TargetSHA: "target", TargetKnown: true,
				},
				want: false,
			},
			{
				name:   "different fingerprint",
				record: QGFailureRecord{Fingerprint: "candidate-failure"},
				attribution: QGFailureAttribution{
					CandidateSHA: "candidate", TargetSHA: "target", TargetKnown: true,
					TargetFingerprint: "target-failure",
				},
				want: false,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := targetBaselineHasFailure(tt.record, tt.attribution); got != tt.want {
					t.Fatalf("targetBaselineHasFailure() = %t, want %t", got, tt.want)
				}
			})
		}
	})

	t.Run("candidate only deterministic evidence", func(t *testing.T) {
		passingDistinct := QGFailureAttribution{
			CandidateSHA: "candidate", TargetSHA: "target", TargetKnown: true, TargetPassed: true,
		}
		tests := []struct {
			name        string
			text        string
			attribution QGFailureAttribution
			want        bool
		}{
			{name: "passing target", text: "revive failed: builtinShadow", attribution: passingDistinct, want: true},
			{name: "non deterministic", text: "command timed out", attribution: passingDistinct, want: false},
			{name: "unknown target", text: "revive failed", attribution: QGFailureAttribution{TargetPassed: true}, want: false},
			{
				name: "unknown target with complete revisions", text: "revive failed",
				attribution: QGFailureAttribution{
					CandidateSHA: "candidate", TargetSHA: "target", TargetPassed: true,
				},
				want: false,
			},
			{
				name: "target did not pass", text: "revive failed",
				attribution: QGFailureAttribution{
					CandidateSHA: "candidate", TargetSHA: "target", TargetKnown: true,
				},
				want: false,
			},
			{
				name: "candidate missing", text: "revive failed",
				attribution: QGFailureAttribution{TargetSHA: "target", TargetKnown: true, TargetPassed: true},
				want:        false,
			},
			{
				name: "target missing", text: "revive failed",
				attribution: QGFailureAttribution{CandidateSHA: "candidate", TargetKnown: true, TargetPassed: true},
				want:        false,
			},
			{
				name: "candidate equals target", text: "revive failed",
				attribution: QGFailureAttribution{
					CandidateSHA: "target", TargetSHA: "target", TargetKnown: true, TargetPassed: true,
				},
				want: false,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := candidateOnlyDeterministicFailure(tt.text, tt.attribution); got != tt.want {
					t.Fatalf("candidateOnlyDeterministicFailure() = %t, want %t", got, tt.want)
				}
			})
		}
	})

	t.Run("classification precedence", func(t *testing.T) {
		deterministic := QGFailureRecord{Fingerprint: "failure", Output: "revive failed: builtinShadow"}
		tests := []struct {
			name            string
			record          QGFailureRecord
			history         QGFailureHistory
			attribution     QGFailureAttribution
			omitAttribution bool
			wantClass       QGFailureClass
			wantDecision    QGFailureDecision
		}{
			{
				name: "exact target beats deterministic marker", record: deterministic,
				attribution: QGFailureAttribution{CandidateSHA: "target", TargetSHA: "target", TargetKnown: true},
				wantClass:   QGFailureClassSystemic, wantDecision: QGFailureDecisionCreateOrReuseInfra,
			},
			{
				name: "fingerprint match beats deterministic marker", record: deterministic,
				attribution: QGFailureAttribution{
					CandidateSHA: "candidate", TargetSHA: "target", TargetKnown: true,
					TargetFingerprint: "failure",
				},
				wantClass: QGFailureClassSystemic, wantDecision: QGFailureDecisionCreateOrReuseInfra,
			},
			{
				name: "candidate only beats cross bead history", record: deterministic,
				history: QGFailureHistory{AffectedBeads: 3},
				attribution: QGFailureAttribution{
					CandidateSHA: "candidate", TargetSHA: "target", TargetKnown: true, TargetPassed: true,
				},
				wantClass: QGFailureClassWorkerDeterministic, wantDecision: QGFailureDecisionRetryOriginal,
			},
			{
				name: "candidate retry exhausted", record: deterministic,
				history: QGFailureHistory{RetryExhausted: true},
				attribution: QGFailureAttribution{
					CandidateSHA: "candidate", TargetSHA: "target", TargetKnown: true, TargetPassed: true,
				},
				wantClass: QGFailureClassWorkerDeterministic, wantDecision: QGFailureDecisionReopenOriginal,
			},
			{
				name: "unknown target preserves systemic history", record: deterministic,
				history:   QGFailureHistory{AffectedBeads: 2},
				wantClass: QGFailureClassSystemic, wantDecision: QGFailureDecisionCreateOrReuseInfra,
			},
			{
				name: "deterministic fallback", record: deterministic,
				wantClass: QGFailureClassWorkerDeterministic, wantDecision: QGFailureDecisionRetryOriginal,
			},
			{
				name: "zero attribution deterministic fallback", record: deterministic,
				omitAttribution: true,
				wantClass:       QGFailureClassWorkerDeterministic, wantDecision: QGFailureDecisionRetryOriginal,
			},
			{
				name: "zero attribution deterministic retry exhausted", record: deterministic,
				history: QGFailureHistory{RetryExhausted: true}, omitAttribution: true,
				wantClass: QGFailureClassWorkerDeterministic, wantDecision: QGFailureDecisionReopenOriginal,
			},
			{
				name:      "single affected bead is not systemic",
				record:    QGFailureRecord{Output: "unrecognized quality gate output"},
				history:   QGFailureHistory{AffectedBeads: 1},
				wantClass: QGFailureClassUnknown, wantDecision: QGFailureDecisionStopForTriage,
			},
			{
				name:      "package loader output is systemic",
				record:    QGFailureRecord{Output: "package loader returned malformed export data"},
				wantClass: QGFailureClassSystemic, wantDecision: QGFailureDecisionCreateOrReuseInfra,
			},
			{
				name:      "known flaky history",
				record:    QGFailureRecord{Output: "unrecognized quality gate output"},
				history:   QGFailureHistory{KnownFlaky: true},
				wantClass: QGFailureClassFlaky, wantDecision: QGFailureDecisionBackoffRetry,
			},
			{
				name:      "passing rerun history",
				record:    QGFailureRecord{Output: "unrecognized quality gate output"},
				history:   QGFailureHistory{RerunPassed: true},
				wantClass: QGFailureClassFlaky, wantDecision: QGFailureDecisionBackoffRetry,
			},
			{
				name:      "flaky output",
				record:    QGFailureRecord{Output: "intermittent flaky quality gate"},
				wantClass: QGFailureClassFlaky, wantDecision: QGFailureDecisionBackoffRetry,
			},
			{
				name: "unknown output", record: QGFailureRecord{Output: "unrecognized quality gate output"},
				wantClass: QGFailureClassUnknown, wantDecision: QGFailureDecisionStopForTriage,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var got QGFailureClassification
				if tt.omitAttribution {
					got = ClassifyQGFailure(tt.record, tt.history)
				} else {
					got = ClassifyQGFailure(tt.record, tt.history, tt.attribution)
				}
				if got.Class != tt.wantClass || got.Decision != tt.wantDecision {
					t.Fatalf("ClassifyQGFailure() = %+v, want class=%q decision=%q", got, tt.wantClass, tt.wantDecision)
				}
				if got.Confidence == "" || got.Reason == "" {
					t.Fatalf("ClassifyQGFailure() omitted confidence or reason: %+v", got)
				}
			})
		}
	})

	t.Run("accepted target evidence", func(t *testing.T) {
		t.Run("nil database", func(t *testing.T) {
			d := &Dispatcher{shutdownRunner: &qgClassifierDecisionMutationRunner{err: errors.New("unused")}}
			if qgClassifierDecisionAcceptedWithin(t, d, context.Background(), "target") {
				t.Fatal("nil database target was accepted")
			}
		})

		t.Run("empty target", func(t *testing.T) {
			d := newQGClassifierDecisionMutationDispatcher(t, true)
			if _, err := d.db.Exec(`INSERT INTO review_checkpoints VALUES
				('', '', 'evidence.json', 'hash')`); err != nil {
				t.Fatalf("insert empty classifier decision evidence: %v", err)
			}
			if qgClassifierDecisionAcceptedWithin(t, d, context.Background(), "") {
				t.Fatal("empty target was accepted")
			}
		})

		t.Run("missing table", func(t *testing.T) {
			d := newQGClassifierDecisionMutationDispatcher(t, false)
			if qgClassifierDecisionAcceptedWithin(t, d, context.Background(), "target") {
				t.Fatal("target without review table was accepted")
			}
		})

		t.Run("exact completed evidence", func(t *testing.T) {
			d := newQGClassifierDecisionMutationDispatcher(t, true)
			if _, err := d.db.Exec(`INSERT INTO review_checkpoints VALUES
				('other', 'target', 'evidence.json', 'hash'),
				('target', 'target', '', 'hash'),
				('target', 'target', 'evidence.json', ''),
				('target', 'target', 'evidence.json', 'hash')`); err != nil {
				t.Fatalf("insert classifier decision evidence: %v", err)
			}
			if !qgClassifierDecisionAcceptedWithin(t, d, context.Background(), "target") {
				t.Fatal("exact target with complete evidence was not accepted")
			}
			if qgClassifierDecisionAcceptedWithin(t, d, context.Background(), "other") {
				t.Fatal("mismatched target evidence was accepted")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if qgClassifierDecisionAcceptedWithin(t, d, ctx, "target") {
				t.Fatal("canceled target lookup was accepted")
			}
		})
	})
}
