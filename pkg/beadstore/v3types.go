package beadstore

import "errors"

// JourneyEvent is a single entry in a bead's append-only audit trail.
type JourneyEvent struct {
	ID      int64
	BeadID  string
	Ts      string // RFC3339Nano
	Actor   string // worker | oracle | dispatcher | ops_review | premortem | human | migration | system
	Event   string // closed vocabulary per §4.3
	Payload string // JSON, event-specific; empty string when absent
}

// GateState is the premortem gate status for a bead (§11.4).
type GateState string

// Gate state values for SetGateState transitions.
const (
	GateNone      GateState = "none"
	GateEligible  GateState = "eligible"
	GateSatisfied GateState = "satisfied"
	GateBlocked   GateState = "blocked"
	GateReplan    GateState = "replan"
	GateEscalated GateState = "escalated"
)

// PipelineStage is the current stage in the bead's workflow pipeline (§11.9).
type PipelineStage string

// Pipeline stage values for TransitionPipelineStage transitions.
const (
	StageAssess    PipelineStage = "assess"
	StagePlan      PipelineStage = "plan"
	StagePremortem PipelineStage = "premortem"
	StagePrepare   PipelineStage = "prepare"
	StageExecute   PipelineStage = "execute"
	StageValidate  PipelineStage = "validate"
	StageEvolve    PipelineStage = "evolve"
	StageNone      PipelineStage = "none"
)

// ErrStaleStage is returned by TransitionPipelineStage when the `from` stage
// does not match the current pipeline_stage (concurrent writer raced ahead).
var ErrStaleStage = errors.New("beadstore: pipeline stage changed by concurrent writer")

// ErrStaleGate is returned by SetGateState when the `from` gate does not match
// the current gate_state (concurrent writer raced ahead).
var ErrStaleGate = errors.New("beadstore: gate state changed by concurrent writer")
