package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/cards"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

func newCurrentCmd() *cobra.Command {
	return newCurrentCmdWithStore(nil)
}

func newCurrentCmdWithStore(store beadstore.Store) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:          "current",
		Short:        "Show current work context (in-progress tasks, journey, cards)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeRenderError(cmd, fmt.Errorf("store: %w", err))
			}
			if err := runCurrent(cmd.Context(), s, format, cmd.OutOrStdout()); err != nil {
				return writeRenderError(cmd, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return cmd
}

// currentViewJSON is the JSON shape for `oro current --format json`.
type currentViewJSON struct {
	Snapshot      string            `json:"snapshot"`
	InProgress    []string          `json:"in_progress"`
	RecentJourney []journeyItemJSON `json:"recent_journey"`
	Cards         []cardSummaryJSON `json:"cards"`
}

type journeyItemJSON struct {
	BeadID  string `json:"bead_id"`
	Ts      string `json:"ts"`
	Actor   string `json:"actor"`
	Event   string `json:"event"`
	Payload string `json:"payload,omitempty"`
}

type cardSummaryJSON struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	BodySummary string   `json:"body_summary"`
	Tags        []string `json:"tags"`
	Score       float64  `json:"score"`
}

// runCurrent renders the current work context into w.
// All reads happen inside a single WithReadTx; w is only written after the tx
// succeeds (fail-closed: no partial output on error).
func runCurrent(ctx context.Context, store beadstore.Store, format string, w io.Writer) error {
	var view currentViewJSON
	if err := store.WithReadTx(ctx, func(tx beadstore.ReadTx) error {
		var err error
		view, err = buildCurrentView(ctx, tx)
		return err
	}); err != nil {
		return fmt.Errorf("current render: %w", err)
	}

	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(view); err != nil {
			return fmt.Errorf("encode current JSON: %w", err)
		}
		return nil
	}

	return renderCurrentText(w, view)
}

func buildCurrentView(ctx context.Context, tx beadstore.ReadTx) (currentViewJSON, error) {
	beads, err := tx.InProgress(ctx)
	if err != nil {
		return currentViewJSON{}, fmt.Errorf("InProgress: %w", err)
	}

	view := currentViewJSON{
		Snapshot:   time.Now().UTC().Format(time.RFC3339Nano),
		InProgress: make([]string, 0, len(beads)),
	}

	var allEvents []journeyItemJSON
	seen := map[string]struct{}{}

	for _, b := range beads {
		view.InProgress = append(view.InProgress, b.ID)

		events, err := tx.LatestJourney(ctx, b.ID, 20)
		if err != nil {
			return currentViewJSON{}, fmt.Errorf("LatestJourney(%s): %w", b.ID, err)
		}
		for _, e := range events {
			allEvents = append(allEvents, journeyItemJSON{
				BeadID:  e.BeadID,
				Ts:      e.Ts,
				Actor:   e.Actor,
				Event:   e.Event,
				Payload: e.Payload,
			})
		}

		if tx.Cards() != nil {
			relevant, err := tx.Cards().Relevant(ctx, beadRelevanceQuery(b))
			if err != nil {
				return currentViewJSON{}, fmt.Errorf("Cards.Relevant(%s): %w", b.ID, err)
			}
			for _, c := range relevant.Deck {
				if _, ok := seen[c.ID]; !ok {
					seen[c.ID] = struct{}{}
					view.Cards = append(view.Cards, cardSummaryFromDeckCard(c))
				}
			}
		}
	}

	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].Ts > allEvents[j].Ts
	})
	view.RecentJourney = allEvents
	return view, nil
}

func renderCurrentText(w io.Writer, view currentViewJSON) error {
	fmt.Fprintf(w, "## Current Work (snapshot: %s)\n\n", view.Snapshot)
	if len(view.InProgress) == 0 {
		fmt.Fprintln(w, "No in-progress work.")
		return nil
	}
	fmt.Fprintf(w, "**In Progress:** %v\n\n", view.InProgress)
	if len(view.RecentJourney) > 0 {
		fmt.Fprintln(w, "**Recent Events:**")
		for _, e := range view.RecentJourney {
			fmt.Fprintf(w, "- %s [%s] %s\n", e.Ts, e.Actor, e.Event)
		}
		fmt.Fprintln(w)
	}
	if len(view.Cards) > 0 {
		fmt.Fprintln(w, "**Cards:**")
		for _, c := range view.Cards {
			fmt.Fprintf(w, "- [%s] %s\n", c.ID, c.Title)
		}
	}
	return nil
}

// beadRelevanceQuery builds a cards.RelevanceQuery from a bead.
func beadRelevanceQuery(b protocol.Bead) cards.RelevanceQuery {
	return cards.RelevanceQuery{
		BeadType:        b.Type,
		BeadTags:        b.Tags,
		BeadDescription: b.Description,
		MaxTokens:       2000,
	}
}

// cardSummaryFromDeckCard converts a deck card to the JSON shape.
func cardSummaryFromDeckCard(c cards.DeckCard) cardSummaryJSON {
	return cardSummaryJSON{
		ID:          c.ID,
		Type:        string(c.Type),
		Title:       c.Title,
		BodySummary: c.BodySummary,
		Tags:        c.Tags,
		Score:       c.Score,
	}
}

// writeRenderError writes a structured JSON error to stderr and returns err
// so the command exits non-zero. No output is written to stdout.
func writeRenderError(cmd *cobra.Command, err error) error {
	fmt.Fprintf(cmd.ErrOrStderr(), `{"ok":false,"error":%q}`+"\n", err.Error())
	return err
}
