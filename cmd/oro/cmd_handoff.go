package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"oro/pkg/beadstore"

	"github.com/spf13/cobra"
)

func newHandoffCmd() *cobra.Command {
	return newHandoffCmdWithStore(nil)
}

func newHandoffCmdWithStore(store beadstore.Store) *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:          "handoff",
		Short:        "Show session-scoped work context (in-progress beads, recent journey, cards)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeRenderError(cmd, fmt.Errorf("store: %w", err))
			}
			d, err := time.ParseDuration(since)
			if err != nil {
				return writeRenderError(cmd, fmt.Errorf("invalid --since %q: %w", since, err))
			}
			if err := runHandoff(cmd.Context(), s, d, cmd.OutOrStdout()); err != nil {
				return writeRenderError(cmd, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "4h", "session window duration (e.g. 1h, 4h, 8h)")
	return cmd
}

// handoffViewJSON is the JSON shape for `oro handoff`.
type handoffViewJSON struct {
	Snapshot       string            `json:"snapshot"`
	Since          string            `json:"since"`
	InProgress     []string          `json:"in_progress"`
	SessionJourney []journeyItemJSON `json:"session_journey"`
	Cards          []cardSummaryJSON `json:"cards"`
}

// runHandoff renders the session-scoped work context into w.
// All reads happen inside a single WithReadTx; w is only written after the tx
// succeeds (fail-closed: no partial output on error).
func runHandoff(ctx context.Context, store beadstore.Store, since time.Duration, w io.Writer) error {
	cutoff := time.Now().UTC().Add(-since)

	var view handoffViewJSON
	if err := store.WithReadTx(ctx, func(tx beadstore.ReadTx) error {
		var err error
		view, err = buildHandoffView(ctx, tx, cutoff)
		return err
	}); err != nil {
		return fmt.Errorf("handoff render: %w", err)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(view); err != nil {
		return fmt.Errorf("encode handoff JSON: %w", err)
	}
	return nil
}

func buildHandoffView(ctx context.Context, tx beadstore.ReadTx, cutoff time.Time) (handoffViewJSON, error) {
	beads, err := tx.InProgress(ctx)
	if err != nil {
		return handoffViewJSON{}, fmt.Errorf("InProgress: %w", err)
	}

	view := handoffViewJSON{
		Snapshot:   time.Now().UTC().Format(time.RFC3339Nano),
		Since:      cutoff.Format(time.RFC3339Nano),
		InProgress: make([]string, 0, len(beads)),
	}

	var allEvents []journeyItemJSON
	seen := map[string]struct{}{}

	for _, b := range beads {
		view.InProgress = append(view.InProgress, b.ID)

		events, err := tx.Journey(ctx, b.ID, cutoff)
		if err != nil {
			return handoffViewJSON{}, fmt.Errorf("Journey(%s): %w", b.ID, err)
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
				return handoffViewJSON{}, fmt.Errorf("Cards.Relevant(%s): %w", b.ID, err)
			}
			for _, c := range relevant.Deck {
				if _, ok := seen[c.ID]; !ok {
					seen[c.ID] = struct{}{}
					view.Cards = append(view.Cards, cardSummaryFromSummary(c))
				}
			}
		}
	}

	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].Ts > allEvents[j].Ts
	})
	view.SessionJourney = allEvents
	return view, nil
}
