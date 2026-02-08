#!/usr/bin/env bash
# =============================================================================
# oro-worker.sh — Manual worker for testing the oro bead execution loop
#
# Usage: ad_hoc/oro-worker.sh <bead-id> [--model <model>]
#
# Simulates what the dispatcher + oro-worker binary will do:
#   1. Reads bead details from .beads/issues.jsonl
#   2. Creates a git worktree on branch agent/<bead-id>
#   3. Queries memory store for relevant context
#   4. Assembles the 12-section worker prompt
#   5. Launches claude -p in the worktree (with hooks + search)
#   6. Reports results
#
# After completion, merge with:
#   ad_hoc/oro-merge.sh agent/<bead-id>
# =============================================================================

set -euo pipefail

# ---- Args ----
BEAD_ID=""
MODEL="sonnet"
AUTO_YES=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --model)  MODEL="$2"; shift 2 ;;
        --yes|-y) AUTO_YES=true; shift ;;
        -*)       echo "Unknown flag: $1" >&2; exit 1 ;;
        *)        BEAD_ID="$1"; shift ;;
    esac
done

if [ -z "$BEAD_ID" ]; then
    echo "Usage: ad_hoc/oro-worker.sh <bead-id> [--model <model>]" >&2
    echo ""
    echo "Available beads:"
    bd ready 2>/dev/null || echo "  (run 'bd ready' to see available work)"
    exit 1
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"
WORKTREE_BASE="${REPO_ROOT}/.worktrees"
WORKTREE="${WORKTREE_BASE}/agent-${BEAD_ID}"
BRANCH="agent/${BEAD_ID}"
LOG_FILE="/tmp/oro-worker-${BEAD_ID}.log"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log()  { printf '%b[worker]%b %s\n' "$BLUE" "$NC" "$1"; }
ok()   { printf '%b[worker]%b %s\n' "$GREEN" "$NC" "$1"; }
err()  { printf '%b[worker]%b %s\n' "$RED" "$NC" "$1"; }
warn() { printf '%b[worker]%b %s\n' "$YELLOW" "$NC" "$1"; }

# ---- Cleanup on failure ----
cleanup_on_error() {
    err "Worker failed. Cleaning up..."
    if [ -d "$WORKTREE" ]; then
        git -C "$REPO_ROOT" worktree remove --force "$WORKTREE" 2>/dev/null || true
    fi
    git -C "$REPO_ROOT" branch -D "$BRANCH" 2>/dev/null || true
    bd update "$BEAD_ID" --status=open 2>/dev/null || true
}

# =============================================================================
# 1. VALIDATE BEAD
# =============================================================================
log "Fetching bead ${BEAD_ID}..."

BEAD_JSON=$(jq -r "select(.id==\"${BEAD_ID}\")" "${REPO_ROOT}/.beads/issues.jsonl")
if [ -z "$BEAD_JSON" ]; then
    err "Bead ${BEAD_ID} not found in .beads/issues.jsonl"
    exit 1
fi

BEAD_TITLE=$(echo "$BEAD_JSON" | jq -r '.title')
BEAD_DESC=$(echo "$BEAD_JSON" | jq -r '.description // "No description provided."')
BEAD_NOTES=$(echo "$BEAD_JSON" | jq -r '.notes // empty')
BEAD_TYPE=$(echo "$BEAD_JSON" | jq -r '.issue_type // "task"')
BEAD_PRIORITY=$(echo "$BEAD_JSON" | jq -r '.priority // 2')
BEAD_STATUS=$(echo "$BEAD_JSON" | jq -r '.status')

if [ "$BEAD_STATUS" != "open" ]; then
    err "Bead ${BEAD_ID} is '${BEAD_STATUS}', expected 'open'"
    exit 1
fi

echo ""
log "Bead:     ${BEAD_ID} — ${BEAD_TITLE}"
log "Type:     ${BEAD_TYPE} (P${BEAD_PRIORITY})"
log "Model:    ${MODEL}"
log "Branch:   ${BRANCH}"
log "Worktree: ${WORKTREE}"

# =============================================================================
# 2. CLAIM BEAD
# =============================================================================
log "Claiming bead..."
bd update "$BEAD_ID" --status=in_progress

# =============================================================================
# 3. CREATE WORKTREE
# =============================================================================
log "Creating worktree..."
mkdir -p "$WORKTREE_BASE"

if git -C "$REPO_ROOT" worktree list | grep -q "$WORKTREE"; then
    warn "Worktree already exists, removing..."
    git -C "$REPO_ROOT" worktree remove --force "$WORKTREE"
fi

if git -C "$REPO_ROOT" show-ref --verify --quiet "refs/heads/${BRANCH}"; then
    warn "Branch ${BRANCH} already exists, deleting..."
    git -C "$REPO_ROOT" branch -D "$BRANCH"
fi

git -C "$REPO_ROOT" worktree add "$WORKTREE" -b "$BRANCH" main

# =============================================================================
# 4. QUERY MEMORIES
# =============================================================================
log "Querying memories..."
MEMORIES=$(cd "$REPO_ROOT" && oro recall "$BEAD_TITLE" 2>/dev/null || true)
if [ -z "$MEMORIES" ]; then
    MEMORIES="No relevant memories found."
fi

# =============================================================================
# 5. ASSEMBLE 12-SECTION WORKER PROMPT
# =============================================================================
log "Assembling worker prompt..."

# Build the notes section only if notes exist
NOTES_SECTION=""
if [ -n "$BEAD_NOTES" ]; then
    NOTES_SECTION="
**Notes:** ${BEAD_NOTES}"
fi

read -r -d '' PROMPT <<'STATIC_SECTIONS' || true
## 1. Role

You are an oro worker. You execute one bead at a time. You write code and tests — nothing else. You are fully autonomous: no human will answer questions. Everything you need is in this prompt. If something is ambiguous, make a reasonable choice and document it in a commit message.
STATIC_SECTIONS

PROMPT="${PROMPT}

## 2. Bead

**ID:** ${BEAD_ID}
**Title:** ${BEAD_TITLE}
**Type:** ${BEAD_TYPE} (Priority: P${BEAD_PRIORITY})
**Description:** ${BEAD_DESC}${NOTES_SECTION}

## 3. Memory (learnings from prior sessions)

${MEMORIES}

## 4. Coding Rules

- Functional first: pure functions, immutability, higher-order functions, early returns.
- Pure core (business logic), impure edges (I/O, CLI). No side effects inside logic.
- Go: gofumpt, golangci-lint, go-arch-lint. Table-driven tests. Errors are values.
- Python: PEP 8, ruff, pyright, pytest fixtures > classes. f-strings. Type hints.
- Fail fast with early returns. No deep nesting. No unnecessary abstractions.

## 5. TDD

Write tests FIRST. Red-green-refactor. Every feature and bugfix needs a test.
Run tests frequently: \`go test -race -p 2 ./...\`
Never commit code that doesn't pass tests.

## 6. Quality Gate

Before declaring done, run from the worktree root:

\`\`\`bash
./quality_gate.sh
\`\`\`

ALL checks must pass (formatting, linting, architecture, tests, security, build).
If they fail, fix every issue and re-run. Do NOT skip or bypass the quality gate.
Do NOT modify quality_gate.sh.

## 7. Worktree

You are working in: \`${WORKTREE}\`
Your branch: \`${BRANCH}\`
The main codebase is at: \`${REPO_ROOT}\` (read-only reference — do NOT modify)
Commit to your branch only. Do not switch branches.

## 8. Git Conventions

- Conventional commits: \`feat(scope): msg\`, \`fix(scope): msg\`, \`test(scope): msg\`
- Create new commits only — NEVER amend
- Commit early and often — small, atomic commits
- No co-author credits

## 9. Beads Tools

If you discover additional work needed:
\`\`\`bash
bd create --title=\"...\" --type=task --priority=2
\`\`\`
If you need to mark a dependency:
\`\`\`bash
bd dep add <new-bead> <depends-on>
\`\`\`
Do NOT close the bead yourself — the dispatcher handles that.

## 10. Constraints

- Do NOT run \`git push\`
- Do NOT modify files outside your worktree
- Do NOT modify the main branch
- Do NOT amend existing commits
- Do NOT create documentation files unless the bead requires it
- Do NOT install new dependencies without justification in a commit message

## 11. Failure Protocol

- 3 failed attempts on same test failure → create P0 bead describing the problem, commit what you have, then exit
- Bead too big to complete → decompose into smaller beads with \`bd create\`, then exit
- Context running low → commit progress, create beads for remaining work, then exit
- Blocked by missing dependency → create a blocker bead with \`bd dep add\`, then exit

## 12. Exit

When the bead's acceptance criteria are met AND the quality gate passes:
1. Ensure all changes are committed (no dirty worktree)
2. Print a summary: what was done, files changed, tests added
3. Exit"

# =============================================================================
# 6. SHOW PROMPT AND CONFIRM
# =============================================================================
echo ""
echo "═══════════════════════════════════════════════════════════════"
echo " WORKER PROMPT ($(echo "$PROMPT" | wc -c | tr -d ' ') bytes)"
echo "═══════════════════════════════════════════════════════════════"
echo "$PROMPT"
echo "═══════════════════════════════════════════════════════════════"
echo ""

if [ "$AUTO_YES" = false ]; then
    read -rp "Launch worker? [y/N] " confirm
    if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
        warn "Aborted by user."
        cleanup_on_error
        exit 0
    fi
fi

# =============================================================================
# 7. LAUNCH CLAUDE -P
# =============================================================================
log "Launching claude -p (model=${MODEL})..."
log "Output log: ${LOG_FILE}"
echo ""

export ORO_ROLE=worker

# Run claude -p in the worktree with:
#   --model: configurable model
#   --verbose: stream tool use and progress in real time
#   --permission-mode acceptEdits: auto-approve file edits
#   --add-dir: allow reading main repo as reference
CLAUDE_EXIT=0
(cd "$WORKTREE" && claude -p "$PROMPT" \
    --model "$MODEL" \
    --verbose \
    --permission-mode "acceptEdits" \
    --add-dir "$REPO_ROOT" \
) 2>&1 | tee "$LOG_FILE" || CLAUDE_EXIT=$?

# =============================================================================
# 8. POST-RUN REPORT
# =============================================================================
echo ""
echo "═══════════════════════════════════════════════════════════════"
echo " WORKER REPORT"
echo "═══════════════════════════════════════════════════════════════"
echo ""
echo "  Bead:      ${BEAD_ID} — ${BEAD_TITLE}"
echo "  Branch:    ${BRANCH}"
echo "  Worktree:  ${WORKTREE}"
echo "  Model:    ${MODEL}"
echo "  Exit code: ${CLAUDE_EXIT}"
echo "  Log:       ${LOG_FILE}"
echo ""

# Show what the worker committed
COMMITS=$(git -C "$WORKTREE" log main..HEAD --oneline 2>/dev/null || echo "(no commits)")
echo "  Commits:"
printf '    %s\n' "$COMMITS"
echo ""

# Show files changed
FILES=$(git -C "$WORKTREE" diff --stat main 2>/dev/null || echo "(no changes)")
echo "  Files changed:"
printf '    %s\n' "$FILES"
echo ""

echo "═══════════════════════════════════════════════════════════════"
echo ""

# =============================================================================
# 9. NEXT STEPS
# =============================================================================
if [ "$CLAUDE_EXIT" -eq 0 ]; then
    ok "Worker completed. Next steps:"
    echo ""
    echo "  1. Review the changes:"
    echo "     cd ${WORKTREE}"
    echo "     git log main..HEAD --oneline"
    echo "     git diff main"
    echo ""
    echo "  2. Run quality gate (if worker didn't):"
    echo "     cd ${WORKTREE} && ./quality_gate.sh"
    echo ""
    echo "  3. Merge to main:"
    echo "     cd ${REPO_ROOT}"
    echo "     ad_hoc/oro-merge.sh ${BRANCH}"
    echo ""
    echo "  4. Close the bead:"
    echo "     bd close ${BEAD_ID}"
    echo ""
    echo "  5. Clean up worktree:"
    echo "     git worktree remove ${WORKTREE}"
    echo "     git branch -d ${BRANCH}"
else
    err "Worker exited with code ${CLAUDE_EXIT}. Review the log:"
    echo "     less ${LOG_FILE}"
    echo ""
    echo "  Worktree preserved at: ${WORKTREE}"
    echo "  To retry: cd ${WORKTREE} && claude"
    echo "  To clean up: git worktree remove --force ${WORKTREE}"
fi
