//nolint:testpackage // The acceptance proof exercises the dispatcher persistence boundary.
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/cards"
	"oro/pkg/protocol"
	"oro/pkg/reviewcontract"
)

func TestReviewArtifactAndFindingOverflow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "checkpoints.sqlite")
	artifactDir := filepath.Join(root, "review-recovery")
	store := openReviewCheckpointStore(ctx, t, dbPath)
	checkpoint, err := store.CreateOrReuse(ctx, reviewCheckpointInput("oro-overflow"))
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}

	t.Run("inline stays inline and bounded", func(t *testing.T) {
		findings := []reviewcontract.Finding{reviewOverflowFinding("inline", "small")}
		recovery, err := prepareReviewRecovery(artifactDir, checkpoint.ID, "rejected-head", "acceptance", 1, findings)
		if err != nil {
			t.Fatalf("prepare inline recovery: %v", err)
		}
		if recovery.FindingsRef != nil {
			t.Fatalf("inline recovery reference = %#v, want nil", recovery.FindingsRef)
		}
		if !reflect.DeepEqual(recovery.Findings, findings) {
			t.Fatalf("inline findings changed: got %#v want %#v", recovery.Findings, findings)
		}
		assertReviewRecoveryPayloadBounded(t, recovery)
	})

	findings := []reviewcontract.Finding{
		reviewOverflowFinding("critical-overflow", strings.Repeat("lossless-detail-", 20_000)),
		reviewOverflowFinding("important-overflow", strings.Repeat("exact-required-action-", 8_000)),
	}
	recovery, err := prepareReviewRecovery(artifactDir, checkpoint.ID, "rejected-head", "acceptance", 2, findings)
	if err != nil {
		t.Fatalf("prepare overflow recovery: %v", err)
	}
	if recovery.FindingsRef == nil {
		t.Fatal("overflow recovery reference is nil")
	}
	if recovery.Findings != nil {
		t.Fatalf("overflow inline findings = %#v, want nil", recovery.Findings)
	}
	assertReviewRecoveryPayloadBounded(t, recovery)

	if err := store.SaveRejectedFindings(ctx, checkpoint.ID, findings, recovery.FindingsRef); err != nil {
		t.Fatalf("commit rejected findings and reference: %v", err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatalf("close store before restart: %v", err)
	}

	restarted := openReviewCheckpointStore(ctx, t, dbPath)
	loadedRecovery, err := restarted.LoadReviewRecovery(ctx, checkpoint.ID)
	if err != nil {
		t.Fatalf("reload review recovery: %v", err)
	}
	if loadedRecovery.Findings != nil {
		t.Fatalf("restart regenerated partial inline findings: %#v", loadedRecovery.Findings)
	}
	if !reflect.DeepEqual(loadedRecovery.FindingsRef, recovery.FindingsRef) {
		t.Fatalf("restart reference = %#v, want exact %#v", loadedRecovery.FindingsRef, recovery.FindingsRef)
	}
	reloadedFindings, err := LoadRecoveryArtifact(*loadedRecovery.FindingsRef)
	if err != nil {
		t.Fatalf("load persisted recovery artifact: %v", err)
	}
	if !reflect.DeepEqual(reloadedFindings, findings) {
		t.Fatalf("reloaded findings changed: got %#v want %#v", reloadedFindings, findings)
	}
	artifactInfo, err := os.Stat(recovery.FindingsRef.Path)
	if err != nil {
		t.Fatalf("stat recovery artifact: %v", err)
	}
	if artifactInfo.Mode().Perm() != recoveryArtifactFileMode {
		t.Fatalf("artifact mode = %o, want %o", artifactInfo.Mode().Perm(), recoveryArtifactFileMode)
	}

	t.Run("canonical payload builder preserves the exact bounded reference", func(t *testing.T) {
		d, beadSource, _, _, _, _ := newTestDispatcher(t)
		beadSource.shown["oro-overflow"] = &protocol.BeadDetail{
			Title:              strings.Repeat("title-", 1024),
			Description:        strings.Repeat("description-", 2048),
			AcceptanceCriteria: strings.Repeat("acceptance-", 2048),
		}
		d.shutdownRunner = &mockCommandRunner{}
		workerProgram := filepath.Join(t.TempDir(), "worker-program.md")
		if err := os.WriteFile(workerProgram, []byte(strings.Repeat("w", maxWorkerProgramSize)), 0o600); err != nil {
			t.Fatalf("write maximum worker program: %v", err)
		}
		d.cfg.WorkerProgram = workerProgram
		d.cardStore = &staticRelevantCardStore{result: maximumAssignmentTestCards()}
		payload := d.buildAssignPayload(ctx, &trackedWorker{
			beadID:       "oro-overflow",
			worktree:     "/tmp/oro-overflow",
			runtime:      strings.Repeat("runtime-", 32),
			model:        strings.Repeat("model-", 32),
			reasoning:    strings.Repeat("reasoning-", 32),
			targetBranch: strings.Repeat("target-", 32),
		}, 2, strings.Repeat("feedback-", 2048), strings.Repeat("memory-", 2048), WorkerExecutionContext{
			AssignmentID:   19,
			Generation:     1,
			ActorRole:      strings.Repeat("role-", 32),
			Project:        strings.Repeat("project-", 32),
			Capability:     strings.Repeat("capability-", 32),
			ReviewRecovery: &loadedRecovery,
		})
		payload.CodeSearchContext = strings.Repeat("c", maxCodeSearchContextSize)
		if payload.ReviewRecovery == nil || !reflect.DeepEqual(payload.ReviewRecovery.FindingsRef, recovery.FindingsRef) {
			t.Fatalf("built recovery = %#v, want exact reference %#v", payload.ReviewRecovery, recovery.FindingsRef)
		}
		if len(payload.WorkerProgram) != maxWorkerProgramSize || len(payload.CodeSearchContext) != maxCodeSearchContextSize {
			t.Fatalf("canonical bounded fields = worker program %d/code search %d, want %d/%d",
				len(payload.WorkerProgram), len(payload.CodeSearchContext), maxWorkerProgramSize, maxCodeSearchContextSize)
		}
		deckJSON, err := json.Marshal(payload.Cards.Deck)
		if err != nil {
			t.Fatalf("marshal bounded deck: %v", err)
		}
		inlinedJSON, err := json.Marshal(payload.Cards.Inlined)
		if err != nil {
			t.Fatalf("marshal bounded inline cards: %v", err)
		}
		if len(deckJSON) <= maxAssignmentCardDeckJSONSize/2 || len(deckJSON) > maxAssignmentCardDeckJSONSize {
			t.Fatalf("canonical deck bytes = %d, want (%d, %d]", len(deckJSON), maxAssignmentCardDeckJSONSize/2, maxAssignmentCardDeckJSONSize)
		}
		if len(inlinedJSON) <= maxAssignmentCardInlinedJSONSize/2 || len(inlinedJSON) > maxAssignmentCardInlinedJSONSize {
			t.Fatalf("canonical inline card bytes = %d, want (%d, %d]", len(inlinedJSON), maxAssignmentCardInlinedJSONSize/2, maxAssignmentCardInlinedJSONSize)
		}
		assertReviewRecoveryPayloadBounded(t, *payload.ReviewRecovery)
		messageJSON, err := json.Marshal(protocol.Message{Type: protocol.MsgAssign, Assign: payload})
		if err != nil {
			t.Fatalf("marshal canonical maximum-field ASSIGN: %v", err)
		}
		if len(messageJSON) >= protocol.MaxMessageSize {
			t.Fatalf("canonical maximum-field ASSIGN bytes = %d, want < %d", len(messageJSON), protocol.MaxMessageSize)
		}
	})

	t.Run("staged recovery transport emits no findings event payload", func(t *testing.T) {
		if _, err := restarted.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  type TEXT NOT NULL,
  source TEXT NOT NULL,
  bead_id TEXT,
  worker_id TEXT,
  payload TEXT
)`); err != nil {
			t.Fatalf("create event observation surface: %v", err)
		}
		if err := restarted.SaveRejectedFindings(ctx, checkpoint.ID, findings, recovery.FindingsRef); err != nil {
			t.Fatalf("replay staged recovery with event surface: %v", err)
		}
		var findingsEvents int
		if err := restarted.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM events
WHERE COALESCE(payload, '') LIKE '%critical-overflow%'
   OR COALESCE(payload, '') LIKE '%important-overflow%'`).Scan(&findingsEvents); err != nil {
			t.Fatalf("query findings-bearing events: %v", err)
		}
		if findingsEvents != 0 {
			t.Fatalf("findings-bearing event payloads = %d, want 0; staged recovery transport is ASSIGN-only", findingsEvents)
		}
	})

	t.Run("rejected retention and mismatched replacement are fail closed", func(t *testing.T) {
		prunable, err := restarted.ListPrunableArtifacts(ctx, time.Now().Add(24*time.Hour))
		if err != nil {
			t.Fatalf("list prunable artifacts: %v", err)
		}
		if len(prunable) != 0 {
			t.Fatalf("rejected recovery artifact is prunable: %#v", prunable)
		}

		mismatched := append([]reviewcontract.Finding(nil), findings...)
		mismatched[0].Detail += " changed"
		if err := restarted.SaveRejectedFindings(ctx, checkpoint.ID, mismatched, recovery.FindingsRef); err == nil {
			t.Fatal("mismatched findings committed against existing artifact reference")
		}
		stillLoaded, err := restarted.LoadReviewRecovery(ctx, checkpoint.ID)
		if err != nil {
			t.Fatalf("reload after rejected replacement: %v", err)
		}
		if !reflect.DeepEqual(stillLoaded.FindingsRef, recovery.FindingsRef) {
			t.Fatalf("reference after rejected replacement = %#v, want %#v", stillLoaded.FindingsRef, recovery.FindingsRef)
		}
	})

	t.Run("idempotent concurrent persistence keeps one exact artifact", func(t *testing.T) {
		const writers = 8
		refs := make([]ReviewRecoveryArtifactRef, writers)
		errs := make([]error, writers)
		var wg sync.WaitGroup
		for i := range writers {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				refs[index], errs[index] = PersistRecoveryArtifact(artifactDir, checkpoint.ID, findings)
			}(i)
		}
		wg.Wait()
		for i := range writers {
			if errs[i] != nil {
				t.Fatalf("writer %d: %v", i, errs[i])
			}
			if !reflect.DeepEqual(refs[i], *recovery.FindingsRef) {
				t.Fatalf("writer %d ref = %#v, want %#v", i, refs[i], *recovery.FindingsRef)
			}
		}
		entries, err := os.ReadDir(artifactDir)
		if err != nil {
			t.Fatalf("read artifact directory: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("artifact files = %d, want one durable identity", len(entries))
		}
	})

	t.Run("missing or corrupt artifact fails typed without partial findings", func(t *testing.T) {
		missing := *recovery.FindingsRef
		missing.Path = filepath.Join(artifactDir, "missing.json")
		got, err := LoadRecoveryArtifact(missing)
		assertTypedRecoveryFailure(t, got, err, RecoveryArtifactMissing)

		movedPath := recovery.FindingsRef.Path + ".moved"
		if err := os.Rename(recovery.FindingsRef.Path, movedPath); err != nil {
			t.Fatalf("move recovery artifact: %v", err)
		}
		loaded, err := restarted.LoadReviewRecovery(ctx, checkpoint.ID)
		assertTypedStoredRecoveryFailure(t, loaded, err, RecoveryArtifactMissing)
		if err := os.Rename(movedPath, recovery.FindingsRef.Path); err != nil {
			t.Fatalf("restore recovery artifact: %v", err)
		}

		if err := os.WriteFile(recovery.FindingsRef.Path, []byte("corrupt"), 0o600); err != nil {
			t.Fatalf("corrupt recovery artifact: %v", err)
		}
		got, err = LoadRecoveryArtifact(*recovery.FindingsRef)
		assertTypedRecoveryFailure(t, got, err, RecoveryArtifactCorrupt)
		loaded, err = restarted.LoadReviewRecovery(ctx, checkpoint.ID)
		assertTypedStoredRecoveryFailure(t, loaded, err, RecoveryArtifactCorrupt)
	})
}

func maximumAssignmentTestCards() cards.RelevantCards {
	deck := make([]cards.DeckCard, 0, 300)
	for i := 0; i < cap(deck); i++ {
		deck = append(deck, cards.DeckCard{
			ID:          fmt.Sprintf("deck-%03d", i),
			Type:        cards.CardTypePattern,
			Title:       strings.Repeat("title", 8),
			BodySummary: strings.Repeat("summary", 96),
			Score:       1,
			Tags:        []string{"dispatcher", "maximum"},
		})
	}
	inlined := make([]cards.InlinedCard, 0, 220)
	for i := 0; i < cap(inlined); i++ {
		inlined = append(inlined, cards.InlinedCard{
			ID:          fmt.Sprintf("inline-%03d", i),
			Type:        cards.CardTypePattern,
			Title:       strings.Repeat("title", 8),
			BodySummary: strings.Repeat("summary", 32),
			BodyFull:    strings.Repeat("full", 128),
			Score:       1,
			Tags:        []string{"dispatcher", "maximum"},
		})
	}
	return cards.RelevantCards{Deck: deck, Inlined: inlined}
}

func TestReviewRecoveryArtifactRetentionRemovesUnreferencedFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	artifactDir := filepath.Join(root, "review-recovery")
	store := openReviewCheckpointStore(ctx, t, filepath.Join(root, "checkpoints.sqlite"))
	checkpoint, err := store.CreateOrReuse(ctx, reviewCheckpointInput("oro-artifact-retention"))
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}

	findingsA := []reviewcontract.Finding{reviewOverflowFinding("A", "committed first")}
	refA, err := PersistRecoveryArtifact(artifactDir, checkpoint.ID, findingsA)
	if err != nil {
		t.Fatalf("persist A: %v", err)
	}
	if err := store.SaveRejectedFindings(ctx, checkpoint.ID, findingsA, &refA); err != nil {
		t.Fatalf("commit A: %v", err)
	}
	if err := store.CompareAndSwap(ctx, checkpoint.ID, ReviewCheckpointStateRejected, ReviewCheckpointStateReviewRunning); err != nil {
		t.Fatalf("start replacement review: %v", err)
	}

	findingsB := []reviewcontract.Finding{reviewOverflowFinding("B", "durable replacement")}
	refB, err := PersistRecoveryArtifact(artifactDir, checkpoint.ID, findingsB)
	if err != nil {
		t.Fatalf("persist B: %v", err)
	}
	if err := store.SaveRejectedFindings(ctx, checkpoint.ID, findingsB, &refB); err != nil {
		t.Fatalf("commit B: %v", err)
	}

	findingsC := []reviewcontract.Finding{reviewOverflowFinding("C", "database write loses CAS")}
	refC, err := PersistRecoveryArtifact(artifactDir, checkpoint.ID, findingsC)
	if err != nil {
		t.Fatalf("persist C: %v", err)
	}
	if err := store.SaveRejectedFindings(ctx, checkpoint.ID, findingsC, &refC); !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("commit C error = %v, want ErrCheckpointConflict", err)
	}

	// A fresh unreferenced contender may still be between file persistence and
	// its DB commit. The retention grace window must protect it from this sweep.
	findingsFresh := []reviewcontract.Finding{reviewOverflowFinding("fresh", "concurrent contender")}
	refFresh, err := PersistRecoveryArtifact(artifactDir, checkpoint.ID, findingsFresh)
	if err != nil {
		t.Fatalf("persist fresh contender: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, path := range []string{refA.Path, refB.Path, refC.Path} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("age artifact %s: %v", path, err)
		}
	}

	d := &Dispatcher{
		db:                        store.db,
		nowFunc:                   time.Now,
		reviewArtifactRetention:   time.Hour,
		reviewRecoveryArtifactDir: artifactDir,
	}
	d.pruneReviewArtifacts(ctx)

	for name, path := range map[string]string{"A": refA.Path, "C": refC.Path} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unreferenced artifact %s stat error = %v, want not exist", name, err)
		}
	}
	for name, path := range map[string]string{"B": refB.Path, "fresh": refFresh.Path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained artifact %s stat: %v", name, err)
		}
	}
}

func TestReviewRecoveryArtifactRenewalCannotBePrunedBeforeCheckpointCommit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	artifactDir := filepath.Join(root, "review-recovery")
	store := openReviewCheckpointStore(ctx, t, filepath.Join(root, "checkpoints.sqlite"))
	checkpoint, err := store.CreateOrReuse(ctx, reviewCheckpointInput("oro-renew-prune-race"))
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	findings := []reviewcontract.Finding{reviewOverflowFinding("same-content", "renew deterministic path")}
	oldRef, err := PersistRecoveryArtifact(artifactDir, checkpoint.ID, findings)
	if err != nil {
		t.Fatalf("persist old artifact: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldRef.Path, old, old); err != nil {
		t.Fatalf("age old artifact: %v", err)
	}

	classified := make(chan struct{})
	allowDelete := make(chan struct{})
	d := &Dispatcher{
		db:                        store.db,
		nowFunc:                   time.Now,
		reviewArtifactRetention:   time.Hour,
		reviewRecoveryArtifactDir: artifactDir,
		testReviewArtifactBeforeDelete: func(artifact ArtifactRef) {
			if artifact.Path != oldRef.Path {
				return
			}
			close(classified)
			<-allowDelete
		},
	}
	pruneDone := make(chan struct{})
	go func() {
		defer close(pruneDone)
		d.pruneReviewArtifacts(ctx)
	}()
	<-classified

	type saveResult struct {
		ref ReviewRecoveryArtifactRef
		err error
	}
	saved := make(chan saveResult, 1)
	go func() {
		ref, persistErr := PersistRecoveryArtifact(artifactDir, checkpoint.ID, findings)
		if persistErr == nil {
			persistErr = store.SaveRejectedFindings(ctx, checkpoint.ID, findings, &ref)
		}
		saved <- saveResult{ref: ref, err: persistErr}
	}()

	var result saveResult
	select {
	case result = <-saved:
		close(allowDelete)
		<-pruneDone
	case <-time.After(250 * time.Millisecond):
		// A synchronized pruner owns the artifact lifecycle until its stale
		// deletion completes; renewal then recreates and commits the path.
		close(allowDelete)
		<-pruneDone
		result = <-saved
	}
	if result.err != nil {
		t.Fatalf("renew and commit artifact: %v", result.err)
	}
	loaded, err := store.LoadReviewRecovery(ctx, checkpoint.ID)
	if err != nil {
		t.Fatalf("load committed recovery after prune race: %v", err)
	}
	if loaded.FindingsRef == nil || !reflect.DeepEqual(*loaded.FindingsRef, result.ref) {
		t.Fatalf("loaded recovery ref = %#v, want %#v", loaded.FindingsRef, result.ref)
	}
	got, err := LoadRecoveryArtifact(result.ref)
	if err != nil {
		t.Fatalf("load renewed artifact after checkpoint commit: %v", err)
	}
	if !reflect.DeepEqual(got, findings) {
		t.Fatalf("renewed artifact findings = %#v, want %#v", got, findings)
	}
}

func TestPersistRecoveryArtifactRejectsSymlinkedDirectoryComponents(t *testing.T) {
	findings := []reviewcontract.Finding{reviewOverflowFinding("confined", "do not escape the artifact directory")}

	t.Run("artifact directory symlink", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Chmod(outside, 0o751); err != nil {
			t.Fatalf("set outside mode: %v", err)
		}
		artifactDir := filepath.Join(root, "review-recovery")
		if err := os.Symlink(outside, artifactDir); err != nil {
			t.Fatalf("symlink artifact directory: %v", err)
		}

		if _, err := PersistRecoveryArtifact(artifactDir, 71, findings); err == nil {
			t.Fatal("persist through artifact directory symlink succeeded, want error")
		}
		assertDirectoryUntouched(t, outside, 0o751)
	})

	t.Run("intermediate directory symlink", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Chmod(outside, 0o751); err != nil {
			t.Fatalf("set outside mode: %v", err)
		}
		if err := os.Symlink(outside, filepath.Join(root, ".oro")); err != nil {
			t.Fatalf("symlink intermediate directory: %v", err)
		}

		if _, err := PersistRecoveryArtifact(filepath.Join(root, ".oro", "review-recovery"), 72, findings); err == nil {
			t.Fatal("persist through intermediate directory symlink succeeded, want error")
		}
		assertDirectoryUntouched(t, outside, 0o751)
	})
}

func TestPersistRecoveryArtifactSyncsCreatedDirectoriesBeforeReturningReference(t *testing.T) {
	root := t.TempDir()
	artifactDir := filepath.Join(root, ".oro", "review-recovery")
	findings := []reviewcontract.Finding{reviewOverflowFinding("durable-directories", "sync every created directory entry")}
	syncFailure := errors.New("injected parent directory sync failure")
	var syncs []string
	failed := false
	syncDirectory := func(directory *os.File) error {
		syncs = append(syncs, filepath.Base(directory.Name()))
		if filepath.Base(directory.Name()) == filepath.Base(root) && !failed {
			failed = true
			return syncFailure
		}
		return directory.Sync()
	}

	ref, err := persistRecoveryArtifactWithDirSync(artifactDir, 73, findings, syncDirectory)
	if !errors.Is(err, syncFailure) {
		t.Fatalf("first persist error = %v, want injected sync failure", err)
	}
	if ref != (ReviewRecoveryArtifactRef{}) {
		t.Fatalf("first persist returned reference before directory durability: %#v", ref)
	}
	if _, err := os.Stat(filepath.Join(root, ".oro")); err != nil {
		t.Fatalf("created .oro directory after failed sync: %v", err)
	}

	ref, err = persistRecoveryArtifactWithDirSync(artifactDir, 73, findings, syncDirectory)
	if err != nil {
		t.Fatalf("retry persist after parent sync failure: %v", err)
	}
	wantSyncs := []string{filepath.Base(root), filepath.Base(root), ".oro", "review-recovery"}
	if !reflect.DeepEqual(syncs, wantSyncs) {
		t.Fatalf("directory sync sequence = %v, want retry durability sequence %v", syncs, wantSyncs)
	}
	loaded, err := LoadRecoveryArtifact(ref)
	if err != nil {
		t.Fatalf("load artifact after durable retry: %v", err)
	}
	if !reflect.DeepEqual(loaded, findings) {
		t.Fatalf("loaded findings = %#v, want %#v", loaded, findings)
	}
}

func TestLoadRecoveryArtifactRejectsSymlinkedPathComponents(t *testing.T) {
	findings := []reviewcontract.Finding{reviewOverflowFinding("confined-load", "never follow replacement symlinks")}

	t.Run("artifact file symlink", func(t *testing.T) {
		root := t.TempDir()
		ref, err := PersistRecoveryArtifact(filepath.Join(root, ".oro", "review-recovery"), 74, findings)
		if err != nil {
			t.Fatalf("persist artifact: %v", err)
		}
		data, err := os.ReadFile(ref.Path)
		if err != nil {
			t.Fatalf("read persisted artifact: %v", err)
		}
		outsidePath := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outsidePath, data, recoveryArtifactFileMode); err != nil {
			t.Fatalf("write outside artifact: %v", err)
		}
		if err := os.Remove(ref.Path); err != nil {
			t.Fatalf("remove persisted artifact before replacement: %v", err)
		}
		if err := os.Symlink(outsidePath, ref.Path); err != nil {
			t.Fatalf("replace artifact with symlink: %v", err)
		}

		if loaded, err := LoadRecoveryArtifact(ref); loaded != nil || !errors.Is(err, ErrRecoveryArtifactCorrupt) {
			t.Fatalf("load through artifact symlink = %#v, %v; want nil ErrRecoveryArtifactCorrupt", loaded, err)
		}
		assertFileContent(t, outsidePath, data)
	})

	t.Run("intermediate directory symlink", func(t *testing.T) {
		root := t.TempDir()
		artifactDir := filepath.Join(root, ".oro", "review-recovery")
		ref, err := PersistRecoveryArtifact(artifactDir, 75, findings)
		if err != nil {
			t.Fatalf("persist artifact: %v", err)
		}
		data, err := os.ReadFile(ref.Path)
		if err != nil {
			t.Fatalf("read persisted artifact: %v", err)
		}
		outsideOro := filepath.Join(t.TempDir(), "outside-oro")
		outsideArtifactDir := filepath.Join(outsideOro, "review-recovery")
		if err := os.MkdirAll(outsideArtifactDir, recoveryArtifactDirMode); err != nil {
			t.Fatalf("create outside artifact directory: %v", err)
		}
		outsidePath := filepath.Join(outsideArtifactDir, filepath.Base(ref.Path))
		if err := os.WriteFile(outsidePath, data, recoveryArtifactFileMode); err != nil {
			t.Fatalf("write outside artifact: %v", err)
		}
		if err := os.Rename(filepath.Join(root, ".oro"), filepath.Join(root, ".oro-original")); err != nil {
			t.Fatalf("move original .oro directory: %v", err)
		}
		if err := os.Symlink(outsideOro, filepath.Join(root, ".oro")); err != nil {
			t.Fatalf("replace .oro with symlink: %v", err)
		}

		if loaded, err := LoadRecoveryArtifact(ref); loaded != nil || !errors.Is(err, ErrRecoveryArtifactCorrupt) {
			t.Fatalf("load through intermediate symlink = %#v, %v; want nil ErrRecoveryArtifactCorrupt", loaded, err)
		}
		assertFileContent(t, outsidePath, data)
	})
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read protected outside file: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("protected outside file content = %q, want %q", got, want)
	}
}

func assertDirectoryUntouched(t *testing.T, path string, wantMode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat outside directory: %v", err)
	}
	if info.Mode().Perm() != wantMode {
		t.Fatalf("outside directory mode = %o, want %o", info.Mode().Perm(), wantMode)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read outside directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("outside directory entries = %d, want none: %#v", len(entries), entries)
	}
}

func TestPrepareReviewRecoveryExactInlineBoundary(t *testing.T) {
	root := t.TempDir()
	base := protocol.ReviewRecovery{
		CheckpointID:    41,
		RejectedHeadSHA: "rejected-head",
		Findings:        []reviewcontract.Finding{{ID: "boundary", Severity: reviewcontract.SevImportant}},
		Attempt:         2,
		AcceptanceHash:  "acceptance",
	}
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("marshal base recovery: %v", err)
	}
	padding := maxReviewRecoveryInlineBytes - len(encoded)
	if padding <= 0 {
		t.Fatalf("base recovery bytes = %d, want below %d", len(encoded), maxReviewRecoveryInlineBytes)
	}
	findings := []reviewcontract.Finding{{ID: "boundary", Severity: reviewcontract.SevImportant, Detail: strings.Repeat("x", padding)}}
	boundary := base
	boundary.Findings = findings
	encoded, err = json.Marshal(boundary)
	if err != nil {
		t.Fatalf("marshal boundary recovery: %v", err)
	}
	if len(encoded) != maxReviewRecoveryInlineBytes {
		t.Fatalf("boundary recovery bytes = %d, want exactly %d", len(encoded), maxReviewRecoveryInlineBytes)
	}

	inline, err := prepareReviewRecovery(root, boundary.CheckpointID, boundary.RejectedHeadSHA, boundary.AcceptanceHash, boundary.Attempt, findings)
	if err != nil {
		t.Fatalf("prepare exact boundary recovery: %v", err)
	}
	if inline.FindingsRef != nil || !reflect.DeepEqual(inline.Findings, findings) {
		t.Fatalf("exact boundary recovery = %#v, want findings inline", inline)
	}

	findings[0].Detail += "x"
	overflow, err := prepareReviewRecovery(root, boundary.CheckpointID, boundary.RejectedHeadSHA, boundary.AcceptanceHash, boundary.Attempt, findings)
	if err != nil {
		t.Fatalf("prepare boundary overflow recovery: %v", err)
	}
	if overflow.FindingsRef == nil || overflow.Findings != nil {
		t.Fatalf("boundary overflow recovery = %#v, want artifact reference", overflow)
	}
}

func TestLoadRecoveryArtifactOversizedPrecedesStaleByteIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, recoveryArtifactFileMode)
	if err != nil {
		t.Fatalf("create oversized artifact: %v", err)
	}
	if err := file.Truncate(maxReviewRecoveryArtifactBytes + 1); err != nil {
		_ = file.Close()
		t.Fatalf("truncate oversized artifact: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close oversized artifact: %v", err)
	}

	ref := ReviewRecoveryArtifactRef{
		Path:         path,
		SHA256:       strings.Repeat("0", 64),
		Bytes:        1,
		FindingCount: 1,
	}
	findings, err := LoadRecoveryArtifact(ref)
	if findings != nil {
		t.Fatalf("oversized recovery returned findings: %#v", findings)
	}
	if !errors.Is(err, ErrRecoveryArtifactOversized) {
		t.Fatalf("oversized recovery error = %v, want ErrRecoveryArtifactOversized", err)
	}
	assertTypedRecoveryFailure(t, findings, err, RecoveryArtifactOversized)
}

func reviewOverflowFinding(id, detail string) reviewcontract.Finding {
	return reviewcontract.Finding{
		ID:             id,
		Severity:       reviewcontract.SevImportant,
		Category:       "correctness",
		Title:          "preserve exact review finding",
		Detail:         detail,
		Evidence:       []reviewcontract.Evidence{{File: "pkg/dispatcher/review.go", LineStart: 1, LineEnd: 1}},
		Confidence:     99,
		Sources:        []string{"correctness"},
		Origin:         "review",
		ContractImpact: reviewcontract.ContractImplementationFix,
		RequiredAction: detail,
	}
}

func assertReviewRecoveryPayloadBounded(t *testing.T, recovery protocol.ReviewRecovery) {
	t.Helper()
	recoveryJSON, err := json.Marshal(recovery)
	if err != nil {
		t.Fatalf("marshal review recovery: %v", err)
	}
	if len(recoveryJSON) > maxReviewRecoveryInlineBytes {
		t.Fatalf("review recovery bytes = %d, want <= %d", len(recoveryJSON), maxReviewRecoveryInlineBytes)
	}
	messageJSON, err := json.Marshal(protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:         "oro-overflow",
			Worktree:       "/tmp/oro-overflow",
			ReviewRecovery: &recovery,
		},
	})
	if err != nil {
		t.Fatalf("marshal ASSIGN: %v", err)
	}
	if len(messageJSON) >= protocol.MaxMessageSize {
		t.Fatalf("ASSIGN bytes = %d, want < %d", len(messageJSON), protocol.MaxMessageSize)
	}
}

func assertTypedRecoveryFailure(t *testing.T, findings []reviewcontract.Finding, err error, kind RecoveryArtifactErrorKind) {
	t.Helper()
	if findings != nil {
		t.Fatalf("failed recovery returned partial findings: %#v", findings)
	}
	var typed *RecoveryArtifactError
	if !errors.As(err, &typed) {
		t.Fatalf("recovery error = %T %v, want *RecoveryArtifactError", err, err)
	}
	if typed.Kind != kind {
		t.Fatalf("recovery error kind = %q, want %q", typed.Kind, kind)
	}
}

func assertTypedStoredRecoveryFailure(t *testing.T, recovery protocol.ReviewRecovery, err error, kind RecoveryArtifactErrorKind) {
	t.Helper()
	if recovery.Findings != nil || recovery.FindingsRef != nil {
		t.Fatalf("failed stored recovery returned partial context: %#v", recovery)
	}
	var typed *RecoveryArtifactError
	if !errors.As(err, &typed) {
		t.Fatalf("stored recovery error = %T %v, want *RecoveryArtifactError", err, err)
	}
	if typed.Kind != kind {
		t.Fatalf("stored recovery error kind = %q, want %q", typed.Kind, kind)
	}
}
