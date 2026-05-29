package main

import (
	"fmt"
	"strconv"
	"strings"

	"oro/pkg/cards"

	"github.com/spf13/cobra"
)

func newCardsReviewQueueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "review-queue",
		Short: "List unresolved learning candidates queued for review",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, closeStore, err := openDefaultCardStore()
			if err != nil {
				return err
			}
			defer closeStore()

			queue, err := store.ReviewQueue(cmd.Context())
			if err != nil {
				return fmt.Errorf("list review queue: %w", err)
			}
			if len(queue) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No learning candidates queued for review")
				return nil
			}
			for _, learning := range queue {
				fmt.Fprintf(
					cmd.OutOrStdout(),
					"%d\t%s\t%s\t%s",
					learning.ID,
					learning.BeadID,
					learning.Candidate.Type,
					learning.Candidate.Title,
				)
				if learning.Reason != nil && strings.TrimSpace(*learning.Reason) != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "\t%s", strings.TrimSpace(*learning.Reason))
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		},
	}
}

func newCardsPromoteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "promote <learning-id>",
		Short: "Promote a reviewed learning candidate to a card",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			learningID, err := parseLearningID(args[0])
			if err != nil {
				return err
			}
			store, closeStore, err := openDefaultCardStore()
			if err != nil {
				return err
			}
			defer closeStore()

			cardID, err := store.PromoteLearning(cmd.Context(), learningID)
			if err != nil {
				return fmt.Errorf("promote learning %d: %w", learningID, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Promoted learning %d to card %s\n", learningID, cardID)
			return nil
		},
	}
}

func newCardsRejectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reject <learning-id>",
		Short: "Reject a reviewed learning candidate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			learningID, err := parseLearningID(args[0])
			if err != nil {
				return err
			}
			store, closeStore, err := openDefaultCardStore()
			if err != nil {
				return err
			}
			defer closeStore()

			if err := store.RejectLearning(cmd.Context(), learningID, "rejected by cards review CLI"); err != nil {
				return fmt.Errorf("reject learning %d: %w", learningID, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Rejected learning %d\n", learningID)
			return nil
		},
	}
}

func openDefaultCardStore() (cards.Store, func(), error) {
	paths, err := ResolveProjectDBPaths()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve paths: %w", err)
	}
	db, err := openStateDB(paths.StateDBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open state db: %w", err)
	}
	store, err := cards.NewStore(db)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("init card store: %w", err)
	}
	return store, func() { _ = db.Close() }, nil
}

func parseLearningID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid learning id %q", raw)
	}
	return id, nil
}
