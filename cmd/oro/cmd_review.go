package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"oro/pkg/beadstore"

	"github.com/spf13/cobra"
)

func newReviewCmd() *cobra.Command {
	return newReviewCmdWithStore(nil)
}

func newReviewCmdWithStore(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "review",
		Short:        "Manage structured review findings",
		SilenceUsage: true,
	}
	cmd.AddCommand(newReviewTriageCmd(store))
	return cmd
}

type reviewTriageHistoryEntry struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Note   string `json:"note"`
	Ts     string `json:"ts"`
}

func newReviewTriageCmd(store beadstore.Store) *cobra.Command {
	var status string
	var note string
	cmd := &cobra.Command{
		Use:          "triage <task-id> <finding-id>",
		Short:        "Append a triage entry for a structured review finding",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !validReviewTriageStatus(status) {
				return fmt.Errorf("invalid status %q: expected open, false-positive, fixed, wont-fix, or uncertain", status)
			}
			if note == "" {
				return fmt.Errorf("--note is required")
			}
			s, err := resolveBeadStore(store)
			if err != nil {
				return fmt.Errorf("store: %w", err)
			}
			if err := runReviewTriage(cmd.Context(), s, args[0], args[1], status, note); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "triaged %s as %s\n", args[1], status)
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "triage status: open, false-positive, fixed, wont-fix, uncertain")
	cmd.Flags().StringVar(&note, "note", "", "triage note")
	return cmd
}

func validReviewTriageStatus(status string) bool {
	switch status {
	case "open", "false-positive", "fixed", "wont-fix", "uncertain":
		return true
	default:
		return false
	}
}

func runReviewTriage(ctx context.Context, store beadstore.Store, beadID, findingID, status, note string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	payload, err := reviewTriagePayloadFor(ctx, store, beadID, findingID, status, note, now)
	if err != nil {
		return err
	}
	if err := store.AppendJourney(ctx, beadID, beadstore.JourneyEvent{
		Ts:      now,
		Actor:   "human",
		Event:   "review_finding",
		Payload: string(payload),
	}); err != nil {
		return fmt.Errorf("append review triage journey: %w", err)
	}
	return nil
}

func reviewTriagePayloadFor(
	ctx context.Context,
	store beadstore.Store,
	beadID string,
	findingID string,
	status string,
	note string,
	now string,
) ([]byte, error) {
	prior, err := latestReviewFindingPayload(ctx, store, beadID, findingID)
	if err != nil {
		return nil, err
	}
	if prior == nil {
		prior = map[string]any{
			"id":         findingID,
			"finding_id": findingID,
		}
	}
	prior["id"] = findingID
	prior["finding_id"] = findingID
	prior["status"] = status
	prior["history"] = appendReviewTriageHistory(prior["history"], reviewTriageHistoryEntry{
		Kind:   "triage",
		Status: status,
		Note:   note,
		Ts:     now,
	})
	payload, err := json.Marshal(prior)
	if err != nil {
		return nil, fmt.Errorf("marshal review triage payload: %w", err)
	}
	return payload, nil
}

func latestReviewFindingPayload(ctx context.Context, store beadstore.Store, beadID, findingID string) (map[string]any, error) {
	events, err := store.LatestJourney(ctx, beadID, 200)
	if err != nil {
		return nil, fmt.Errorf("load review finding history: %w", err)
	}
	if len(events) == 0 {
		return nil, nil
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Event != "review_finding" || events[i].Payload == "" {
			continue
		}
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(events[i].Payload), &payload); err != nil {
			continue
		}
		if payloadMatchesFindingID(payload, findingID) {
			return payload, nil
		}
	}
	return nil, nil
}

func payloadMatchesFindingID(payload map[string]any, findingID string) bool {
	for _, key := range []string{"id", "finding_id"} {
		if value, ok := payload[key].(string); ok && value == findingID {
			return true
		}
	}
	return false
}

func appendReviewTriageHistory(existing any, entry reviewTriageHistoryEntry) []reviewTriageHistoryEntry {
	history := make([]reviewTriageHistoryEntry, 0)
	rawEntries, ok := existing.([]any)
	if ok {
		history = appendExistingReviewTriageHistory(history, rawEntries)
	}
	return append(history, entry)
}

func appendExistingReviewTriageHistory(
	history []reviewTriageHistoryEntry,
	rawEntries []any,
) []reviewTriageHistoryEntry {
	for _, raw := range rawEntries {
		rawEntry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		history = append(history, reviewTriageHistoryEntry{
			Kind:   stringValue(rawEntry["kind"]),
			Status: stringValue(rawEntry["status"]),
			Note:   stringValue(rawEntry["note"]),
			Ts:     stringValue(rawEntry["ts"]),
		})
	}
	return history
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}
