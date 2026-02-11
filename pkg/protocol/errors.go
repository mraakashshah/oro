package protocol

import "fmt"

// QualityGateError represents a quality gate failure with detailed context.
// It wraps QG failure details to enable typed error discrimination via errors.As.
// The existing boolean DonePayload.QualityGatePassed remains for wire compatibility.
type QualityGateError struct {
	BeadID   string
	WorkerID string
	Output   string // Quality gate output (failure details)
	Attempt  int    // Retry attempt number
}

func (e *QualityGateError) Error() string {
	return fmt.Sprintf("quality gate failed for bead %s (worker %s, attempt %d): %s",
		e.BeadID, e.WorkerID, e.Attempt, e.Output)
}

// WorkerUnreachableError represents a worker communication failure.
// It enables typed error discrimination for worker connectivity issues.
type WorkerUnreachableError struct {
	WorkerID string
	BeadID   string
	Reason   string // Human-readable failure reason (e.g., "connection timeout")
}

func (e *WorkerUnreachableError) Error() string {
	return fmt.Sprintf("worker %s unreachable (bead %s): %s",
		e.WorkerID, e.BeadID, e.Reason)
}

// BeadNotFoundError represents a bead lookup failure.
// It enables typed error discrimination for bead resolution issues.
type BeadNotFoundError struct {
	BeadID string
}

func (e *BeadNotFoundError) Error() string {
	return fmt.Sprintf("bead %s not found", e.BeadID)
}
