package ops

import (
	"fmt"
	"path/filepath"
	"strings"
)

// buildACPrompt assembles the exploratory Opus prompt that reads task context,
// explores the codebase, and writes precise testable acceptance criteria.
//
// The agent is instructed to use 'oro task update --acceptance' to save its output —
// it does NOT write source code or create worktrees.
func buildACPrompt(opts WriteACOpts) string {
	var b strings.Builder

	writeACHeader(&b)
	writeACContext(&b, opts)
	writeACPlaybook(&b, opts)
	writeACOutputFormat(&b, opts)

	return b.String()
}

func writeACHeader(b *strings.Builder) {
	b.WriteString("You are a one-shot Opus agent. Your sole job is to write precise, testable acceptance criteria for a task.\n")
	b.WriteString("Do NOT write source code. Do NOT implement any feature. Do NOT create worktrees.\n\n")
	b.WriteString("CRITICAL: Do NOT use TaskOutput or run tasks in the background.\n")
	b.WriteString("Use the Read tool to check output files. Run all commands in foreground.\n\n")
}

func writeACContext(b *strings.Builder, opts WriteACOpts) {
	b.WriteString("## Task Context\n")
	fmt.Fprintf(b, "Task: %s", opts.BeadID)
	if opts.BeadTitle != "" {
		fmt.Fprintf(b, " — %s", opts.BeadTitle)
	}
	b.WriteString("\n")
	if opts.BeadDescription != "" {
		fmt.Fprintf(b, "Description: %s\n", opts.BeadDescription)
	}
	b.WriteString("\n")
}

func writeACPlaybook(b *strings.Builder, opts WriteACOpts) {
	b.WriteString("## Exploration Steps\n\n")
	b.WriteString("Work through these steps in order before writing any acceptance criteria:\n\n")

	fmt.Fprintf(b, "1. Run `oro task show %s` to read the full task context, including title, description, and any existing notes.\n\n", opts.BeadID)

	b.WriteString("2. If the task has blocking or blocked dependencies, run `oro task show <dep-id>` on each to understand how they relate. " +
		"Acceptance criteria must be compatible with what depends on or is depended on by this task.\n\n")

	b.WriteString("3. Use Grep and Glob to explore the codebase for symbols, file paths, and packages referenced in the task title and description. " +
		"This tells you what already exists and what needs to be created.\n\n")

	docsPlans := "docs/plans"
	if opts.OroDocsDir != "" {
		docsPlans = filepath.Join(opts.OroDocsDir, "plans")
	}
	fmt.Fprintf(b, "4. Check `%s/` for any related specs or design documents that constrain the implementation.\n\n", docsPlans)

	b.WriteString("5. Look at existing passing tests in the relevant packages to understand AC format conventions used in this project. " +
		"Match their style and specificity.\n\n")

	b.WriteString("**Verification:** AC must reference real files that exist in the codebase. " +
		"Before saving, confirm each file path and test function name with Grep or Read.\n\n")
}

func writeACOutputFormat(b *strings.Builder, opts WriteACOpts) {
	b.WriteString("## Output Format\n\n")
	b.WriteString("Write acceptance criteria using this exact format:\n\n")
	b.WriteString("  Test: <file>:<FunctionName> | Cmd: <command to run> | Assert: <expected outcome>\n\n")
	b.WriteString("Examples:\n")
	b.WriteString("  Test: pkg/ops/ac_prompt_test.go:TestBuildACPrompt | Cmd: go test ./pkg/ops/... -run TestBuildACPrompt -v | Assert: PASS\n")
	b.WriteString("  Test: tests/test_parser.py::test_parse_empty | Cmd: uv run pytest tests/test_parser.py::test_parse_empty -v | Assert: PASS\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- One acceptance criterion per task (one Test/Cmd/Assert triple)\n")
	b.WriteString("- The test must not exist yet (you are specifying what to build, not what already passes)\n")
	b.WriteString("- Cmd must be a shell command that runs the test in isolation\n")
	b.WriteString("- Assert must be 'PASS' or a specific observable output\n\n")

	fmt.Fprintf(b, "When you have written the acceptance criteria, save it with:\n\n")
	fmt.Fprintf(b, "  oro task update %s --acceptance=\"Test: <file>:<Fn> | Cmd: <cmd> | Assert: PASS\"\n", opts.BeadID)
}
