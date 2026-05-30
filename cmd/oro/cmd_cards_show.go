package main

import (
	"context"
	"encoding/json"
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
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show <card-id>",
		Short: "Show a card",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if store != nil {
				return runCardsShow(cmd.Context(), store, args[0], cmd.OutOrStdout(), jsonOut)
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
			return runCardsShow(cmd.Context(), store, args[0], cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit card as JSON")
	return cmd
}

func runCardsShow(ctx context.Context, store cards.Store, id string, w io.Writer, jsonOut bool) error {
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
	if jsonOut {
		return writeCardShowJSON(w, card)
	}
	fmt.Fprintf(w, "Title: %s\n", card.Title)
	fmt.Fprintf(w, "ID: %s\n", card.ID)
	fmt.Fprintf(w, "Type: %s\n\n", card.Type)
	fmt.Fprintf(w, "summary: %s\n", card.BodySummary)
	fmt.Fprintf(w, "score: %.3f\n", card.Score)
	fmt.Fprintf(w, "tags: %v\n\n", card.Tags)
	fmt.Fprintln(w, card.BodyFull)
	return nil
}

type cardShowJSON struct {
	ID          string         `json:"id"`
	Type        cards.CardType `json:"type"`
	Title       string         `json:"title"`
	BodySummary string         `json:"body_summary"`
	BodyFull    string         `json:"body_full"`
	Tags        []string       `json:"tags"`
	Score       float64        `json:"score"`
}

func writeCardShowJSON(w io.Writer, card *cards.Card) error {
	payload := cardShowJSON{
		ID:          card.ID,
		Type:        card.Type,
		Title:       card.Title,
		BodySummary: card.BodySummary,
		BodyFull:    card.BodyFull,
		Tags:        card.Tags,
		Score:       card.Score,
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		return fmt.Errorf("encode card JSON: %w", err)
	}
	return nil
}
