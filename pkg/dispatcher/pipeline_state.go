package dispatcher

import (
	"context"
	"errors"
	"fmt"

	"oro/pkg/beadstore"
)

// ErrInvalidTransition is returned when a pipeline stage transition is not permitted
// by the state machine defined in §11.9.
var ErrInvalidTransition = errors.New("dispatcher: invalid pipeline stage transition")

var validTransitions = map[beadstore.PipelineStage][]beadstore.PipelineStage{ //nolint:gochecknoglobals // §11.9 state machine — static config table, read-only after init
	beadstore.StageNone:     {beadstore.StageAssess},
	beadstore.StageAssess:   {beadstore.StagePlan},
	beadstore.StagePlan:     {beadstore.StagePrepare},
	beadstore.StagePrepare:  {beadstore.StageExecute},
	beadstore.StageExecute:  {beadstore.StageValidate, beadstore.StageExecute},
	beadstore.StageValidate: {beadstore.StageEvolve, beadstore.StageExecute},
	beadstore.StageEvolve:   {beadstore.StageAssess},
}

// pipelineStageTransitioner is the beadstore capability required for pipeline transitions.
type pipelineStageTransitioner interface {
	TransitionPipelineStage(ctx context.Context, beadID string, from, to beadstore.PipelineStage) error
}

// transitionPipeline validates from→to against the §11.9 state machine and delegates
// to the store. Returns ErrInvalidTransition for arcs not in the spec.
func transitionPipeline(ctx context.Context, store pipelineStageTransitioner, beadID string, from, to beadstore.PipelineStage) error {
	allowed, ok := validTransitions[from]
	if !ok {
		return fmt.Errorf("%w: %s → %s (unknown from-stage)", ErrInvalidTransition, from, to)
	}
	for _, s := range allowed {
		if s == to {
			if err := store.TransitionPipelineStage(ctx, beadID, from, to); err != nil {
				return fmt.Errorf("pipeline transition %s → %s: %w", from, to, err)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, from, to)
}
