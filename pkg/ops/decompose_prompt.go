package ops

import (
	"fmt"
	"strings"
)

// buildDecomposePrompt assembles the task decomposition agent prompt from the
// task ID and quality gate output that triggered the decomposition request.
func buildDecomposePrompt(opts DecomposeOpts) string {
	var b strings.Builder

	b.WriteString("You are a task decomposition agent. A task has exhausted all worker retry attempts.\n\n")
	b.WriteString("CRITICAL: Do NOT use TaskOutput or run tasks in the background.\n")
	b.WriteString("Use the Read tool to check output files. Run all commands in foreground.\n\n")

	fmt.Fprintf(&b, "## Task\n")
	fmt.Fprintf(&b, "Task ID: %s\n\n", opts.BeadID)

	if opts.QGOutput != "" {
		b.WriteString("## Quality Gate Output\n")
		b.WriteString(opts.QGOutput)
		b.WriteString("\n\n")
	}

	b.WriteString("## Steps\n")
	fmt.Fprintf(&b, "1. Run `oro task show %s` to read the full task details and acceptance criteria.\n", opts.BeadID)
	b.WriteString("2. Analyze why the task is too large or ambiguous.\n")
	b.WriteString("3. Create 2-4 smaller child tasks:\n")
	b.WriteString("   For each child task:\n")
	fmt.Fprintf(&b, "   a. `oro task create --title=\"...\" --type=task --parent=%s --acceptance=\"...\" --estimate=<min>`\n", opts.BeadID)
	fmt.Fprintf(&b, "      (`--parent` sets hierarchy only, no dep)\n")
	fmt.Fprintf(&b, "   b. `oro task dep add %s <child-id>`  (epic depends on child — correct direction)\n", opts.BeadID)
	fmt.Fprintf(&b, "4. Convert parent to epic: `oro task update %s --type=epic`\n", opts.BeadID)
	b.WriteString("5. If all steps succeed, print exactly:\n")
	b.WriteString("   VERDICT: resolved\n\n")
	b.WriteString("   If unable to decompose, print:\n")
	b.WriteString("   VERDICT: failed: <one-line reason>\n\n")

	b.WriteString("## Constraint\n")
	b.WriteString("Do not write code. Only create tasks and update task type.\n")

	return b.String()
}

// parseDecomposeOutput extracts the VERDICT from decomposition agent output.
func parseDecomposeOutput(stdout string) (verdict Verdict, feedback string) {
	upper := strings.ToUpper(stdout)
	if strings.Contains(upper, "VERDICT: RESOLVED") {
		return VerdictResolved, extractFeedback(stdout, "VERDICT: RESOLVED")
	}
	if strings.Contains(upper, "VERDICT: FAILED") {
		return VerdictFailed, extractFeedback(stdout, "VERDICT: FAILED")
	}
	return VerdictFailed, "no verdict in output"
}
