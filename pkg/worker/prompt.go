package worker

import (
	"fmt"
	"strings"

	"oro/pkg/protocol"
)

// PromptParams contains all inputs needed to assemble the 12-section worker prompt.
type PromptParams struct {
	BeadID             string
	Title              string
	Description        string
	AcceptanceCriteria string
	MemoryContext      string // may be empty
	CodeSearchContext  string // formatted code search results from FTS5Search
	WorktreePath       string
	Model              string
	Attempt            int    // QG retry attempt (0 = first attempt)
	Feedback           string // rejection/QG failure feedback from previous attempt
}

// section writes a markdown section (## header + body) to the builder.
func section(b *strings.Builder, header, body string) {
	fmt.Fprintf(b, "## %s\n\n%s\n\n", header, body)
}

// memoryBody returns the memory section content, falling back to a
// placeholder when no prior context is available.
func memoryBody(ctx string) string {
	if ctx == "" {
		return "No prior context for this bead."
	}
	return ctx
}

// AssemblePrompt builds the complete 12-section worker prompt from bead details
// and context. This prompt is passed to `claude -p` when spawning a worker.
func AssemblePrompt(params PromptParams) string {
	var b strings.Builder

	// 1. Role
	section(&b, "Role", "You are an oro worker. You execute one bead at a time.")

	// 2. Bead
	beadBody := fmt.Sprintf(
		"- **ID:** %s\n- **Title:** %s\n- **Description:** %s\n- **Acceptance Criteria:** %s",
		params.BeadID, params.Title, params.Description, params.AcceptanceCriteria,
	)
	if params.Attempt > 0 {
		beadBody += fmt.Sprintf("\n\n> **Retry attempt %d.** The quality gate has failed on previous attempts. Focus on fixing the issues identified in the feedback.", params.Attempt)
	}
	section(&b, "Bead", beadBody)

	// 2b. Previous Feedback (only on retries with feedback)
	if params.Attempt > 0 && params.Feedback != "" {
		section(&b, "Previous Feedback",
			fmt.Sprintf("**This is retry attempt %d.** The previous attempt was rejected. You MUST address the feedback below before doing anything else.\n\n```\n%s\n```\n\nStart by running `git checkout . && git clean -fd` to reset the worktree, then fix the issues above.",
				params.Attempt, params.Feedback))
	}

	// 3. Memory
	section(&b, "Memory", memoryBody(params.MemoryContext))

	// 3b. Relevant Code (only if CodeSearchContext is non-empty)
	if params.CodeSearchContext != "" {
		section(&b, "Relevant Code", params.CodeSearchContext)
	}

	appendStaticSections(&b, params.WorktreePath, params.BeadID)

	// 11. Failure
	section(&b, "Failure", strings.Join([]string{
		"- 3 failed test attempts: create a P0 bead describing the failure, then exit.",
		"  `bd create --title=\"P0: <bead-title> test failure\" --type=bug --priority=0 --description=\"QG output: <paste error>\"`",
		"- Bead too big: decompose with `bd create`, then exit.",
		"  `bd create --title=\"<subtask>\" --type=task --parent=<bead-id>` for each piece",
		"- Context limit reached: create handoff beads, then exit.",
		"  `bd create --title=\"Continue: <bead-title>\" --type=task --description=\"Remaining: <what's left>\"`",
		"- Blocked: create a blocker bead, then declare the dependency and exit.",
		"  `bd create --title=\"Blocker: <what's blocking>\" --type=bug --priority=0`",
		"  then `bd dep add <this-bead> <blocker-bead>`",
	}, "\n"))

	// 12. Exit
	b.WriteString("## Exit\n\n")
	b.WriteString("When acceptance criteria pass and quality gate is green, exit.\n")

	return b.String()
}

// appendStaticSections writes the invariant sections (4-10) of the worker prompt.
func appendStaticSections(b *strings.Builder, worktreePath, beadID string) {
	section(b, "Coding Rules", strings.Join([]string{
		"- Functional first: pure functions, immutability, early returns",
		"- Pure core (business logic), impure edges (I/O, CLI)",
		"- Go: gofumpt, golangci-lint, go-arch-lint",
		"- Python: PEP 8, ruff, pyright, pytest fixtures > classes",
	}, "\n"))
	section(b, "TDD", "Write tests FIRST. Red-green-refactor. Every feature/fix needs a test.")
	section(b, "Quality Gate", "Before completing, run `./quality_gate.sh` and ensure it passes.")
	section(b, "Worktree", fmt.Sprintf(
		"You are in `%s`. Commit to branch `%s%s`.", worktreePath, protocol.BranchPrefix, beadID,
	))
	section(b, "Git", "Use conventional commits (`feat(scope): msg`, `fix(scope): msg`, `test(scope): msg`).\nNo amend, new commits only.")
	section(b, "Beads Tools", strings.Join([]string{
		"- `bd create` — decompose a bead into smaller sub-beads",
		"- `bd close` — mark a bead as done",
		"- `bd dep add` — declare a blocker dependency",
	}, "\n"))
	section(b, "Constraints", strings.Join([]string{
		"- Do no git push",
		"- Do not modify files outside your worktree",
		"- Do not modify the main branch",
	}, "\n"))
}
