package dispatcher //nolint:testpackage // white-box mutation tests exercise private ops-run transitions and routing state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"oro/pkg/storage"
)

type opsAuthoritativeResult struct {
	id          int64
	affected    int64
	idErr       error
	affectedErr error
}

func (r opsAuthoritativeResult) LastInsertId() (int64, error) { return r.id, r.idErr }
func (r opsAuthoritativeResult) RowsAffected() (int64, error) { return r.affected, r.affectedErr }

type opsAuthoritativeStore struct {
	db     *sql.DB
	result sql.Result
	err    error
}

func (s opsAuthoritativeStore) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return s.result, s.err
}

func (s opsAuthoritativeStore) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, query, args...)
}

type opsAuthoritativeProcess struct {
	output string
	err    error
	wait   <-chan struct{}
}

func (p *opsAuthoritativeProcess) Wait() error {
	if p.wait != nil {
		<-p.wait
	}
	return p.err
}
func (p *opsAuthoritativeProcess) Kill() error             { return nil }
func (p *opsAuthoritativeProcess) Output() (string, error) { return p.output, nil }
func (p *opsAuthoritativeProcess) LastOutputAt() time.Time { return time.Now() }

type opsAuthoritativeBatchSpawner struct {
	mu      sync.Mutex
	prompts []string
	err     error
	wait    <-chan struct{}
}

func (s *opsAuthoritativeBatchSpawner) Spawn(_ context.Context, _, prompt, _ string) (ops.Process, error) {
	s.mu.Lock()
	s.prompts = append(s.prompts, prompt)
	s.mu.Unlock()
	return &opsAuthoritativeProcess{
		output: `{"schema_version":1,"verdict":"approved","summary":"approved","findings":[]}`,
		err:    s.err,
		wait:   s.wait,
	}, nil
}

func (s *opsAuthoritativeBatchSpawner) promptText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.prompts, "\n")
}

type opsAuthoritativeWorktrees struct {
	WorktreeManager
	exists bool
}

func (w *opsAuthoritativeWorktrees) Exists(context.Context, string) bool { return w.exists }

func newOpsAuthoritativeHarness(t *testing.T) (*Dispatcher, *sql.DB, *opsAuthoritativeBatchSpawner) {
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
	batch := &opsAuthoritativeBatchSpawner{}
	d, err := New(Config{
		RepoRoot:          t.TempDir(),
		ReviewEvidenceDir: filepath.Join(t.TempDir(), "review-evidence"),
		MaxWorkers:        1,
		DefaultBranch:     "main",
	}, db, nil, ops.NewSpawner(batch), beadstore.NewSQLiteStore(db),
		&opsAuthoritativeWorktrees{exists: true}, nil, nil)
	if err != nil {
		t.Fatalf("create dispatcher: %v", err)
	}
	t.Cleanup(d.wg.Wait)
	return d, db, batch
}

func opsAuthoritativeFetch(t *testing.T, db *sql.DB, id int64) OpsRunRecord {
	t.Helper()
	rec, err := loadOpsRunByID(t.Context(), db, id)
	if err != nil {
		t.Fatalf("load ops run %d: %v", id, err)
	}
	return rec
}

func opsAuthoritativeCreate(t *testing.T, db *sql.DB, runType ops.Type, beadID string) OpsRunRecord {
	t.Helper()
	rec, created, err := CreateOpsRun(t.Context(), db, OpsRunRecord{Type: string(runType), BeadID: beadID})
	if err != nil || !created {
		t.Fatalf("create ops run = %+v created %t err %v", rec, created, err)
	}
	return rec
}

func opsAuthoritativeFail(t *testing.T, db *sql.DB, runType ops.Type, beadID, incident string) OpsRunRecord {
	t.Helper()
	rec := opsAuthoritativeCreate(t, db, runType, beadID)
	if err := CompleteOpsRun(t.Context(), db, rec.ID, opsRunStatusFailed, "failed", "old feedback", incident); err != nil {
		t.Fatalf("fail ops run: %v", err)
	}
	return opsAuthoritativeFetch(t, db, rec.ID)
}

func opsAuthoritativeInstallInsertFailure(t *testing.T, db *sql.DB, beadID string) {
	t.Helper()
	if _, err := db.Exec(fmt.Sprintf(`
CREATE TRIGGER ops_authoritative_insert_failure
BEFORE INSERT ON ops_runs
WHEN NEW.bead_id = %q
BEGIN
  SELECT RAISE(FAIL, 'authoritative replacement insert failure');
END`, beadID)); err != nil {
		t.Fatalf("install insert failure trigger: %v", err)
	}
}

func TestOpsAuthoritativeSurvivorMutationPublicAndStoreGuards(t *testing.T) {
	ctx := context.Background()
	if _, created, err := CreateOpsRun(ctx, nil, OpsRunRecord{}); err == nil || created || !strings.Contains(err.Error(), "db is nil") {
		t.Fatalf("nil create = created %t err %v", created, err)
	}
	if err := CompleteOpsRun(ctx, nil, 1, opsRunStatusFailed, "", "", ""); err == nil || !strings.Contains(err.Error(), "db is nil") {
		t.Fatalf("nil complete error = %v", err)
	}

	d, db, _ := newOpsAuthoritativeHarness(t)
	_ = d
	if _, created, err := createOpsRun(ctx, db, OpsRunRecord{Type: string(ops.OpsDiagnosis), Status: "corrupt"}); err == nil || created {
		t.Fatalf("invalid status create = created %t err %v", created, err)
	}
	original := opsAuthoritativeCreate(t, db, ops.OpsDiagnosis, "authoritative-duplicate")
	duplicate, created, err := createOpsRun(ctx, db, OpsRunRecord{Type: original.Type, BeadID: original.BeadID, WorkerID: "contender"})
	if err != nil || created || duplicate.ID != original.ID {
		t.Fatalf("duplicate owner = %+v created %t err %v", duplicate, created, err)
	}
	if _, err := completeOpsRunFromStatus(ctx, db, original.ID, "corrupt", opsRunStatusFailed, "", "", ""); err == nil {
		t.Fatal("invalid expected completion status accepted")
	}
	if _, err := completeOpsRunFromStatus(ctx, db, original.ID, opsRunStatusRunning, opsRunStatusRunning, "", "", ""); err == nil {
		t.Fatal("invalid terminal completion status accepted")
	}
	if _, err := completeOpsRunFromStatus(ctx, opsAuthoritativeStore{db: db, err: errors.New("authoritative update failure")},
		original.ID, opsRunStatusRunning, opsRunStatusFailed, "", "", ""); err == nil || !strings.Contains(err.Error(), "authoritative update failure") {
		t.Fatalf("completion update error = %v", err)
	}
	if _, err := completeOpsRunFromStatus(ctx, opsAuthoritativeStore{db: db, result: opsAuthoritativeResult{affectedErr: errors.New("authoritative rows failure")}},
		original.ID, opsRunStatusRunning, opsRunStatusFailed, "", "", ""); err == nil || !strings.Contains(err.Error(), "authoritative rows failure") {
		t.Fatalf("completion rows error = %v", err)
	}
	if err := CompleteOpsRun(ctx, db, original.ID, opsRunStatusResolved, "resolved", "done", ""); err != nil {
		t.Fatalf("complete replay fixture: %v", err)
	}
	outcome, err := completeOpsRunFromStatus(ctx, db, original.ID, opsRunStatusRunning, opsRunStatusResolved, "resolved", "done", "")
	if err != nil || outcome != opsRunCompletionExactReplay {
		t.Fatalf("exact replay = %v err %v", outcome, err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX ops_authoritative_constant_unique ON ops_runs((1))`); err != nil {
		t.Fatalf("create unrelated unique index: %v", err)
	}
	if _, created, err := createOpsRun(ctx, db, OpsRunRecord{Type: string(ops.OpsDiagnosis), BeadID: "authoritative-no-owner"}); err == nil || created {
		t.Fatalf("unrelated unique collision = created %t err %v", created, err)
	}

	if _, err := db.Exec(`DROP TABLE ops_runs; CREATE TABLE ops_runs (broken TEXT)`); err != nil {
		t.Fatalf("malform ops run table: %v", err)
	}
	if _, err := findBlockingOpsRun(ctx, db, "diagnosis", "broken"); err == nil || !strings.Contains(err.Error(), "find blocking") {
		t.Fatalf("find blocking error = %v", err)
	}
	if _, err := loadOpsRunByID(ctx, db, 77); err == nil || !strings.Contains(err.Error(), "load ops run 77") {
		t.Fatalf("load by id error = %v", err)
	}
}

func TestOpsAuthoritativeSurvivorMutationResolveContracts(t *testing.T) {
	ctx := context.Background()
	t.Run("invalid and missing", func(t *testing.T) {
		d, _, _ := newOpsAuthoritativeHarness(t)
		if _, err := d.applyOpsResolve(""); err == nil || !strings.Contains(err.Error(), "requires") {
			t.Fatalf("invalid resolve error = %v", err)
		}
		if _, err := d.applyOpsResolve("999999 operator checked"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("missing resolve error = %v", err)
		}
	})
	t.Run("load and completion errors", func(t *testing.T) {
		d, db, _ := newOpsAuthoritativeHarness(t)
		if _, err := db.Exec(`DROP TABLE ops_runs; CREATE TABLE ops_runs (broken TEXT)`); err != nil {
			t.Fatalf("malform ops runs: %v", err)
		}
		if _, err := d.applyOpsResolve("1 operator checked"); err == nil || !strings.Contains(err.Error(), "load ops run 1") {
			t.Fatalf("resolve malformed store error = %v", err)
		}
	})
	t.Run("corrupt status rejects side effects", func(t *testing.T) {
		d, db, _ := newOpsAuthoritativeHarness(t)
		rec := opsAuthoritativeCreate(t, db, ops.OpsDiagnosis, "authoritative-resolve-corrupt")
		if _, err := db.ExecContext(ctx, `UPDATE ops_runs SET status='corrupt' WHERE id=?`, rec.ID); err != nil {
			t.Fatalf("corrupt status: %v", err)
		}
		if _, err := d.applyOpsResolve(fmt.Sprintf("%d operator checked", rec.ID)); err == nil || !strings.Contains(err.Error(), "invalid expected status") {
			t.Fatalf("corrupt resolve error = %v", err)
		}
	})
	t.Run("resolved replay reports durable state", func(t *testing.T) {
		d, db, _ := newOpsAuthoritativeHarness(t)
		rec := opsAuthoritativeCreate(t, db, ops.OpsDiagnosis, "authoritative-resolve-replay")
		if err := CompleteOpsRun(ctx, db, rec.ID, opsRunStatusResolved, "", "", "checked"); err != nil {
			t.Fatalf("resolve fixture: %v", err)
		}
		data, err := d.applyOpsResolve(fmt.Sprintf("%d checked again", rec.ID))
		if err != nil {
			t.Fatalf("resolved replay: %v", err)
		}
		var response opsResolveResponse
		if err := json.Unmarshal([]byte(data), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !response.Resolved || response.ID != rec.ID || response.Status != opsRunStatusResolved {
			t.Fatalf("resolved response = %+v", response)
		}
	})
}

func TestOpsAuthoritativeSurvivorMutationReplaceTransactions(t *testing.T) {
	ctx := context.Background()
	t.Run("begin error", func(t *testing.T) {
		d, db, _ := newOpsAuthoritativeHarness(t)
		_ = d
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
		if _, _, err := replaceOpsRun(ctx, db, OpsRunRecord{ID: 1, Status: opsRunStatusRunning}, OpsRunRecord{}, "replace"); err == nil || !strings.Contains(err.Error(), "begin") {
			t.Fatalf("begin error = %v", err)
		}
	})
	t.Run("validation and replay ownership", func(t *testing.T) {
		_, db, _ := newOpsAuthoritativeHarness(t)
		if _, _, err := replaceOpsRun(ctx, db, OpsRunRecord{ID: 2, Status: "corrupt"}, OpsRunRecord{}, "replace"); err == nil {
			t.Fatal("invalid replacement status accepted")
		}
		current := opsAuthoritativeCreate(t, db, ops.OpsDiagnosis, "authoritative-replace-replay")
		const reason = "already superseded"
		if err := CompleteOpsRun(ctx, db, current.ID, opsRunStatusSuperseded, current.Verdict, current.Feedback, reason); err != nil {
			t.Fatalf("prepare replay: %v", err)
		}
		next := current
		next.ID = 0
		if _, _, err := replaceOpsRun(ctx, db, current, next, reason); err == nil || !strings.Contains(err.Error(), "ownership") {
			t.Fatalf("replay ownership error = %v", err)
		}
	})
	t.Run("insert error rolls back", func(t *testing.T) {
		_, db, _ := newOpsAuthoritativeHarness(t)
		current := opsAuthoritativeCreate(t, db, ops.OpsDiagnosis, "authoritative-replace-insert")
		opsAuthoritativeInstallInsertFailure(t, db, current.BeadID)
		next := current
		next.ID = 0
		if _, _, err := replaceOpsRun(ctx, db, current, next, "replace"); err == nil || !strings.Contains(err.Error(), "authoritative replacement") {
			t.Fatalf("insert error = %v", err)
		}
		if got := opsAuthoritativeFetch(t, db, current.ID); got.Status != opsRunStatusRunning {
			t.Fatalf("current status after insert rollback = %q", got.Status)
		}
	})
	t.Run("blocking owner returns without commit", func(t *testing.T) {
		_, db, _ := newOpsAuthoritativeHarness(t)
		current := opsAuthoritativeCreate(t, db, ops.OpsDiagnosis, "authoritative-replace-current")
		owner := opsAuthoritativeCreate(t, db, ops.OpsReview, "authoritative-replace-owner")
		next := owner
		next.ID = 0
		got, created, err := replaceOpsRun(ctx, db, current, next, "collision")
		if err != nil || created || got.ID != owner.ID {
			t.Fatalf("blocking owner = %+v created %t err %v", got, created, err)
		}
		if got := opsAuthoritativeFetch(t, db, current.ID); got.Status != opsRunStatusRunning {
			t.Fatalf("current status after collision = %q", got.Status)
		}
	})
	t.Run("checkpoint relink error rolls back", func(t *testing.T) {
		_, db, _ := newOpsAuthoritativeHarness(t)
		current := opsAuthoritativeCreate(t, db, ops.OpsDiagnosis, "authoritative-replace-relink")
		if _, err := db.Exec(`DROP TABLE review_checkpoints`); err != nil {
			t.Fatalf("drop checkpoints: %v", err)
		}
		next := current
		next.ID = 0
		if _, _, err := replaceOpsRun(ctx, db, current, next, "replace"); err == nil || !strings.Contains(err.Error(), "relink") {
			t.Fatalf("relink error = %v", err)
		}
		if got := opsAuthoritativeFetch(t, db, current.ID); got.Status != opsRunStatusRunning {
			t.Fatalf("current status after relink rollback = %q", got.Status)
		}
	})
}

func TestOpsAuthoritativeSurvivorMutationRetryNormalization(t *testing.T) {
	ctx := context.Background()
	for _, runType := range []ops.Type{ops.OpsDiagnosis, ops.OpsDecompose, ops.OpsEscalation} {
		t.Run(string(runType), func(t *testing.T) {
			d, db, _ := newOpsAuthoritativeHarness(t)
			d.ops = nil
			incident := "authoritative " + string(runType) + " incident"
			rec := opsAuthoritativeFail(t, db, runType, "authoritative-retry-"+string(runType), incident)
			if _, err := db.ExecContext(ctx, `UPDATE ops_runs SET dispatcher_pid=-71, process_pid=-72, runtime='', model='' WHERE id=?`, rec.ID); err != nil {
				t.Fatalf("seed stale identity: %v", err)
			}
			rec = opsAuthoritativeFetch(t, db, rec.ID)
			replacement, routed, err := d.supersedeOpsRunForRetry(rec)
			if err != nil || routed {
				t.Fatalf("retry = %+v routed %t err %v", replacement, routed, err)
			}
			wantError := fmt.Sprintf("manual retry of ops run %d", rec.ID)
			if runType == ops.OpsDecompose || runType == ops.OpsEscalation {
				wantError = incident
			}
			if replacement.ID == rec.ID || replacement.Status != opsRunStatusRunning ||
				replacement.DispatcherPID != os.Getpid() || replacement.ProcessPID != 0 ||
				replacement.Verdict != "" || replacement.Feedback != "" || replacement.Error != wantError ||
				replacement.Runtime == "" || replacement.Model == "" {
				t.Fatalf("retry replacement not normalized: %+v", replacement)
			}
		})
	}
	t.Run("lookup and replacement failures propagate", func(t *testing.T) {
		d, db, _ := newOpsAuthoritativeHarness(t)
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
		if _, _, err := d.supersedeOpsRunForRetry(OpsRunRecord{ID: 9, Type: string(ops.OpsDiagnosis), Status: opsRunStatusResolved}); err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("closed lookup error = %v", err)
		}
	})
	t.Run("insert failure propagates", func(t *testing.T) {
		d, db, _ := newOpsAuthoritativeHarness(t)
		rec := opsAuthoritativeFail(t, db, ops.OpsDiagnosis, "authoritative-retry-error", "incident")
		opsAuthoritativeInstallInsertFailure(t, db, rec.BeadID)
		if _, _, err := d.supersedeOpsRunForRetry(rec); err == nil || !strings.Contains(err.Error(), "authoritative replacement") {
			t.Fatalf("retry insert error = %v", err)
		}
	})
	t.Run("replacement collision reports durable owner", func(t *testing.T) {
		d, db, _ := newOpsAuthoritativeHarness(t)
		current := opsAuthoritativeFail(t, db, ops.OpsDiagnosis, "authoritative-retry-current", "incident")
		owner := opsAuthoritativeCreate(t, db, ops.OpsReview, "authoritative-retry-owner")
		synthetic := current
		synthetic.Type = owner.Type
		synthetic.BeadID = owner.BeadID
		if _, routed, err := d.supersedeOpsRunForRetry(synthetic); err == nil || routed || !strings.Contains(err.Error(), fmt.Sprintf("blocking ops run %d", owner.ID)) {
			t.Fatalf("retry collision = routed %t err %v", routed, err)
		}
		if opsAuthoritativeFetch(t, db, current.ID).Status != opsRunStatusFailed {
			t.Fatal("retry collision changed current owner")
		}
	})
}

func TestOpsAuthoritativeSurvivorMutationStartupReroute(t *testing.T) {
	ctx := context.Background()
	d, db, _ := newOpsAuthoritativeHarness(t)
	d.ops = nil
	const beadID = "authoritative-startup"
	original, created, err := CreateOpsRun(ctx, db, OpsRunRecord{
		Type: string(ops.OpsDiagnosis), BeadID: beadID, WorkerID: "old-worker",
		DispatcherPID: -71, ProcessPID: -72, Verdict: "old verdict", Feedback: "old feedback", Error: "old error",
	})
	if err != nil || !created {
		t.Fatalf("create startup fixture = created %t err %v", created, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ops_runs SET runtime='', model='' WHERE id=?`, original.ID); err != nil {
		t.Fatalf("clear runtime/model: %v", err)
	}
	original = opsAuthoritativeFetch(t, db, original.ID)
	if err := d.supersedeAndRerouteOpsRun(ctx, original); err != nil {
		t.Fatalf("supersede and reroute: %v", err)
	}
	var replacementID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM ops_runs WHERE bead_id=? AND id<>?`, beadID, original.ID).Scan(&replacementID); err != nil {
		t.Fatalf("load replacement id: %v", err)
	}
	replacement := opsAuthoritativeFetch(t, db, replacementID)
	if replacement.Status != opsRunStatusFailed || replacement.DispatcherPID != os.Getpid() ||
		replacement.ProcessPID != 0 || replacement.Verdict != "" || replacement.Feedback != "" ||
		replacement.Runtime == "" || replacement.Model == "" || replacement.CompletedAt == "" ||
		!strings.Contains(replacement.Error, "could not be routed") {
		t.Fatalf("startup replacement not normalized: %+v", replacement)
	}
	if got := opsAuthoritativeFetch(t, db, original.ID); got.Status != opsRunStatusSuperseded {
		t.Fatalf("original status = %q", got.Status)
	}
	var payload string
	if err := db.QueryRowContext(ctx, `SELECT payload FROM events WHERE type='ops_run_superseded' AND bead_id=?`, beadID).Scan(&payload); err != nil {
		t.Fatalf("load supersede event: %v", err)
	}
	for _, want := range []string{fmt.Sprintf(`"ops_run_id":%d`, original.ID), fmt.Sprintf(`"new_ops_run_id":%d`, replacement.ID), `"routed":false`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("supersede event %q missing %q", payload, want)
		}
	}

	t.Run("replacement collision is a no-op", func(t *testing.T) {
		d, db, _ := newOpsAuthoritativeHarness(t)
		d.ops = nil
		current := opsAuthoritativeCreate(t, db, ops.OpsDiagnosis, "authoritative-startup-current")
		owner := opsAuthoritativeCreate(t, db, ops.OpsDiagnosis, "authoritative-startup-owner")
		synthetic := current
		synthetic.BeadID = owner.BeadID
		if err := d.supersedeAndRerouteOpsRun(ctx, synthetic); err != nil {
			t.Fatalf("collision reroute: %v", err)
		}
		if opsAuthoritativeFetch(t, db, current.ID).Status != opsRunStatusRunning ||
			opsAuthoritativeFetch(t, db, owner.ID).Status != opsRunStatusRunning {
			t.Fatal("collision changed existing owners")
		}
	})

	t.Run("replacement and terminal failures propagate", func(t *testing.T) {
		d, db, _ := newOpsAuthoritativeHarness(t)
		d.ops = nil
		rec := opsAuthoritativeCreate(t, db, ops.OpsDiagnosis, "authoritative-startup-insert")
		opsAuthoritativeInstallInsertFailure(t, db, rec.BeadID)
		if err := d.supersedeAndRerouteOpsRun(ctx, rec); err == nil || !strings.Contains(err.Error(), "authoritative replacement") {
			t.Fatalf("startup insert error = %v", err)
		}
	})

	t.Run("unroutable terminal failure propagates", func(t *testing.T) {
		d, db, _ := newOpsAuthoritativeHarness(t)
		d.ops = nil
		rec := opsAuthoritativeCreate(t, db, ops.OpsDiagnosis, "authoritative-startup-terminal")
		if _, err := db.Exec(fmt.Sprintf(`
CREATE TRIGGER ops_authoritative_terminal_failure
BEFORE UPDATE OF status ON ops_runs
WHEN OLD.id <> %d AND NEW.status = 'failed'
BEGIN
  SELECT RAISE(FAIL, 'authoritative terminal failure');
END`, rec.ID)); err != nil {
			t.Fatalf("install terminal failure trigger: %v", err)
		}
		if err := d.supersedeAndRerouteOpsRun(ctx, rec); err == nil || !strings.Contains(err.Error(), "authoritative terminal failure") {
			t.Fatalf("terminal completion error = %v", err)
		}
	})

	t.Run("routed replacement is normalized before completion", func(t *testing.T) {
		d, db, batch := newOpsAuthoritativeHarness(t)
		release := make(chan struct{})
		batch.wait = release
		defer func() {
			close(release)
			d.wg.Wait()
		}()
		rec, created, err := CreateOpsRun(ctx, db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "authoritative-startup-routed",
			DispatcherPID: -81, ProcessPID: -82, Verdict: "old verdict", Feedback: "old feedback", Error: "old error",
		})
		if err != nil || !created {
			t.Fatalf("create routed fixture = created %t err %v", created, err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE ops_runs SET runtime='', model='' WHERE id=?`, rec.ID); err != nil {
			t.Fatalf("clear routed runtime/model: %v", err)
		}
		rec = opsAuthoritativeFetch(t, db, rec.ID)
		if err := d.supersedeAndRerouteOpsRun(ctx, rec); err != nil {
			t.Fatalf("route replacement: %v", err)
		}
		replacement, err := FindBlockingOpsRun(ctx, db, rec.Type, rec.BeadID)
		if err != nil || replacement == nil {
			t.Fatalf("load routed replacement = %+v err %v", replacement, err)
		}
		if replacement.ID == rec.ID || replacement.Status != opsRunStatusRunning ||
			replacement.DispatcherPID != os.Getpid() || replacement.ProcessPID != 0 ||
			replacement.Verdict != "" || replacement.Feedback != "" || replacement.Error != "" ||
			replacement.Runtime == "" || replacement.Model == "" {
			t.Fatalf("pending replacement not normalized: %+v", replacement)
		}
	})
}

func TestOpsAuthoritativeSurvivorMutationReviewContexts(t *testing.T) {
	ctx := context.Background()
	var nilDispatcher *Dispatcher
	if got := nilDispatcher.reviewContextForOpsRun(ctx, OpsRunRecord{BeadID: "bead"}); got != (reviewOpsRunContext{}) {
		t.Fatalf("nil dispatcher context = %+v", got)
	}

	d, db, _ := newOpsAuthoritativeHarness(t)
	if empty := d.reviewContextForOpsRun(ctx, OpsRunRecord{}); empty != (reviewOpsRunContext{}) {
		t.Fatalf("empty bead context = %+v", empty)
	}
	var emptyEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='review_checkpoint_context_restore_failed'`).Scan(&emptyEvents); err != nil || emptyEvents != 0 {
		t.Fatalf("empty bead restore events = %d err %v", emptyEvents, err)
	}
	if absent := d.reviewContextForOpsRun(ctx, OpsRunRecord{BeadID: "authoritative-absent"}); absent != (reviewOpsRunContext{}) {
		t.Fatalf("absent worker context = %+v", absent)
	}
	checkpoint, err := NewReviewCheckpointStore(db).CreateOrReuse(ctx, CheckpointInput{
		CheckpointKey: "authoritative-checkpoint", BeadID: "authoritative-owned",
		OriginAssignmentID: 71, CurrentAssignmentID: 72, Worktree: "/tmp/authoritative-owned",
		Branch: "agent/authoritative-owned", TargetBranch: "epic/authoritative", HeadSHA: "head", TargetSHA: "target",
		AcceptanceHash: "acceptance", QGScriptHash: "script", QGMode: "full", ReviewPolicyHash: "policy",
		TriageRevision: "triage", ReadyAttempt: "ready", State: ReviewCheckpointStateReviewRunning,
	})
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	got := d.reviewContextForOpsRun(ctx, OpsRunRecord{BeadID: checkpoint.BeadID, WorkerID: "fallback"})
	if got.worktree != checkpoint.Worktree || got.targetBranch != checkpoint.TargetBranch || got.assignmentID != 72 {
		t.Fatalf("checkpoint context = %+v", got)
	}

	d.mu.Lock()
	d.workers["worker-partial-a"] = &trackedWorker{id: "worker-partial-a", beadID: "authoritative-partial", worktree: "/tmp/partial", assignmentID: 81}
	d.workers["worker-partial-b"] = &trackedWorker{id: "worker-partial-b", beadID: "authoritative-partial", targetBranch: "epic/partial", assignmentID: 82}
	partial := d.reviewContextFromAnyWorkerLocked("authoritative-partial")
	d.mu.Unlock()
	if partial.worktree != "/tmp/partial" || partial.targetBranch != "epic/partial" || partial.workerID == "" {
		t.Fatalf("combined worker context = %+v", partial)
	}

	d.mu.Lock()
	d.workers["worker-nil"] = nil
	fromWorker := d.reviewContextFromWorkerLocked(OpsRunRecord{BeadID: "different", WorkerID: "worker-nil"})
	d.mu.Unlock()
	if fromWorker != (reviewOpsRunContext{}) {
		t.Fatalf("nil worker context = %+v", fromWorker)
	}

	d.mu.Lock()
	d.workers["worker-default"] = &trackedWorker{id: "worker-default", beadID: "authoritative-default", worktree: "/tmp/default", assignmentID: 91}
	d.mu.Unlock()
	defaulted := d.reviewContextForOpsRun(ctx, OpsRunRecord{BeadID: "authoritative-default", WorkerID: "worker-default"})
	if defaulted.worktree != "/tmp/default" || defaulted.targetBranch != "main" {
		t.Fatalf("defaulted worker context = %+v", defaulted)
	}

	if _, err := db.Exec(`DROP TABLE review_checkpoints`); err != nil {
		t.Fatalf("drop checkpoints: %v", err)
	}
	if failed := d.reviewContextForOpsRun(ctx, OpsRunRecord{BeadID: "authoritative-error", WorkerID: "worker"}); failed != (reviewOpsRunContext{}) {
		t.Fatalf("checkpoint error context = %+v", failed)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='review_checkpoint_context_restore_failed'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("context restore events = %d err %v", events, err)
	}
}

func TestOpsAuthoritativeSurvivorMutationRoutingGuards(t *testing.T) {
	ctx := context.Background()
	t.Run("route ops nil and unknown", func(t *testing.T) {
		d, _, batch := newOpsAuthoritativeHarness(t)
		d.ops = nil
		if d.routeOpsRun(ctx, OpsRunRecord{Type: string(ops.OpsDiagnosis)}) {
			t.Fatal("nil ops spawner routed")
		}
		d.ops = ops.NewSpawner(batch)
		if d.routeOpsRun(ctx, OpsRunRecord{Type: "future"}) {
			t.Fatal("unknown ops type routed")
		}
	})
	t.Run("escalation receives fallback type", func(t *testing.T) {
		d, _, batch := newOpsAuthoritativeHarness(t)
		if !d.routeOpsRun(ctx, OpsRunRecord{Type: string(ops.OpsEscalation), BeadID: "authoritative-escalation"}) {
			t.Fatal("escalation route = false")
		}
		d.wg.Wait()
		if !strings.Contains(batch.promptText(), "ORPHANED_OPS_RUN") {
			t.Fatalf("escalation prompt missing fallback type: %s", batch.promptText())
		}
	})
	for _, tc := range []struct {
		name       string
		worker     *trackedWorker
		worktrees  WorktreeManager
		controller *storage.Controller
	}{
		{name: "missing context", worktrees: &opsAuthoritativeWorktrees{exists: true}},
		{name: "missing worktree", worker: &trackedWorker{id: "worker", beadID: "bead", targetBranch: "main"}, worktrees: &opsAuthoritativeWorktrees{exists: true}},
		{name: "nil manager", worker: &trackedWorker{id: "worker", beadID: "bead", worktree: "/tmp/bead", targetBranch: "main"}},
		{name: "absent worktree", worker: &trackedWorker{id: "worker", beadID: "bead", worktree: "/tmp/bead", targetBranch: "main"}, worktrees: &opsAuthoritativeWorktrees{exists: false}},
		{name: "storage observation error", worker: &trackedWorker{id: "worker", beadID: "bead", worktree: "/tmp/bead", targetBranch: "main"}, worktrees: &opsAuthoritativeWorktrees{exists: true}, controller: &storage.Controller{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _ := newOpsAuthoritativeHarness(t)
			d.worktrees = tc.worktrees
			d.cfg.StorageController = tc.controller
			if tc.worker != nil {
				d.mu.Lock()
				d.workers[tc.worker.id] = tc.worker
				d.mu.Unlock()
			}
			if d.routeReviewOpsRun(ctx, OpsRunRecord{Type: string(ops.OpsReview), BeadID: "bead", WorkerID: "worker"}) {
				t.Fatal("guarded review route = true")
			}
		})
	}
}

func TestOpsAuthoritativeSurvivorMutationWatcherFailureAudit(t *testing.T) {
	d, db, _ := newOpsAuthoritativeHarness(t)
	resultCh := make(chan ops.Result, 1)
	resultCh <- ops.Result{Type: ops.OpsDiagnosis, Verdict: ops.VerdictFailed, Feedback: "failed"}
	d.watchReroutedOpsRunResult(context.Background(), OpsRunRecord{
		ID: 999999, Type: string(ops.OpsDiagnosis), BeadID: "authoritative-watch", WorkerID: "worker",
	}, resultCh, nil)
	d.wg.Wait()
	var payload string
	if err := db.QueryRow(`SELECT payload FROM events WHERE type='ops_run_complete_failed' AND bead_id='authoritative-watch'`).Scan(&payload); err != nil {
		t.Fatalf("load watcher failure event: %v", err)
	}
	if !strings.Contains(payload, `"ops_run_id":999999`) || !strings.Contains(payload, `"status":"failed"`) {
		t.Fatalf("watcher failure payload = %q", payload)
	}
}
