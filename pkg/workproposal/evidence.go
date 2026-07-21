package workproposal

import (
	"errors"
	"fmt"
	"reflect"
	"time"
)

const maxEvidenceAge = 20 * time.Minute

// EvidenceManifest is the immutable identity captured by a dispatcher-owned
// command execution.
//
//oro:testonly — proposal admission wires this validator in a later task.
type EvidenceManifest struct {
	Project      string
	AssignmentID int64
	WorkerID     string
	BeadID       string
	Worktree     string
	Branch       string
	HEAD         string
	Command      []string
	StartedAt    time.Time
	CompletedAt  time.Time
	Terminal     bool
}

// AssignmentSnapshot is the authoritative identity resolved at proposal
// admission time.
//
//oro:testonly — proposal admission wires this validator in a later task.
type AssignmentSnapshot struct {
	Project      string
	AssignmentID int64
	WorkerID     string
	BeadID       string
	Worktree     string
	Branch       string
	HEAD         string
	Command      []string
}

// ValidateEvidence rejects evidence that is not terminal, fresh, and exactly
// bound to the assignment and repository state observed at admission time.
//
//oro:testonly — proposal admission wires this validator in a later task.
func ValidateEvidence(manifest EvidenceManifest, live AssignmentSnapshot, now time.Time) error {
	if !manifest.Terminal {
		return errors.New("evidence is not terminal")
	}
	if err := validateEvidenceIdentity(manifest, live); err != nil {
		return err
	}
	return validateEvidenceTimestamp(manifest, now)
}

func validateEvidenceIdentity(manifest EvidenceManifest, live AssignmentSnapshot) error {
	for _, identity := range []struct {
		name string
		got  any
		want any
	}{
		{name: "project", got: manifest.Project, want: live.Project},
		{name: "assignment", got: manifest.AssignmentID, want: live.AssignmentID},
		{name: "worker", got: manifest.WorkerID, want: live.WorkerID},
		{name: "bead", got: manifest.BeadID, want: live.BeadID},
		{name: "worktree", got: manifest.Worktree, want: live.Worktree},
		{name: "branch", got: manifest.Branch, want: live.Branch},
		{name: "HEAD", got: manifest.HEAD, want: live.HEAD},
		{name: "command", got: manifest.Command, want: live.Command},
	} {
		if !reflect.DeepEqual(identity.got, identity.want) {
			return fmt.Errorf("evidence %s does not match live assignment", identity.name)
		}
	}
	return nil
}

func validateEvidenceTimestamp(manifest EvidenceManifest, now time.Time) error {
	if now.IsZero() || manifest.StartedAt.IsZero() || manifest.CompletedAt.IsZero() {
		return errors.New("evidence timestamp is incomplete")
	}
	if manifest.CompletedAt.Before(manifest.StartedAt) {
		return errors.New("evidence completed before it started")
	}
	if manifest.CompletedAt.After(now) {
		return errors.New("evidence completed in the future")
	}
	if now.Sub(manifest.CompletedAt) > maxEvidenceAge {
		return errors.New("evidence is expired")
	}
	return nil
}
