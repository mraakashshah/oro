package ops

import (
	"fmt"
	"strings"
)

// buildWriteACPrompt assembles the prompt for an acceptance-criteria writing agent.
func buildWriteACPrompt(opts WriteACOpts) string {
	var b strings.Builder
	b.WriteString("CRITICAL: Do NOT use TaskOutput or run tasks in the background.\n")
	b.WriteString("Use the Read tool to check output files. Run all commands in foreground.\n\n")
	fmt.Fprintf(&b, "Write acceptance criteria for bead %s: %s\n", opts.BeadID, opts.BeadTitle)
	if opts.BeadDescription != "" {
		fmt.Fprintf(&b, "Description: %s\n", opts.BeadDescription)
	}
	b.WriteString("\nWrite clear, testable acceptance criteria that specify exact pass/fail conditions.\n")
	b.WriteString("Update the bead using: bd update " + opts.BeadID + " --acceptance-criteria=\"...\"\n")
	return b.String()
}
