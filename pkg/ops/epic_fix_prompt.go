package ops

import "fmt"

// buildEpicFixPrompt creates a prompt for the epic acceptance-failure diagnostic
// agent. The agent reads the failed test output, identifies the root cause, and
// creates fix beads under the epic using `bd create --parent <epicID>`.
func buildEpicFixPrompt(opts EpicFixOpts) string {
	return fmt.Sprintf(`You are a diagnostic agent. The epic %q passed all child bead quality gates but its acceptance test failed.

## Epic acceptance criteria

%s

## Failed command

%s

## Test output

%s

## Your task

1. Read the acceptance test output above and identify the root cause of the failure.
2. Create one or more fix beads under the epic using:
   bd create --title="Fix: <short description>" --type=task --priority=1 --parent=%s --description="<root cause and what needs to change>"
3. Each fix bead must have a Cmd:/Assert: acceptance criteria so the fix is machine-verifiable.

Focus on integration failures: components exist but are not wired, prompt fields unused, wrong data flow, etc.
Do not re-decompose the epic. Only create targeted fix beads for the specific failure above.`,
		opts.EpicID,
		opts.AC,
		opts.Cmd,
		opts.Output,
		opts.EpicID,
	)
}
