package agentruntime

// RuntimeID identifies an agent runtime implementation.
type RuntimeID string

const (
	// RuntimeIDClaude identifies the Claude runtime.
	RuntimeIDClaude RuntimeID = "claude"
	// RuntimeIDCodex identifies the Codex runtime.
	RuntimeIDCodex RuntimeID = "codex"
)

// InstructionLayout describes where runtime instruction files should be placed.
type InstructionLayout struct {
	Workdir    string
	ExtraPaths []string
}

// StreamFormat identifies a runtime stdout contract.
type StreamFormat string

const (
	// StreamFormatClaudeJSON is Claude's JSON event stream format.
	StreamFormatClaudeJSON StreamFormat = "claude_stream_json"
	// StreamFormatLineText is a plain line-oriented stream format.
	StreamFormatLineText StreamFormat = "line_text"
)
