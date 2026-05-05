package worker

import "encoding/json"

// ContextPctFromLine parses context% from a single subprocess output line
// using the appropriate parser for the given stream format.
// Returns (pct, true) on success, (100.0, false) when no signal is present.
func ContextPctFromLine(format StreamFormat, line []byte) (float64, bool) {
	switch format {
	case StreamFormatClaudeJSON:
		return ParseClaudeContextPct(line)
	case StreamFormatGeminiJSON:
		return ParseGeminiContextPct(line)
	default:
		// StreamFormatLineText and future line-text runtimes use the Codex parser.
		return ParseCodexContextPct(line)
	}
}

// ParseClaudeContextPct extracts context_pct from a single JSON line emitted
// by the Claude Code worker subprocess ({"event":"turn_end","context_pct":68}).
// Returns (pct, true) on success, (100.0, false) when no signal is present.
func ParseClaudeContextPct(line []byte) (float64, bool) {
	if len(line) == 0 {
		return 100.0, false
	}
	var event struct {
		Event      string   `json:"event"`
		ContextPct *float64 `json:"context_pct"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return 100.0, false
	}
	if event.Event != "turn_end" || event.ContextPct == nil {
		return 100.0, false
	}
	return *event.ContextPct, true
}

// ParseCodexContextPct extracts context_pct from a JSON line emitted by the
// Codex CLI worker subprocess ({"context_pct":55}).
// Returns (pct, true) on success, (100.0, false) when no signal is present.
func ParseCodexContextPct(line []byte) (float64, bool) {
	if len(line) == 0 {
		return 100.0, false
	}
	var event struct {
		ContextPct *float64 `json:"context_pct"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return 100.0, false
	}
	if event.ContextPct == nil {
		return 100.0, false
	}
	return *event.ContextPct, true
}

// ParseGeminiContextPct extracts context_pct from a JSON line emitted by the
// Gemini CLI worker subprocess ({"context_pct":42}).
// Returns (pct, true) on success, (100.0, false) when no signal is present.
func ParseGeminiContextPct(line []byte) (float64, bool) {
	if len(line) == 0 {
		return 100.0, false
	}
	var event struct {
		ContextPct *float64 `json:"context_pct"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return 100.0, false
	}
	if event.ContextPct == nil {
		return 100.0, false
	}
	return *event.ContextPct, true
}
