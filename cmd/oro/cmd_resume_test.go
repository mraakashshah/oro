package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/cards"
)

func TestResumeDropsIntoBeadContext(t *testing.T) {
	ctx := context.Background()
	beadStore, cardStore := openTestRenderStore(t)

	// Seed bead with a known AC.
	inProg := "in_progress"
	if _, err := beadStore.Create(ctx, beadstore.CreateParams{
		ID:                 "bead-resume-1",
		Title:              "Resume Target",
		Type:               "task",
		AcceptanceCriteria: "tests pass and gate is green",
	}); err != nil {
		t.Fatalf("Create bead: %v", err)
	}
	if err := beadStore.Update(ctx, "bead-resume-1", beadstore.UpdateParams{Status: &inProg}); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	// Seed 6 journey events (more than 5 so we can assert only last 5 shown).
	now := time.Now().UTC()
	journeyEvents := []beadstore.JourneyEvent{
		{Ts: now.Add(-6 * time.Minute).Format(time.RFC3339Nano), Actor: "worker", Event: "started"},
		{Ts: now.Add(-5 * time.Minute).Format(time.RFC3339Nano), Actor: "worker", Event: "assess"},
		{Ts: now.Add(-4 * time.Minute).Format(time.RFC3339Nano), Actor: "worker", Event: "plan"},
		{Ts: now.Add(-3 * time.Minute).Format(time.RFC3339Nano), Actor: "worker", Event: "execute"},
		{Ts: now.Add(-2 * time.Minute).Format(time.RFC3339Nano), Actor: "worker", Event: "validate"},
		{Ts: now.Add(-1 * time.Minute).Format(time.RFC3339Nano), Actor: "worker", Event: "checkpoint"},
	}
	for _, e := range journeyEvents {
		if err := beadStore.AppendJourney(ctx, "bead-resume-1", e); err != nil {
			t.Fatalf("AppendJourney: %v", err)
		}
	}

	// Seed a linked card.
	if _, err := cardStore.Create(ctx, cards.CardCreateParams{
		ID:          "card-resume-1",
		Type:        cards.CardTypePattern,
		Title:       "Go error handling pattern",
		BodySummary: "wrap errors with context",
		BodyFull:    "full body",
		Tags:        []string{"go", "task"},
	}); err != nil {
		t.Fatalf("Create card: %v", err)
	}

	// Wrap with tx counter.
	counter := &txCountStore{Store: beadStore}

	cmd := newResumeCmdWithStore(counter)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"bead-resume-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("oro resume bead-resume-1: %v\nstderr: %s", err, stderr.String())
	}

	out := stdout.String()

	// Must contain bead title.
	if !strings.Contains(out, "Resume Target") {
		t.Fatalf("stdout missing bead title; got:\n%s", out)
	}
	// Must contain status.
	if !strings.Contains(out, "in_progress") {
		t.Fatalf("stdout missing status; got:\n%s", out)
	}
	// Must contain acceptance criteria.
	if !strings.Contains(out, "tests pass and gate is green") {
		t.Fatalf("stdout missing AC; got:\n%s", out)
	}
	// Must contain last 5 journey events (not the 6th oldest).
	for _, evt := range []string{"assess", "plan", "execute", "validate", "checkpoint"} {
		if !strings.Contains(out, evt) {
			t.Fatalf("stdout missing event %q; got:\n%s", evt, out)
		}
	}
	// First event ("started") should NOT appear (only last 5 of 6 shown).
	if strings.Contains(out, "started") {
		t.Fatalf("stdout should not contain 'started' (6th oldest event); got:\n%s", out)
	}
	// Must contain linked card.
	if !strings.Contains(out, "Go error handling pattern") {
		t.Fatalf("stdout missing card title; got:\n%s", out)
	}

	// All reads inside exactly one WithReadTx span.
	if counter.count != 1 {
		t.Fatalf("WithReadTx call count = %d, want 1", counter.count)
	}
}

func TestResumeRendersDeckCardSummaryWithoutFullBody(t *testing.T) {
	ctx := context.Background()
	beadStore, cardStore := openTestRenderStore(t)

	_, err := beadStore.Create(ctx, beadstore.CreateParams{
		ID:                 "bead-resume-1",
		Title:              "Resume Target",
		Type:               "task",
		AcceptanceCriteria: "resume deck acceptance",
	})
	if err != nil {
		t.Fatalf("Create bead: %v", err)
	}

	_, err = cardStore.Create(ctx, cards.CardCreateParams{
		ID:          "card-resume-deck",
		Type:        cards.CardTypePattern,
		Title:       "Resume Deck Card",
		BodySummary: "RESUME_SUMMARY_SENTINEL",
		BodyFull:    "RESUME_FULL_BODY_SENTINEL",
		Tags:        []string{"resume-tag"},
	})
	if err != nil {
		t.Fatalf("Create card: %v", err)
	}

	cmd := newResumeCmdWithStore(beadStore)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"bead-resume-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("oro resume bead-resume-1: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	for _, want := range []string{"Resume Deck Card", "RESUME_SUMMARY_SENTINEL"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stdout missing %q; got:\n%s", want, output)
		}
	}
	for _, forbidden := range []string{"body_full", "RESUME_FULL_BODY_SENTINEL"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("stdout included %q; got:\n%s", forbidden, output)
		}
	}
}

func TestResumeCommandRegisteredInRoot(t *testing.T) {
	root := newRootCmd()
	for _, cmd := range root.Commands() {
		if cmd.Name() == "resume" {
			return
		}
	}
	t.Fatal("root command did not register resume subcommand")
}
