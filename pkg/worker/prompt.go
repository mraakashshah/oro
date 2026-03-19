package worker

import (
	"fmt"
	"log"
	"strings"

	"oro/pkg/langprofile"
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
	WorkerProgram      string // may be empty; worker-specific program content
	WorktreePath       string
	Model              string
	Attempt            int    // QG retry attempt (0 = first attempt)
	Feedback           string // rejection/QG failure feedback from previous attempt
	ProjectRoot        string // optional: path to project root for reading .oro/config.yaml
	TargetBranch       string // merge target branch; defaults to "main" if empty
	GitLog             string // git log context; may be empty
	WorkerProgram      string // worker program invocation string; may be empty
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

	// 3c. Git History (only if GitLog is non-empty)
	if params.GitLog != "" {
		section(&b, "Git History", params.GitLog)
	}

	appendStaticSections(&b, params)

	return b.String()
}

// EpicPromptParams contains inputs for building an epic decomposition prompt.
type EpicPromptParams struct {
	BeadID      string
	Title       string
	Description string
}

// buildBranchAndRebaseBead builds the Branch & Rebase Bead section for epic decomposition.
func buildBranchAndRebaseBead(epicID string) string {
	return strings.Join([]string{
		"After decomposing all child beads:",
		"",
		"1. **Create feature branch**:",
		"   ```",
		"   git branch epic/" + epicID + " main",
		"   ```",
		"2. **Create rebase bead**: After all sibling task/feature beads are complete, create a final rebase bead that integrates all changes onto your epic branch.",
		"   ```",
		"   bd create --title=\"rebase: integrate " + epicID + " into epic branch\" \\",
		"     --type=task \\",
		"     --tag rebase \\",
		"     --acceptance-criteria=\"Rebase all child commits onto epic/" + epicID + " branch\"",
		"   ```",
		"3. **Wire rebase dependency**: Make the rebase bead depend on all sibling beads so it runs last:",
		"   ```",
		"   bd dep add <rebase-bead-id> <sibling-1-id>",
		"   bd dep add <rebase-bead-id> <sibling-2-id>",
		"   # ...for each sibling bead",
		"   ```",
	}, "\n")
}

// BuildEpicDecompositionPrompt builds a prompt for decomposing an epic into
// child beads using beadcraft. No TDD/QG/worktree sections — this is planning only.
func BuildEpicDecompositionPrompt(params EpicPromptParams) string {
	var b strings.Builder

	section(&b, "Role", "You are an oro worker in epic decomposition mode. Your job is to break this epic into executable child beads.")

	epicBody := fmt.Sprintf("- **ID:** %s\n- **Title:** %s\n- **Description:** %s",
		params.BeadID, params.Title, params.Description)
	section(&b, "Epic", epicBody)

	section(&b, "Workflow", strings.Join([]string{
		"1. **Explore**: Read the codebase to understand what this epic requires.",
		"2. **Premortem**: Before decomposing, identify what could go wrong — tigers (likely failures), elephants (unlikely but catastrophic), paper tigers (seem scary but aren't).",
		"3. **Decompose with beadcraft**: Break the epic into task/bug beads using `bd create`.",
		"   - Each bead must have full acceptance criteria: `Test: | Cmd: | Assert:`",
		"   - Each bead must have `Read:`, `Signature:` (when adding functions), and `Edges:` fields",
		"   - Run the Rule of Five (P1-P5) on every bead before creating it",
		"   - Size limit: <=7 min estimate, 1-3 source files, single-purpose title",
		"4. **Wire dependencies**: `bd dep add <later> <earlier>` where ordering matters.",
		"5. **Verify**: Run `bd show " + params.BeadID + "` to confirm the tree looks correct.",
	}, "\n"))

	if params.BeadID != "" {
		section(&b, "Branch & Rebase Bead", buildBranchAndRebaseBead(params.BeadID))
	}

	section(&b, "Bead Creation", strings.Join([]string{
		"Use this command for each child bead (do NOT use `--parent` flag on create — it adds a backwards dependency):",
		"```",
		"bd create --title=\"<specific task>\" \\",
		"  --type=task \\",
		"  --acceptance=\"Test: <path>:<FnName> | Cmd: <test_cmd> | Assert: <expected>",
		"Read: <file1>:<Symbol1>, <file2>:<Symbol2>",
		"Signature: <func signature if applicable>",
		"Edges: <error conditions if applicable>\" \\",
		"  --estimate=<minutes>",
		"```",
		"Then set parent and wire dep (epic depends on child, not the other way around):",
		"```",
		"bd update <child-id> --parent " + params.BeadID,
		"bd dep add " + params.BeadID + " <child-id>",
		"```",
	}, "\n"))

	section(&b, "Constraints", strings.Join([]string{
		"- Do NOT write code or create worktrees — only create beads",
		"- Do NOT close the epic — children must complete first",
		"- Do NOT push to git",
		"- Every bead must pass beadcraft Rule of Five before creation",
	}, "\n"))

	section(&b, "Exit", "When all child beads are created and dependencies wired, your work is complete. Exit cleanly.")

	return b.String()
}

// collectCodingRules returns coding rules from .oro/config.yaml when ProjectRoot
// is set and the config contains non-empty rules. Falls back to hardcoded defaults
// when ProjectRoot is empty, the config file is absent, ReadConfig errors, or
// all language coding_rules fields are empty.
func collectCodingRules(projectRoot string) []string {
	fallback := []string{
		"- Functional first: pure functions, immutability, early returns",
		"- Pure core (business logic), impure edges (I/O, CLI)",
		"- Go: gofumpt, golangci-lint, go-arch-lint",
		"- Python: PEP 8, ruff, pyright, pytest fixtures > classes",
	}
	if projectRoot == "" {
		return fallback
	}
	cfg, err := langprofile.ReadConfig(projectRoot)
	if err != nil {
		log.Printf("warn: prompt: ReadConfig(%q): %v; using hardcoded rules", projectRoot, err)
		return fallback
	}
	if cfg == nil {
		return fallback
	}
	var rules []string
	for _, langCfg := range cfg.Languages {
		rules = append(rules, langCfg.CodingRules...)
	}
	if len(rules) == 0 {
		return fallback
	}
	return rules
}

// appendStaticSections writes the invariant sections (4-10) and Failure/Exit sections of the worker prompt.
func appendStaticSections(b *strings.Builder, params PromptParams) {
	section(b, "Coding Rules", strings.Join(collectCodingRules(params.ProjectRoot), "\n"))
	if params.WorkerProgram != "" {
		section(b, "Worker Program", params.WorkerProgram)
	}
	section(b, "TDD", "Write tests FIRST. Red-green-refactor. Every feature/fix needs a test.")
	section(b, "Quality Gate", "Before completing, run `./scripts/quality_gate.sh` and ensure it passes.")
	section(b, "Worktree", fmt.Sprintf(
		"You are in `%s`. Commit to branch `%s%s`.", params.WorktreePath, protocol.BranchPrefix, params.BeadID,
	))

	// Default to "main" if TargetBranch is empty
	targetBranch := params.TargetBranch
	if targetBranch == "" {
		targetBranch = "main"
	}
	section(b, "Merge Target", fmt.Sprintf("Your work merges to branch `%s`.", targetBranch))

	section(b, "Git", "Use conventional commits (`feat(scope): msg`, `fix(scope): msg`, `test(scope): msg`).\nNo amend, new commits only.")
	section(b, "Beads Tools",
		"- `bd create` — decompose a bead into smaller sub-beads\n"+
			"- `bd dep add` — declare a blocker dependency")
	section(b, "Constraints", strings.Join([]string{
		"- Do no git push",
		"- Do not modify files outside your worktree",
		"- Do not modify the main branch",
		"- NEVER replace function/method calls with blank identifier assignments (`_, _ = fn, arg`). If a linter reports an unused variable, remove the declaration — do not silence it by replacing the call with `_ =`.",
	}, "\n"))
	section(b, "Autonomy", strings.Join([]string{
		"You have full authority to execute this bead without asking for permission or confirmation.",
		"",
		"Use these 3 strategies to stay autonomous:",
		"1. **Decide and act** \u2014 make implementation choices yourself based on acceptance criteria.",
		"2. **Recover from errors** \u2014 if a test fails or a command errors, diagnose and fix without escalating.",
		"3. **Timebox exploration** \u2014 if you spend more than 5 minutes stuck, create a blocker bead and exit.",
	}, "\n"))
	appendContextHandoffSection(b)
	appendFailureSection(b, params.BeadID)
	appendExitSection(b)
}

// appendContextHandoffSection writes the Context Handoff section with model thresholds.
func appendContextHandoffSection(b *strings.Builder) {
	section(b, "Context Handoff", strings.Join([]string{
		"Complete each atomic step before context fills. Context thresholds by model:",
		"",
		"| Model   | Soft (warn) | Hard (stop) |",
		"|---------|-------------|-------------|",
		"| opus    | 65%         | 75%         |",
		"| sonnet  | 50%         | 60%         |",
		"| haiku   | 40%         | 50%         |",
		"",
		"At the soft threshold: run `git add && git commit` to save your work, then invoke the `create-handoff` skill. After creating the handoff, exit immediately — do not continue working.",
		"At the hard threshold: the dispatcher will force-stop the worker.",
	}, "\n"))
}

// appendFailureSection writes the Failure section with escalation instructions.
func appendFailureSection(b *strings.Builder, beadID string) {
	section(b, "Failure", strings.Join([]string{
		"All bug beads MUST use --priority=0. Bugs are always P0.",
		"",
		"- 3 failed test attempts: create a P0 bead describing the failure, then exit.",
		"  `bd create --title=\"P0: <bead-title> test failure\" --type=bug --priority=0 --description=\"QG output: <paste error>\"`",
		"- Bead too big: decompose with `bd create`, then set parent and wire deps. Do NOT use `bd create --parent` (it adds a backwards dependency).",
		"  `bd create --title=\"<subtask>\" --type=task` for each piece",
		"  then `bd update <child-id> --parent <bead-id>` + `bd dep add <bead-id> <child-id>` for each child",
		"- Context limit reached: create handoff beads, then exit.",
		"  `bd create --title=\"Continue: <bead-title>\" --type=task --acceptance-criteria=\"<copy same acceptance criteria from above>\" --description=\"Remaining: <what's left>\"`",
		fmt.Sprintf("  then `bd update <child-id> --parent %s` + `bd dep add %s <child-id>`", beadID, beadID),
		"- Blocked: create a blocker bead, then declare the dependency and exit.",
		"  `bd create --title=\"Blocker: <what's blocking>\" --type=bug --priority=0`",
		"  then `bd dep add <this-bead> <blocker-bead>`",
	}, "\n"))
}

// appendExitSection writes the Exit section with completion instructions.
func appendExitSection(b *strings.Builder) {
	section(b, "Exit", strings.Join([]string{
		"When acceptance criteria pass and quality gate is green:",
		"",
		"1. Reflect: did you discover anything non-obvious? Run `oro remember` for each:",
		"   `oro remember \"lesson: <what you learned\"`",
		"   `oro remember \"gotcha: <trap to avoid>\"`",
		"   `oro remember \"decision: <what you chose and why>\"`",
		"",
		"2. Your work is complete. The dispatcher will:",
		"   - Receive your completion signal",
		"   - Merge your worktree branch to main",
		"   - Close the bead if merge succeeds",
		"   - Escalate to the manager if merge fails",
		"",
		"You do NOT need to merge to main or close the bead yourself.",
	}, "\n"))
}
