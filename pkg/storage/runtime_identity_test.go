package storage_test

import (
	"errors"
	"testing"
	"time"

	"oro/pkg/storage"
)

func TestRuntimeIdentityContract(t *testing.T) {
	identity := validRuntimeIdentity()
	if err := identity.Validate(); err != nil {
		t.Fatalf("valid runtime identity rejected: %v", err)
	}
	if err := identity.MatchesObserved(identity.Process); err != nil {
		t.Fatalf("matching process identity rejected: %v", err)
	}

	invalidRuntime := []struct {
		name   string
		mutate func(*storage.RuntimeIdentity)
	}{
		{name: "empty task", mutate: func(candidate *storage.RuntimeIdentity) { candidate.TaskID = "" }},
		{name: "whitespace task", mutate: func(candidate *storage.RuntimeIdentity) { candidate.TaskID = " \t" }},
		{name: "empty run", mutate: func(candidate *storage.RuntimeIdentity) { candidate.RunID = "" }},
		{name: "whitespace run", mutate: func(candidate *storage.RuntimeIdentity) { candidate.RunID = " \t" }},
		{name: "empty bead", mutate: func(candidate *storage.RuntimeIdentity) { candidate.BeadID = "" }},
		{name: "whitespace bead", mutate: func(candidate *storage.RuntimeIdentity) { candidate.BeadID = " \t" }},
		{name: "empty worker", mutate: func(candidate *storage.RuntimeIdentity) { candidate.WorkerID = "" }},
		{name: "whitespace worker", mutate: func(candidate *storage.RuntimeIdentity) { candidate.WorkerID = "  " }},
		{name: "zero assignment", mutate: func(candidate *storage.RuntimeIdentity) { candidate.AssignmentID = 0 }},
		{name: "negative assignment", mutate: func(candidate *storage.RuntimeIdentity) { candidate.AssignmentID = -1 }},
		{name: "zero generation", mutate: func(candidate *storage.RuntimeIdentity) { candidate.Generation = 0 }},
		{name: "negative generation", mutate: func(candidate *storage.RuntimeIdentity) { candidate.Generation = -1 }},
		{name: "zero created time", mutate: func(candidate *storage.RuntimeIdentity) { candidate.CreatedAt = time.Time{} }},
		{name: "zero retain time", mutate: func(candidate *storage.RuntimeIdentity) { candidate.RetainUntil = time.Time{} }},
		{name: "non-UTC created time", mutate: func(candidate *storage.RuntimeIdentity) {
			candidate.CreatedAt = time.Date(2026, time.August, 10, 12, 0, 0, 0, time.FixedZone("EDT", -4*60*60))
		}},
		{name: "non-UTC retain time", mutate: func(candidate *storage.RuntimeIdentity) {
			candidate.RetainUntil = time.Date(2026, time.August, 10, 13, 0, 0, 0, time.FixedZone("EDT", -4*60*60))
		}},
		{name: "retain before created", mutate: func(candidate *storage.RuntimeIdentity) {
			candidate.RetainUntil = candidate.CreatedAt.Add(-time.Second)
		}},
	}
	for _, test := range invalidRuntime {
		t.Run(test.name, func(t *testing.T) {
			candidate := identity
			test.mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, storage.ErrInvalidRuntimeIdentity) {
				t.Fatalf("Validate() error = %v, want ErrInvalidRuntimeIdentity", err)
			}
		})
	}

	invalidProcess := []struct {
		name   string
		mutate func(*storage.ProcessIdentity)
	}{
		{name: "zero pid", mutate: func(candidate *storage.ProcessIdentity) { candidate.PID = 0 }},
		{name: "empty start marker", mutate: func(candidate *storage.ProcessIdentity) { candidate.StartMarker = "" }},
		{name: "whitespace executable", mutate: func(candidate *storage.ProcessIdentity) { candidate.Executable = " \t" }},
		{name: "zero process group", mutate: func(candidate *storage.ProcessIdentity) { candidate.ProcessGroup = 0 }},
	}
	for _, test := range invalidProcess {
		t.Run("persisted process "+test.name, func(t *testing.T) {
			candidate := identity
			test.mutate(&candidate.Process)
			if err := candidate.Validate(); !errors.Is(err, storage.ErrInvalidProcessIdentity) {
				t.Fatalf("Validate() error = %v, want ErrInvalidProcessIdentity", err)
			}
		})

		t.Run("observed process "+test.name, func(t *testing.T) {
			observed := identity.Process
			test.mutate(&observed)
			if err := identity.MatchesObserved(observed); !errors.Is(err, storage.ErrInvalidProcessIdentity) {
				t.Fatalf("MatchesObserved() error = %v, want ErrInvalidProcessIdentity", err)
			}
		})
	}

	mismatches := []struct {
		name   string
		mutate func(*storage.ProcessIdentity)
	}{
		{name: "pid", mutate: func(candidate *storage.ProcessIdentity) { candidate.PID++ }},
		{name: "start marker", mutate: func(candidate *storage.ProcessIdentity) { candidate.StartMarker += "-new" }},
		{name: "executable", mutate: func(candidate *storage.ProcessIdentity) { candidate.Executable = "/usr/bin/oro" }},
		{name: "process group", mutate: func(candidate *storage.ProcessIdentity) { candidate.ProcessGroup++ }},
	}
	for _, test := range mismatches {
		t.Run("observed mismatch "+test.name, func(t *testing.T) {
			observed := identity.Process
			test.mutate(&observed)
			if err := identity.MatchesObserved(observed); !errors.Is(err, storage.ErrProcessIdentityMismatch) {
				t.Fatalf("MatchesObserved() error = %v, want ErrProcessIdentityMismatch", err)
			}
		})
	}

	invalidPersisted := identity
	invalidPersisted.TaskID = ""
	invalidObserved := identity.Process
	invalidObserved.PID = 0
	if err := invalidPersisted.MatchesObserved(invalidObserved); !errors.Is(err, storage.ErrInvalidRuntimeIdentity) {
		t.Fatalf("MatchesObserved() validation order error = %v, want ErrInvalidRuntimeIdentity", err)
	}
}

func validRuntimeIdentity() storage.RuntimeIdentity {
	created := time.Date(2026, time.August, 10, 16, 0, 0, 0, time.UTC)
	return storage.RuntimeIdentity{
		TaskID:       "task-1",
		RunID:        "run-1",
		BeadID:       "bead-1",
		WorkerID:     "worker-1",
		AssignmentID: 7,
		Generation:   3,
		Process: storage.ProcessIdentity{
			PID:          42,
			StartMarker:  "linux:12345",
			Executable:   "/usr/local/bin/oro",
			ProcessGroup: 42,
		},
		CreatedAt:   created,
		RetainUntil: created.Add(time.Hour),
	}
}
