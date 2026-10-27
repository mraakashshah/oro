package factory

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (b *managedDeliveryBackend) execute(ctx context.Context, stage string, input commandInput, deadline time.Time) commandResult {
	if stage != "worker" || !deadline.Equal(b.owner.manifest.Deadline) {
		return commandResult{ExitCode: -1, Err: interrupt("candidate", errors.New("worker identity or objective deadline differs"))}
	}
	err := b.owner.journal.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		current, err := workspaceOwner(ctx, tx, b.owner)
		if err != nil {
			return false, err
		}
		if input.Objective != current.Spec.Objective || input.Attempt != current.Attempts || input.Worktree != current.Worktree || input.BaseSHA != current.BaseSHA || input.CandidateSHA != current.CandidateSHA || input.Feedback != current.Feedback {
			return false, errors.New("worker input differs from saved snapshot")
		}
		return false, nil
	})
	if err != nil {
		return commandResult{ExitCode: -1, Err: interrupt("candidate", err)}
	}
	intent, err := reserveManagedCandidate(ctx, b.owner, input.Worktree)
	if err != nil {
		return commandResult{ExitCode: -1, Err: interrupt("candidate", err)}
	}
	if _, err = b.docker.prepareCandidate(ctx, b.owner, intent); err != nil {
		return commandResult{ExitCode: -1, Err: interrupt("candidate", err)}
	}
	return commandResult{ExitCode: -1, Err: interrupt("candidate", errors.New("engineer-adapter-unavailable"))}
}
