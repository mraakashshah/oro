package ops

import (
	"fmt"
	"strings"
)

// EscalationOpts configures a one-shot escalation agent.
type EscalationOpts struct {
	EscalationType string // e.g. "STUCK_WORKER", "MERGE_CONFLICT", "PRIORITY_CONTENTION", "MISSING_AC"
	BeadID         string
	BeadTitle      string
	BeadContext    string // summary of what triggered the escalation
	RecentHistory  string // recent escalation events for context
	Workdir        string // project root for CLI access
}

// buildEscalationPrompt assembles the one-shot manager prompt from escalation
// context and the appropriate playbook for the escalation type.
//
// Design decision: Hybrid ack+state verification (Option C from oro-pi5l).
// The one-shot must output ACK: <action taken> so the dispatcher can confirm
// the escalation was handled. The dispatcher also re-checks the triggering
// condition after the one-shot exits.
func buildEscalationPrompt(opts EscalationOpts) string {
	var b strings.Builder

	writeEscalationHeader(&b, opts)
	writeEscalationContext(&b, opts)
	writePlaybook(&b, opts.EscalationType)
	writeAvailableCLI(&b)
	writeEscalationOutput(&b)

	return b.String()
}

func writeEscalationHeader(b *strings.Builder, opts EscalationOpts) {
	b.WriteString("You are a one-shot manager agent handling an escalation from the oro dispatcher.\n")
	fmt.Fprintf(b, "Escalation type: %s\n", opts.EscalationType)
	b.WriteString("Your job: diagnose the situation, take corrective action, and report what you did.\n\n")
}

func writeEscalationContext(b *strings.Builder, opts EscalationOpts) {
	b.WriteString("## Context\n")
	fmt.Fprintf(b, "Bead: %s", opts.BeadID)
	if opts.BeadTitle != "" {
		fmt.Fprintf(b, " — %s", opts.BeadTitle)
	}
	b.WriteString("\n")
	if opts.BeadContext != "" {
		fmt.Fprintf(b, "Situation: %s\n", opts.BeadContext)
	}
	if opts.RecentHistory != "" {
		fmt.Fprintf(b, "Recent history: %s\n", opts.RecentHistory)
	}
	b.WriteString("\n")
}

func writePlaybook(b *strings.Builder, escalationType string) {
	b.WriteString("## Playbook\n")

	switch escalationType {
	case "STUCK_WORKER":
		b.WriteString("A worker has stalled with no progress beyond the progress timeout.\n\n")
		b.WriteString("Steps:\n")
		b.WriteString("1. Check the worker's current bead and worktree state\n")
		b.WriteString("2. Look at recent event log for the bead: bd show <bead-id>\n")
		b.WriteString("3. If the worker is genuinely stuck, restart it: oro directive restart-worker <worker-id>\n")
		b.WriteString("4. If the bead itself is problematic, add notes: bd update <bead-id> --notes \"<diagnosis>\"\n")
		b.WriteString("5. If the bead needs decomposition, escalate to persistent manager\n\n")

	case "MERGE_CONFLICT":
		b.WriteString("A merge conflict could not be automatically resolved by the ops merge agent.\n\n")
		b.WriteString("Steps:\n")
		b.WriteString("1. Inspect the conflicting files in the worktree\n")
		b.WriteString("2. Understand both sides of the conflict from bead context\n")
		b.WriteString("3. Resolve the conflict manually if straightforward\n")
		b.WriteString("4. If the conflict is semantic (not just textual), note it for the human manager\n")
		b.WriteString("5. After resolution, run tests to verify: go test ./...\n\n")

	case "PRIORITY_CONTENTION":
		b.WriteString("A P0 (critical priority) bead is queued but all workers are busy with lower-priority work.\n\n")
		b.WriteString("Steps:\n")
		b.WriteString("1. List current worker assignments: bd list --status=in_progress\n")
		b.WriteString("2. Identify the lowest-priority bead currently being worked\n")
		b.WriteString("3. Consider preempting: oro directive restart-worker <worker-id> to free a slot\n")
		b.WriteString("4. The freed worker will pick up the P0 bead on next assignment cycle\n")
		b.WriteString("5. If all work is equally critical, note the contention for the human manager\n\n")

	case "MISSING_AC":
		b.WriteString("A bead was about to be assigned but has no acceptance criteria.\n\n")
		b.WriteString("Steps:\n")
		b.WriteString("1. Read the bead details: bd show <bead-id>\n")
		b.WriteString("2. Infer acceptance criteria from the title and description\n")
		b.WriteString("3. Add acceptance criteria: bd update <bead-id> --acceptance \"<criteria>\"\n")
		b.WriteString("4. The dispatcher will retry assignment on the next cycle\n\n")

	default:
		fmt.Fprintf(b, "Unknown escalation type: %s\n", escalationType)
		b.WriteString("Investigate the situation and take appropriate corrective action.\n\n")
	}
}

func writeAvailableCLI(b *strings.Builder) {
	b.WriteString("## Available CLI Commands\n")
	b.WriteString("- `bd show <id>` — view bead details and acceptance criteria\n")
	b.WriteString("- `bd list --status=in_progress` — see active work\n")
	b.WriteString("- `bd update <id> --notes \"...\"` — add notes to a bead\n")
	b.WriteString("- `bd update <id> --acceptance \"...\"` — set acceptance criteria\n")
	b.WriteString("- `bd ready` — list beads ready for assignment\n")
	b.WriteString("- `oro directive restart-worker <worker-id>` — kill and respawn a worker\n")
	b.WriteString("- `oro directive preempt <worker-id>` — preempt a worker for higher-priority work\n\n")
}

func writeEscalationOutput(b *strings.Builder) {
	b.WriteString("## Output Format\n")
	b.WriteString("You MUST include this line in your output to confirm the escalation was handled:\n\n")
	b.WriteString("ACK: <brief description of action taken>\n\n")
	b.WriteString("If you cannot resolve the situation, output:\n\n")
	b.WriteString("ESCALATE: <reason this needs human attention>\n")
}

// parseEscalationOutput extracts the ACK or ESCALATE verdict from one-shot output.
func parseEscalationOutput(stdout string) (verdict Verdict, feedback string) {
	upper := strings.ToUpper(stdout)
	if strings.Contains(upper, "ACK:") {
		return VerdictResolved, extractFeedback(stdout, "ACK:")
	}
	if strings.Contains(upper, "ESCALATE:") {
		return VerdictFailed, extractFeedback(stdout, "ESCALATE:")
	}
	// No explicit verdict — treat the whole output as feedback.
	return VerdictFailed, stdout
}
