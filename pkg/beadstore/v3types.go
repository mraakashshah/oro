package beadstore

import "errors"

// JourneyEvent is a single entry in a bead's append-only audit trail.
type JourneyEvent struct {
	ID      int64
	BeadID  string
	Ts      string // RFC3339Nano
	Actor   string // worker | oracle | dispatcher | ops_review | human | migration | system
	Event   string // closed vocabulary per §4.3
	Payload string // JSON, event-specific; empty string when absent
}

// PipelineStage is the current stage in the bead's workflow pipeline (§11.9).
type PipelineStage string

// Pipeline stage values for TransitionPipelineStage transitions.
const (
	StageAssess   PipelineStage = "assess"
	StagePlan     PipelineStage = "plan"
	StagePrepare  PipelineStage = "prepare"
	StageExecute  PipelineStage = "execute"
	StageValidate PipelineStage = "validate"
	StageEvolve   PipelineStage = "evolve"
	StageNone     PipelineStage = "none"
)

// ErrStaleStage is returned by TransitionPipelineStage when the `from` stage
// does not match the current pipeline_stage (concurrent writer raced ahead).
var ErrStaleStage = errors.New("beadstore: pipeline stage changed by concurrent writer")
