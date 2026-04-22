package ops

import (
	"os"
	"path/filepath"
	"strings"
)

const sharedInstructionsFilename = "ORO_AGENT.md"

// buildReviewPrompt assembles a comprehensive code review prompt from project
// files (shared instructions, Claude compatibility files, .claude/rules/,
// assets/review-patterns.md) and bead context.
func buildReviewPrompt(opts ReviewOpts) string {
	base := opts.BaseBranch
	if base == "" {
		base = "main"
	}

	var b strings.Builder
	writeHeader(&b, base)
	writeContext(&b, opts)
	writeProjectContext(&b, opts)
	writePhases(&b, base)
	writeVerdictAndOutput(&b)
	return b.String()
}

func writeHeader(b *strings.Builder, base string) {
	b.WriteString("You are reviewing code changes before merge to ")
	b.WriteString(base)
	b.WriteString(".\n")
	b.WriteString("The quality gate already passed. Do NOT re-check formatting, linting,\n")
	b.WriteString("or compilation — those are mechanical checks the QG handles.\n\n")
	b.WriteString("MANDATORY BEFORE APPROVAL: Run the full relevant test package to catch\n")
	b.WriteString("regressions in existing tests that the bead's own tests do not cover.\n")
	b.WriteString("Determine the package(s) from the diff (e.g. if changes are in\n")
	b.WriteString("pkg/dispatcher/, run: go test -race -count=1 ./pkg/dispatcher/...).\n")
	b.WriteString("A regression in any existing test is a Critical finding → REJECTED.\n\n")
	b.WriteString("CRITICAL: You MUST NOT use the TaskOutput tool or run tasks in the\n")
	b.WriteString("background. TaskOutput uses 'tail -f' internally which hangs indefinitely\n")
	b.WriteString("on completed tasks. To check task output, use the Read tool on output files.\n")
	b.WriteString("All commands must run in the foreground.\n\n")
}

func writeContext(b *strings.Builder, opts ReviewOpts) {
	b.WriteString("## Context\n")
	b.WriteString("Bead: ")
	b.WriteString(opts.BeadID)
	if opts.BeadTitle != "" {
		b.WriteString(" — ")
		b.WriteString(opts.BeadTitle)
	}
	b.WriteString("\n")
	if opts.AcceptanceCriteria != "" {
		b.WriteString("Acceptance: ")
		b.WriteString(opts.AcceptanceCriteria)
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func writeProjectContext(b *strings.Builder, opts ReviewOpts) {
	root := opts.ProjectRoot
	if root == "" {
		root = opts.Worktree
	}
	if root == "" {
		return
	}

	if standards := readProjectStandards(root, opts.AgentInstructions, opts.ClaudeMD); standards != "" {
		b.WriteString("## Project Standards\n")
		b.WriteString(standards)
		b.WriteString("\n\n")
	}

	if patterns := readAntiPatterns(root, opts.ReviewPatterns); patterns != "" {
		b.WriteString("## Known Anti-Patterns\n")
		b.WriteString(patterns)
		b.WriteString("\n\n")
	}
}

func writePhases(b *strings.Builder, base string) {
	// Phase 1: Understand
	b.WriteString("## Phase 1: Understand\n")
	b.WriteString("1. git diff ")
	b.WriteString(base)
	b.WriteString(" --stat (scope)\n")
	b.WriteString("2. git diff ")
	b.WriteString(base)
	b.WriteString(" (changes)\n")
	b.WriteString("3. For each modified file: read the full file (if >300 lines, read\n")
	b.WriteString("   modified sections with 50 lines of surrounding context)\n")
	b.WriteString("4. Read the test file(s) — these are the spec. Understand what\n")
	b.WriteString("   behavior is being claimed before evaluating the implementation.\n")
	b.WriteString("5. Read 2-3 neighboring files in the same package (architecture context)\n")
	b.WriteString("6. Summarize the INTENT in one sentence.\n")
	b.WriteString("   If you cannot articulate the intent → Critical finding.\n\n")

	// Phase 1.5: Scope Drift
	b.WriteString("## Phase 1.5: Scope Drift\n")
	b.WriteString("Compare AC against diff. If the diff does work not described in AC,\n")
	b.WriteString("or AC describes work not in the diff → Critical finding.\n\n")

	// Phase 2: Critique
	b.WriteString("## Phase 2: Critique\n")
	b.WriteString("Classify each finding: Critical / Important / Minor.\n\n")
	b.WriteString("- **Absence**: For each public function, trace error paths — are they\n")
	b.WriteString("  all handled? For each test, do error and boundary cases exist?\n")
	b.WriteString("- **Adversarial**: What input breaks this? What call sequence hits a\n")
	b.WriteString("  race condition or deadlock?\n")
	b.WriteString("- **Design**: Right abstraction? Right coupling level? Single\n")
	b.WriteString("  responsibility?\n")
	b.WriteString("- **Architecture fit**: Consistent with neighboring files' patterns?\n")
	b.WriteString("  Right package for this code?\n")
	b.WriteString("- **Test-as-spec**: Can you understand the feature by ONLY reading\n")
	b.WriteString("  the test? If not, the test is underspecified — Important finding.\n")
	b.WriteString("- **Anti-patterns**: Does any pattern from the list above apply?\n\n")

	// Anti-sycophancy
	b.WriteString("**Anti-sycophancy:** Banned phrases: 'likely handled', 'probably tested',\n")
	b.WriteString("'could work'. Required replacement: take a position, cite evidence.\n\n")

	// Verification of claims
	b.WriteString("**Verification of claims:** If you claim this is handled elsewhere,\n")
	b.WriteString("cite the file and line. If you claim tests cover this, name the test.\n")
	b.WriteString("Never say likely or probably.\n\n")

	// Engineering cognitive patterns
	b.WriteString("**Engineering patterns:**\n")
	b.WriteString("- Prefer existing patterns over novel solutions.\n")
	b.WriteString("- Consider blast radius before approving large changes.\n")
	b.WriteString("- Essential vs accidental complexity: reject accidental complexity creep.\n")
	b.WriteString("- Systems over heroes: solutions that work without the author present.\n\n")
}

func writeVerdictAndOutput(b *strings.Builder) {
	b.WriteString("## Review Calibration\n")
	b.WriteString("Only REJECT for issues requiring judgment or design changes.\n")
	b.WriteString("Mechanical issues (formatting, unused imports) are not grounds for\n")
	b.WriteString("rejection — a quality gate handles those.\n\n")

	b.WriteString("## Verdict\n")
	b.WriteString("- Any Critical → REJECTED\n")
	b.WriteString("- Any Important → REJECTED\n")
	b.WriteString("- Minor only → APPROVED\n\n")

	b.WriteString("## Output\n")
	b.WriteString("APPROVED or REJECTED\n\n")
	b.WriteString("Findings as: [severity] file:line — description and specific fix.\n\n")
	b.WriteString("If APPROVED and you noticed a recurring pattern worth capturing,\n")
	b.WriteString("add a line:\nPATTERN: tag: trigger → fix\n")
}

// readProjectStandards reads shared instructions, Claude compatibility files,
// and .claude/rules/*.md from the project root.
func readProjectStandards(root, agentInstructionsPath, claudeMDPath string) string {
	var parts []string

	for _, candidate := range instructionCandidates(root, agentInstructionsPath, claudeMDPath) {
		content := readFileIfExists(candidate)
		if content != "" {
			parts = append(parts, content)
			break
		}
	}

	// Read .claude/rules/*.md
	rulesDir := filepath.Join(root, ".claude", "rules")
	entries, err := os.ReadDir(rulesDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			content := readFileIfExists(filepath.Join(rulesDir, entry.Name()))
			if content != "" {
				parts = append(parts, content)
			}
		}
	}

	return strings.Join(parts, "\n")
}

func instructionCandidates(root, agentInstructionsPath, claudeMDPath string) []string {
	var candidates []string
	if agentInstructionsPath != "" {
		candidates = append(candidates, agentInstructionsPath)
	}
	if root != "" {
		candidates = append(candidates,
			filepath.Join(root, sharedInstructionsFilename),
		)
	}
	if claudeMDPath != "" {
		candidates = append(candidates, claudeMDPath)
	}
	if root != "" {
		candidates = append(candidates,
			filepath.Join(root, "CLAUDE.md"),
			filepath.Join(root, ".claude", "CLAUDE.md"),
		)
	}
	return candidates
}

// readAntiPatterns reads assets/review-patterns.md if it exists.
// reviewPatternsPath, when non-empty, is used directly instead of root/assets/review-patterns.md.
func readAntiPatterns(root, reviewPatternsPath string) string {
	if reviewPatternsPath != "" {
		return readFileIfExists(reviewPatternsPath)
	}
	return readFileIfExists(filepath.Join(root, "assets", "review-patterns.md"))
}

// readFileIfExists reads a file and returns its contents, or empty string if
// the file doesn't exist or can't be read.
func readFileIfExists(path string) string {
	//nolint:gosec // path is constructed from trusted project root
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ExtractPatterns parses PATTERN: lines from reviewer output.
func ExtractPatterns(stdout string) []string {
	var patterns []string
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "PATTERN:") {
			pattern := strings.TrimSpace(strings.TrimPrefix(trimmed, "PATTERN:"))
			if pattern != "" {
				patterns = append(patterns, pattern)
			}
		}
	}
	return patterns
}
