package dispatcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"oro/pkg/cards"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"strconv"
	"strings"
	"time"
)

// MemoryRejection is dispatcher-owned rejection history read from the memory boundary.
type MemoryRejection struct {
	ID        int64
	BeadID    string
	WorkerID  string
	Feedback  string
	CreatedAt string
}

// DreamAction is a dispatcher-local memory mutation requested by a dream agent.
type DreamAction struct {
	Kind   string
	ID     int64
	IDs    []int64
	Params protocol.MemoryInsertParams
}

// MemoryStore is the dispatcher-owned subset of memory behavior.
type MemoryStore interface {
	Insert(ctx context.Context, m protocol.MemoryInsertParams) (int64, error)
	GetByID(ctx context.Context, id int64) (protocol.Memory, error)
	DumpAll(ctx context.Context) ([]protocol.Memory, error)
	HasEmbedder() bool
}

// MemoryServices contains memory-specific operations supplied by outer packages.
type MemoryServices struct {
	Store            MemoryStore
	InsertRejection  func(ctx context.Context, beadID, workerID, feedback string) error
	GetRejections    func(ctx context.Context, beadID string) ([]MemoryRejection, error)
	Consolidate      func(ctx context.Context) (merged, pruned int, err error)
	TrimSearchEvents func(ctx context.Context, maxAge time.Duration) (int64, error)
	ExecuteDream     func(ctx context.Context, actions []DreamAction, logFn func(string)) error
	HandoffInserter  func(cardStore cards.Store) LearningSink
}

// LearningSink persists worker-emitted learning candidates for card review.
type LearningSink interface {
	AppendLearningPending(ctx context.Context, beadID string, c cards.CardCandidate) (int64, error)
}

type noopLearningSink struct{}

// AppendLearningPending ignores pending learning writes when cards are unavailable.
func (noopLearningSink) AppendLearningPending(context.Context, string, cards.CardCandidate) (int64, error) {
	return 0, nil
}

type handoffLearningSink struct {
	cardStore cards.Store
}

// HandoffInserter adapts handoff memory params into pending card candidates.
func HandoffInserter(cardStore cards.Store) LearningSink {
	if cardStore == nil {
		return noopLearningSink{}
	}
	return handoffLearningSink{cardStore: cardStore}
}

// AppendLearningPending forwards handoff learning candidates to the card store.
func (s handoffLearningSink) AppendLearningPending(ctx context.Context, beadID string, c cards.CardCandidate) (int64, error) {
	id, err := s.cardStore.AppendLearningPending(ctx, beadID, c)
	if err != nil {
		return 0, fmt.Errorf("append handoff pending learning: %w", err)
	}
	return id, nil
}

// maybeConsolidateMemory increments the completion counter and triggers an
// async memory consolidation when the threshold is reached.
func (d *Dispatcher) maybeConsolidateMemory(ctx context.Context) {
	d.mu.Lock()
	d.completionsSinceConsolidate++
	shouldConsolidate := d.cfg.ConsolidateAfterN > 0 && d.completionsSinceConsolidate >= d.cfg.ConsolidateAfterN
	if shouldConsolidate {
		d.completionsSinceConsolidate = 0
	}
	d.mu.Unlock()

	if shouldConsolidate {
		d.safeGo(func() {
			if d.memoryServices.Consolidate == nil {
				return
			}
			merged, pruned, err := d.memoryServices.Consolidate(ctx)
			if err != nil {
				_ = d.logEvent(ctx, "memory_consolidation_failed", "dispatcher", "", "",
					fmt.Sprintf(`{"error":%q}`, err.Error()))
				return
			}
			_ = d.logEvent(ctx, "memory_consolidation", "dispatcher", "", "",
				fmt.Sprintf(`{"merged":%d,"pruned":%d}`, merged, pruned))
		})
	}
}

// maybeTriggerDream increments beadsSinceDream and, when it reaches
// DreamInterval, resets the counter and spawns an async dream agent.
// DreamInterval=0 disables dreaming entirely.
func (d *Dispatcher) maybeTriggerDream(ctx context.Context) {
	d.mu.Lock()
	if d.cfg.DreamInterval <= 0 {
		d.mu.Unlock()
		return
	}
	d.beadsSinceDream++
	fire := d.beadsSinceDream >= d.cfg.DreamInterval
	if fire {
		d.beadsSinceDream = 0
	}
	d.mu.Unlock()

	if fire {
		d.triggerDream(ctx)
	}
}

// maybeTriggerJanitor counts completed merges and starts a janitor scan only
// once the configured interval is reached. Normal runs wait for a sufficiently
// idle queue; three intervals force a run so sustained load cannot starve
// cleanliness work. Every configured audit cadence replaces that janitor run.
func (d *Dispatcher) maybeTriggerJanitor(ctx context.Context) {
	d.mu.Lock()
	if !d.cfg.JanitorEnabled || d.cfg.JanitorInterval <= 0 {
		d.mu.Unlock()
		return
	}

	interval := uint64(d.cfg.JanitorInterval)
	d.mergesSinceJanitor++
	forceRun := d.mergesSinceJanitor/interval >= 3
	ready := d.mergesSinceJanitor >= interval && d.cachedQueueDepth <= d.cfg.JanitorIdleThreshold
	if !ready && !forceRun {
		d.mu.Unlock()
		return
	}

	d.mergesSinceJanitor = 0
	spawn := d.janitorSpawnFn
	if d.cfg.AuditEnabled && d.cfg.AuditEveryNJanitors > 0 {
		d.janitorRunsSinceAudit++
		if d.janitorRunsSinceAudit >= uint64(d.cfg.AuditEveryNJanitors) {
			d.janitorRunsSinceAudit = 0
			spawn = d.auditSpawnFn
			d.mu.Unlock()
			d.safeGo(func() {
				if spawn != nil {
					spawn(ctx)
					return
				}
				d.spawnAudit(ctx)
			})
			return
		}
	}
	d.mu.Unlock()

	d.safeGo(func() { d.spawnJanitor(ctx, spawn) })
}

// spawnJanitor is the serialized asynchronous boundary for a selected janitor
// cycle. Failed scans restore one interval of merge budget for a prompt retry.
func (d *Dispatcher) spawnJanitor(ctx context.Context, spawn func(context.Context)) {
	d.cleanlinessCycleMu.Lock()
	defer d.cleanlinessCycleMu.Unlock()

	if spawn != nil {
		spawn(ctx)
		return
	}
	if err := d.runJanitor(ctx); err != nil {
		d.restoreJanitorCadenceAfterFailure()
		_ = d.logEvent(ctx, "janitor_scan_failed", "dispatcher", "", "", err.Error())
	}
}

func (d *Dispatcher) restoreJanitorCadenceAfterFailure() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cfg.JanitorInterval > 0 {
		interval := uint64(d.cfg.JanitorInterval)
		const maxUint64 = ^uint64(0)
		if maxUint64-d.mergesSinceJanitor < interval {
			d.mergesSinceJanitor = maxUint64
		} else {
			d.mergesSinceJanitor += interval
		}
	}
	if d.cfg.AuditEnabled && d.cfg.AuditEveryNJanitors > 0 && d.janitorRunsSinceAudit > 0 {
		d.janitorRunsSinceAudit--
	}
}

// spawnAudit is the asynchronous boundary for an audit that replaces a
// janitor cycle. Cleanliness cycles serialize across both roles.
func (d *Dispatcher) spawnAudit(ctx context.Context) {
	d.cleanlinessCycleMu.Lock()
	defer d.cleanlinessCycleMu.Unlock()

	roleBeadID, err := d.ensureRoleBead(ctx, "audit")
	if err != nil {
		d.restoreAuditCadenceAfterFailure()
		_ = d.logEvent(ctx, "audit_role_failed", "dispatcher", "", "", err.Error())
		return
	}
	if err := d.runAudit(ctx, roleBeadID); err != nil {
		d.restoreAuditCadenceAfterFailure()
		_ = d.logEvent(ctx, "audit_scan_failed", "dispatcher", "", "", err.Error())
	}
}

func (d *Dispatcher) restoreAuditCadenceAfterFailure() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cfg.JanitorInterval > 0 {
		interval := uint64(d.cfg.JanitorInterval)
		const maxUint64 = ^uint64(0)
		if maxUint64-d.mergesSinceJanitor < interval {
			d.mergesSinceJanitor = maxUint64
		} else {
			d.mergesSinceJanitor += interval
		}
	}
	if d.cfg.AuditEnabled && d.cfg.AuditEveryNJanitors > 0 {
		d.janitorRunsSinceAudit = uint64(d.cfg.AuditEveryNJanitors - 1)
	}
}

// triggerDream spawns a dream memory-consolidation agent and handles the result
// asynchronously. DreamInterval<=0 disables dreaming entirely (no-op).
// Errors from the agent are logged but do not propagate.
func (d *Dispatcher) triggerDream(ctx context.Context) {
	if d.cfg.DreamInterval <= 0 {
		return
	}
	if d.memoryServices.TrimSearchEvents != nil {
		n, err := d.memoryServices.TrimSearchEvents(ctx, 30*24*time.Hour)
		if err != nil {
			_ = d.logEvent(ctx, "retention_trim_failed", "dispatcher", "", "",
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		} else if n > 0 {
			_ = d.logEvent(ctx, "retention_trim", "dispatcher", "", "",
				fmt.Sprintf(`{"deleted":%d}`, n))
		}
	}
	resultCh := d.ops.Dream(ctx, d.dreamOpts(ctx))
	d.safeGo(func() { d.handleDreamResult(ctx, resultCh) })
}

func (d *Dispatcher) dreamOpts(ctx context.Context) ops.DreamOpts {
	return ops.DreamOpts{
		Memories:       d.dumpMemoriesForDream(ctx),
		ActiveBiasTags: d.activeBiasTags(ctx),
	}
}

type calibratingCardStore interface {
	Calibration(context.Context) (cards.Scorecard, error)
}

func (d *Dispatcher) activeBiasTags(ctx context.Context) []string {
	store, ok := d.cardStore.(calibratingCardStore)
	if !ok {
		return nil
	}
	scorecard, err := store.Calibration(ctx)
	if err != nil || scorecard.Skipped {
		return nil
	}
	return scorecard.ActiveBiasTags
}

// dumpMemoriesForDream serializes all memories as a text block for the dream agent.
// Returns empty string on error or when no memory store is configured.
func (d *Dispatcher) dumpMemoriesForDream(ctx context.Context) string {
	if d.memories == nil {
		return ""
	}
	mems, err := d.memories.DumpAll(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "dream_dump_failed", "dispatcher", "", "",
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return ""
	}
	if len(mems) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range mems {
		fmt.Fprintf(&b, "- [%d] (%s) %s\n", m.ID, m.Type, m.Content)
	}
	return b.String()
}

// handleDreamResult waits for the dream agent result, parses any dream actions
// from the feedback, and applies them via dreamExecuteFn (defaults to
// memoryServices.ExecuteDream). Ops errors are logged; the function never panics.
func (d *Dispatcher) handleDreamResult(ctx context.Context, resultCh <-chan ops.Result) {
	select {
	case <-ctx.Done():
		return
	case result := <-resultCh:
		if result.Err != nil {
			_ = d.logEvent(ctx, "dream_failed", "dispatcher", "", "",
				fmt.Sprintf(`{"error":%q}`, result.Err.Error()))
			return
		}
		actions := ParseDreamActions(result.Feedback)
		if d.cfg.GradeGateEnabled {
			d.writeDreamProposals(ctx, actions)
			_ = d.logEvent(ctx, "dream_complete", "dispatcher", "", "",
				fmt.Sprintf(`{"actions":%d}`, len(actions)))
			return
		}
		execFn := d.dreamExecuteFn
		if execFn == nil {
			execFn = func(ctx context.Context, actions []DreamAction, _ MemoryStore, logFn func(string)) error {
				if d.memoryServices.ExecuteDream == nil {
					return nil
				}
				return d.memoryServices.ExecuteDream(ctx, actions, logFn)
			}
		}
		logFn := func(msg string) {
			_ = d.logEvent(ctx, "dream_execute_error", "dispatcher", "", "", msg)
		}
		if err := execFn(ctx, actions, d.memories, logFn); err != nil {
			_ = d.logEvent(ctx, "dream_execute_failed", "dispatcher", "", "",
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		_ = d.logEvent(ctx, "dream_complete", "dispatcher", "", "",
			fmt.Sprintf(`{"actions":%d}`, len(actions)))
	}
}

func (d *Dispatcher) writeDreamProposals(ctx context.Context, actions []DreamAction) {
	if d.cardStore == nil {
		return
	}
	for _, action := range actions {
		if _, err := d.cardStore.Create(ctx, dreamProposalParams(action)); err != nil {
			_ = d.logEvent(ctx, "dream_proposal_failed", "dispatcher", "", "",
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
	}
}

func dreamProposalParams(action DreamAction) cards.CardCreateParams {
	content := dreamActionContent(action)
	return cards.CardCreateParams{
		Type:         dreamActionCardType(action),
		Title:        dreamProposalTitle(content),
		BodySummary:  content,
		BodyFull:     content,
		Tags:         action.Params.Tags,
		GradeState:   "proposed",
		ProposalHash: dreamProposalHash(action),
	}
}

func dreamActionCardType(action DreamAction) cards.CardType {
	if action.Params.Type == "decision" {
		return cards.CardTypeDecision
	}
	return cards.CardTypePattern
}

func dreamActionContent(action DreamAction) string {
	if action.Params.Content != "" {
		return action.Params.Content
	}
	switch action.Kind {
	case "DELETE":
		return fmt.Sprintf("Dream proposed deleting memory %d", action.ID)
	case "MERGE":
		return fmt.Sprintf("Dream proposed merging memories %s", joinInt64s(action.IDs))
	default:
		return fmt.Sprintf("Dream proposed %s action", strings.ToLower(action.Kind))
	}
}

func dreamProposalTitle(content string) string {
	const maxTitleRunes = 80
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= maxTitleRunes {
		return string(runes)
	}
	return string(runes[:maxTitleRunes])
}

func dreamProposalHash(action DreamAction) string {
	h := sha256.Sum256([]byte(fmt.Sprintf(
		"dream-proposal-v1|%s|%d|%v|%s|%s|%s",
		action.Kind,
		action.ID,
		action.IDs,
		action.Params.Type,
		strings.Join(action.Params.Tags, ","),
		action.Params.Content,
	)))
	return hex.EncodeToString(h[:])
}

func joinInt64s(values []int64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.FormatInt(value, 10))
	}
	return strings.Join(parts, ",")
}
