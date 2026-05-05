package main

import (
	"context"
	"fmt"
	"io"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

func newResumeCmd() *cobra.Command {
	return newResumeCmdWithStore(nil)
}

func newResumeCmdWithStore(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "resume <bead-id>",
		Short:        "Drop into a bead's context (title, status, AC, recent journey, cards)",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeRenderError(cmd, fmt.Errorf("store: %w", err))
			}
			if err := runResume(cmd.Context(), s, args[0], cmd.OutOrStdout()); err != nil {
				return writeRenderError(cmd, err)
			}
			return nil
		},
	}
	return cmd
}

// resumeData holds data loaded inside the read transaction.
type resumeData struct {
	bead   *protocol.Bead
	events []beadstore.JourneyEvent
	cards  []cardSummaryJSON
}

// runResume renders a single bead's context into w.
// All reads happen inside a single WithReadTx; w is only written after the tx
// succeeds (fail-closed: no partial output on error).
func runResume(ctx context.Context, store beadstore.Store, beadID string, w io.Writer) error {
	var data resumeData
	if err := store.WithReadTx(ctx, func(tx beadstore.ReadTx) error {
		bead, err := tx.Show(ctx, beadID)
		if err != nil {
			return fmt.Errorf("show bead %s: %w", beadID, err)
		}
		if bead == nil {
			return fmt.Errorf("bead %s not found", beadID)
		}
		data.bead = bead

		events, err := tx.LatestJourney(ctx, beadID, 5)
		if err != nil {
			return fmt.Errorf("LatestJourney(%s): %w", beadID, err)
		}
		data.events = events

		if tx.Cards() != nil {
			relevant, err := tx.Cards().Relevant(ctx, beadRelevanceQuery(*bead))
			if err != nil {
				return fmt.Errorf("Cards.Relevant(%s): %w", beadID, err)
			}
			for _, c := range relevant.Deck {
				data.cards = append(data.cards, cardSummaryFromSummary(c))
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("resume render: %w", err)
	}

	return renderResumeText(w, data)
}

func renderResumeText(w io.Writer, data resumeData) error {
	b := data.bead
	fmt.Fprintf(w, "# %s (%s)\n\n", b.Title, b.Status)

	if b.AcceptanceCriteria != "" {
		fmt.Fprintln(w, "**Acceptance Criteria:**")
		fmt.Fprintln(w, b.AcceptanceCriteria)
		fmt.Fprintln(w)
	}

	if len(data.events) > 0 {
		fmt.Fprintln(w, "**Recent Events:**")
		for _, e := range data.events {
			fmt.Fprintf(w, "- %s [%s] %s\n", e.Ts, e.Actor, e.Event)
		}
		fmt.Fprintln(w)
	}

	if len(data.cards) > 0 {
		fmt.Fprintln(w, "**Linked Cards:**")
		for _, c := range data.cards {
			fmt.Fprintf(w, "- [%s] %s: %s\n", c.ID, c.Title, c.BodySummary)
		}
	}
	return nil
}
