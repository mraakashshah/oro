package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/factoryhealth"
	"oro/pkg/protocol"
)

type healthAuthoritativeStore struct {
	DeferredStore
	ready       []protocol.Bead
	readyErr    error
	hasChildren map[string]bool
}

func (s *healthAuthoritativeStore) Ready(context.Context) ([]protocol.Bead, error) {
	return s.ready, s.readyErr
}

func (s *healthAuthoritativeStore) HasChildren(_ context.Context, id string) (bool, error) {
	return s.hasChildren[id], nil
}

func newHealthAuthoritativeHarness(t *testing.T) (*Dispatcher, *sql.DB, *healthAuthoritativeStore) {
	t.Helper()
	t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "dispatcher.db"))
	if err != nil {
		t.Fatalf("open dispatcher database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("initialize dispatcher schema: %v", err)
	}
	if err := protocol.InitializeBeadSchema(t.Context(), db); err != nil {
		t.Fatalf("initialize bead schema: %v", err)
	}
	store := &healthAuthoritativeStore{
		DeferredStore: beadstore.NewSQLiteStore(db),
		hasChildren:   make(map[string]bool),
	}
	d, err := New(Config{
		RepoRoot:          t.TempDir(),
		ReviewEvidenceDir: filepath.Join(t.TempDir(), "review-evidence"),
		MaxWorkers:        1,
		DefaultBranch:     "main",
	}, db, nil, nil, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("create dispatcher: %v", err)
	}
	d.beads = store
	return d, db, store
}

func healthAuthoritativeEventTypes(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT type FROM events ORDER BY id`)
	if err != nil {
		t.Fatalf("query health events: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := make(map[string]bool)
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatalf("scan health event: %v", err)
		}
		got[eventType] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate health events: %v", err)
	}
	return got
}

func TestHealthAuthoritativeSurvivorMutationApplyContracts(t *testing.T) {
	t.Run("ready observation failure clears unsafe queue and remains visible", func(t *testing.T) {
		d, _, store := newHealthAuthoritativeHarness(t)
		store.ready = []protocol.Bead{{ID: "unsafe-ready", Type: "task"}}
		store.readyErr = errors.New("authoritative ready failure")
		data, err := d.applyHealth()
		if err != nil {
			t.Fatalf("apply health: %v", err)
		}
		var health factoryhealth.FactoryHealth
		if err := json.Unmarshal([]byte(data), &health); err != nil {
			t.Fatalf("decode health: %v", err)
		}
		if health.Metrics.ReadyQueue != 0 {
			t.Fatalf("ready queue = %d, want fail-closed zero", health.Metrics.ReadyQueue)
		}
		foundObservation := false
		for _, finding := range health.Findings {
			if finding.Code == "assignment_admission_unknown" &&
				strings.Contains(finding.Message, "authoritative ready failure") {
				foundObservation = true
			}
		}
		if !foundObservation {
			t.Fatalf("missing ready observation finding: %+v", health.Findings)
		}
	})

	t.Run("status queue filtering is reflected in health", func(t *testing.T) {
		d, _, store := newHealthAuthoritativeHarness(t)
		store.ready = []protocol.Bead{
			{ID: "epic-with-children", Type: "epic"},
			{ID: "ordinary-task", Type: "task"},
		}
		store.hasChildren["epic-with-children"] = true
		data, err := d.applyHealth()
		if err != nil {
			t.Fatalf("apply filtered health: %v", err)
		}
		var health factoryhealth.FactoryHealth
		if err := json.Unmarshal([]byte(data), &health); err != nil {
			t.Fatalf("decode filtered health: %v", err)
		}
		if health.Metrics.ReadyQueue != 1 {
			t.Fatalf("ready queue = %d, want only ordinary task", health.Metrics.ReadyQueue)
		}
	})

	t.Run("recovery quarantine load failure is audited", func(t *testing.T) {
		d, db, _ := newHealthAuthoritativeHarness(t)
		if _, err := db.Exec(`DROP TABLE recovery_quarantines; CREATE TABLE recovery_quarantines (broken TEXT)`); err != nil {
			t.Fatalf("malform recovery quarantine table: %v", err)
		}
		if _, err := d.applyHealth(); err != nil {
			t.Fatalf("apply health with malformed quarantine table: %v", err)
		}
		if !healthAuthoritativeEventTypes(t, db)["factory_health_recovery_quarantine_load_failed"] {
			t.Fatal("missing recovery quarantine load failure audit event")
		}
	})
}

func TestHealthAuthoritativeSurvivorMutationMetricFailureAudits(t *testing.T) {
	d, db, _ := newHealthAuthoritativeHarness(t)
	for _, statement := range []string{
		`DROP TABLE assignments; CREATE TABLE assignments (broken TEXT)`,
		`DROP TABLE ops_runs; CREATE TABLE ops_runs (broken TEXT)`,
		`DROP TABLE escalations; CREATE TABLE escalations (broken TEXT)`,
		`DROP TABLE epic_branch_admissions; CREATE TABLE epic_branch_admissions (broken TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("malform health metric input: %v", err)
		}
	}
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	d.evaluateFactoryHealth(context.Background(), now, factoryHealthInput{daemonRunning: true})
	events := healthAuthoritativeEventTypes(t, db)
	for _, want := range []string{
		"factory_health_assignment_load_failed",
		"factory_health_throughput_load_failed",
		"factory_health_ops_runs_load_failed",
		"factory_health_pending_escalations_load_failed",
		"factory_health_epic_branch_admission_load_failed",
	} {
		if !events[want] {
			t.Errorf("missing %s audit event; events=%v", want, events)
		}
	}
}

func TestHealthAuthoritativeSurvivorMutationNilObservationIsSafe(t *testing.T) {
	var d *Dispatcher
	d.recordAssignmentObservation("ready", errors.New("must not panic"))
}
