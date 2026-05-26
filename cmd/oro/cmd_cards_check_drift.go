package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"oro/pkg/cards"
	"oro/pkg/memory"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

// newCheckDriftCmd creates the "oro cards check-drift" subcommand that reports
// memory entries that have no corresponding dual-write card mirror.
func newCheckDriftCmd() *cobra.Command {
	var backfill bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "check-drift",
		Short: "Report memory entries missing a card mirror (D.3 dual-write drift)",
		Long: "Compares all memory entries against pattern cards tagged with\n" +
			"legacy_memory_dual_write. Prints one line per unmirrored entry.\n" +
			"Exit code 1 if any drift is found.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheckDrift(cmd, backfill, dryRun)
		},
	}
	cmd.Flags().BoolVar(&backfill, "backfill", false, "create missing card mirrors for legacy memory rows")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview backfill without writing")
	return cmd
}

func runCheckDrift(cmd *cobra.Command, backfill, dryRun bool) error {
	paths, err := ResolveProjectDBPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	db, err := openStateDB(paths.StateDBPath)
	if err != nil {
		return fmt.Errorf("open state db: %w", err)
	}
	defer func() { _ = db.Close() }()

	memStore := memory.NewStore(db)
	cardStore, err := cards.NewStore(db)
	if err != nil {
		return fmt.Errorf("init card store: %w", err)
	}

	ctx := cmd.Context()
	if backfill {
		n, err := backfillMemoryCardMirrors(ctx, memStore, cardStore, dryRun)
		if err != nil {
			return fmt.Errorf("backfill memory card mirrors: %w", err)
		}
		if dryRun {
			fmt.Fprintf(cmd.OutOrStdout(), "dry-run: would backfill %d card mirror(s)\n", n)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "backfilled %d card mirror(s)\n", n)
		}
	}

	return reportCardDrift(cmd, db, cardStore)
}

func reportCardDrift(cmd *cobra.Command, db *sql.DB, cardStore cards.Store) error {
	failures, err := checkCardDriftWithoutReadTelemetry(cmd.Context(), db, cardStore)
	if err != nil {
		return fmt.Errorf("check-drift: %w", err)
	}
	if len(failures) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no drift detected")
		return nil
	}
	for _, f := range failures {
		fmt.Fprintf(cmd.OutOrStdout(), "DRIFT: memory %d — %s\n", f.MemoryID, truncateLine(f.Content, 80))
	}
	return fmt.Errorf("%d unmirrored memory entry(s) detected", len(failures))
}

func checkCardDriftWithoutReadTelemetry(ctx context.Context, db *sql.DB, cs cards.Store) ([]memory.DriftResult, error) {
	failures, err := memory.CheckCardDrift(ctx, memory.NewStore(db), cs)
	if err != nil {
		return nil, fmt.Errorf("check card drift without read telemetry: %w", err)
	}
	return failures, nil
}

// backfillMemoryCardMirrors creates missing dual-write card mirrors for legacy
// memory rows. It uses mem-id tags as the idempotency key.
func backfillMemoryCardMirrors(ctx context.Context, mem *memory.Store, cs cards.Store, dryRun bool) (int, error) {
	covered, err := coveredMemoryCardMirrorIDs(ctx, cs)
	if err != nil {
		return 0, fmt.Errorf("list existing card mirrors: %w", err)
	}

	all, err := mem.List(ctx, memory.ListOpts{Limit: 100000})
	if err != nil {
		return 0, fmt.Errorf("list memories: %w", err)
	}

	count := 0
	for _, m := range all {
		if covered[m.ID] {
			continue
		}
		count++
		if dryRun {
			continue
		}
		if _, err := cs.Create(ctx, buildMemoryCardMirror(m)); err != nil {
			return count - 1, fmt.Errorf("create card mirror for memory %d: %w", m.ID, err)
		}
		covered[m.ID] = true
	}
	return count, nil
}

func coveredMemoryCardMirrorIDs(ctx context.Context, cs cards.Store) (map[int64]bool, error) {
	all, err := cs.List(ctx, cards.ListQuery{Type: cards.CardTypePattern})
	if err != nil {
		return nil, fmt.Errorf("list pattern cards: %w", err)
	}
	covered := make(map[int64]bool, len(all))
	for _, c := range all {
		if !hasTag(c.Tags, "legacy_memory_dual_write") {
			continue
		}
		for _, tag := range c.Tags {
			if !strings.HasPrefix(tag, "mem-id:") {
				continue
			}
			var id int64
			if _, err := fmt.Sscanf(tag, "mem-id:%d", &id); err == nil {
				covered[id] = true
			}
		}
	}
	return covered, nil
}

func buildMemoryCardMirror(m protocol.Memory) cards.CardCreateParams {
	title := firstNonEmptyLine(m.Content)
	tags := append([]string{"legacy_memory_dual_write", fmt.Sprintf("mem-id:%d", m.ID)}, decodeMemoryTags(m.Tags)...)
	return cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       memTruncate(title, 200),
		BodySummary: memTruncate(title, 200),
		BodyFull:    m.Content,
		Tags:        tags,
	}
}

func decodeMemoryTags(raw string) []string {
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil
	}
	return tags
}

func hasTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

// truncateLine truncates s to maxLen characters, appending "…" if cut.
func truncateLine(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
