// Package factory implements an experimental autonomous delivery lifecycle for trusted command adapters.
package factory

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Command identifies an admitted executable and its literal arguments.
type Command struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
}

// Spec fixes an objective, its executable policies, and its lifetime budget.
type Spec struct {
	ID                 string   `json:"id"`
	Objective          string   `json:"objective"`
	Repository         string   `json:"repository"`
	TargetBranch       string   `json:"target_branch"`
	Worker             Command  `json:"worker"`
	Acceptance         Command  `json:"acceptance"`
	Review             Command  `json:"review"`
	RequiredChecks     []string `json:"required_checks"`
	MaxAttempts        int      `json:"max_attempts"`
	TimeoutSeconds     int      `json:"timeout_seconds"`
	StepTimeoutSeconds int      `json:"step_timeout_seconds"`
}

// Status describes durable objective progress or its terminal outcome.
type Status struct {
	ID           string `json:"id"`
	State        string `json:"state"`
	Attempts     int    `json:"attempts"`
	CandidateSHA string `json:"candidate_sha,omitempty"`
	TargetBranch string `json:"target_branch"`
	Reason       string `json:"reason,omitempty"`
}

type evidence struct {
	Stage        string
	CandidateSHA string
	BaseSHA      string
	PolicyHash   string
	OutputHash   string
	ExitCode     int
}

var objectiveID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func validateSpec(spec Spec) error {
	if !objectiveID.MatchString(spec.ID) {
		return errors.New("invalid objective ID")
	}
	if strings.TrimSpace(spec.Objective) == "" || strings.TrimSpace(spec.TargetBranch) == "" {
		return errors.New("objective and target branch must be nonempty")
	}
	if !filepath.IsAbs(spec.Repository) {
		return errors.New("repository path must be absolute")
	}
	for name, command := range map[string]Command{"worker": spec.Worker, "acceptance": spec.Acceptance, "review": spec.Review} {
		if !filepath.IsAbs(command.Path) {
			return fmt.Errorf("%s program path must be absolute", name)
		}
	}
	if spec.MaxAttempts <= 0 || spec.TimeoutSeconds <= 0 || spec.StepTimeoutSeconds <= 0 ||
		spec.StepTimeoutSeconds > spec.TimeoutSeconds || int64(spec.TimeoutSeconds) > math.MaxInt64/int64(time.Second) {
		return errors.New("attempt and timeout limits must be positive, representable, and step timeout must not exceed total timeout")
	}
	if len(spec.RequiredChecks) == 0 {
		return errors.New("at least one required check must be specified")
	}
	seen := make(map[string]bool, len(spec.RequiredChecks))
	for _, check := range spec.RequiredChecks {
		if strings.TrimSpace(check) == "" || seen[check] {
			return errors.New("required checks must be nonempty and unique")
		}
		seen[check] = true
	}
	return nil
}
