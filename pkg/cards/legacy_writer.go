package cards

import (
	"context"
	"fmt"
	"log"
	"strings"

	"oro/pkg/memory"
)

const dualWriteTag = "legacy_memory_dual_write"

// LegacyWriter wraps memory.Store to dual-write each Insert into the cards store.
// A cards write failure logs a warning but does NOT fail the memory insert.
type LegacyWriter struct {
	mem   *memory.Store
	cards Store
	logf  func(format string, args ...any)
}

// NewLegacyWriter creates a LegacyWriter that mirrors memory writes to cards.
func NewLegacyWriter(mem *memory.Store, cards Store) *LegacyWriter {
	return &LegacyWriter{mem: mem, cards: cards, logf: log.Printf}
}

// Insert writes to memory, then synchronously mirrors to cards.
// Cards write failures are logged as warnings and do not fail the insert.
func (w *LegacyWriter) Insert(ctx context.Context, m memory.InsertParams) (int64, error) {
	id, err := w.mem.Insert(ctx, m)
	if err != nil {
		return 0, fmt.Errorf("memory insert: %w", err)
	}
	if mirrorErr := w.mirrorToCards(ctx, id, m); mirrorErr != nil {
		w.logf("cards dual-write: mirror memory %d failed: %v", id, mirrorErr)
	}
	return id, nil
}

// mirrorToCards creates a pattern card mirroring the memory entry.
// Tags include dualWriteTag, the memory ID tag for drift correlation, and original tags.
func (w *LegacyWriter) mirrorToCards(ctx context.Context, memID int64, m memory.InsertParams) error {
	tags := make([]string, 0, 2+len(m.Tags))
	tags = append(tags, dualWriteTag, fmt.Sprintf("mem-id:%d", memID))
	tags = append(tags, m.Tags...)

	title := firstNonEmptyLine(m.Content)
	_, err := w.cards.Create(ctx, CardCreateParams{
		Type:        CardTypePattern,
		Title:       title,
		BodySummary: title,
		BodyFull:    m.Content,
		Tags:        tags,
	})
	if err != nil {
		return fmt.Errorf("create card mirror: %w", err)
	}
	return nil
}

// firstNonEmptyLine returns the first non-empty line of s, truncated to 200 chars.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 200 {
				return line[:197] + "..."
			}
			return line
		}
	}
	return s
}

// DriftResult describes a memory entry that has no matching card mirror.
type DriftResult struct {
	MemoryID int64
	Content  string
}

// CheckDrift returns one DriftResult for each memory entry that lacks a
// corresponding card with the legacy_memory_dual_write tag. These represent
// dual-write failures that require investigation.
func CheckDrift(ctx context.Context, mem *memory.Store, cs Store) ([]DriftResult, error) {
	covered, err := coveredMemoryIDs(ctx, cs)
	if err != nil {
		return nil, fmt.Errorf("check drift list cards: %w", err)
	}

	all, err := mem.List(ctx, memory.ListOpts{Limit: 100000})
	if err != nil {
		return nil, fmt.Errorf("check drift list memories: %w", err)
	}

	var failures []DriftResult
	for _, m := range all {
		if !covered[m.ID] {
			failures = append(failures, DriftResult{MemoryID: m.ID, Content: m.Content})
		}
	}
	return failures, nil
}

// coveredMemoryIDs returns the set of memory IDs that have been mirrored to cards,
// identified by the "mem-id:<id>" tag on pattern cards with the dual-write tag.
func coveredMemoryIDs(ctx context.Context, cs Store) (map[int64]bool, error) {
	all, err := cs.List(ctx, ListQuery{Type: CardTypePattern})
	if err != nil {
		return nil, fmt.Errorf("list pattern cards: %w", err)
	}
	covered := make(map[int64]bool, len(all))
	for _, c := range all {
		for _, tag := range c.Tags {
			if strings.HasPrefix(tag, "mem-id:") {
				var id int64
				if _, parseErr := fmt.Sscanf(tag, "mem-id:%d", &id); parseErr == nil {
					covered[id] = true
				}
			}
		}
	}
	return covered, nil
}
