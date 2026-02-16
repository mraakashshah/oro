package protocol

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
