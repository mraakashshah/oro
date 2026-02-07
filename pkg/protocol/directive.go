package protocol

// Directive represents a manager-issued instruction to the dispatcher.
type Directive string

const (
	DirectiveStart Directive = "start" // Begin pulling and assigning ready work.
	DirectiveStop  Directive = "stop"  // Finish current work, don't assign new beads.
	DirectivePause Directive = "pause" // Hold new assignments, workers keep running.
	DirectiveFocus Directive = "focus" // Prioritize beads from a specific epic.
)

// Valid reports whether d is one of the four known directive values.
func (d Directive) Valid() bool {
	switch d {
	case DirectiveStart, DirectiveStop, DirectivePause, DirectiveFocus:
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
