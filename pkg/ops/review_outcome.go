package ops

import "oro/pkg/reviewcontract"

// ReviewDecision is the terminal result of a typed review.
type ReviewDecision string

const (
	ReviewApproved ReviewDecision = "approved"
	ReviewRejected ReviewDecision = "rejected"
	ReviewBlocked  ReviewDecision = "blocked"
	ReviewFailed   ReviewDecision = "failed"
)

// ReviewExecutionKind classifies the review subprocess outcome.
type ReviewExecutionKind string

const (
	ReviewExecSucceeded  ReviewExecutionKind = "succeeded"
	ReviewExecSpawnError ReviewExecutionKind = "spawn_error"
	ReviewExecExitError  ReviewExecutionKind = "exit_error"
	ReviewExecTimeout    ReviewExecutionKind = "timeout"
	ReviewExecIdle       ReviewExecutionKind = "idle_timeout"
	ReviewExecCancelled  ReviewExecutionKind = "cancelled"
)

// ReviewBlocker describes an environment or infrastructure block.
type ReviewBlocker struct {
	Class     string `json:"class"`
	Scope     string `json:"scope"`
	Command   string `json:"command,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Summary   string `json:"summary"`
}

// ReviewVerification records acceptance-command status.
type ReviewVerification struct {
	AcceptanceCommand string `json:"acceptance_command,omitempty"`
	AcceptanceStatus  string `json:"acceptance_status"`
	AcceptanceExit    int    `json:"acceptance_exit,omitempty"`
}

// ReviewArtifactRef identifies the bounded raw review artifact.
type ReviewArtifactRef struct {
	Path      string `json:"path,omitempty"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ReviewPersonaExecution records a required or optional persona result.
type ReviewPersonaExecution struct {
	Persona   string              `json:"persona"`
	Required  bool                `json:"required"`
	Kind      ReviewExecutionKind `json:"kind"`
	ErrorCode string              `json:"error_code,omitempty"`
}

// ReviewExecution records typed review process completion.
type ReviewExecution struct {
	Kind              ReviewExecutionKind      `json:"kind"`
	ExitCode          int                      `json:"exit_code,omitempty"`
	ErrorCode         string                   `json:"error_code,omitempty"`
	Complete          bool                     `json:"complete"`
	RequiredPersonas  []string                 `json:"required_personas,omitempty"`
	CompletedPersonas []string                 `json:"completed_personas,omitempty"`
	PersonaExecutions []ReviewPersonaExecution `json:"persona_executions,omitempty"`
}

// ReviewOutcome is the schema-validated review result consumed by dispatch.
type ReviewOutcome struct {
	Decision     ReviewDecision           `json:"decision"`
	Findings     []reviewcontract.Finding `json:"findings,omitempty"`
	Blockers     []ReviewBlocker          `json:"blockers,omitempty"`
	Verification ReviewVerification       `json:"verification"`
	Execution    ReviewExecution          `json:"execution"`
	Summary      string                   `json:"summary"`
	Artifact     ReviewArtifactRef        `json:"artifact"`
}
