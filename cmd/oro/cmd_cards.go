package main

import (
	"fmt"
	"strconv"
	"strings"

	"oro/pkg/cards"

	"github.com/spf13/cobra"
)

// newCardsCmd creates the "oro cards" command group.
func newCardsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cards",
		Short: "Manage knowledge cards",
		Long:  "Manage durable knowledge cards (rules, patterns, decisions, facts).\nCards are the long-lived knowledge layer that replaces pkg/memory (§5 harness spec).",
	}
	cmd.AddCommand(newCardsShowCmd())
	cmd.AddCommand(newImportFromMemoryCmd())
	cmd.AddCommand(newCheckDriftCmd())
	cmd.AddCommand(newMemoryRetirementCheckCmd())
	cmd.AddCommand(newCardsReviewQueueCmd())
	cmd.AddCommand(newCardsPromoteCmd())
	cmd.AddCommand(newCardsRejectCmd())
	cmd.AddCommand(newCardsCreateCmd())
	cmd.AddCommand(newCardsRetireCmd())
	cmd.AddCommand(newCardsListCmd())
	return cmd
}

func newCardsCreateCmd() *cobra.Command {
	var (
		summary    string
		body       string
		tags       []string
		confidence string
	)
	cmd := &cobra.Command{
		Use:   "create <type> <title>",
		Short: "Create a manual knowledge card",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cardType := cards.CardType(args[0])
			if !validCardType(cardType) {
				return fmt.Errorf("invalid card type %q", args[0])
			}
			if strings.TrimSpace(summary) == "" {
				return fmt.Errorf("--summary is required")
			}
			if strings.TrimSpace(body) == "" {
				body = summary
			}

			var promotionConfidence *float64
			if confidence != "" {
				parsed, err := strconv.ParseFloat(confidence, 64)
				if err != nil {
					return fmt.Errorf("invalid confidence %q: %w", confidence, err)
				}
				promotionConfidence = &parsed
			}

			store, closeStore, err := openDefaultCardStore()
			if err != nil {
				return err
			}
			defer closeStore()

			card, err := store.Create(cmd.Context(), cards.CardCreateParams{
				Type:                cardType,
				Title:               args[1],
				BodySummary:         summary,
				BodyFull:            body,
				Tags:                tags,
				PromotionConfidence: promotionConfidence,
			})
			if err != nil {
				return fmt.Errorf("create card: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created card %s\n", card.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&summary, "summary", "", "One-line card summary")
	cmd.Flags().StringVar(&body, "body", "", "Full card body")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "Card tag; repeat for multiple tags")
	cmd.Flags().StringVar(&confidence, "confidence", "", "Promotion confidence for imported/reviewed cards")
	return cmd
}

func newCardsRetireCmd() *cobra.Command {
	var (
		reason       string
		supersededBy string
	)
	cmd := &cobra.Command{
		Use:   "retire <card-id>",
		Short: "Retire a knowledge card",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("--reason is required")
			}
			store, closeStore, err := openDefaultCardStore()
			if err != nil {
				return err
			}
			defer closeStore()

			id := args[0]
			if err := store.Retire(cmd.Context(), id, reason, supersededBy); err != nil {
				return fmt.Errorf("retire card %s: %w", id, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Retired card %s\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Retirement reason")
	cmd.Flags().StringVar(&supersededBy, "superseded-by", "", "Replacement card ID")
	return cmd
}

func newCardsListCmd() *cobra.Command {
	var (
		cardType       string
		includeRetired bool
		limit          int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List knowledge card summaries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := cards.ListQuery{
				IncludeRetired: includeRetired,
				Limit:          limit,
			}
			if cardType != "" {
				query.Type = cards.CardType(cardType)
				if !validCardType(query.Type) {
					return fmt.Errorf("invalid card type %q", cardType)
				}
			}

			store, closeStore, err := openDefaultCardStore()
			if err != nil {
				return err
			}
			defer closeStore()

			list, err := store.List(cmd.Context(), query)
			if err != nil {
				return fmt.Errorf("list cards: %w", err)
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No cards")
				return nil
			}
			for _, card := range list {
				fmt.Fprintf(
					cmd.OutOrStdout(),
					"%s\t%s\t%.1f\t%s\t%s\n",
					card.ID,
					card.Type,
					card.Score,
					card.Title,
					card.BodySummary,
				)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cardType, "type", "", "Filter by card type")
	cmd.Flags().BoolVar(&includeRetired, "include-retired", false, "Include retired cards")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum cards to list")
	return cmd
}

func validCardType(cardType cards.CardType) bool {
	switch cardType {
	case cards.CardTypeRule, cards.CardTypePattern, cards.CardTypeTaste, cards.CardTypeDecision, cards.CardTypeFact:
		return true
	default:
		return false
	}
}
