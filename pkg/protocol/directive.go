package protocol

// Directive represents a manager-issued instruction to the dispatcher.
type Directive string

// Known directive values.
const (
	DirectiveStart         Directive = "start"          // Begin pulling and assigning ready work.
	DirectiveStop          Directive = "stop"           // Finish current work, don't assign new beads.
	DirectivePause         Directive = "pause"          // Hold new assignments, workers keep running.
	DirectiveResume        Directive = "resume"         // Resume from paused state.
	DirectiveScale         Directive = "scale"          // Set target worker pool size.
	DirectiveFocus         Directive = "focus"          // Prioritize beads from a specific epic.
	DirectiveStatus        Directive = "status"         // Query dispatcher state.
	DirectiveShutdown      Directive = "shutdown"       // Authorize SIGTERM and initiate graceful shutdown.
	DirectiveKillWorker    Directive = "kill-worker"    // Terminate a specific worker and return its bead to queue.
	DirectiveSpawnFor      Directive = "spawn-for"      // Spawn a worker for a specific bead.
	DirectiveRestartWorker Directive = "restart-worker" // Kill a specific worker and spawn a new one, requeue bead.
)

// Valid reports whether d is one of the known directive values.
func (d Directive) Valid() bool {
	switch d {
	case DirectiveStart, DirectiveStop, DirectivePause, DirectiveResume, DirectiveScale, DirectiveFocus, DirectiveStatus, DirectiveShutdown, DirectiveKillWorker, DirectiveSpawnFor, DirectiveRestartWorker:
		return true
	default:
		return false
	}
}

// Command represents a row in the commands SQLite table.
// The manager writes commands; the dispatcher reads and processes them.
type Command struct {
	ID        int64  `json:"id"`
	Ts        string `json:"ts"`        // ISO 8601 timestamp
	Directive string `json:"directive"` // start | stop | pause | focus
	Target    string `json:"target"`    // epic_id for focus, empty for others
	Processed bool   `json:"processed"`
}
