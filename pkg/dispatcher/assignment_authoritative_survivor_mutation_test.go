package dispatcher //nolint:testpackage // white-box mutation tests verify assignment admission decisions

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func assignmentAuthoritativeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "dispatcher.db"))
	if err != nil {
		t.Fatalf("open assignment survivor database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("initialize dispatcher schema: %v", err)
	}
	if err := protocol.InitializeBeadSchema(t.Context(), db); err != nil {
		t.Fatalf("initialize bead schema: %v", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemorySearchEvents); err != nil {
		t.Fatalf("initialize semantic search events: %v", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemoryReadEvents); err != nil {
		t.Fatalf("initialize semantic read events: %v", err)
	}
	return db
}

func assignmentAuthoritativeEventPayloads(t *testing.T, db *sql.DB, eventType string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT payload FROM events WHERE type=? ORDER BY id`, eventType)
	if err != nil {
		t.Fatalf("query %s events: %v", eventType, err)
	}
	defer rows.Close()
	var payloads []string
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scan %s event: %v", eventType, err)
		}
		payloads = append(payloads, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s events: %v", eventType, err)
	}
	return payloads
}

func assignmentAuthoritativeSeedBlockingCheckpoint(t *testing.T, d *Dispatcher, beadID string) {
	t.Helper()
	result, err := d.db.Exec(`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'requeued')`,
		beadID, "authoritative-review-worker", "/tmp/authoritative-"+beadID)
	if err != nil {
		t.Fatalf("insert checkpoint origin assignment: %v", err)
	}
	originID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("load checkpoint origin assignment ID: %v", err)
	}
	_, err = NewReviewCheckpointStore(d.db).CreateOrReuse(t.Context(), CheckpointInput{
		CheckpointKey:      "authoritative-checkpoint-" + beadID,
		BeadID:             beadID,
		OriginAssignmentID: originID,
		Worktree:           "/tmp/authoritative-" + beadID,
		Branch:             protocol.BranchPrefix + beadID,
		TargetBranch:       "main",
		HeadSHA:            "authoritative-head-" + beadID,
		TargetSHA:          "authoritative-target-" + beadID,
		AcceptanceHash:     "authoritative-acceptance-" + beadID,
		QGScriptHash:       "authoritative-qg-" + beadID,
		QGMode:             "full",
		ReviewPolicyHash:   "authoritative-policy-" + beadID,
		TriageRevision:     "authoritative-triage-" + beadID,
		ReadyAttempt:       "authoritative-ready-" + beadID,
		State:              ReviewCheckpointStateReviewRunning,
	})
	if err != nil {
		t.Fatalf("create blocking review checkpoint: %v", err)
	}
}

func TestAssignmentAuthoritativeSurvivorMutationCheckpointAdmission(t *testing.T) {
	t.Run("unblocked admission returns true without a blocked audit", func(t *testing.T) {
		d := &Dispatcher{db: assignmentAuthoritativeDB(t)}
		if !d.checkpointAssignmentAdmissionAllowed(context.Background(), "authoritative-open", "worker-open", "initial") {
			t.Fatal("checkpoint admission rejected a bead with no blocking checkpoint")
		}
		if d.checkpointObservationError != "" {
			t.Fatalf("successful checkpoint observation retained error %q", d.checkpointObservationError)
		}
		if payloads := assignmentAuthoritativeEventPayloads(t, d.db, "review_checkpoint_assignment_blocked"); len(payloads) != 0 {
			t.Fatalf("unblocked admission emitted blocked audits: %v", payloads)
		}
	})

	t.Run("blocking checkpoint returns false with the exact stage audit", func(t *testing.T) {
		d := &Dispatcher{db: assignmentAuthoritativeDB(t)}
		assignmentAuthoritativeSeedBlockingCheckpoint(t, d, "authoritative-blocked")
		if d.checkpointAssignmentAdmissionAllowed(context.Background(), "authoritative-blocked", "worker-blocked", "final_recheck") {
			t.Fatal("checkpoint admission accepted a bead with a nonterminal review checkpoint")
		}
		payloads := assignmentAuthoritativeEventPayloads(t, d.db, "review_checkpoint_assignment_blocked")
		if len(payloads) != 1 || !strings.Contains(payloads[0], `"reason":"durable_nonterminal_review_checkpoint"`) ||
			!strings.Contains(payloads[0], `"stage":"final_recheck"`) {
			t.Fatalf("blocked admission audits = %v, want one durable reason with final_recheck stage", payloads)
		}
	})

	t.Run("observation failure fails closed and records health and audit detail", func(t *testing.T) {
		d := &Dispatcher{db: assignmentAuthoritativeDB(t)}
		if _, err := d.db.Exec(`DROP VIEW review_checkpoints_blocking_assignment`); err != nil {
			t.Fatalf("remove checkpoint observation view: %v", err)
		}
		if d.checkpointAssignmentAdmissionAllowed(context.Background(), "authoritative-unknown", "worker-unknown", "atomic_insert") {
			t.Fatal("checkpoint admission failed open when checkpoint ownership was unobservable")
		}
		if !strings.Contains(d.checkpointObservationError, "review_checkpoint: query blocking review checkpoint") {
			t.Fatalf("checkpoint observation health = %q", d.checkpointObservationError)
		}
		payloads := assignmentAuthoritativeEventPayloads(t, d.db, "review_checkpoint_assignment_recheck_failed")
		if len(payloads) != 1 || !strings.Contains(payloads[0], `"stage":"atomic_insert"`) ||
			!strings.Contains(payloads[0], "query blocking review checkpoint") {
			t.Fatalf("failed observation audits = %v, want one stage and query error", payloads)
		}
	})
}

func TestAssignmentAuthoritativeSurvivorMutationInsertFailureDecision(t *testing.T) {
	t.Run("blocking cause never reopens even without another checkpoint query", func(t *testing.T) {
		d := &Dispatcher{db: assignmentAuthoritativeDB(t)}
		if d.assignmentInsertFailureAllowsReopen(context.Background(), "authoritative-cause", "worker-cause",
			errAssignmentBlockedByReviewCheckpoint) {
			t.Fatal("checkpoint-blocked assignment insert was allowed to reopen")
		}
		if d.checkpointObservationError != "" {
			t.Fatalf("direct blocked cause unexpectedly performed an observation: %q", d.checkpointObservationError)
		}
	})

	t.Run("unblocked cleanup observation permits reopen", func(t *testing.T) {
		d := &Dispatcher{db: assignmentAuthoritativeDB(t)}
		if !d.assignmentInsertFailureAllowsReopen(context.Background(), "authoritative-reopen", "worker-reopen",
			sql.ErrTxDone) {
			t.Fatal("unblocked assignment insert cleanup was not allowed to reopen")
		}
		if d.checkpointObservationError != "" {
			t.Fatalf("successful cleanup observation retained error %q", d.checkpointObservationError)
		}
	})

	t.Run("observation failure denies reopen and records health and audit detail", func(t *testing.T) {
		d := &Dispatcher{db: assignmentAuthoritativeDB(t)}
		if _, err := d.db.Exec(`DROP VIEW review_checkpoints_blocking_assignment`); err != nil {
			t.Fatalf("remove checkpoint observation view: %v", err)
		}
		if d.assignmentInsertFailureAllowsReopen(context.Background(), "authoritative-cleanup-unknown", "worker-cleanup-unknown",
			sql.ErrTxDone) {
			t.Fatal("assignment insert cleanup failed open when checkpoint ownership was unobservable")
		}
		if !strings.Contains(d.checkpointObservationError, "review_checkpoint: query blocking review checkpoint") {
			t.Fatalf("cleanup observation health = %q", d.checkpointObservationError)
		}
		payloads := assignmentAuthoritativeEventPayloads(t, d.db, "review_checkpoint_assignment_cleanup_observation_failed")
		if len(payloads) != 1 || !strings.Contains(payloads[0], "query blocking review checkpoint") {
			t.Fatalf("cleanup observation audits = %v, want one query error", payloads)
		}
	})
}
