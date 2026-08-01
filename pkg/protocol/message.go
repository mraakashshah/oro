// Package protocol defines UDS message types for the Oro dispatcher-worker protocol.
// Messages are line-delimited JSON over Unix domain sockets.
package protocol

import (
	"fmt"
	"regexp"
	"time"

	"oro/pkg/cards"
	"oro/pkg/reviewcontract"
)

// MaxMessageSize is the maximum size in bytes for a single UDS message.
// Scanner buffers are configured to accept up to this size.
const MaxMessageSize = 1 * 1024 * 1024 // 1 MB

// MaxBuildIDLength bounds immutable build identities carried by protocol
// messages. Build IDs are diagnostic identity, not an unbounded log field.
const MaxBuildIDLength = 256

// MessageType identifies the kind of UDS message.
type MessageType string

// Range is an inclusive supported protocol version range.
type Range struct {
	Min uint64 `json:"min"`
	Max uint64 `json:"max"`
}

// Validate rejects empty or inverted protocol ranges.
func (r Range) Validate() error {
	if r.Min == 0 {
		return fmt.Errorf("protocol range minimum cannot be zero")
	}
	if r.Max == 0 {
		return fmt.Errorf("protocol range maximum cannot be zero")
	}
	if r.Min > r.Max {
		return fmt.Errorf("protocol range minimum %d exceeds maximum %d", r.Min, r.Max)
	}
	return nil
}

func (r Range) overlaps(other Range) bool {
	return r.Min <= other.Max && other.Min <= r.Max
}

// Identity is the immutable dispatcher expectation for a worker.
type Identity struct {
	ProjectID         string
	WorkerID          string
	WorkerGeneration  uint64
	RestartGeneration uint64
	BuildID           string
}

func (i Identity) validate(allowUnassignedRestart bool) error {
	if i.ProjectID == "" {
		return fmt.Errorf("project ID cannot be empty")
	}
	if i.WorkerID == "" {
		return fmt.Errorf("worker ID cannot be empty")
	}
	if i.WorkerGeneration == 0 {
		return fmt.Errorf("worker generation cannot be zero")
	}
	if i.RestartGeneration == 0 && !allowUnassignedRestart {
		return fmt.Errorf("restart generation cannot be zero")
	}
	if i.BuildID == "" {
		return fmt.Errorf("build ID cannot be empty")
	}
	if len(i.BuildID) > MaxBuildIDLength {
		return fmt.Errorf("build ID exceeds %d bytes", MaxBuildIDLength)
	}
	return nil
}

// Metadata versions every control message after HELLO negotiation and
// binds it to one project, worker, restart, and build identity.
type Metadata struct {
	ProtocolRange     Range  `json:"protocol_range"`
	ProjectID         string `json:"project_id"`
	WorkerID          string `json:"worker_id"`
	WorkerGeneration  uint64 `json:"worker_generation"`
	RestartGeneration uint64 `json:"restart_generation"`
	BuildID           string `json:"build_id"`
}

func (m Metadata) identity() Identity {
	return Identity{
		ProjectID:         m.ProjectID,
		WorkerID:          m.WorkerID,
		WorkerGeneration:  m.WorkerGeneration,
		RestartGeneration: m.RestartGeneration,
		BuildID:           m.BuildID,
	}
}

// Hello starts version negotiation before worker registration.
type Hello struct {
	ProtocolRange     Range  `json:"protocol_range"`
	ProjectID         string `json:"project_id"`
	RestartGeneration uint64 `json:"restart_generation"`
	BuildID           string `json:"build_id"`
}

// HelloACK confirms the negotiated version.
type HelloACK struct {
	ProtocolVersion uint64 `json:"protocol_version"`
}

// CandidateReady transfers a committed candidate to dispatcher ownership.
type CandidateReady struct {
	ProjectID     string `json:"project_id"`
	AssignmentID  string `json:"assignment_id"`
	CandidateSHA  string `json:"candidate_sha"`
	AdoptionToken string `json:"adoption_token"`
}

// Dispatcher -> Worker message types.
const (
	MsgAssign            MessageType = "ASSIGN"
	MsgShutdown          MessageType = "SHUTDOWN"
	MsgPrepareShutdown   MessageType = "PREPARE_SHUTDOWN"
	MsgPreempt           MessageType = "PREEMPT"
	MsgACK               MessageType = "ACK"
	MsgCapabilityRefresh MessageType = "CAPABILITY_REFRESH"
	MsgReviewResult      MessageType = "REVIEW_RESULT"
	MsgHello             MessageType = "HELLO"
	MsgHelloACK          MessageType = "HELLO_ACK"
	MsgCorrection        MessageType = "CORRECTION"
)

// Worker -> Dispatcher message types.
const (
	MsgHeartbeat            MessageType = "HEARTBEAT"
	MsgStatus               MessageType = "STATUS"
	MsgHandoff              MessageType = "HANDOFF"
	MsgDone                 MessageType = "DONE"
	MsgReadyForReview       MessageType = "READY_FOR_REVIEW"
	MsgReconnect            MessageType = "RECONNECT"
	MsgShutdownApproved     MessageType = "SHUTDOWN_APPROVED"
	MsgCheckpointAck        MessageType = "CHECKPOINT_ACK"
	MsgCapabilityRefreshACK MessageType = "CAPABILITY_REFRESH_ACK"
	MsgCandidateReady       MessageType = "CANDIDATE_READY"
)

// Manager -> Dispatcher message types.
const (
	MsgDirective MessageType = "DIRECTIVE"
)

// Semantic memory message types.
const (
	MsgEmbedRequest  MessageType = "EMBED_REQUEST"
	MsgEmbedResponse MessageType = "EMBED_RESPONSE"

	MsgRerankByIDsRequest  MessageType = "RERANK_BY_IDS_REQUEST"
	MsgRerankByIDsResponse MessageType = "RERANK_BY_IDS_RESPONSE"
)

// Work intake message types use a short-lived connection. They intentionally
// do not participate in the worker registration lifecycle.
const (
	MsgEvidenceRequest      MessageType = "EVIDENCE_REQUEST"
	MsgEvidenceResponse     MessageType = "EVIDENCE_RESPONSE"
	MsgWorkProposalRequest  MessageType = "WORK_PROPOSAL_REQUEST"
	MsgWorkProposalResponse MessageType = "WORK_PROPOSAL_RESPONSE"
)

// Message is the envelope for all UDS messages. The Type field selects which
// payload pointer is populated; unused payloads are nil and omitted from JSON.
type Message struct {
	Type                 MessageType                  `json:"type"`
	Protocol             Metadata                     `json:"protocol"`
	Hello                *Hello                       `json:"hello,omitempty"`
	HelloACK             *HelloACK                    `json:"hello_ack,omitempty"`
	Assign               *AssignPayload               `json:"assign,omitempty"`
	Heartbeat            *HeartbeatPayload            `json:"heartbeat,omitempty"`
	Status               *StatusPayload               `json:"status,omitempty"`
	Handoff              *HandoffPayload              `json:"handoff,omitempty"`
	Done                 *DonePayload                 `json:"done,omitempty"`
	ReadyForReview       *ReadyForReviewPayload       `json:"ready_for_review,omitempty"`
	Reconnect            *ReconnectPayload            `json:"reconnect,omitempty"`
	PrepareShutdown      *PrepareShutdownPayload      `json:"prepare_shutdown,omitempty"`
	ShutdownApproved     *ShutdownApprovedPayload     `json:"shutdown_approved,omitempty"`
	Directive            *DirectivePayload            `json:"directive,omitempty"`
	ACK                  *ACKPayload                  `json:"ack,omitempty"`
	CapabilityRefresh    *CapabilityRefreshPayload    `json:"capability_refresh,omitempty"`
	CapabilityRefreshACK *CapabilityRefreshACKPayload `json:"capability_refresh_ack,omitempty"`
	ReviewResult         *ReviewResultPayload         `json:"review_result,omitempty"`
	Embed                *EmbedRequest                `json:"embed,omitempty"`
	EmbedResp            *EmbedResponse               `json:"embed_response,omitempty"`
	RerankReq            *RerankByIDsRequest          `json:"rerank_req,omitempty"`
	RerankResp           *RerankByIDsResponse         `json:"rerank_resp,omitempty"`
	CheckpointAck        *CheckpointAckPayload        `json:"checkpoint_ack,omitempty"`
	EvidenceRequest      *EvidenceRequest             `json:"evidence_request,omitempty"`
	EvidenceResponse     *EvidenceResponse            `json:"evidence_response,omitempty"`
	WorkProposalRequest  *WorkProposalRequest         `json:"work_proposal_request,omitempty"`
	WorkProposalResponse *WorkProposalResponse        `json:"work_proposal_response,omitempty"`
	Candidate            *CandidateReady              `json:"candidate,omitempty"`
}

// Validate rejects stale, cross-project, oversized, or incompatible control
// messages before callers register a worker or mutate dispatcher state.
func (m Message) Validate(supported Range, expected Identity) error {
	if !m.requiresProtocolValidation() {
		return nil
	}
	if err := m.validateProtocolEnvelope(supported, expected); err != nil {
		return err
	}
	return m.validateProtocolPayload(supported)
}

func (m Message) validateProtocolEnvelope(supported Range, expected Identity) error {
	if err := supported.Validate(); err != nil {
		return fmt.Errorf("supported protocol range: %w", err)
	}
	if err := expected.validate(m.Type == MsgHello); err != nil {
		return fmt.Errorf("expected protocol identity: %w", err)
	}
	if err := m.Protocol.ProtocolRange.Validate(); err != nil {
		return fmt.Errorf("message protocol range: %w", err)
	}
	if !m.Protocol.ProtocolRange.overlaps(supported) {
		return fmt.Errorf("unsupported protocol range %d-%d", m.Protocol.ProtocolRange.Min, m.Protocol.ProtocolRange.Max)
	}
	if got := m.Protocol.identity(); got != expected {
		return fmt.Errorf("protocol identity does not match active worker")
	}
	return nil
}

func (m Message) validateProtocolPayload(supported Range) error {
	switch m.Type {
	case MsgHello:
		return m.validateHello()
	case MsgHelloACK:
		return m.validateHelloACK(supported)
	case MsgCandidateReady:
		return m.validateCandidate()
	}
	return nil
}

func (m Message) validateHello() error {
	if m.Hello == nil {
		return fmt.Errorf("HELLO payload is required")
	}
	if err := m.Hello.ProtocolRange.Validate(); err != nil {
		return fmt.Errorf("HELLO protocol range: %w", err)
	}
	if m.Hello.ProtocolRange != m.Protocol.ProtocolRange ||
		m.Hello.ProjectID != m.Protocol.ProjectID ||
		m.Hello.RestartGeneration != m.Protocol.RestartGeneration ||
		m.Hello.BuildID != m.Protocol.BuildID {
		return fmt.Errorf("HELLO payload does not match protocol metadata")
	}
	return nil
}

func (m Message) validateHelloACK(supported Range) error {
	if m.HelloACK == nil {
		return fmt.Errorf("HELLO_ACK payload is required")
	}
	if m.HelloACK.ProtocolVersion < supported.Min || m.HelloACK.ProtocolVersion > supported.Max ||
		m.HelloACK.ProtocolVersion < m.Protocol.ProtocolRange.Min || m.HelloACK.ProtocolVersion > m.Protocol.ProtocolRange.Max {
		return fmt.Errorf("HELLO_ACK protocol version %d is not negotiated", m.HelloACK.ProtocolVersion)
	}
	return nil
}

func (m Message) validateCandidate() error {
	if m.Candidate == nil {
		return fmt.Errorf("CANDIDATE_READY payload is required")
	}
	if m.Candidate.ProjectID != m.Protocol.ProjectID || m.Candidate.AssignmentID == "" ||
		m.Candidate.CandidateSHA == "" || m.Candidate.AdoptionToken == "" {
		return fmt.Errorf("CANDIDATE_READY payload is invalid")
	}
	return nil
}

func (m Message) requiresProtocolValidation() bool {
	switch m.Type {
	case MsgHello, MsgHelloACK, MsgAssign, MsgHeartbeat, MsgCandidateReady,
		MsgReadyForReview, MsgReviewResult, MsgDone, MsgShutdown, MsgCorrection:
		return true
	default:
		return false
	}
}

// AssignPayload is sent by the dispatcher to assign a bead to a worker.
// MemoryContext contains formatted memories from previous sessions for this bead,
// generated by memory.ForPrompt() and injected by the dispatcher on reassignment.
// CodeSearchContext contains formatted code search results from FTS5Search,
// injected by the dispatcher based on the bead title.
// CodeStructureContext contains formatted nav-maps (file outlines + line ranges)
// produced by codestruct, injected by the dispatcher for the relevant files.
// ProjectRoot is the path to the project root for loading .oro/config.yaml.
// TargetBranch is the branch that work will merge to; defaults to "main" if empty.
// GitLog contains the git log context for the bead (omitted when empty).
// WorkerProgram contains the worker program invocation string (omitted when empty).
type AssignPayload struct {
	BeadID               string              `json:"bead_id"`
	Worktree             string              `json:"worktree"`
	AssignmentID         int64               `json:"assignment_id"`
	Generation           int64               `json:"generation"`
	ActorRole            string              `json:"actor_role"`
	Project              string              `json:"project"`
	Capability           string              `json:"capability"`
	Runtime              string              `json:"runtime,omitempty"`
	Model                string              `json:"model,omitempty"`
	Reasoning            string              `json:"reasoning,omitempty"`
	Tier                 Tier                `json:"tier,omitempty"`
	MemoryContext        string              `json:"memory_context,omitempty"`
	Cards                cards.RelevantCards `json:"cards,omitempty"`
	CodeSearchContext    string              `json:"code_search_context,omitempty"`
	CodeStructureContext string              `json:"code_structure_context,omitempty"`
	Feedback             string              `json:"feedback,omitempty"`
	Title                string              `json:"title,omitempty"`
	Description          string              `json:"description,omitempty"`
	AcceptanceCriteria   string              `json:"acceptance_criteria,omitempty"`
	Attempt              int                 `json:"attempt,omitempty"`
	IsEpicDecomposition  bool                `json:"is_epic_decomposition,omitempty"`
	ProjectRoot          string              `json:"project_root,omitempty"`
	TargetBranch         string              `json:"target_branch,omitempty"`
	GitLog               string              `json:"git_log,omitempty"`
	WorkerProgram        string              `json:"worker_program,omitempty"`
	ReviewRecovery       *ReviewRecovery     `json:"review_recovery,omitempty"`
}

// ReviewRecoveryArtifactRef identifies the lossless artifact used when findings exceed the wire budget.
//
//oro:testonly
type ReviewRecoveryArtifactRef struct {
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	FindingCount int    `json:"finding_count"`
}

// ReviewRecovery carries rejected-review correction context to a replacement worker.
//
//oro:testonly
type ReviewRecovery struct {
	CheckpointID    int64                      `json:"checkpoint_id"`
	RejectedHeadSHA string                     `json:"rejected_head_sha"`
	Findings        []reviewcontract.Finding   `json:"findings,omitempty"`
	FindingsRef     *ReviewRecoveryArtifactRef `json:"findings_ref,omitempty"`
	Attempt         int                        `json:"attempt"`
	AcceptanceHash  string                     `json:"acceptance_hash"`
}

// Validate checks that the AssignPayload has required fields populated.
// Returns an error if BeadID or Worktree is empty.
func (a *AssignPayload) Validate() error {
	if a.BeadID == "" {
		return fmt.Errorf("bead ID cannot be empty")
	}
	if a.Worktree == "" {
		return fmt.Errorf("worktree cannot be empty")
	}
	return nil
}

// HeartbeatPayload is sent by a worker to report liveness and context usage.
type HeartbeatPayload struct {
	BeadID     string `json:"bead_id"`
	WorkerID   string `json:"worker_id"`
	ContextPct int    `json:"context_pct"`
}

// StatusPayload is sent by a worker to report state transitions.
type StatusPayload struct {
	BeadID   string `json:"bead_id"`
	WorkerID string `json:"worker_id"`
	State    string `json:"state"`
	Result   string `json:"result"`
}

// Summary holds a structured session summary populated by the worker before
// handoff or completion. Persisted as type=summary in the memories table for
// cross-session bead continuity.
type Summary struct {
	Request      string `json:"request"`
	Investigated string `json:"investigated"`
	Learned      string `json:"learned"`
	Completed    string `json:"completed"`
	NextSteps    string `json:"next_steps"`
}

// FormatContent formats the Summary as a pipe-delimited content string
// suitable for storage in the memories table.
func (s *Summary) FormatContent() string {
	return fmt.Sprintf("request: %s | investigated: %s | learned: %s | completed: %s | next_steps: %s",
		s.Request, s.Investigated, s.Learned, s.Completed, s.NextSteps)
}

// HandoffPayload is sent by a worker when it hands off to another worker.
// Includes typed context fields that the worker populates from .oro/ files
// before sending, enabling cross-session memory persistence.
type HandoffPayload struct {
	BeadID         string   `json:"bead_id"`
	WorkerID       string   `json:"worker_id"`
	Learnings      []string `json:"learnings,omitempty"`
	Decisions      []string `json:"decisions,omitempty"`
	FilesModified  []string `json:"files_modified,omitempty"`
	ContextSummary string   `json:"context_summary,omitempty"`
	Summary        *Summary `json:"summary,omitempty"`
}

// DonePayload is sent by a worker when it completes its bead.
type DonePayload struct {
	BeadID            string                 `json:"bead_id"`
	WorkerID          string                 `json:"worker_id"`
	QualityGatePassed bool                   `json:"quality_gate_passed"`
	QGOutput          string                 `json:"qg_output,omitempty"`
	FailureReason     string                 `json:"failure_reason,omitempty"`
	SubprocessExit    *SubprocessExitPayload `json:"subprocess_exit,omitempty"`
}

// SubprocessExitPayload captures forensic evidence when a worker runtime exits
// before completing the assigned bead.
type SubprocessExitPayload struct {
	Runtime    string `json:"runtime,omitempty"`
	Model      string `json:"model,omitempty"`
	ExitCode   int    `json:"exit_code"`
	ExitError  string `json:"exit_error,omitempty"`
	StderrTail string `json:"stderr_tail,omitempty"`
}

// CheckpointAckPayload is sent by a worker to acknowledge a checkpoint_requested signal.
// The CheckpointID must match the one in the corresponding checkpoint_requested event.
type CheckpointAckPayload struct {
	BeadID        string `json:"bead_id"`
	CheckpointID  string `json:"checkpoint_id"`
	CommittedSHA  string `json:"committed_sha,omitempty"`
	IntentSummary string `json:"intent_summary,omitempty"`
}

// ReadyForReviewPayload is sent by a worker when its bead is ready for review.
type ReadyForReviewPayload struct {
	BeadID   string `json:"bead_id"`
	WorkerID string `json:"worker_id"`
}

// ReviewResultPayload is sent by the dispatcher to a worker after a review
// completes. Verdict is "approved" or "rejected". On rejection the dispatcher
// typically re-assigns via MsgAssign instead, so this message is primarily
// used for the approval path.
type ReviewResultPayload struct {
	Verdict  string `json:"verdict"`
	Feedback string `json:"feedback,omitempty"`
}

// PrepareShutdownPayload is sent by the dispatcher to request a graceful shutdown.
// The worker should save context (send MsgHandoff with learnings/decisions),
// then reply with MsgShutdownApproved before the timeout expires.
type PrepareShutdownPayload struct {
	Timeout time.Duration `json:"timeout"`
}

// ShutdownApprovedPayload is sent by a worker after it has saved context
// in response to a PrepareShutdown request.
type ShutdownApprovedPayload struct {
	WorkerID string `json:"worker_id"`
}

// ReconnectPayload is sent by a worker reconnecting after a disconnect.
type ReconnectPayload struct {
	WorkerID       string    `json:"worker_id"`
	BeadID         string    `json:"bead_id"`
	State          string    `json:"state"`
	ContextPct     int       `json:"context_pct"`
	BufferedEvents []Message `json:"buffered_events"`
}

// maxBufferedEvents is the maximum number of buffered events allowed in a
// ReconnectPayload. This prevents unbounded memory usage during reconnection.
const maxBufferedEvents = 100

// Validate checks that the ReconnectPayload is within acceptable limits.
// Returns an error if BufferedEvents exceeds maxBufferedEvents.
func (r *ReconnectPayload) Validate() error {
	if len(r.BufferedEvents) > maxBufferedEvents {
		return fmt.Errorf("too many buffered events: %d > %d", len(r.BufferedEvents), maxBufferedEvents)
	}
	return nil
}

// DirectivePayload is sent by the manager to issue directives to the dispatcher.
type DirectivePayload struct {
	Op     string `json:"op"`               // start | stop | pause | focus
	Args   string `json:"args"`             // optional arguments (e.g., epic ID for focus)
	Source string `json:"source,omitempty"` // actor that issued the directive
	Reason string `json:"reason,omitempty"` // operator or policy rationale
}

// ACKPayload is sent by the dispatcher in response to a directive.
type ACKPayload struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// CapabilityRefreshPayload delivers a replacement assignment credential without
// restarting the worker subprocess. The bearer is intentionally transient.
type CapabilityRefreshPayload struct {
	AssignmentID int64     `json:"assignment_id"`
	Generation   int64     `json:"generation"`
	CapabilityID string    `json:"capability_id"`
	Capability   string    `json:"capability"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// CapabilityRefreshACKPayload confirms the worker atomically installed a
// replacement credential.
type CapabilityRefreshACKPayload struct {
	AssignmentID int64  `json:"assignment_id"`
	CapabilityID string `json:"capability_id"`
}

// beadIDPattern validates bead IDs for path safety. Matches IDs like "oro-1nf",
// "oro-1nf.1", "oro-dfe.3". Must start with lowercase letter or digit, followed
// by 0-61 chars of lowercase letters, digits, dots, hyphens, or underscores,
// and must end with lowercase letter or digit.
var beadIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,61}[a-z0-9]$`)

// ValidateBeadID validates a bead ID for path safety to prevent directory
// traversal attacks. Returns an error if the ID contains path traversal
// sequences (../, /), special characters, or violates format constraints.
func ValidateBeadID(id string) error {
	if id == "" {
		return fmt.Errorf("bead ID cannot be empty")
	}

	if len(id) < 2 {
		return fmt.Errorf("bead ID must be at least 2 characters")
	}

	if len(id) > 63 {
		return fmt.Errorf("bead ID must not exceed 63 characters")
	}

	if !beadIDPattern.MatchString(id) {
		return fmt.Errorf("bead ID %q is invalid: must start with lowercase letter or digit, followed by lowercase letters, digits, dots, hyphens, or underscores", id)
	}

	return nil
}
