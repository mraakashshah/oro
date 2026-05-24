# Enforcement Doctrine

Rules are strongest when tools enforce them. Promote rules upward whenever a deterministic enforcement path is practical.

LEVEL 1 - Lint: A custom or configured linter fails CI or IDE checks when violated.
Example: no `fmt.Errorf` without `%w` when preserving a wrapped error cause.
Implementation: golangci-lint custom analyzer.

LEVEL 2 - Types: The compiler or type-checker rejects the violation.
Example: `context.Context` is the first argument of every RPC handler.
Implementation: interface signature.

LEVEL 3 - Formatter: Formatting always rewrites the code into the preferred shape.
Example: imports are grouped as standard library, third-party, then internal packages.
Implementation: goimports, black, prettier, or equivalent formatter config.

LEVEL 4 - Pre-commit: The local commit hook blocks the violation before it enters history.
Example: no committed binary blobs in source directories.
Implementation: pre-commit hook checking staged file contents.

LEVEL 5 - CI: The merge gate blocks the violation before it reaches the target branch.
Example: all tests pass before merge.
Implementation: CI quality gate.

LEVEL 6 - CLAUDE.md (BEST EFFORT): A worker prompt asks for the behavior when no deterministic enforcement is feasible.
Example: when ambiguous, prefer simpler abstractions.
Implementation: prompt guidance in `AGENTS.md`, `CLAUDE.md`, or worker instructions.
