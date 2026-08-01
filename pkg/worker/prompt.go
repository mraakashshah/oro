package worker

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"oro/pkg/cards"
	"oro/pkg/langprofile"
	"oro/pkg/protocol"
)

const maxDeckViewBytes = 256 * 1024

// PromptParams contains all inputs needed to assemble the 12-section worker prompt.
type PromptParams struct {
	BeadID               string
	Title                string
	Description          string
	AcceptanceCriteria   string
	MemoryContext        string              // deprecated after D.4: no longer rendered; use Cards
	Cards                cards.RelevantCards // relevant knowledge cards (§5.5, §13.1)
	CodeSearchContext    string              // formatted code search results from FTS5Search
	CodeStructureContext string              // formatted nav-maps from codestruct (outline + line ranges)
	WorktreePath         string
	Model                string
	Attempt              int    // QG retry attempt (0 = first attempt)
	Feedback             string // rejection/QG failure feedback from previous attempt
	ProjectRoot          string // optional: path to project root for reading .oro/config.yaml
	TargetBranch         string // merge target branch; defaults to "main" if empty
	GitLog               string // git log context; may be empty
	WorkerProgram        string // worker program invocation string; may be empty
	LegacyFailurePrompt  bool   // retain direct task instructions while proposal gateway rolls out
}

// section writes a markdown section (## header + body) to the builder.
func section(b *strings.Builder, header, body string) {
	fmt.Fprintf(b, "## %s\n\n%s\n\n", header, body)
}

// cardsBody renders the Cards section body from a RelevantCards result (§5.5).
// Inlined cards show their full body; any deck entries beyond the inline set
// appear in the deck-view format so the worker can request deep content on demand.
func cardsBody(rc cards.RelevantCards) string {
	if len(rc.Deck) == 0 && len(rc.Inlined) == 0 {
		return "No relevant cards for this task."
	}

	var b strings.Builder

	// Inline cards with full body.
	for _, c := range rc.Inlined {
		fmt.Fprintf(&b, "**[%s] %s** (id: %s, score: %.1f)\n\n%s\n\n", c.Type, c.Title, c.ID, c.Score, c.BodyFull)
	}

	// Deck view for cards beyond the inline budget.
	inlinedIDs := make(map[string]bool, len(rc.Inlined))
	for _, c := range rc.Inlined {
		inlinedIDs[c.ID] = true
	}
	deckOnlyCount := 0
	for _, c := range rc.Deck {
		if !inlinedIDs[c.ID] {
			deckOnlyCount++
		}
	}
	if deckOnlyCount > 0 {
		b.WriteString(boundedDeckView(rc.Deck, inlinedIDs, deckOnlyCount))
	}

	return b.String()
}

func boundedDeckView(deck []cards.DeckCard, inlinedIDs map[string]bool, deckOnlyCount int) string {
	const header = "=== Cards (deck view) ===\n\n"

	var b strings.Builder
	b.WriteString(header)
	rendered := 0
	for _, c := range deck {
		if inlinedIDs[c.ID] {
			continue
		}

		suffix := deckViewSuffix(deckOnlyCount - rendered - 1)
		available := maxDeckViewBytes - b.Len() - len(suffix)
		prefix := fmt.Sprintf("[%-8s] %-40s score %.1f   id %s\n", string(c.Type), c.Title, c.Score, c.ID)
		if available < len(prefix)+1 {
			break
		}
		if len(prefix)+len(c.BodySummary)+1 <= available {
			b.WriteString(prefix)
			b.WriteString(c.BodySummary)
			b.WriteByte('\n')
			rendered++
			continue
		}

		b.WriteString(prefix)
		b.WriteString(truncateUTF8(c.BodySummary, available-len(prefix)-1))
		b.WriteByte('\n')
		rendered++
		break
	}
	b.WriteString(deckViewSuffix(deckOnlyCount - rendered))
	return b.String()
}

func deckViewSuffix(omitted int) string {
	var b strings.Builder
	if omitted > 0 {
		fmt.Fprintf(&b, "\n%d deck cards omitted due to prompt size limit.\n", omitted)
	}
	b.WriteString("\nTo see full body of any card: `oro cards show <id>`\n")
	return b.String()
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	if maxBytes <= 0 {
		return ""
	}
	const ellipsis = "…"
	if maxBytes < len(ellipsis) {
		return ""
	}
	end := maxBytes - len(ellipsis)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + ellipsis
}

// AssemblePrompt builds the complete 12-section worker prompt from task details
// and context. This prompt is passed to `claude -p` when spawning a worker.
func AssemblePrompt(params PromptParams) string {
	var b strings.Builder

	// 1. Role
	section(&b, "Role", "You are an oro worker. You execute one task at a time.")

	// 2. Task
	beadBody := fmt.Sprintf(
		"- **ID:** %s\n- **Title:** %s\n- **Description:** %s\n- **Acceptance Criteria:** %s",
		params.BeadID, params.Title, params.Description, params.AcceptanceCriteria,
	)
	if params.Attempt > 0 {
		beadBody += fmt.Sprintf("\n\n> **Retry attempt %d.** The quality gate has failed on previous attempts. Focus on fixing the issues identified in the feedback.", params.Attempt)
	}
	section(&b, "Task", beadBody)

	// 3. Cards (replaces Memory + Previous Feedback per §13.2)
	section(&b, "Cards", cardsBody(params.Cards))

	// 3b. Code Structure nav-maps (only if CodeStructureContext is non-empty)
	if params.CodeStructureContext != "" {
		section(&b, "Code Structure", params.CodeStructureContext)
	}

	// 3c. Relevant Code (only if CodeSearchContext is non-empty)
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
	BeadID             string
	Title              string
	Description        string
	AcceptanceCriteria string
	ParentTier         string // routing tier of the epic; included in oro task create when non-empty
}

// buildBranchAndRebaseBead builds the Branch & Rebase Task section for epic decomposition.
func buildBranchAndRebaseBead(epicID string) string {
	return strings.Join([]string{
		"After decomposing all child tasks:",
		"",
		"1. **Create feature branch**:",
		"   ```",
		"   git branch epic/" + epicID + " main",
		"   ```",
		"2. **Create rebase task**: After all sibling task/feature tasks are complete, create a final rebase task that integrates all changes onto your epic branch.",
		"   ```",
		"   oro task create --title=\"rebase: integrate " + epicID + " into epic branch\" \\",
		"     --type=task \\",
		"     --tag rebase \\",
		"     --acceptance-criteria=\"Rebase all child commits onto epic/" + epicID + " branch\"",
		"   ```",
		"3. **Wire rebase dependency**: Make the rebase task depend on all sibling tasks so it runs last:",
		"   ```",
		"   oro task dep add <rebase-task-id> <sibling-1-id>",
		"   oro task dep add <rebase-task-id> <sibling-2-id>",
		"   # ...for each sibling task",
		"   ```",
	}, "\n")
}

func epicDecompositionWorkflow(epicID string) string {
	return strings.Join([]string{
		"1. **Goal-satisfaction gate**: Run `oro task show " + epicID + "` and inspect the acceptance criteria. If it contains `Cmd:`, Run the epic acceptance command before decomposing. Do NOT create child tasks if the command passes; close the epic with `oro task close " + epicID + " --reason \"Acceptance command already passes\"` and exit.",
		"2. **Explore**: Read the codebase to understand what this epic requires.",
		"3. **Premortem**: Before decomposing, identify what could go wrong — tigers (likely failures), elephants (unlikely but catastrophic), paper tigers (seem scary but aren't).",
		"4. **Decompose with beadcraft**: Break the epic into task/bug tasks using `oro task create`.",
		"   - Each task must have full acceptance criteria: `Test: | Cmd: | Assert:`",
		"   - Use neutral runtime tier language when a task needs routing guidance: `fast`, `balanced`, `deep`, `background`",
		"   - Each task must have `Read:`, `Signature:` (when adding functions), and `Edges:` fields",
		"   - Run the Rule of Five (P1-P5) on every task before creating it",
		"   - Size limit: <=7 min estimate, 1-3 source files, single-purpose title",
		"5. **Wire dependencies**: `oro task dep add <later> <earlier>` where ordering matters.",
		"6. **Verify**: Run `oro task show " + epicID + "` to confirm the tree looks correct.",
	}, "\n")
}

// BuildEpicDecompositionPrompt builds a prompt for decomposing an epic into
// child tasks using beadcraft. No TDD/QG/worktree sections — this is planning only.
func BuildEpicDecompositionPrompt(params EpicPromptParams) string {
	var b strings.Builder

	section(&b, "Role", "You are an oro worker in epic decomposition mode. Your job is to break this epic into executable child tasks.")

	epicBody := fmt.Sprintf("- **ID:** %s\n- **Title:** %s\n- **Description:** %s\n- **Acceptance Criteria:** %s",
		params.BeadID, params.Title, params.Description, params.AcceptanceCriteria)
	section(&b, "Epic", epicBody)

	section(&b, "Workflow", epicDecompositionWorkflow(params.BeadID))

	if params.BeadID != "" {
		section(&b, "Branch & Rebase Task", buildBranchAndRebaseBead(params.BeadID))
	}

	taskCreateLines := []string{
		"Use this command for each child task. `--parent` attaches the child to this epic; it does not create a dependency:",
		"```",
		"oro task create --title=\"<specific task>\" \\",
		"  --type=task \\",
		"  --parent " + params.BeadID + " \\",
	}
	if params.ParentTier != "" {
		taskCreateLines = append(taskCreateLines, "  --tier="+params.ParentTier+" \\")
	}
	taskCreateLines = append(taskCreateLines,
		"  --acceptance=\"Test: <path>:<FnName> | Cmd: <test_cmd> | Assert: <expected>",
		"Read: <file1>:<Symbol1>, <file2>:<Symbol2>",
		"Signature: <func signature if applicable>",
		"Edges: <error conditions if applicable>\" \\",
		"  --estimate=<minutes>",
		"```",
		"Then wire the explicit completion dependency (epic depends on child, not the other way around):",
		"```",
		"oro task dep add "+params.BeadID+" <child-id>",
		"```",
	)
	section(&b, "Task Creation", strings.Join(taskCreateLines, "\n"))

	section(&b, "Constraints", strings.Join([]string{
		"- Do NOT write code or create worktrees — only create tasks",
		"- Do NOT close the epic unless the goal-satisfaction gate passes before decomposition",
		"- Do NOT push to git",
		"- Prefer neutral routing tiers over provider names: `fast`, `balanced`, `deep`, `background`",
		"- Every task must pass beadcraft Rule of Five before creation",
	}, "\n"))

	section(&b, "Exit", "When all child tasks are created and dependencies wired, your work is complete. Exit cleanly.")

	return b.String()
}

type codingRuleSet struct {
	language string
	rules    []string
}

// collectCodingRuleSets returns coding rules from .oro/config.yaml when
// ProjectRoot is set and the config contains non-empty rules. Falls back to
// hardcoded defaults when ProjectRoot is empty, the config file is absent,
// ReadConfig errors, or all language coding_rules fields are empty.
func collectCodingRuleSets(projectRoot string) []codingRuleSet {
	fallback := []codingRuleSet{
		{
			language: "Default",
			rules: []string{
				"- Functional first: pure functions, immutability, early returns",
				"- Pure core (business logic), impure edges (I/O, CLI)",
				"- Go: gofumpt, golangci-lint, go-arch-lint",
				"- Python: PEP 8, ruff, pyright, pytest fixtures > classes",
			},
		},
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
	languages := make([]string, 0, len(cfg.Languages))
	for language := range cfg.Languages {
		languages = append(languages, language)
	}
	sort.Strings(languages)

	var sets []codingRuleSet
	for _, language := range languages {
		rules := cfg.Languages[language].CodingRules
		if len(rules) == 0 {
			continue
		}
		sets = append(sets, codingRuleSet{
			language: displayLanguageName(language),
			rules:    rules,
		})
	}
	if len(sets) == 0 {
		return fallback
	}
	return sets
}

func displayLanguageName(language string) string {
	switch strings.ToLower(language) {
	case "go":
		return "Go"
	case "js", "javascript":
		return "JavaScript"
	case "py", "python":
		return "Python"
	case "ts", "typescript":
		return "TypeScript"
	default:
		if language == "" {
			return "Unspecified"
		}
		return strings.ToUpper(language[:1]) + language[1:]
	}
}

const fallbackCodingRulesDoctrine = "# Enforcement Doctrine\n\nDoctrine asset unavailable; promote coding rules to deterministic enforcement when practical."

func loadCodingRulesDoctrine(projectRoot string) string {
	for _, candidate := range doctrinePathCandidates(projectRoot) {
		data, err := os.ReadFile(candidate) //nolint:gosec // candidate paths are internal repo/project asset locations.
		if err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return fallbackCodingRulesDoctrine
}

func doctrinePathCandidates(projectRoot string) []string {
	var candidates []string
	if projectRoot != "" {
		candidates = append(candidates, filepath.Join(projectRoot, "assets", "doctrine.md"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "assets", "doctrine.md"))
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(file), "..", "..", "assets", "doctrine.md"))
	}
	return candidates
}

func renderCodingRules(projectRoot string) string {
	parts := []string{loadCodingRulesDoctrine(projectRoot)}
	ruleSets := collectCodingRuleSets(projectRoot)
	if len(ruleSets) > 0 {
		var b strings.Builder
		b.WriteString("Per-language rules:")
		for _, set := range ruleSets {
			b.WriteString("\n\n")
			b.WriteString(set.language)
			b.WriteString(":\n")
			b.WriteString(strings.Join(set.rules, "\n"))
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "\n\n")
}

// appendStaticSections writes the invariant sections (4-10) and Failure/Exit sections of the worker prompt.
func appendStaticSections(b *strings.Builder, params PromptParams) {
	section(b, "Coding Rules", renderCodingRules(params.ProjectRoot))
	if params.WorkerProgram != "" {
		section(b, "Worker Program", params.WorkerProgram)
	}
	section(b, "TDD", "Write tests FIRST. Red-green-refactor. Every feature/fix needs a test.")
	section(b, "Quality Gate", "Run the task acceptance command and focused tests needed to validate your work; do not run the full quality gate yourself. The worker harness owns and enforces the full quality gate after your subprocess exits.")
	section(b, "Worktree", fmt.Sprintf(
		"You are in `%s`. Commit to branch `%s%s`.", params.WorktreePath, protocol.BranchPrefix, params.BeadID,
	))

	// Default to "main" if TargetBranch is empty
	targetBranch := params.TargetBranch
	if targetBranch == "" {
		targetBranch = "main"
	}
	section(b, "Merge Target", fmt.Sprintf("Your work merges to branch `%s`.", targetBranch))

	section(b, "Git", "Use conventional commits (`feat(scope): msg`, `fix(scope): msg`, `test(scope): msg`).\nNo amend, new commits only.\nNever run bare `git commit`; always provide the message non-interactively with `git commit -m \"<message>\"` or `git commit --message \"<message>\"`.")
	section(b, "Task Tools",
		"- `oro evidence run` — record assignment-scoped diagnostic evidence\n"+
			"- `oro task propose-blocker` — propose an evidence-backed blocker through the gateway")
	appendEditToolsSection(b)
	section(b, "Constraints", strings.Join([]string{
		"- NEVER run `git push` — you are in a worktree on an agent branch. Pushing is the dispatcher/manager's job. This overrides any global rules that say to push.",
		"- Do not modify files outside your worktree",
		"- Keep the worktree CLEAN. Write scratch output — coverage profiles, lint caches, ad-hoc logs, verification dumps — under `$TMPDIR`, never inside the worktree. Any untracked file makes `git status --porcelain` non-empty, which the dispatcher reads as unpreserved work and quarantines as `stale_active_assignment`, freezing your assignment. If you must create a file in the worktree, delete it before you finish.",
		fmt.Sprintf("- Do not modify the %s branch", targetBranch),
		"- NEVER replace function/method calls with blank identifier assignments (`_, _ = fn, arg`). If a linter reports an unused variable, remove the declaration — do not silence it by replacing the call with `_ =`.",
		"",
		"**Iron Law:** No fixes without root cause. If a test fails, diagnose before changing code.",
		"",
		"**3-strike rule:** After 3 failed debugging hypotheses, STOP and re-read the error from scratch.",
		"",
		"**Anti-rationalization table:**",
		"",
		"| Excuse | Rebuttal |",
		"|--------|----------|",
		"| Issue is simple | Simple issues have root causes too |",
		"| Emergency no time | Systematic is FASTER than thrashing |",
		"| Just try this first | First fix sets the pattern |",
		"",
		"**DO** stop for: test failures, 3 failed debugging hypotheses, security concerns.",
		"**NEVER** stop for: style preferences, naming choices, trivial confirmations.",
	}, "\n"))
	section(b, "Autonomy", strings.Join([]string{
		"You have full authority to execute this task without asking for permission or confirmation.",
		"",
		"Use these 3 strategies to stay autonomous:",
		"1. **Decide and act** \u2014 make implementation choices yourself based on acceptance criteria.",
		"2. **Recover from errors** \u2014 if a test fails or a command errors, diagnose and fix without escalating.",
		"3. **Timebox exploration** \u2014 if you spend more than 5 minutes stuck, record evidence and propose a blocker through the work-proposal gateway, then exit.",
	}, "\n"))
	appendContextHandoffSection(b)
	appendFailureSection(b, params.BeadID, params.LegacyFailurePrompt)
	appendExitSection(b)
}

// appendEditToolsSection writes the Edit Tools section describing the 12 worker-facing
// oro edit:* subcommands that provide AST-aware file editing from Bash.
func appendEditToolsSection(b *strings.Builder) {
	section(b, "Edit Tools", strings.Join([]string{
		"AST-aware file editing. Invoke via Bash as `oro edit:<op> ...`:",
		"",
		"- `oro edit:replace FILE SYMBOL --snippet '...'` — replace a symbol's body",
		"- `oro edit:after FILE SYMBOL --snippet '...'` — insert snippet after a symbol",
		"- `oro edit:delete FILE SYMBOL [--force]` — delete a symbol",
		"- `oro edit:rename FILE OLD NEW` — rename a symbol within a file",
		"- `oro edit:rename-all DIR OLD NEW [--only KIND] [--dry-run]` — rename across all files",
		"- `oro edit:move FILE SYMBOL --after OTHER` — reposition a symbol within a file",
		"- `oro edit:move-to-file SYMBOL FROM TO [--dry-run]` — move symbol to another file",
		"- `oro edit:read FILE` — print symbol map (name → line range)",
		"- `oro edit:diff FILE` — show pending edits as unified diff",
		"- `oro edit:undo FILE` — reverse the last edit to a file",
		"- `oro edit:batch FILE --edits '[...]'` — apply multiple edits atomically",
		"- `oro edit:check` — reparse all edited files and surface errors",
	}, "\n"))
}

// appendContextHandoffSection writes the Context Handoff section with neutral tier thresholds.
func appendContextHandoffSection(b *strings.Builder) {
	section(b, "Context Handoff", strings.Join([]string{
		"Complete each atomic step before context fills. Context thresholds by tier:",
		"",
		"| Tier       | Soft (warn) | Hard (stop) |",
		"|------------|-------------|-------------|",
		"| fast       | 40%         | 50%         |",
		"| balanced   | 40%         | 50%         |",
		"| deep       | 40%         | 50%         |",
		"| background | 40%         | 50%         |",
		"",
		"At the soft threshold: run `git add <relevant files> && git commit -m \"<type>(<scope>): <desc>\"` to save your work, then invoke the `create-handoff` skill. After creating the handoff, exit immediately — do not continue working.",
		"At the hard threshold: the dispatcher will force-stop the worker.",
	}, "\n"))
}

// appendFailureSection writes the Failure section with escalation instructions.
func appendFailureSection(b *strings.Builder, beadID string, legacy bool) {
	if legacy {
		appendLegacyFailureSection(b, beadID)
		return
	}
	section(b, "Failure", strings.Join([]string{
		"Report failures through the assignment-scoped work-proposal gateway; do not create tasks or dependencies directly.",
		"",
		"1. Record terminal diagnostic evidence:",
		"   `oro evidence run --kind diagnostic --timeout 2m -- <command> <args...>`",
		"2. Propose the blocker backed by that evidence:",
		"   `oro task propose-blocker --evidence-run <run-id> --fingerprint <fingerprint> --summary <summary> --kind prerequisite --priority=2`",
		"Use priority P2 by default. Request a different severity only when the validated severity policy requires it; do not assume every bug is P0.",
	}, "\n"))
}

func appendLegacyFailureSection(b *strings.Builder, beadID string) {
	section(b, "Failure", strings.Join([]string{
		"All bug tasks MUST use --priority=0. Bugs are always P0.",
		"",
		"- 3 failed test attempts: create a P0 task describing the failure, then exit.",
		"  `oro task create --title=\"P0: <task-title> test failure\" --type=bug --priority=0 --description=\"QG output: <paste error>\"`",
		"- Task too big: decompose with `oro task create --parent <task-id>` for each child. `--parent` only attaches the child; add dependencies explicitly when needed.",
		"  `oro task create --title=\"<subtask>\" --type=task --parent <task-id>` for each piece",
		"  then `oro task dep add <task-id> <child-id>` for each child that must finish before the parent",
		"- Context limit reached: create handoff tasks, then exit.",
		fmt.Sprintf("  `oro task create --title=\"Continue: <task-title>\" --type=task --parent %s --acceptance-criteria=\"<copy same acceptance criteria from above>\" --description=\"Remaining: <what's left>\"`", beadID),
		fmt.Sprintf("  then `oro task dep add %s <child-id>` if the parent must wait for the handoff task", beadID),
		"- Blocked: create a blocker task, then declare the dependency and exit.",
		"  `oro task create --title=\"Blocker: <what's blocking>\" --type=bug --priority=0`",
		"  then `oro task dep add <this-task> <blocker-task>`",
	}, "\n"))
}

// appendExitSection writes the Exit section with completion instructions.
func appendExitSection(b *strings.Builder) {
	section(b, "Exit", strings.Join([]string{
		"When acceptance criteria pass and quality gate is green:",
		"",
		"1. Reflect: did you discover anything non-obvious? If so, record learnings with the cards flow by emitting `[MEMORY]` markers in your final output:",
		"   `[MEMORY] type=lesson tags=<comma-separated>: <what you learned>`",
		"   `[MEMORY] type=gotcha tags=<comma-separated>: <trap to avoid>`",
		"   `[MEMORY] type=decision tags=<comma-separated>: <what you chose and why>`",
		"   Use `oro current` to inspect the current task and relevant card context; reviewers can promote queued learnings with `oro cards review-queue`.",
		"",
		"2. Your work is complete. The dispatcher will:",
		"   - Receive your completion signal",
		"   - Integrate your work according to the active dispatcher mode",
		"   - In auto-merge mode, merge your worktree branch to main and close the task if merge succeeds",
		"   - In manual-integration mode, preserve your branch/worktree and escalate for coordinator review",
		"   - Escalate to the manager if integration fails",
		"",
		"You do NOT need to merge to main or close the task yourself.",
	}, "\n"))
}
