package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/cards"
)

func TestHandoffScopedToSessionWindow(t *testing.T) {
	ctx := context.Background()
	beadStore, _ := openTestRenderStore(t)

	// Seed one in-progress bead.
	inProg := "in_progress"
	if _, err := beadStore.Create(ctx, beadstore.CreateParams{
		ID: "bead-hoff-1", Title: "Handoff Task", Type: "task",
	}); err != nil {
		t.Fatalf("Create bead: %v", err)
	}
	if err := beadStore.Update(ctx, "bead-hoff-1", beadstore.UpdateParams{Status: &inProg}); err != nil {
		t.Fatalf("Update bead: %v", err)
	}

	now := time.Now().UTC()

	// Seed old events (> 1h ago — should be excluded by --since 1h).
	oldEvents := []beadstore.JourneyEvent{
		{Ts: now.Add(-3 * time.Hour).Format(time.RFC3339Nano), Actor: "worker", Event: "started"},
		{Ts: now.Add(-2 * time.Hour).Format(time.RFC3339Nano), Actor: "worker", Event: "checkpoint"},
		{Ts: now.Add(-90 * time.Minute).Format(time.RFC3339Nano), Actor: "worker", Event: "plan"},
	}
	for _, e := range oldEvents {
		if err := beadStore.AppendJourney(ctx, "bead-hoff-1", e); err != nil {
			t.Fatalf("AppendJourney old: %v", err)
		}
	}

	// Seed recent events (< 1h ago — should be included by --since 1h).
	recentEvents := []beadstore.JourneyEvent{
		{Ts: now.Add(-45 * time.Minute).Format(time.RFC3339Nano), Actor: "worker", Event: "execute"},
		{Ts: now.Add(-20 * time.Minute).Format(time.RFC3339Nano), Actor: "worker", Event: "validate"},
	}
	for _, e := range recentEvents {
		if err := beadStore.AppendJourney(ctx, "bead-hoff-1", e); err != nil {
			t.Fatalf("AppendJourney recent: %v", err)
		}
	}

	cutoff := now.Add(-1 * time.Hour)

	cmd := newHandoffCmdWithStore(beadStore)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--since", "1h"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("oro handoff --since 1h: %v\nstderr: %s", err, stderr.String())
	}

	var result struct {
		SessionJourney []map[string]any `json:"session_journey"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal handoff output: %v\noutput: %s", err, stdout.String())
	}

	// All returned events must have ts >= cutoff.
	for _, item := range result.SessionJourney {
		ts, _ := item["ts"].(string)
		if ts == "" {
			t.Fatal("event missing ts field")
		}
		if ts < cutoff.Format(time.RFC3339Nano) {
			t.Fatalf("event ts %q is before cutoff %s (should be excluded by --since 1h)", ts, cutoff.Format(time.RFC3339Nano))
		}
	}

	// Recent events must be present.
	if len(result.SessionJourney) != len(recentEvents) {
		t.Fatalf("session_journey len = %d, want %d (recent only)", len(result.SessionJourney), len(recentEvents))
	}

	// Output must be ordered DESC by ts (deterministic for the same fixture).
	for i := 1; i < len(result.SessionJourney); i++ {
		tsA, _ := result.SessionJourney[i-1]["ts"].(string)
		tsB, _ := result.SessionJourney[i]["ts"].(string)
		if tsA < tsB {
			t.Fatalf("session_journey not sorted DESC: [%d]=%q < [%d]=%q", i-1, tsA, i, tsB)
		}
	}
}

func TestHandoffRendersDeckCardSummariesWithoutFullBody(t *testing.T) {
	ctx := context.Background()
	beadStore, cardStore := openTestRenderStore(t)

	_, err := beadStore.Create(ctx, beadstore.CreateParams{
		ID:                 "bead-handoff-deck",
		Title:              "Handoff Deck Bead",
		Type:               "task",
		AcceptanceCriteria: "handoff deck acceptance",
	})
	if err != nil {
		t.Fatalf("Create bead: %v", err)
	}
	inProgress := "in_progress"
	if err := beadStore.Update(ctx, "bead-handoff-deck", beadstore.UpdateParams{Status: &inProgress}); err != nil {
		t.Fatalf("Update bead to in_progress: %v", err)
	}

	_, err = cardStore.Create(ctx, cards.CardCreateParams{
		ID:          "card-handoff-deck",
		Type:        cards.CardTypePattern,
		Title:       "Handoff Deck Card",
		BodySummary: "HANDOFF_SUMMARY_SENTINEL",
		BodyFull:    "HANDOFF_FULL_BODY_SENTINEL",
		Tags:        []string{"handoff-tag"},
	})
	if err != nil {
		t.Fatalf("Create card: %v", err)
	}

	cmd := newHandoffCmdWithStore(beadStore)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--since", "1h"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("oro handoff --since 1h: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	if strings.Contains(output, "body_full") {
		t.Fatalf("handoff JSON included body_full field:\n%s", output)
	}
	if strings.Contains(output, "HANDOFF_FULL_BODY_SENTINEL") {
		t.Fatalf("handoff JSON included full body sentinel:\n%s", output)
	}

	var result struct {
		Cards []cardSummaryJSON `json:"cards"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal output: %v\noutput: %s", err, output)
	}

	for _, got := range result.Cards {
		if got.ID != "card-handoff-deck" {
			continue
		}
		if got.Title != "Handoff Deck Card" {
			t.Fatalf("title = %q, want Handoff Deck Card", got.Title)
		}
		if got.BodySummary != "HANDOFF_SUMMARY_SENTINEL" {
			t.Fatalf("body_summary = %q, want HANDOFF_SUMMARY_SENTINEL", got.BodySummary)
		}
		if got.Score == 0 {
			t.Fatalf("score = %v, want non-zero score", got.Score)
		}
		for _, tag := range got.Tags {
			if tag == "handoff-tag" {
				return
			}
		}
		t.Fatalf("tags = %v, want handoff-tag", got.Tags)
	}
	t.Fatalf("card-handoff-deck missing from cards: %#v", result.Cards)
}

func TestHandoffCommandRegisteredInRoot(t *testing.T) {
	root := newRootCmd()
	for _, cmd := range root.Commands() {
		if cmd.Name() == "handoff" {
			return
		}
	}
	t.Fatal("root command did not register handoff subcommand")
}
