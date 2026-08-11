package storage

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrInvalidRuntimeIdentity reports missing or malformed runtime identity fields.
	ErrInvalidRuntimeIdentity = errors.New("invalid runtime identity")
	// ErrInvalidProcessIdentity reports missing process identity fields.
	ErrInvalidProcessIdentity = errors.New("invalid process identity")
	// ErrProcessIdentityMismatch reports a valid observation belonging to another process.
	ErrProcessIdentityMismatch = errors.New("process identity mismatch")
)

// RuntimeIdentity identifies one assigned runtime and the process authorized to own it.
type RuntimeIdentity struct {
	TaskID, RunID, BeadID, WorkerID string
	AssignmentID, Generation        int64
	Process                         ProcessIdentity
	CreatedAt, RetainUntil          time.Time
}

// Validate checks fields intrinsic to one runtime identity.
func (identity RuntimeIdentity) Validate() error {
	for name, value := range map[string]string{
		"task_id":   identity.TaskID,
		"run_id":    identity.RunID,
		"bead_id":   identity.BeadID,
		"worker_id": identity.WorkerID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidRuntimeIdentity, name)
		}
	}
	if identity.AssignmentID <= 0 {
		return fmt.Errorf("%w: assignment_id must be positive", ErrInvalidRuntimeIdentity)
	}
	if identity.Generation <= 0 {
		return fmt.Errorf("%w: generation must be positive", ErrInvalidRuntimeIdentity)
	}
	if !isUTCTimestamp(identity.CreatedAt) {
		return fmt.Errorf("%w: created_at must be a nonzero UTC timestamp", ErrInvalidRuntimeIdentity)
	}
	if !isUTCTimestamp(identity.RetainUntil) {
		return fmt.Errorf("%w: retain_until must be a nonzero UTC timestamp", ErrInvalidRuntimeIdentity)
	}
	if identity.RetainUntil.Before(identity.CreatedAt) {
		return fmt.Errorf("%w: retain_until precedes created_at", ErrInvalidRuntimeIdentity)
	}
	return validateProcessIdentity(identity.Process)
}

// MatchesObserved validates a persisted identity and fresh process observation,
// then compares every process identity field.
func (identity RuntimeIdentity) MatchesObserved(observed ProcessIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if err := validateProcessIdentity(observed); err != nil {
		return err
	}
	if !identity.Process.Matches(observed) {
		return ErrProcessIdentityMismatch
	}
	return nil
}

func validateProcessIdentity(identity ProcessIdentity) error {
	if identity.PID <= 0 {
		return fmt.Errorf("%w: pid must be positive", ErrInvalidProcessIdentity)
	}
	if strings.TrimSpace(identity.StartMarker) == "" {
		return fmt.Errorf("%w: start_marker is required", ErrInvalidProcessIdentity)
	}
	if strings.TrimSpace(identity.Executable) == "" {
		return fmt.Errorf("%w: executable is required", ErrInvalidProcessIdentity)
	}
	if identity.ProcessGroup <= 0 {
		return fmt.Errorf("%w: process_group must be positive", ErrInvalidProcessIdentity)
	}
	return nil
}

func isUTCTimestamp(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}
