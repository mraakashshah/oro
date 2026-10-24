package factory //nolint:testpackage // Promotion failure classification is tested through real recovery boundaries.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckedOutPromotionTarget(t *testing.T) {
	for _, phase := range []string{"approved", "promoting"} {
		for _, limit := range []int{2, 3} {
			t.Run(fmt.Sprintf("%s/limit-%d", phase, limit), func(t *testing.T) {
				spec, state := engineFixture(t, "repair")
				spec.MaxAttempts = limit
				initial := engineGit(t, spec.Repository, "rev-parse", spec.TargetBranch)
				crash := errors.New("interrupted before promotion")
				if _, err := run(t.Context(), state, spec, recoveryCrashAt(phase, crash)); !errors.Is(err, crash) {
					t.Fatalf("did not interrupt at %s: %v", phase, err)
				}
				before := recoverySnapshot(t, state, spec.ID)
				if before.State != phase || before.Attempts != 2 {
					t.Fatalf("unexpected interrupted snapshot: %+v", before)
				}
				linked := filepath.Join(t.TempDir(), "target-owner")
				engineGit(t, spec.Repository, "worktree", "add", linked, spec.TargetBranch)
				status, err := Run(t.Context(), state, spec)
				if err != nil || status.State != "failed" {
					t.Fatalf("checked-out target did not fail durably: %+v, %v", status, err)
				}
				if !strings.Contains(status.Reason, "checked out") || !strings.Contains(status.Reason, spec.TargetBranch) {
					t.Errorf("actionable target failure masked: %q", status.Reason)
				}
				if status.Attempts != before.Attempts {
					t.Errorf("checked-out target spent another attempt: %d -> %d", before.Attempts, status.Attempts)
				}
				if target := engineGit(t, spec.Repository, "rev-parse", spec.TargetBranch); target != initial {
					t.Errorf("checked-out target changed: %s -> %s", initial, target)
				}
				stored, err := ReadStatus(t.Context(), state, spec.ID)
				if err != nil || stored != status {
					t.Fatalf("actionable terminal status not durable: %+v, %v", stored, err)
				}
			})
		}
	}
}
