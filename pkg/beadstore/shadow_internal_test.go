package beadstore

import (
	"context"
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

func TestShadowCompareShownClassifiesNilAndDriftCases(t *testing.T) {
	startedAt := mustParseShadowInternalTime(t, "2026-04-28T10:00:00Z")
	var events []ShadowDivergence
	store := &ShadowStore{
		shadowStartedAt: startedAt,
		reporter: func(event ShadowDivergence) {
			events = append(events, event)
		},
	}

	store.compareShown(nil, nil, nil, nil)
	if len(events) != 0 {
		t.Fatalf("compareShown nil/nil reported events: %#v", events)
	}

	stableSecondary := &protocol.Bead{ID: "secondary-only", UpdatedAt: "2026-04-28T09:00:00Z"}
	store.compareShown(nil, nil, stableSecondary, nil)
	if len(events) != 1 || events[0].Kind != ShadowDivergenceReal || events[0].Reason != "show result mismatch" {
		t.Fatalf("compareShown stable secondary-only events = %#v, want one real mismatch", events)
	}

	hotPrimary := &protocol.Bead{ID: "primary-only", UpdatedAt: "2026-04-28T11:00:00Z"}
	store.compareShown(hotPrimary, nil, nil, nil)
	if len(events) != 2 || events[1].Kind != ShadowDivergenceDrift || events[1].Reason != "show result mismatch" {
		t.Fatalf("compareShown hot primary-only events = %#v, want drift mismatch", events)
	}

	store.compareShown(nil, errors.New("primary failed"), nil, nil)
	if len(events) != 3 || events[2].Kind != ShadowDivergenceReal || events[2].Reason != "read error" {
		t.Fatalf("compareShown error events = %#v, want real read error", events)
	}
}

func TestShadowCompareBeadsIgnoresResolverBeadWhenResolverErrors(t *testing.T) {
	startedAt := mustParseShadowInternalTime(t, "2026-04-28T10:00:00Z")
	var events []ShadowDivergence
	store := &ShadowStore{
		primary:         resolverErrorStore{Store: NewFakeStore()},
		shadowStartedAt: startedAt,
		reporter: func(event ShadowDivergence) {
			events = append(events, event)
		},
	}

	store.compareBeads(
		t.Context(),
		"Ready",
		nil,
		nil,
		[]protocol.Bead{{ID: "secondary-only", UpdatedAt: "2026-04-28T09:00:00Z"}},
		nil,
	)

	if len(events) != 1 {
		t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
	}
	if events[0].Operation != "Ready" || events[0].Kind != ShadowDivergenceReal {
		t.Fatalf("divergence = %#v, want real unresolved secondary-only row", events[0])
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

type resolverErrorStore struct {
	Store
}

func (s resolverErrorStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
	return &protocol.Bead{ID: id, UpdatedAt: "2026-04-28T11:00:00Z"}, errors.New("resolver stale read failed")
}
