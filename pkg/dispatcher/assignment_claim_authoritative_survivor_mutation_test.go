package dispatcher //nolint:testpackage // authoritative white-box mutation contracts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/agentmodel"
	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

type assignmentClaimAuthoritativeStore struct {
	DeferredStore

	mu         sync.Mutex
	updateErr  error
	updateHook func(string, string)
	statuses   []string
	showErrs   map[string]error
}

func (s *assignmentClaimAuthoritativeStore) Update(ctx context.Context, id string, params beadstore.UpdateParams) error {
	s.mu.Lock()
	err := s.updateErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := s.DeferredStore.Update(ctx, id, params); err != nil {
		return err
	}
	if params.Status != nil {
		s.mu.Lock()
		s.statuses = append(s.statuses, *params.Status)
		hook := s.updateHook
		s.mu.Unlock()
		if hook != nil {
			hook(id, *params.Status)
		}
	}
	return nil
}

func (s *assignmentClaimAuthoritativeStore) observedStatuses() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.statuses)
}

func (s *assignmentClaimAuthoritativeStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
	s.mu.Lock()
	err := s.showErrs[id]
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.DeferredStore.Show(ctx, id)
}

type assignmentClaimAuthoritativeWorktrees struct {
	mu sync.Mutex

	created      []string
	removed      []string
	existsPaths  []string
	createFn     func(context.Context, string, string) (string, string, error)
	existsFn     func(context.Context, string) bool
	branchHeadFn func(context.Context, string) (string, error)
}

func (w *assignmentClaimAuthoritativeWorktrees) Create(ctx context.Context, beadID, baseBranch string) (string, string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.created = append(w.created, beadID+"@"+baseBranch)
	if w.createFn != nil {
		return w.createFn(ctx, beadID, baseBranch)
	}
	return "/tmp/claim-authoritative-" + beadID, protocol.BranchPrefix + beadID, nil
}

func (w *assignmentClaimAuthoritativeWorktrees) Remove(_ context.Context, path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.removed = append(w.removed, path)
	return nil
}

func (*assignmentClaimAuthoritativeWorktrees) Prune(context.Context) error { return nil }
func (*assignmentClaimAuthoritativeWorktrees) DeleteBranch(context.Context, string) error {
	return nil
}

func (*assignmentClaimAuthoritativeWorktrees) DeleteBranchMergedInto(context.Context, string, string) error {
	return nil
}

func (*assignmentClaimAuthoritativeWorktrees) ForceDeleteBranch(context.Context, string) error {
	return nil
}

func (*assignmentClaimAuthoritativeWorktrees) BranchExists(context.Context, string) (bool, error) {
	return false, nil
}

func (*assignmentClaimAuthoritativeWorktrees) MergeFFOnly(context.Context, string, string) (string, error) {
	return "", nil
}

func (*assignmentClaimAuthoritativeWorktrees) UpdateBranchRef(context.Context, string, string) error {
	return nil
}

func (w *assignmentClaimAuthoritativeWorktrees) BranchHead(ctx context.Context, branch string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.branchHeadFn != nil {
		return w.branchHeadFn(ctx, branch)
	}
	return "sha-" + strings.TrimSpace(branch), nil
}

func (*assignmentClaimAuthoritativeWorktrees) GCClosedWorktrees(context.Context, func(string) bool) error {
	return nil
}

func (w *assignmentClaimAuthoritativeWorktrees) Exists(ctx context.Context, path string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.existsPaths = append(w.existsPaths, path)
	if w.existsFn != nil {
		return w.existsFn(ctx, path)
	}
	return true
}

func (*assignmentClaimAuthoritativeWorktrees) CurrentBranch(_ context.Context, path string) (string, error) {
	return protocol.BranchPrefix + strings.TrimPrefix(filepath.Base(path), "claim-authoritative-"), nil
}

func (*assignmentClaimAuthoritativeWorktrees) RebaseOnto(context.Context, string, string) error {
	return nil
}
func (*assignmentClaimAuthoritativeWorktrees) PushBranch(context.Context, string) error { return nil }
func (*assignmentClaimAuthoritativeWorktrees) CreateBranch(context.Context, string, string) error {
	return nil
}

type assignmentClaimAuthoritativeConn struct {
	mu       sync.Mutex
	writes   [][]byte
	writeErr error
	closed   bool
}

func (*assignmentClaimAuthoritativeConn) Read([]byte) (int, error) { return 0, net.ErrClosed }
func (c *assignmentClaimAuthoritativeConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	c.writes = append(c.writes, slices.Clone(data))
	return len(data), nil
}

func (c *assignmentClaimAuthoritativeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
func (*assignmentClaimAuthoritativeConn) LocalAddr() net.Addr              { return nil }
func (*assignmentClaimAuthoritativeConn) RemoteAddr() net.Addr             { return nil }
func (*assignmentClaimAuthoritativeConn) SetDeadline(time.Time) error      { return nil }
func (*assignmentClaimAuthoritativeConn) SetReadDeadline(time.Time) error  { return nil }
func (*assignmentClaimAuthoritativeConn) SetWriteDeadline(time.Time) error { return nil }

type assignmentClaimAuthoritativeEstimator struct {
	calls  int
	result int
	hook   func()
}

func (e *assignmentClaimAuthoritativeEstimator) Estimate(context.Context, string, string) int {
	e.calls++
	if e.hook != nil {
		e.hook()
	}
	return e.result
}

type assignmentClaimAuthoritativeCodeIndex struct {
	chunks []CodeChunk
}

func (i *assignmentClaimAuthoritativeCodeIndex) FTS5Search(context.Context, string, int) ([]CodeChunk, error) {
	return slices.Clone(i.chunks), nil
}

func (*assignmentClaimAuthoritativeCodeIndex) Search(context.Context, string, int) ([]SearchResult, error) {
	return nil, nil
}

type assignmentClaimAuthoritativeHarness struct {
	d         *Dispatcher
	store     *assignmentClaimAuthoritativeStore
	worktrees *assignmentClaimAuthoritativeWorktrees
	worker    *trackedWorker
	conn      *assignmentClaimAuthoritativeConn
	bead      protocol.Bead
}

func newAssignmentClaimAuthoritativeHarness(t *testing.T, beadID string) *assignmentClaimAuthoritativeHarness {
	t.Helper()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "dispatcher.db"))
	if err != nil {
		t.Fatalf("open dispatcher db: %v", err)
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
	baseStore := beadstore.NewSQLiteStore(db)
	store := &assignmentClaimAuthoritativeStore{DeferredStore: baseStore, showErrs: make(map[string]error)}
	worktrees := &assignmentClaimAuthoritativeWorktrees{}
	d, err := New(Config{
		RepoRoot:          t.TempDir(),
		ReviewEvidenceDir: filepath.Join(t.TempDir(), "review-evidence"),
		MaxWorkers:        1,
		DefaultBranch:     "main",
	}, db, nil, nil, store, worktrees, nil, nil)
	if err != nil {
		t.Fatalf("create dispatcher: %v", err)
	}
	created, err := store.Create(t.Context(), beadstore.CreateParams{
		ID:                 beadID,
		Title:              "Authoritative assignment " + beadID,
		Type:               "task",
		Status:             "open",
		AcceptanceCriteria: "Test: authoritative assignment | Assert: exact state",
	})
	if err != nil {
		t.Fatalf("create bead: %v", err)
	}
	conn := &assignmentClaimAuthoritativeConn{}
	worker := &trackedWorker{
		id:      "worker-" + beadID,
		state:   protocol.WorkerIdle,
		conn:    conn,
		encoder: json.NewEncoder(conn),
	}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()
	return &assignmentClaimAuthoritativeHarness{
		d: d, store: store, worktrees: worktrees, worker: worker, conn: conn, bead: *created,
	}
}

func assignmentClaimAuthoritativeEventCount(t *testing.T, db *sql.DB, eventType string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type=?`, eventType).Scan(&count); err != nil {
		t.Fatalf("count %s events: %v", eventType, err)
	}
	return count
}

func assignmentClaimAuthoritativeAssign(t *testing.T, h *assignmentClaimAuthoritativeHarness) (claims []bool, outcomes []assignmentSetupOutcome) {
	t.Helper()
	done := make(chan error, 1)
	ctx := t.Context()
	go func() {
		done <- h.d.assignBeadWithClaim(ctx, h.worker, h.bead, nil,
			func(claimed bool) { claims = append(claims, claimed) },
			func(outcome assignmentSetupOutcome) { outcomes = append(outcomes, outcome) })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("assign bead: %v", err)
		}
		assertDispatcherMutexAvailableWithin(t, h.d, 250*time.Millisecond)
	case <-time.After(2 * time.Second):
		t.Fatal("assign bead did not return; dispatcher lock was retained")
	}
	return claims, outcomes
}

func assignmentClaimAuthoritativePayload(t *testing.T, conn *assignmentClaimAuthoritativeConn) *protocol.AssignPayload {
	t.Helper()
	conn.mu.Lock()
	writes := slices.Clone(conn.writes)
	conn.mu.Unlock()
	if len(writes) != 1 {
		t.Fatalf("worker writes = %d, want 1", len(writes))
	}
	var message protocol.Message
	if err := json.Unmarshal(writes[0], &message); err != nil {
		t.Fatalf("decode assignment: %v", err)
	}
	if message.Type != protocol.MsgAssign || message.Assign == nil {
		t.Fatalf("worker message = %+v, want ASSIGN payload", message)
	}
	return message.Assign
}

func TestAssignmentClaimAuthoritativeSurvivorMutation(t *testing.T) {
	run := func(name string, test func(*testing.T)) {
		if t.Failed() {
			return
		}
		t.Run(name, test)
	}

	run("dirty reservation fields are reset before status failure", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-dirty-reservation")
		h.worker.assignmentID = 91
		h.worker.epicID = "old-epic"
		h.worker.isEpicDecomp = true
		h.worker.worktree = "/old/worktree"
		h.worker.baseBranch = "old-base"
		h.worker.targetBranch = "old-target"
		h.worker.runtime = "old-runtime"
		h.worker.model = "old-model"
		h.worker.reasoning = "old-reasoning"
		h.worker.reservationGen = 7
		h.store.mu.Lock()
		h.store.updateErr = errors.New("injected status failure")
		h.store.mu.Unlock()

		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		if !slices.Equal(claims, []bool{true}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) {
			t.Fatalf("callbacks = %v/%v", claims, outcomes)
		}
		if h.worker.state != protocol.WorkerIdle || h.worker.assignmentID != 0 || h.worker.beadID != "" ||
			h.worker.epicID != "" || h.worker.isEpicDecomp || h.worker.worktree != "" || h.worker.baseBranch != "" ||
			h.worker.targetBranch != "" || h.worker.runtime != "" || h.worker.model != "" || h.worker.reasoning != "" ||
			h.worker.reservationGen != 9 {
			t.Fatalf("released dirty reservation = %+v", h.worker)
		}
	})

	run("worker admission guards reject every invalid owner shape", func(t *testing.T) {
		t.Helper()
		cases := []struct {
			name   string
			mutate func(*assignmentClaimAuthoritativeHarness)
		}{
			{name: "missing", mutate: func(h *assignmentClaimAuthoritativeHarness) { delete(h.d.workers, h.worker.id) }},
			{name: "different pointer", mutate: func(h *assignmentClaimAuthoritativeHarness) {
				h.d.workers[h.worker.id] = &trackedWorker{id: h.worker.id, state: protocol.WorkerIdle}
			}},
			{name: "busy", mutate: func(h *assignmentClaimAuthoritativeHarness) { h.worker.state = protocol.WorkerBusy }},
			{name: "draining", mutate: func(h *assignmentClaimAuthoritativeHarness) { h.worker.drainAfterAssignment = true }},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				h := newAssignmentClaimAuthoritativeHarness(t, "claim-worker-"+strings.ReplaceAll(test.name, " ", "-"))
				h.d.mu.Lock()
				test.mutate(h)
				h.d.mu.Unlock()
				claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
				if !slices.Equal(claims, []bool{false}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) ||
					len(h.worktrees.created) != 0 {
					t.Fatalf("guard result = claims %v outcomes %v worktrees %v", claims, outcomes, h.worktrees.created)
				}
			})
		}
	})

	run("ephemeral assigning claim blocks duplicate", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-ephemeral-duplicate")
		h.d.mu.Lock()
		h.d.assigningBeads[h.bead.ID] = true
		h.d.mu.Unlock()
		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		if !slices.Equal(claims, []bool{false}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) ||
			h.worker.state != protocol.WorkerIdle || len(h.worktrees.created) != 0 ||
			assignmentClaimAuthoritativeEventCount(t, h.d.db, "assignment_race_detected") != 1 {
			t.Fatalf("duplicate result = claims %v outcomes %v worker %q worktrees %v", claims, outcomes, h.worker.state, h.worktrees.created)
		}
	})

	run("busy owner blocks while idle lookalike does not", func(t *testing.T) {
		t.Helper()
		for _, ownerState := range []protocol.WorkerState{protocol.WorkerBusy, protocol.WorkerReserved} {
			h := newAssignmentClaimAuthoritativeHarness(t, "claim-owner-"+string(ownerState))
			owner := &trackedWorker{id: "owner-" + string(ownerState), state: ownerState, beadID: h.bead.ID}
			h.d.mu.Lock()
			h.d.workers[owner.id] = owner
			h.d.mu.Unlock()
			claims, _ := assignmentClaimAuthoritativeAssign(t, h)
			if !slices.Equal(claims, []bool{false}) || h.worker.state != protocol.WorkerIdle || len(h.worktrees.created) != 0 {
				t.Fatalf("owner %q did not block: claims %v worker %q", ownerState, claims, h.worker.state)
			}
		}

		h := newAssignmentClaimAuthoritativeHarness(t, "claim-idle-lookalike")
		idle := &trackedWorker{id: "idle-lookalike", state: protocol.WorkerIdle, beadID: h.bead.ID}
		h.d.mu.Lock()
		h.d.workers[idle.id] = idle
		h.d.mu.Unlock()
		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		if !slices.Equal(claims, []bool{true}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupDelivered}) {
			t.Fatalf("idle lookalike callbacks = %v/%v", claims, outcomes)
		}
	})

	run("successful delivery carries capability search and exact worker state", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-rich-delivery")
		h.d.codeIndex = &assignmentClaimAuthoritativeCodeIndex{chunks: []CodeChunk{{
			FilePath: "pkg/example.go", StartLine: 4, EndLine: 9, Content: "func Example() {}",
		}}}
		h.bead.Model = "custom-provider-model"
		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		if !slices.Equal(claims, []bool{true}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupDelivered}) {
			t.Fatalf("delivery callbacks = %v/%v", claims, outcomes)
		}
		payload := assignmentClaimAuthoritativePayload(t, h.conn)
		if payload.Capability == "" ||
			!strings.Contains(payload.CodeSearchContext, "pkg/example.go:4-9") {
			t.Fatalf("payload capability/search = %q / %q", payload.Capability, payload.CodeSearchContext)
		}
		if h.worker.state != protocol.WorkerBusy || h.worker.assignmentID <= 0 || h.worker.beadID != h.bead.ID ||
			h.worker.worktree == "" || h.worker.baseBranch != "main" || h.worker.targetBranch != "main" ||
			h.worker.qgEvidenceDir != payload.QGEvidenceDir || h.worker.targetSHA != payload.TargetSHA ||
			h.worker.runtime != payload.Runtime || h.worker.model != "custom-provider-model" || h.worker.reasoning != payload.Reasoning ||
			h.worker.setupReservedAt != (time.Time{}) {
			t.Fatalf("delivered worker = %+v", h.worker)
		}
	})

	run("estimator is gated by both explicit model and estimate", func(t *testing.T) {
		t.Helper()
		cases := []struct {
			name      string
			model     string
			estimate  int
			wantCalls int
		}{
			{name: "needed", wantCalls: 1},
			{name: "explicit model", model: "fixed-model", wantCalls: 0},
			{name: "existing estimate", estimate: 8, wantCalls: 0},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				h := newAssignmentClaimAuthoritativeHarness(t, "claim-estimator-"+strings.ReplaceAll(test.name, " ", "-"))
				estimator := &assignmentClaimAuthoritativeEstimator{result: 3}
				h.d.estimator = estimator
				h.bead.Model = test.model
				h.bead.EstimatedMinutes = test.estimate
				assignmentClaimAuthoritativeAssign(t, h)
				payload := assignmentClaimAuthoritativePayload(t, h.conn)
				expectedBead := h.bead
				if test.wantCalls == 1 {
					expectedBead.EstimatedMinutes = estimator.result
				}
				wantRuntime, wantModel, wantReasoning := agentmodel.ResolveForBead("worker", expectedBead)
				if estimator.calls != test.wantCalls || payload.Runtime != wantRuntime || payload.Model != wantModel || payload.Reasoning != wantReasoning {
					t.Fatalf("estimator calls/route = %d/%q/%q/%q, want %d/%q/%q/%q",
						estimator.calls, payload.Runtime, payload.Model, payload.Reasoning,
						test.wantCalls, wantRuntime, wantModel, wantReasoning)
				}
			})
		}
	})

	run("empty branch metadata keeps default branch", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-empty-branch")
		h.bead.Metadata = map[string]any{MetaBranch: ""}
		assignmentClaimAuthoritativeAssign(t, h)
		if h.worker.baseBranch != "main" || h.worker.targetBranch != "main" ||
			!slices.Equal(h.worktrees.created, []string{h.bead.ID + "@main"}) {
			t.Fatalf("empty metadata branch = %q/%q creates %v", h.worker.baseBranch, h.worker.targetBranch, h.worktrees.created)
		}
	})

	run("absent worktree never probes existence", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-no-worktree")
		assignmentClaimAuthoritativeAssign(t, h)
		if len(h.worktrees.existsPaths) != 0 || assignmentClaimAuthoritativeEventCount(t, h.d.db, "stale_worktree_cleared") != 0 {
			t.Fatalf("absent worktree probes/events = %v/%d", h.worktrees.existsPaths,
				assignmentClaimAuthoritativeEventCount(t, h.d.db, "stale_worktree_cleared"))
		}
	})

	run("existing worktree is reused when it still exists", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-reuse-worktree")
		path := "/tmp/claim-authoritative-" + h.bead.ID
		h.d.mu.Lock()
		h.d.worktreeByBead[h.bead.ID] = path
		h.d.mu.Unlock()
		h.worktrees.existsFn = func(context.Context, string) bool { return true }
		assignmentClaimAuthoritativeAssign(t, h)
		if len(h.worktrees.created) != 0 || !slices.Equal(h.worktrees.existsPaths, []string{path}) || h.worker.worktree != path ||
			assignmentClaimAuthoritativeEventCount(t, h.d.db, "worktree_reused") != 1 {
			t.Fatalf("reuse = created %v probes %v worker %q", h.worktrees.created, h.worktrees.existsPaths, h.worker.worktree)
		}
	})

	run("missing tracked worktree is cleared and recreated", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-stale-worktree")
		stale := "/tmp/missing-" + h.bead.ID
		h.d.mu.Lock()
		h.d.worktreeByBead[h.bead.ID] = stale
		h.d.mu.Unlock()
		h.worktrees.existsFn = func(context.Context, string) bool { return false }
		assignmentClaimAuthoritativeAssign(t, h)
		if !slices.Equal(h.worktrees.existsPaths, []string{stale}) ||
			!slices.Equal(h.worktrees.created, []string{h.bead.ID + "@main"}) ||
			h.worker.worktree == stale || assignmentClaimAuthoritativeEventCount(t, h.d.db, "stale_worktree_cleared") != 1 {
			t.Fatalf("stale recovery = probes %v created %v worker %q", h.worktrees.existsPaths, h.worktrees.created, h.worker.worktree)
		}
	})

	run("epic ancestry resolution failure restores the durable claim", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-epic-resolution-error")
		h.bead.Epic = "missing-parent"
		h.store.mu.Lock()
		h.store.showErrs[h.bead.Epic] = errors.New("injected parent lookup failure")
		h.store.mu.Unlock()

		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		h.d.mu.Lock()
		_, assigning := h.d.assigningBeads[h.bead.ID]
		_, failed := h.d.worktreeFailures[h.bead.ID]
		h.d.mu.Unlock()
		if !slices.Equal(claims, []bool{true}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) ||
			assigning || !failed || h.worker.state != protocol.WorkerIdle ||
			!slices.Equal(h.store.observedStatuses(), []string{"in_progress", "open"}) || len(h.worktrees.created) != 0 ||
			assignmentClaimAuthoritativeEventCount(t, h.d.db, "epic_branch_resolve_error") != 1 {
			t.Fatalf("resolution cleanup = callbacks %v/%v assigning %v failed %v worker %q statuses %v creates %v",
				claims, outcomes, assigning, failed, h.worker.state, h.store.observedStatuses(), h.worktrees.created)
		}
	})

	run("assignment insert failure invokes seam and removes created worktree", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-insert-failure")
		if _, err := h.d.db.Exec(`
CREATE TRIGGER claim_authoritative_fail_assignment_insert
BEFORE INSERT ON assignments
BEGIN
  SELECT RAISE(ABORT, 'injected assignment insert failure');
END;`); err != nil {
			t.Fatalf("install assignment failure trigger: %v", err)
		}
		var observed error
		calls := 0
		h.d.afterAssignmentInsertFailure = func(err error) {
			calls++
			observed = err
		}

		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		path := "/tmp/claim-authoritative-" + h.bead.ID
		h.d.mu.Lock()
		_, assigning := h.d.assigningBeads[h.bead.ID]
		_, tracked := h.d.worktreeByBead[h.bead.ID]
		_, failed := h.d.worktreeFailures[h.bead.ID]
		h.d.mu.Unlock()
		if !slices.Equal(claims, []bool{true}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) ||
			calls != 1 || observed == nil || !strings.Contains(observed.Error(), "injected assignment insert failure") ||
			assigning || tracked || !failed || h.worker.state != protocol.WorkerIdle ||
			!slices.Equal(h.store.observedStatuses(), []string{"in_progress", "open"}) ||
			!slices.Equal(h.worktrees.removed, []string{path}) ||
			assignmentClaimAuthoritativeEventCount(t, h.d.db, "assignment_persist_failed") != 1 {
			t.Fatalf("insert cleanup = callbacks %v/%v seam %d/%v assigning %v tracked %v failed %v worker %q statuses %v removed %v",
				claims, outcomes, calls, observed, assigning, tracked, failed, h.worker.state,
				h.store.observedStatuses(), h.worktrees.removed)
		}
	})

	run("worktree creation failure releases reservation and reopens bead", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-worktree-create-error")
		h.worktrees.createFn = func(context.Context, string, string) (string, string, error) {
			return "", "", errors.New("injected worktree create failure")
		}
		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		h.d.mu.Lock()
		_, assigning := h.d.assigningBeads[h.bead.ID]
		_, failed := h.d.worktreeFailures[h.bead.ID]
		h.d.mu.Unlock()
		if !slices.Equal(claims, []bool{true}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) ||
			assigning || !failed || h.worker.state != protocol.WorkerIdle ||
			!slices.Equal(h.store.observedStatuses(), []string{"in_progress", "open"}) ||
			assignmentClaimAuthoritativeEventCount(t, h.d.db, "worktree_error") != 1 {
			t.Fatalf("create failure = callbacks %v/%v assigning %v failed %v worker %q statuses %v",
				claims, outcomes, assigning, failed, h.worker.state, h.store.observedStatuses())
		}
	})

	run("focus change after status claim aborts before worktree creation", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-focus-after-status")
		h.store.mu.Lock()
		h.store.updateHook = func(_ string, status string) {
			if status == "in_progress" {
				h.d.mu.Lock()
				h.d.focusVersion++
				h.d.mu.Unlock()
			}
		}
		h.store.mu.Unlock()
		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		if !slices.Equal(claims, []bool{true}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) ||
			h.worker.state != protocol.WorkerIdle || len(h.worktrees.created) != 0 ||
			!slices.Equal(h.store.observedStatuses(), []string{"in_progress", "open"}) ||
			assignmentClaimAuthoritativeEventCount(t, h.d.db, "assignment_aborted_focus_changed") != 1 {
			t.Fatalf("post-status focus abort = callbacks %v/%v worker %q creates %v statuses %v",
				claims, outcomes, h.worker.state, h.worktrees.created, h.store.observedStatuses())
		}
	})

	run("focus change during target evidence aborts persisted assignment", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-focus-after-insert")
		h.worktrees.branchHeadFn = func(context.Context, string) (string, error) {
			h.d.mu.Lock()
			h.d.focusVersion++
			h.d.mu.Unlock()
			return "target-focus-sha", nil
		}
		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		path := "/tmp/claim-authoritative-" + h.bead.ID
		var status string
		if err := h.d.db.QueryRow(`SELECT status FROM assignments WHERE bead_id=?`, h.bead.ID).Scan(&status); err != nil {
			t.Fatalf("load aborted assignment: %v", err)
		}
		if !slices.Equal(claims, []bool{true}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) ||
			status != "completed" || h.worker.state != protocol.WorkerIdle ||
			!slices.Equal(h.store.observedStatuses(), []string{"in_progress", "open"}) ||
			!slices.Equal(h.worktrees.removed, []string{path}) ||
			assignmentClaimAuthoritativeEventCount(t, h.d.db, "assignment_aborted_focus_changed") != 1 {
			t.Fatalf("post-insert focus abort = callbacks %v/%v assignment %q worker %q statuses %v removed %v",
				claims, outcomes, status, h.worker.state, h.store.observedStatuses(), h.worktrees.removed)
		}
	})

	run("reservation loss during target evidence completes orphan assignment", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-reservation-after-insert")
		h.worktrees.branchHeadFn = func(context.Context, string) (string, error) {
			h.d.mu.Lock()
			h.worker.reservationGen++
			h.d.mu.Unlock()
			return "target-reservation-sha", nil
		}
		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		var status string
		if err := h.d.db.QueryRow(`SELECT status FROM assignments WHERE bead_id=?`, h.bead.ID).Scan(&status); err != nil {
			t.Fatalf("load orphan assignment: %v", err)
		}
		if !slices.Equal(claims, []bool{true}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) ||
			status != "completed" || len(h.conn.writes) != 0 ||
			assignmentClaimAuthoritativeEventCount(t, h.d.db, "assignment_aborted_reservation_lost") != 1 {
			t.Fatalf("post-insert reservation loss = callbacks %v/%v assignment %q writes %d worker %+v",
				claims, outcomes, status, len(h.conn.writes), h.worker)
		}
	})

	run("final focus recheck cleans attached assignment", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-final-focus")
		estimator := &assignmentClaimAuthoritativeEstimator{result: 3, hook: func() {
			h.d.mu.Lock()
			h.d.focusVersion++
			h.d.mu.Unlock()
		}}
		h.d.estimator = estimator
		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		var status string
		if err := h.d.db.QueryRow(`SELECT status FROM assignments WHERE bead_id=?`, h.bead.ID).Scan(&status); err != nil {
			t.Fatalf("load final-focus assignment: %v", err)
		}
		if estimator.calls != 1 || !slices.Equal(claims, []bool{true}) ||
			!slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) || status != "completed" ||
			h.worker.state != protocol.WorkerIdle || len(h.conn.writes) != 0 ||
			assignmentClaimAuthoritativeEventCount(t, h.d.db, "assignment_aborted_focus_changed") != 1 {
			t.Fatalf("final focus = calls %d callbacks %v/%v assignment %q worker %+v writes %d",
				estimator.calls, claims, outcomes, status, h.worker, len(h.conn.writes))
		}
	})

	run("final reservation recheck completes attached orphan", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-final-reservation")
		estimator := &assignmentClaimAuthoritativeEstimator{result: 3, hook: func() {
			h.d.mu.Lock()
			h.worker.reservationGen++
			h.d.mu.Unlock()
		}}
		h.d.estimator = estimator
		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		var status string
		if err := h.d.db.QueryRow(`SELECT status FROM assignments WHERE bead_id=?`, h.bead.ID).Scan(&status); err != nil {
			t.Fatalf("load final-reservation assignment: %v", err)
		}
		if estimator.calls != 1 || !slices.Equal(claims, []bool{true}) ||
			!slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) || status != "completed" ||
			len(h.conn.writes) != 0 || assignmentClaimAuthoritativeEventCount(t, h.d.db, "assignment_aborted_reservation_lost") != 1 {
			t.Fatalf("final reservation = calls %d callbacks %v/%v assignment %q worker %+v writes %d",
				estimator.calls, claims, outcomes, status, h.worker, len(h.conn.writes))
		}
	})

	run("capability failure reopens and removes created worktree", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-capability-failure")
		if _, err := h.d.db.Exec(`
CREATE TRIGGER claim_authoritative_fail_capability
BEFORE INSERT ON assignment_capabilities
BEGIN
  SELECT RAISE(ABORT, 'injected capability failure');
END;`); err != nil {
			t.Fatalf("install capability trigger: %v", err)
		}
		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		path := "/tmp/claim-authoritative-" + h.bead.ID
		var status string
		if err := h.d.db.QueryRow(`SELECT status FROM assignments WHERE bead_id=?`, h.bead.ID).Scan(&status); err != nil {
			t.Fatalf("load capability assignment: %v", err)
		}
		if !slices.Equal(claims, []bool{true}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) ||
			status != "completed" || h.worker.state != protocol.WorkerIdle ||
			!slices.Equal(h.store.observedStatuses(), []string{"in_progress", "open"}) ||
			!slices.Equal(h.worktrees.removed, []string{path}) ||
			assignmentClaimAuthoritativeEventCount(t, h.d.db, "assignment_capability_issue_failed") != 1 {
			t.Fatalf("capability cleanup = callbacks %v/%v assignment %q worker %q statuses %v removed %v",
				claims, outcomes, status, h.worker.state, h.store.observedStatuses(), h.worktrees.removed)
		}
	})

	run("dead worker socket closes worker and durable assignment", func(t *testing.T) {
		t.Helper()
		h := newAssignmentClaimAuthoritativeHarness(t, "claim-dead-socket")
		h.conn.writeErr = errors.New("injected dead socket")
		claims, outcomes := assignmentClaimAuthoritativeAssign(t, h)
		path := "/tmp/claim-authoritative-" + h.bead.ID
		var status string
		if err := h.d.db.QueryRow(`SELECT status FROM assignments WHERE bead_id=?`, h.bead.ID).Scan(&status); err != nil {
			t.Fatalf("load dead-socket assignment: %v", err)
		}
		h.d.mu.Lock()
		_, workerTracked := h.d.workers[h.worker.id]
		_, worktreeTracked := h.d.worktreeByBead[h.bead.ID]
		h.d.mu.Unlock()
		h.conn.mu.Lock()
		closed := h.conn.closed
		h.conn.mu.Unlock()
		if !slices.Equal(claims, []bool{true}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) ||
			status != "completed" || workerTracked || worktreeTracked || !closed ||
			!slices.Equal(h.worktrees.removed, []string{path}) ||
			assignmentClaimAuthoritativeEventCount(t, h.d.db, "worktree_cleanup") != 1 {
			t.Fatalf("dead socket cleanup = callbacks %v/%v assignment %q tracked %v/%v closed %v removed %v",
				claims, outcomes, status, workerTracked, worktreeTracked, closed, h.worktrees.removed)
		}
	})
}
