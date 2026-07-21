package protocol

// WorkProposalPayload is a worker's provisional report of discovered work.
// Canonical scope derivation is deliberately deferred to the controller.
type WorkProposalPayload struct {
	ClientProposalID  string `json:"client_proposal_id"`
	AssignmentID      int64  `json:"assignment_id"`
	WorkerID          string `json:"worker_id"`
	BeadID            string `json:"bead_id"`
	EvidenceRunID     string `json:"evidence_run_id"`
	Fingerprint       string `json:"fingerprint"`
	ScopeHint         string `json:"scope_hint"`
	Kind              string `json:"kind"`
	Summary           string `json:"summary"`
	SuggestedTitle    string `json:"suggested_title"`
	SuggestedType     string `json:"suggested_type"`
	SuggestedPriority int    `json:"suggested_priority"`
}

// EvidenceRun is the durable execution record a proposal must cite.
type EvidenceRun struct {
	ID           string `json:"id"`
	AssignmentID int64  `json:"assignment_id"`
	WorkerID     string `json:"worker_id"`
	BeadID       string `json:"bead_id"`
	Kind         string `json:"kind"`
	Status       string `json:"status"`
}

// EvidenceRequest submits one durable evidence run before a proposal cites it.
type EvidenceRequest struct {
	Evidence  EvidenceRun               `json:"evidence,omitempty"`
	Execution *EvidenceExecutionRequest `json:"execution,omitempty"`
}

// WorkRequestCapability carries the live assignment credential on a
// short-lived worker request.
type WorkRequestCapability struct {
	CapabilityID string `json:"capability_id"`
	Token        string `json:"token"`
	Generation   int64  `json:"generation"`
	Nonce        string `json:"nonce"`
}

// EvidenceExecutionRequest asks the dispatcher to execute argv in the
// authoritative assignment worktree.
type EvidenceExecutionRequest struct {
	AssignmentID int64                 `json:"assignment_id"`
	WorkerID     string                `json:"worker_id"`
	BeadID       string                `json:"bead_id"`
	Kind         string                `json:"kind"`
	Argv         []string              `json:"argv"`
	TimeoutMS    int64                 `json:"timeout_ms"`
	Capability   WorkRequestCapability `json:"capability"`
}

// EvidenceRunResult is the bounded terminal result of dispatcher-owned evidence.
type EvidenceRunResult struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
}

// EvidenceResponse acknowledges an evidence request or reports its validation error.
type EvidenceResponse struct {
	Result EvidenceRunResult `json:"result"`
	Error  string            `json:"error,omitempty"`
}

// WorkProposalResult is the durable response to a work-proposal submission.
type WorkProposalResult struct {
	ProposalID string `json:"proposal_id"`
	State      string `json:"state"`
}

// WorkProposalRequest submits one provisional work proposal.
type WorkProposalRequest struct {
	Proposal   WorkProposalPayload   `json:"proposal"`
	Capability WorkRequestCapability `json:"capability"`
}

// WorkProposalResponse returns the durable proposal result or a validation error.
type WorkProposalResponse struct {
	Result WorkProposalResult `json:"result"`
	Error  string             `json:"error,omitempty"`
}

// Event represents a row in the events SQLite table.
// Tracks all dispatcher/worker lifecycle events.
type Event struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Source    string `json:"source"`
	BeadID    string `json:"bead_id"`
	WorkerID  string `json:"worker_id"`
	Payload   string `json:"payload"`
	CreatedAt string `json:"created_at"`
}

// Assignment represents a row in the assignments SQLite table.
// Tracks worker-to-bead assignment lifecycle.
type Assignment struct {
	ID          int64  `json:"id"`
	BeadID      string `json:"bead_id"`
	WorkerID    string `json:"worker_id"`
	Worktree    string `json:"worktree"`
	Status      string `json:"status"`
	AssignedAt  string `json:"assigned_at"`
	CompletedAt string `json:"completed_at"`
}

// CommandRow represents a row in the commands SQLite table.
// Named CommandRow to avoid collision with the existing Command UDS type.
// Manager writes commands; the dispatcher reads and processes them.
type CommandRow struct {
	ID          int64  `json:"id"`
	Directive   string `json:"directive"`
	Args        string `json:"args"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	ProcessedAt string `json:"processed_at"`
}

// Escalation represents a row in the escalations SQLite table.
// Persistent queue: dispatcher writes pending escalations, manager acks them.
type Escalation struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	BeadID      string `json:"bead_id"`
	WorkerID    string `json:"worker_id"`
	Message     string `json:"message"`
	Status      string `json:"status"` // pending, acked, dismissed
	CreatedAt   string `json:"created_at"`
	AckedAt     string `json:"acked_at"`
	RetryCount  int    `json:"retry_count"`
	LastRetryAt string `json:"last_retry_at"`
}

// Memory represents a row in the memories SQLite table.
// Cross-session project memory: learnings, decisions, gotchas, patterns.
type Memory struct {
	ID            int64   `json:"id"`
	Content       string  `json:"content"`
	Type          string  `json:"type"`
	Tags          string  `json:"tags"`
	Source        string  `json:"source"`
	BeadID        string  `json:"bead_id"`
	WorkerID      string  `json:"worker_id"`
	Confidence    float64 `json:"confidence"`
	CreatedAt     string  `json:"created_at"`
	Embedding     []byte  `json:"embedding"`
	FilesRead     string  `json:"files_read"`
	FilesModified string  `json:"files_modified"`
	Pinned        bool    `json:"pinned"`
}

// MemoryInsertParams holds parameters for inserting a new memory.
type MemoryInsertParams struct {
	Content       string
	Type          string // lesson | decision | gotcha | pattern | preference | summary | self_report
	Tags          []string
	Source        string // self_report | daemon_extracted
	BeadID        string
	WorkerID      string
	Confidence    float64
	FilesRead     []string
	FilesModified []string
	Pinned        bool
}

// MemorySearchOpts configures memory search queries.
type MemorySearchOpts struct {
	Limit    int      // default 10
	Type     string   // optional filter
	Tags     []string // optional tag filter (any match)
	MinScore float64  // minimum combined score threshold
	FilePath string   // optional: filter memories touching this file path
}

// MemoryListOpts configures a memory list query.
type MemoryListOpts struct {
	Type   string
	Tag    string
	Limit  int
	Offset int
}

// MemoryConsolidateOpts configures memory consolidation.
type MemoryConsolidateOpts struct {
	SimilarityThreshold float64
	MinDecayedScore     float64
	DryRun              bool
}

// ScoredMemory is a Memory with an associated relevance score.
type ScoredMemory struct {
	Memory
	Score float64
}
