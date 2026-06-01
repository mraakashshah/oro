package ops

import "strings"

// buildDreamPrompt assembles the memory-consolidation agent prompt.
// The agent reviews the provided memories and distills any new insights.
func buildDreamPrompt(opts DreamOpts) string {
	var b strings.Builder

	b.WriteString("You are a memory consolidation agent. Your job is to review the memories below,\n")
	b.WriteString("identify patterns, contradictions, and insights, and emit a distilled summary.\n\n")

	if opts.Memories != "" {
		b.WriteString("## Memories\n")
		b.WriteString(opts.Memories)
		b.WriteString("\n\n")
	} else {
		b.WriteString("## Memories\n")
		b.WriteString("(none)\n\n")
	}

	if len(opts.ActiveBiasTags) > 0 {
		b.WriteString("## Calibration\n")
		b.WriteString("active_bias_tags: ")
		b.WriteString(strings.Join(opts.ActiveBiasTags, ", "))
		b.WriteString("\nCounter these historical proposal biases when drafting new card actions.\n\n")
	}

	b.WriteString("## Output\n")
	b.WriteString("List any new insights or patterns as concise bullet points.\n")
	b.WriteString("If there is nothing to consolidate, output: (no new insights)\n")

	return b.String()
}
