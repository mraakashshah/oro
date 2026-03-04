package worker

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ActivityKind classifies a parsed stream-json event.
type ActivityKind int

const (
	// ActivityUnknown is the default kind for unparseable or unrecognised events.
	ActivityUnknown ActivityKind = iota
	// ActivityToolUse indicates a tool_use content block.
	ActivityToolUse
	// ActivityTextDelta indicates a text content block.
	ActivityTextDelta
	// ActivityResult indicates a final result event.
	ActivityResult
)

// Activity is the parsed representation of one NDJSON line from claude's
// --output-format stream-json output.
type Activity struct {
	Kind    ActivityKind
	Tool    string // tool name (ToolUse only)
	Text    string // text content (TextDelta, Result)
	IsError bool   // true when result indicates error

	// Result metadata (populated only for ActivityResult).
	NumTurns          int
	StopReason        string
	DurationMs        int
	CostUSD           float64
	PermissionDenials []string
	ResultSubtype     string // "success", "error_max_turns", etc.
}

// ParseStreamEvent parses a single NDJSON line from claude's stream-json
// output into an Activity. Returns ActivityUnknown for unrecognised or
// malformed lines.
func ParseStreamEvent(line []byte) Activity {
	if len(line) == 0 {
		return Activity{Kind: ActivityUnknown}
	}

	var top struct {
		Type    string          `json:"type"`
		Subtype string          `json:"subtype"`
		Message json.RawMessage `json:"message"`
		Result  string          `json:"result"`
		IsError bool            `json:"is_error"`

		// Result metadata.
		NumTurns          int      `json:"num_turns"`
		StopReason        string   `json:"stop_reason"`
		DurationMs        int      `json:"duration_ms"`
		CostUSD           float64  `json:"total_cost_usd"`
		PermissionDenials []string `json:"permission_denials"`
	}
	if err := json.Unmarshal(line, &top); err != nil {
		return Activity{Kind: ActivityUnknown}
	}

	switch top.Type {
	case "result":
		return Activity{
			Kind:              ActivityResult,
			Text:              top.Result,
			IsError:           top.IsError,
			NumTurns:          top.NumTurns,
			StopReason:        top.StopReason,
			DurationMs:        top.DurationMs,
			CostUSD:           top.CostUSD,
			PermissionDenials: top.PermissionDenials,
			ResultSubtype:     top.Subtype,
		}
	case "assistant":
		return parseAssistantContent(top.Message)
	default:
		return Activity{Kind: ActivityUnknown}
	}
}

// parseAssistantContent drills into an assistant message's content blocks.
// ToolUse is prioritised over text (a message may contain both).
func parseAssistantContent(raw json.RawMessage) Activity {
	if len(raw) == 0 {
		return Activity{Kind: ActivityUnknown}
	}

	var msg struct {
		Content []struct {
			Type string `json:"type"`
			Name string `json:"name"` // tool_use
			Text string `json:"text"` // text
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return Activity{Kind: ActivityUnknown}
	}

	// First pass: look for tool_use (higher signal for observability).
	for _, b := range msg.Content {
		if b.Type == "tool_use" {
			return Activity{Kind: ActivityToolUse, Tool: b.Name}
		}
	}
	// Second pass: look for text.
	for _, b := range msg.Content {
		if b.Type == "text" {
			return Activity{Kind: ActivityTextDelta, Text: b.Text}
		}
	}
	return Activity{Kind: ActivityUnknown}
}

// FormatActivity returns a human-readable string for tool-use activities
// ("-> Read", "-> Bash"). Text deltas and other kinds return empty —
// they're too noisy for activity logs.
func FormatActivity(a Activity) string {
	if a.Kind == ActivityToolUse {
		return "-> " + a.Tool
	}
	return ""
}

// FormatResult returns a human-readable summary of a result event,
// including turns, duration, cost, and permission denials.
func FormatResult(a Activity) string {
	if a.Kind != ActivityResult {
		return ""
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("--- Result: %s", a.ResultSubtype))
	if a.NumTurns > 0 {
		parts = append(parts, fmt.Sprintf("    turns=%d duration=%dms cost=$%.4f", a.NumTurns, a.DurationMs, a.CostUSD))
	}
	if a.StopReason != "" {
		parts = append(parts, fmt.Sprintf("    stop_reason=%s", a.StopReason))
	}
	if len(a.PermissionDenials) > 0 {
		parts = append(parts, fmt.Sprintf("    PERMISSION DENIALS: %s", strings.Join(a.PermissionDenials, ", ")))
	}
	if a.IsError {
		parts = append(parts, fmt.Sprintf("    ERROR: %s", a.Text))
	}
	return strings.Join(parts, "\n")
}
