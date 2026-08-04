package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"oro/pkg/cards"
	"oro/pkg/codestruct"
	"oro/pkg/protocol"
)

// maxWorkerProgramSize is the maximum number of bytes read from worker-program.md.
// Content exceeding this limit is truncated with a warning logged.
const maxWorkerProgramSize = 32 * 1024

const (
	maxAssignmentCardDeckJSONSize    = 192 * 1024
	maxAssignmentCardInlinedJSONSize = 128 * 1024
)

// gitLogTimeout is the maximum time allowed for the git log command.
const gitLogTimeout = 2 * time.Second

// WorkerExecutionContext is the assignment authority delivered to a worker.
// It stays separate from the worker's mutable runtime state so retry and
// handoff payloads can preserve the exact issued identity.
type WorkerExecutionContext struct {
	AssignmentID   int64
	Generation     int64
	ActorRole      string
	Project        string
	Capability     string
	ReviewRecovery *protocol.ReviewRecovery
}

func workerExecutionContext(assignmentID int64, isEpicDecomp bool, project string) WorkerExecutionContext {
	role := "execution_worker"
	if isEpicDecomp {
		role = "epic_decomposition_worker"
	}
	return WorkerExecutionContext{
		AssignmentID: assignmentID,
		Generation:   1,
		ActorRole:    role,
		Project:      project,
	}
}

// buildAssignPayload assembles an AssignPayload for a worker from beads.Show
// and filesystem sources. It is the single source of truth for payload
// construction, replacing the ad-hoc inline literals scattered across assignBead,
// qgRetryWithReservation, and handleReviewRejection.
//
// Edges:
//   - beads.Show error → log warning, leave Title/Description/AC empty.
//   - git log timeout (2s) → empty GitLog.
//   - worker-program.md missing → empty WorkerProgram (no warning).
//   - worker-program.md >32KB → truncate with log warning.
//   - isEpicDecomp=true → GitLog and WorkerProgram are always empty.
func (d *Dispatcher) buildAssignPayload(ctx context.Context, w *trackedWorker, attempt int, feedback, memCtx string, execution WorkerExecutionContext) *protocol.AssignPayload {
	var bead protocol.Bead
	p := &protocol.AssignPayload{
		BeadID:              w.beadID,
		Worktree:            w.worktree,
		QGEvidenceDir:       w.qgEvidenceDir,
		TargetSHA:           w.targetSHA,
		Runtime:             w.runtime,
		Model:               w.model,
		Reasoning:           w.reasoning,
		Attempt:             attempt,
		Feedback:            feedback,
		MemoryContext:       memCtx,
		IsEpicDecomposition: w.isEpicDecomp,
		ProjectRoot:         d.cfg.RepoRoot,
		TargetBranch:        w.targetBranch,
	}
	applyExecutionContext(p, execution)

	// Populate metadata from beads.Show.
	detail, err := d.beads.Show(ctx, w.beadID)
	if err != nil {
		_ = d.logEvent(ctx, "build_assign_payload_show_failed", "dispatcher", w.beadID, w.id,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		// Title, Description, AcceptanceCriteria remain empty.
	} else if detail != nil {
		p.Title = detail.Title
		p.Description = detail.Description
		p.AcceptanceCriteria = detail.AcceptanceCriteria
		bead = *detail
	}
	if bead.ID == "" {
		bead.ID = w.beadID
		bead.Title = p.Title
		bead.Description = p.Description
	}
	p.Cards = d.buildCardContext(ctx, bead)

	// Epic decomposition workers don't need git history or the worker program.
	if w.isEpicDecomp {
		return p
	}

	// Git log — 2s hard timeout to keep assignment latency bounded.
	gitCtx, gitCancel := context.WithTimeout(ctx, gitLogTimeout)
	defer gitCancel()
	gitOut, gitErr := d.commandRunner().Run(gitCtx, "git", "log", "--oneline", "-20")
	if gitErr == nil {
		p.GitLog = strings.TrimSpace(string(gitOut))
	}
	// On timeout or error GitLog stays empty.

	// worker-program.md — optional file that provides project-specific guidance.
	wpPath := d.cfg.WorkerProgram
	if wpPath == "" {
		wpPath = filepath.Join(d.cfg.RepoRoot, "worker-program.md")
	}
	wpData, wpErr := os.ReadFile(wpPath) //nolint:gosec // path derived from trusted config
	if wpErr == nil {
		if len(wpData) > maxWorkerProgramSize {
			_ = d.logEvent(ctx, "worker_program_truncated", "dispatcher", w.beadID, w.id,
				fmt.Sprintf(`{"original_size":%d,"truncated_to":%d}`, len(wpData), maxWorkerProgramSize))
			wpData = wpData[:maxWorkerProgramSize]
		}
		p.WorkerProgram = string(wpData)
	}
	// Missing file: WorkerProgram stays empty.

	return p
}

func applyExecutionContext(payload *protocol.AssignPayload, execution WorkerExecutionContext) {
	payload.AssignmentID = execution.AssignmentID
	payload.Generation = execution.Generation
	payload.ActorRole = execution.ActorRole
	payload.Project = execution.Project
	payload.Capability = execution.Capability
	payload.ReviewRecovery = execution.ReviewRecovery
}

func (d *Dispatcher) buildCardContext(ctx context.Context, bead protocol.Bead) cards.RelevantCards {
	if d.cardStore == nil {
		return cards.RelevantCards{}
	}
	result, err := d.cardStore.Relevant(ctx, cards.RelevanceQuery{
		BeadType:        bead.Type,
		BeadTags:        bead.Labels,
		BeadDescription: strings.TrimSpace(bead.Title + " " + bead.Description),
		SymbolHints:     d.assignmentSymbolHints(bead),
		MaxTokens:       2000,
	})
	if err != nil {
		_ = d.logEvent(ctx, "card_context_failed", "dispatcher", bead.ID, "",
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return cards.RelevantCards{}
	}
	return trimAssignmentCardContext(result)
}

func (d *Dispatcher) assignmentSymbolHints(bead protocol.Bead) []string {
	refs := touchedSymbolRefs(bead.AcceptanceCriteria)
	if len(refs) == 0 {
		return nil
	}
	return d.resolveAssignmentSymbolHints(refs)
}

func (d *Dispatcher) resolveAssignmentSymbolHints(refs []symbolRef) []string {
	root := d.cfg.RepoRoot
	if root == "" {
		root = "."
	}

	hints := make(map[string]struct{})
	for _, ref := range refs {
		addSymbolRefHints(hints, ref, nil)
	}

	files := uniqueTouchedFiles(refs)
	symsByFile := make(map[string][]codestruct.Symbol, len(files))
	for _, file := range files {
		abs := filepath.Join(root, file)
		syms, err := codestruct.ExtractGoSymbols(abs)
		if err != nil {
			continue
		}
		symsByFile[file] = syms
	}

	for _, ref := range refs {
		addSymbolRefHints(hints, ref, symsByFile[ref.file])
	}
	addResolvedCalleeHints(hints, files, symsByFile)
	return sortedSymbolHints(hints)
}

type symbolRef struct {
	file   string
	symbol string
}

func touchedSymbolRefs(acceptance string) []symbolRef {
	var refs []symbolRef
	for _, line := range strings.Split(acceptance, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Read:") {
			continue
		}
		content := strings.NewReplacer(";", ",").Replace(strings.TrimPrefix(line, "Read:"))
		for _, part := range strings.Split(content, ",") {
			if ref, ok := parseTouchedSymbolRef(part); ok {
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

func parseTouchedSymbolRef(part string) (symbolRef, bool) {
	part = strings.TrimSpace(part)
	if part == "" || !strings.Contains(part, "/") {
		return symbolRef{}, false
	}
	file, suffix, _ := strings.Cut(part, ":")
	file = filepath.ToSlash(strings.TrimSpace(file))
	if file == "" {
		return symbolRef{}, false
	}
	if !strings.HasSuffix(file, ".go") {
		return symbolRef{}, false
	}
	return symbolRef{file: file, symbol: firstReadSymbol(suffix)}, true
}

func firstReadSymbol(suffix string) string {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" || isDecimalString(suffix) {
		return ""
	}
	symbol, _, _ := strings.Cut(suffix, "/")
	return strings.TrimSpace(symbol)
}

func isDecimalString(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func uniqueTouchedFiles(refs []symbolRef) []string {
	seen := make(map[string]struct{}, len(refs))
	files := make([]string, 0, len(refs))
	for _, ref := range refs {
		if _, ok := seen[ref.file]; ok {
			continue
		}
		seen[ref.file] = struct{}{}
		files = append(files, ref.file)
	}
	return files
}

func addSymbolRefHints(hints map[string]struct{}, ref symbolRef, syms []codestruct.Symbol) {
	if ref.symbol != "" {
		addHint(hints, ref.symbol)
		addHint(hints, ref.file+":"+ref.symbol)
		return
	}
	for _, sym := range syms {
		addHint(hints, sym.Name)
		addHint(hints, ref.file+":"+sym.Name)
	}
}

func addResolvedCalleeHints(hints map[string]struct{}, files []string, symsByFile map[string][]codestruct.Symbol) {
	edges, _, err := codestruct.BuildCallGraph(files, symsByFile)
	if err != nil {
		return
	}
	for _, edge := range edges {
		ref, ok := codestruct.ResolveCallee(edge, nil, symsByFile)
		if !ok {
			continue
		}
		addHint(hints, ref)
		if _, symbol, ok := strings.Cut(ref, ":"); ok {
			addHint(hints, symbol)
		}
	}
}

func addHint(hints map[string]struct{}, hint string) {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return
	}
	hints[hint] = struct{}{}
}

func sortedSymbolHints(hints map[string]struct{}) []string {
	out := make([]string, 0, len(hints))
	for hint := range hints {
		out = append(out, hint)
	}
	sort.Strings(out)
	return out
}

func trimAssignmentCardContext(result cards.RelevantCards) cards.RelevantCards {
	return cards.RelevantCards{
		Deck:    trimDeckCardsByJSONSize(result.Deck, maxAssignmentCardDeckJSONSize),
		Inlined: trimInlinedCardsByJSONSize(result.Inlined, maxAssignmentCardInlinedJSONSize),
	}
}

func trimDeckCardsByJSONSize(in []cards.DeckCard, maxSize int) []cards.DeckCard {
	return trimCardsByJSONSize(in, maxSize)
}

func trimInlinedCardsByJSONSize(in []cards.InlinedCard, maxSize int) []cards.InlinedCard {
	return trimCardsByJSONSize(in, maxSize)
}

func trimCardsByJSONSize[T cards.DeckCard | cards.InlinedCard](in []T, maxSize int) []T {
	if maxSize <= 0 || len(in) == 0 {
		return nil
	}
	out := make([]T, 0, len(in))
	size := 2 // JSON array brackets.
	for _, summary := range in {
		data, err := json.Marshal(summary)
		if err != nil {
			break
		}
		nextSize := size + len(data)
		if len(out) > 0 {
			nextSize++ // comma between array elements.
		}
		if nextSize > maxSize {
			break
		}
		out = append(out, summary)
		size = nextSize
	}
	return out
}
