package ops

import (
	"fmt"
	"strings"
)

// buildEpicFixPrompt creates a prompt for the epic acceptance-failure diagnostic
// agent. The agent reads the failed test output, identifies the root cause, and
// creates fix tasks under the epic using `oro task create`, then wires hierarchy.
func buildEpicFixPrompt(opts EpicFixOpts) string {
	tierFlag := ""
	if opts.Tier != "" {
		tierFlag = " --tier=" + opts.Tier
	}
	createCmd := strings.TrimRight(
		fmt.Sprintf(`oro task create --title="Fix: <short description>" --type=task --priority=1%s --description="<root cause and what needs to change>" --acceptance="Test: <file>:<Fn> | Cmd: <cmd> | Assert: <expected>"`, tierFlag),
		" ",
	)
	return fmt.Sprintf(`You are a diagnostic agent. The epic %q passed all child task quality gates but its acceptance test failed.

## Epic acceptance criteria

%s

## Failed command

%s

## Test output

%s

## Your task

1. Read the acceptance test output above and identify the root cause of the failure.
2. Create one or more fix tasks for the epic using:
   %s
3. Wire each fix task under the epic:
   oro task update <fix-task-id> --parent=%s
   oro task dep add %s <fix-task-id>
4. Each fix task must have a Cmd:/Assert: acceptance criteria so the fix is machine-verifiable.

Focus on integration failures: components exist but are not wired, prompt fields unused, wrong data flow, etc.
Do not re-decompose the epic. Only create targeted fix tasks for the specific failure above.`,
		opts.EpicID,
		opts.AC,
		opts.Cmd,
		opts.Output,
		createCmd,
		opts.EpicID,
		opts.EpicID,
	)
}
