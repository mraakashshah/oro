package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
)

func TestReviewTriage_AppendsHistory(t *testing.T) {
	ctx := context.Background()
	store := beadstore.NewFakeStore()
	seedPayload := `{"id":"fnd_1234abcd","status":"open","history":[{"kind":"detected","status":"open","note":"initial review"}]}`
	if err := store.AppendJourney(ctx, "oro-task", beadstore.JourneyEvent{
		Ts:      time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		Actor:   "ops_review",
		Event:   "review_finding",
		Payload: seedPayload,
	}); err != nil {
		t.Fatalf("seed review finding: %v", err)
	}

	cmd := newReviewCmdWithStore(store)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{
		"triage",
		"oro-task",
		"fnd_1234abcd",
		"--status=false-positive",
		"--note=covered by existing invariant",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("review triage: %v\nstderr: %s", err, stderr.String())
	}

	events, err := store.LatestJourney(ctx, "oro-task", 10)
	if err != nil {
		t.Fatalf("LatestJourney: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("journey event count = %d, want 2", len(events))
	}
	latest := events[len(events)-1]
	if latest.Actor != "human" || latest.Event != "review_finding" {
		t.Fatalf("unexpected event: %+v", latest)
	}

	var payload struct {
		ID        string `json:"id"`
		FindingID string `json:"finding_id"`
		Status    string `json:"status"`
		History   []struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
			Note   string `json:"note"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(latest.Payload), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v\npayload: %s", err, latest.Payload)
	}
	if payload.ID != "fnd_1234abcd" || payload.FindingID != "fnd_1234abcd" || payload.Status != "false-positive" {
		t.Fatalf("unexpected payload finding/status: %+v", payload)
	}
	if len(payload.History) != 2 {
		t.Fatalf("history count = %d, want 2", len(payload.History))
	}
	entry := payload.History[1]
	if entry.Kind != "triage" || entry.Status != "false-positive" || entry.Note != "covered by existing invariant" {
		t.Fatalf("unexpected history entry: %+v", entry)
	}

	invalid := newReviewCmdWithStore(store)
	invalid.SetOut(&stdout)
	invalid.SetErr(&stderr)
	invalid.SetArgs([]string{
		"triage",
		"oro-task",
		"fnd_1234abcd",
		"--status=ignored",
		"--note=bad status",
	})
	if err := invalid.Execute(); err == nil || !strings.Contains(err.Error(), "invalid status") {
		t.Fatalf("invalid status err = %v, want enum validation error", err)
	}
}
