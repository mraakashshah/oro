package beadstore

import (
	"errors"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestShadowCompareValueReportsOnlyMismatchesAndErrors(t *testing.T) {
	var events []ShadowDivergence
	store := &ShadowStore{reporter: func(event ShadowDivergence) {
		events = append(events, event)
	}}

	store.compareValue("CountByStatus", StatusCounts{Open: 1}, nil, StatusCounts{Open: 1}, nil)
	if len(events) != 0 {
		t.Fatalf("compareValue equal reported events: %#v", events)
	}

	store.compareValue("CountByStatus", StatusCounts{Open: 1}, nil, StatusCounts{Open: 2}, nil)
	if len(events) != 1 || events[0].Operation != "CountByStatus" || events[0].Kind != ShadowDivergenceReal || events[0].Reason != "read result mismatch" {
		t.Fatalf("compareValue mismatch events = %#v, want one real mismatch", events)
	}

	store.compareValue("CountByStatus", nil, errors.New("primary failed"), nil, nil)
	if len(events) != 2 || events[1].Operation != "CountByStatus" || events[1].Kind != ShadowDivergenceReal || events[1].Reason != "read error" {
		t.Fatalf("compareValue error events = %#v, want one real read error", events)
	}
}

func TestShadowClassifiesPrimaryOnlyRows(t *testing.T) {
	startedAt := mustParseShadowInternalTime(t, "2026-04-28T10:00:00Z")

	got := ClassifyShadowDivergence(
		[]protocol.Bead{{ID: "stable-primary", UpdatedAt: "2026-04-28T09:00:00Z"}},
		nil,
		startedAt,
	)
	if got != ShadowDivergenceReal {
		t.Fatalf("ClassifyShadowDivergence stable primary-only = %v, want real", got)
	}

	got = ClassifyShadowDivergence(
		[]protocol.Bead{{ID: "hot-primary", UpdatedAt: "2026-04-28T11:00:00Z"}},
		nil,
		startedAt,
	)
	if got != ShadowDivergenceDrift {
		t.Fatalf("ClassifyShadowDivergence hot primary-only = %v, want drift", got)
	}
}

func mustParseShadowInternalTime(t *testing.T, raw string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("parse time %q: %v", raw, err)
	}
	return parsed
}
