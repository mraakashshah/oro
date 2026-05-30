package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"oro/pkg/cards"

	"github.com/spf13/cobra"
)

func newCardsShowCmd() *cobra.Command {
	return newCardsShowCmdWithStore(nil)
}

func newCardsShowCmdWithStore(store cards.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <card-id>",
		Short: "Show a card",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if store != nil {
				return runCardsShow(cmd.Context(), store, args[0], cmd.OutOrStdout())
			}
			paths, err := ResolveProjectDBPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			db, err := openStateDB(paths.StateDBPath)
			if err != nil {
				return fmt.Errorf("open state db: %w", err)
			}
			defer func() { _ = db.Close() }()
			store, err := cards.NewStore(db)
			if err != nil {
				return fmt.Errorf("init card store: %w", err)
			}
			return runCardsShow(cmd.Context(), store, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runCardsShow(ctx context.Context, store cards.Store, id string, w io.Writer) error {
	if store == nil {
		return fmt.Errorf("card store is required")
	}
	card, err := store.Show(ctx, id)
	if err != nil {
		if errors.Is(err, cards.ErrNotFound) {
			return fmt.Errorf("card %s not found: %w", id, err)
		}
		return fmt.Errorf("show card %s: %w", id, err)
	}
	fmt.Fprintf(w, "Title: %s\n", card.Title)
	fmt.Fprintf(w, "ID: %s\n", card.ID)
	fmt.Fprintf(w, "Type: %s\n\n", card.Type)
	fmt.Fprintln(w, card.BodyFull)
	return nil
}
