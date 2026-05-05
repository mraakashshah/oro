package beadstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/cards"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// renderFacingReadMethods are the Store methods reachable from render paths
// (oro current, oro handoff, oro resume, oro pipeline status).
// Export is explicitly excluded per §4.7: it has its own transactional semantics.
var renderFacingReadMethods = []string{
	"Ready",
	"InProgress",
	"Blocked",
	"Closed",
	"Show",
	"HasChildren",
	"AllChildrenClosed",
	"FindByParentAndTag",
	"Journey",
	"LatestJourney",
}

// TestReadTxParity verifies:
//  1. Every render-facing read method on Store exists on ReadTx with an identical signature.
//  2. ReadTx methods are behavior-equivalent to Store methods (same filtering and runtime
//     enrichment for assignment-aware reads).
//  3. Cards reads inside WithReadTx see the snapshot established when the tx began,
//     proving the cards.ReadTx is bound to the same SQL transaction as bead reads.
func TestReadTxParity(t *testing.T) {
	t.Run("method_parity", func(t *testing.T) {
		storeType := reflect.TypeFor[beadstore.Store]()
		txType := reflect.TypeFor[beadstore.ReadTx]()

		for _, name := range renderFacingReadMethods {
			storeMethod, ok := storeType.MethodByName(name)
			if !ok {
				t.Errorf("Store.%s does not exist (update renderFacingReadMethods)", name)
				continue
			}
			txMethod, ok := txType.MethodByName(name)
			if !ok {
				t.Errorf("ReadTx.%s is missing", name)
				continue
			}
			if storeMethod.Type != txMethod.Type {
				t.Errorf("ReadTx.%s signature mismatch:\n  Store has %v\n  ReadTx has %v",
					name, storeMethod.Type, txMethod.Type)
			}
		}
	})

	t.Run("behavioral_parity_assignments", func(t *testing.T) {
		ctx := context.Background()
		db := openBeadDB(ctx, t)
		defer db.Close()
		store := beadstore.NewSQLiteStore(db)

		// Seed beads in different lifecycle states.
		mustCreate(ctx, t, store, beadstore.CreateParams{ID: "bd-ready-free", Title: "free", Priority: 1})
		mustCreate(ctx, t, store, beadstore.CreateParams{ID: "bd-ready-assigned", Title: "ready+assigned", Priority: 1})
		mustCreate(ctx, t, store, beadstore.CreateParams{ID: "bd-blocked-free", Title: "blocked", Priority: 1})
		mustCreate(ctx, t, store, beadstore.CreateParams{ID: "bd-blocked-assigned", Title: "blocked+assigned", Priority: 1})
		mustCreate(ctx, t, store, beadstore.CreateParams{ID: "bd-inprog", Title: "in_progress", Priority: 1})
		mustCreate(ctx, t, store, beadstore.CreateParams{ID: "bd-only-assigned", Title: "open+assigned", Priority: 1})

		// bd-ready-free → no blockers, no assignment (truly Ready)
		// bd-ready-assigned → no blockers but assignment (excluded from Ready, included in InProgress with WorkerID)
		// bd-blocked-free, bd-blocked-assigned: blocked by some other open bead
		// bd-inprog: status=in_progress, also assigned
		// bd-only-assigned: status=open, assigned, no blockers (excluded from Ready, surfaced via InProgress)

		mustExec(ctx, t, db, `UPDATE beads SET status='in_progress' WHERE id=?`, "bd-inprog")
		mustExec(ctx, t, db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, 'blocks')`, "bd-blocked-free", "bd-ready-free")
		mustExec(ctx, t, db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, 'blocks')`, "bd-blocked-assigned", "bd-ready-free")

		mustAssign(ctx, t, db, "bd-ready-assigned", "worker-A")
		mustAssign(ctx, t, db, "bd-blocked-assigned", "worker-B")
		mustAssign(ctx, t, db, "bd-inprog", "worker-C")
		mustAssign(ctx, t, db, "bd-only-assigned", "worker-D")

		// Sanity: blockers register only against open beads
		// (bd-ready-free is open, so its blockers in bead_deps still count).

		var (
			readyStore, blockedStore, inProgressStore []protocol.Bead
			showStore                                 *protocol.Bead
			readyTx, blockedTx, inProgressTx          []protocol.Bead
			showTx                                    *protocol.Bead
		)

		readyStore = mustReady(ctx, t, store)
		blockedStore = mustBlocked(ctx, t, store)
		inProgressStore = mustInProgress(ctx, t, store)
		showStore = mustShow(ctx, t, store, "bd-ready-assigned")

		err := store.WithReadTx(ctx, func(tx beadstore.ReadTx) error {
			var err error
			if readyTx, err = tx.Ready(ctx); err != nil {
				return err
			}
			if blockedTx, err = tx.Blocked(ctx); err != nil {
				return err
			}
			if inProgressTx, err = tx.InProgress(ctx); err != nil {
				return err
			}
			showTx, err = tx.Show(ctx, "bd-ready-assigned")
			return err
		})
		if err != nil {
			t.Fatalf("WithReadTx: %v", err)
		}

		assertSameIDsAndWorker(t, "Ready", readyStore, readyTx)
		assertSameIDsAndWorker(t, "Blocked", blockedStore, blockedTx)
		assertSameIDsAndWorker(t, "InProgress", inProgressStore, inProgressTx)

		// ReadTx must filter assigned beads from Ready (matches Store behavior).
		if containsID(readyTx, "bd-ready-assigned") {
			t.Errorf("tx.Ready returned bd-ready-assigned (should be filtered out: has active worker)")
		}
		// InProgress must include the assignment-only bead with WorkerID set.
		gotOnly := findByID(inProgressTx, "bd-only-assigned")
		if gotOnly == nil {
			t.Errorf("tx.InProgress missing bd-only-assigned (assignment-only bead must surface)")
		} else if gotOnly.WorkerID != "worker-D" {
			t.Errorf("tx.InProgress(bd-only-assigned).WorkerID = %q, want worker-D", gotOnly.WorkerID)
		}
		// Show must populate WorkerID via runtime enrichment.
		if showTx == nil {
			t.Fatalf("tx.Show(bd-ready-assigned) returned nil")
		}
		if showTx.WorkerID != "worker-A" {
			t.Errorf("tx.Show(bd-ready-assigned).WorkerID = %q, want worker-A", showTx.WorkerID)
		}
		if showStore != nil && showStore.WorkerID != showTx.WorkerID {
			t.Errorf("Show divergence: Store=%q ReadTx=%q", showStore.WorkerID, showTx.WorkerID)
		}
	})

	t.Run("cards_reads_share_tx_snapshot", func(t *testing.T) {
		ctx := context.Background()

		// Use a temp file + WAL mode so a separate *sql.DB can write while a read tx
		// is open. WAL gives readers a stable snapshot regardless of concurrent writes.
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "parity.db")
		dsn := "file:" + dbPath + "?_pragma=journal_mode(wal)&_pragma=busy_timeout(2000)"

		db1, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("open db1: %v", err)
		}
		defer db1.Close()
		db1.SetMaxOpenConns(1) // hold a single connection so the read tx claims it

		if _, err := db1.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("apply runtime schema: %v", err)
		}
		if err := protocol.MigrateBeadSchema(ctx, db1); err != nil {
			t.Fatalf("migrate bead schema: %v", err)
		}
		cardsStore, err := cards.NewStore(db1)
		if err != nil {
			t.Fatalf("new cards store: %v", err)
		}
		card, err := cardsStore.Create(ctx, cards.CardCreateParams{
			Type:        cards.CardTypeRule,
			Title:       "snapshot test",
			BodySummary: "v1",
			BodyFull:    "v1",
		})
		if err != nil {
			t.Fatalf("create card: %v", err)
		}

		db2, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("open db2: %v", err)
		}
		defer db2.Close()

		beadStore := beadstore.NewSQLiteStore(db1)

		err = beadStore.WithReadTx(ctx, func(tx beadstore.ReadTx) error {
			// First read inside the tx pins the snapshot.
			first, err := tx.Cards().Show(ctx, card.ID)
			if err != nil {
				return err
			}
			if first.BodyFull != "v1" {
				t.Errorf("first read: BodyFull=%q want v1", first.BodyFull)
			}

			// External write via a separate connection while the tx is still open.
			if _, err := db2.ExecContext(ctx,
				`UPDATE cards SET body_full='v2', updated_at=? WHERE id=?`,
				"2030-01-01T00:00:00Z", card.ID); err != nil {
				return err
			}

			// Second read inside the tx must still see the original snapshot.
			// If Cards() returned a fresh tx (or the raw db), it would see "v2"
			// and this assertion would fail — proving the cards.ReadTx is bound
			// to the same SQL transaction as the bead reads.
			second, err := tx.Cards().Show(ctx, card.ID)
			if err != nil {
				return err
			}
			if second.BodyFull != "v1" {
				t.Errorf("snapshot violation: cards read inside tx saw %q after external write (want v1 — indicates cards.ReadTx is NOT bound to the bead tx)", second.BodyFull)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WithReadTx: %v", err)
		}

		// Sanity: after the tx commits, a fresh read sees the external write.
		after, err := cardsStore.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("post-tx Show: %v", err)
		}
		if after.BodyFull != "v2" {
			t.Errorf("post-tx Show: BodyFull=%q want v2 (external write should now be visible)", after.BodyFull)
		}
	})
}

// --- test helpers ---

func openBeadDB(ctx context.Context, t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		_ = db.Close()
		t.Fatalf("apply runtime schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate bead schema: %v", err)
	}
	return db
}

func mustCreate(ctx context.Context, t *testing.T, store *beadstore.SQLiteStore, params beadstore.CreateParams) {
	t.Helper()
	if _, err := store.Create(ctx, params); err != nil {
		t.Fatalf("create %s: %v", params.ID, err)
	}
}

func mustExec(ctx context.Context, t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func mustAssign(ctx context.Context, t *testing.T, db *sql.DB, beadID, workerID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, '', 'active')`,
		beadID, workerID); err != nil {
		t.Fatalf("assign %s→%s: %v", beadID, workerID, err)
	}
}

func mustReady(ctx context.Context, t *testing.T, store *beadstore.SQLiteStore) []protocol.Bead {
	t.Helper()
	beads, err := store.Ready(ctx)
	if err != nil {
		t.Fatalf("Store.Ready: %v", err)
	}
	return beads
}

func mustBlocked(ctx context.Context, t *testing.T, store *beadstore.SQLiteStore) []protocol.Bead {
	t.Helper()
	beads, err := store.Blocked(ctx)
	if err != nil {
		t.Fatalf("Store.Blocked: %v", err)
	}
	return beads
}

func mustInProgress(ctx context.Context, t *testing.T, store *beadstore.SQLiteStore) []protocol.Bead {
	t.Helper()
	beads, err := store.InProgress(ctx)
	if err != nil {
		t.Fatalf("Store.InProgress: %v", err)
	}
	return beads
}

func mustShow(ctx context.Context, t *testing.T, store *beadstore.SQLiteStore, id string) *protocol.Bead {
	t.Helper()
	bead, err := store.Show(ctx, id)
	if err != nil {
		t.Fatalf("Store.Show(%s): %v", id, err)
	}
	return bead
}

func containsID(beads []protocol.Bead, id string) bool {
	for i := range beads {
		if beads[i].ID == id {
			return true
		}
	}
	return false
}

func findByID(beads []protocol.Bead, id string) *protocol.Bead {
	for i := range beads {
		if beads[i].ID == id {
			return &beads[i]
		}
	}
	return nil
}

// assertSameIDsAndWorker asserts the two slices contain the same set of bead IDs
// with the same WorkerID values (the parts that drive render output).
func assertSameIDsAndWorker(t *testing.T, op string, want, got []protocol.Bead) {
	t.Helper()
	wantMap := map[string]string{}
	for _, b := range want {
		wantMap[b.ID] = b.WorkerID
	}
	gotMap := map[string]string{}
	for _, b := range got {
		gotMap[b.ID] = b.WorkerID
	}
	if !reflect.DeepEqual(wantMap, gotMap) {
		wantIDs := sortedKeys(wantMap)
		gotIDs := sortedKeys(gotMap)
		t.Errorf("%s id/worker divergence:\n  Store: %v\n  ReadTx: %v", op, formatIDWorker(wantIDs, wantMap), formatIDWorker(gotIDs, gotMap))
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func formatIDWorker(ids []string, m map[string]string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if w := m[id]; w != "" {
			out = append(out, id+"@"+w)
		} else {
			out = append(out, id)
		}
	}
	return out
}
