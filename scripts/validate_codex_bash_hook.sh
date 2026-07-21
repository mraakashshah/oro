#!/usr/bin/env bash
#
# validate_codex_bash_hook.sh — live gate for the Codex Bash read-hook.
#
# This is the HARD acceptance gate for epic oro-9f14. Unit tests prove the
# oro-search-hook binary emits the right JSON, but they cannot prove the
# consumer (codex exec) actually fires the hook and honors its rewrite. This
# script drives a REAL `codex exec` — the same spawn path normal workers use —
# and asserts, deterministically, that a large-file `cat`:
#
#   (a) is NOT read raw  (a sentinel that lives only in the raw file body is
#       absent from every command the model ran), AND
#   (b) is replaced by the AST summary (the oro-search-hook summary header
#       reaches the model).
#
# Mechanism under test: codex exec ignores a PreToolUse "deny" for trusted read
# commands (cat/ls/sed), so oro-search-hook ALLOWS the call and rewrites the
# command via updatedInput into `printf '%s' '<summary>'`. Firing requires
# --dangerously-bypass-hook-trust (oro's worker/ops spawn path passes it), since
# codex exec never establishes interactive hook trust.
#
# Exit codes:
#   0  PASS  — raw read suppressed AND summary delivered
#   1  FAIL  — the hook did not intercept (raw file reached the model)
#   2  SKIP  — prerequisite missing (no codex binary, no auth, no ast-grep).
#             Non-zero and clearly logged so CI never mistakes a skip for a pass.
#
# Record a passing run (this output + `codex --version`) in the epic's closing
# notes. The epic cannot close on a bare promise.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SENTINEL="ORO_RAW_BODY_SENTINEL_8fQ2" # lives only in function bodies
SUMMARY_MARK="structural summary"     # from oro-search-hook's summaryHeader

log() { printf '[validate-codex-bash-hook] %s\n' "$*" >&2; }

# --- prerequisites → SKIP (never a silent pass) ---------------------------------
if ! command -v codex >/dev/null 2>&1; then
	log "SKIP: no 'codex' binary on PATH. Run on a host with codex to certify."
	exit 2
fi
if ! command -v ast-grep >/dev/null 2>&1; then
	log "SKIP: no 'ast-grep' on PATH (oro-search-hook needs it to summarize)."
	exit 2
fi

CODEX_HOME_SRC="${CODEX_HOME:-$HOME/.codex}"
if [ ! -f "$CODEX_HOME_SRC/auth.json" ] && [ -z "${OPENAI_API_KEY:-}" ] && [ -z "${CODEX_API_KEY:-}" ]; then
	log "SKIP: no codex auth ($CODEX_HOME_SRC/auth.json or OPENAI_API_KEY/CODEX_API_KEY)."
	exit 2
fi

log "codex: $(codex --version 2>/dev/null | head -1)"

# --- isolated sandbox -----------------------------------------------------------
LAB="$(mktemp -d)"
cleanup() { rm -rf "$LAB"; }
trap cleanup EXIT

export CODEX_HOME="$LAB/codex-home"
HOOKS="$LAB/hooks"
WORK="$LAB/work"
mkdir -p "$CODEX_HOME" "$HOOKS" "$WORK"
[ -f "$CODEX_HOME_SRC/auth.json" ] && cp "$CODEX_HOME_SRC/auth.json" "$CODEX_HOME/auth.json"

# Build the hook binary from the repo under test; stage the sibling Bash-chain
# hooks so the config mirrors production (oro-search-hook runs LAST in the chain).
log "building oro-search-hook…"
go build -C "$REPO_ROOT" -o "$HOOKS/oro-search-hook" ./cmd/oro-search-hook
cp "$REPO_ROOT/assets/hooks/enforce_skills.py" "$HOOKS/"
cp "$REPO_ROOT/assets/hooks/destructive_command_guard.py" "$HOOKS/"

# Large Go file: SENTINEL only in function bodies (absent from the AST summary),
# distinctive exported signatures (present in the summary).
{
	echo 'package probe'
	echo 'import "fmt"'
	for i in $(seq 0 150); do
		echo "func OroGateProbeSig${i}(ctx string, n int) (string, error) {"
		echo "    secret := \"${SENTINEL}\""
		echo "    return fmt.Sprintf(\"%s %s %d\", secret, ctx, n), nil"
		echo "}"
	done
} >"$WORK/probe.go"

# Production-shaped PreToolUse Bash chain: safety guards first, search hook LAST.
cat >"$CODEX_HOME/config.toml" <<EOF
[hooks]
PreToolUse = [
  { matcher = "Bash", hooks = [ { type = "command", command = "python3 $HOOKS/enforce_skills.py", async = false }, { type = "command", command = "python3 $HOOKS/destructive_command_guard.py", async = false }, { type = "command", command = "$HOOKS/oro-search-hook", async = false, timeoutSec = 5, statusMessage = "Searching codebase..." } ] },
]
EOF

# --- drive a real codex exec (same flags as oro's worker spawn) -----------------
EVENTS="$LAB/events.jsonl"
log "running codex exec (cat probe.go)…"
printf 'Run exactly this ONE shell command and nothing else, do not retry, then stop: cat probe.go' |
	timeout 180 codex exec \
		--skip-git-repo-check \
		--sandbox danger-full-access \
		--dangerously-bypass-hook-trust \
		-C "$WORK" --json - >"$EVENTS" 2>/dev/null || true

# --- deterministic assertions over codex's own event stream ---------------------
python3 - "$EVENTS" "$SENTINEL" "$SUMMARY_MARK" <<'PY'
import json, sys
events, sentinel, summary_mark = sys.argv[1], sys.argv[2], sys.argv[3]
raw = summ = 0
try:
    lines = open(events).read().splitlines()
except FileNotFoundError:
    print("no codex event stream produced", file=sys.stderr); sys.exit(1)
for line in lines:
    line = line.strip()
    if not line:
        continue
    try:
        item = json.loads(line).get("item", {})
    except json.JSONDecodeError:
        continue
    if item.get("type") == "command_execution" and item.get("status") == "completed":
        out = item.get("aggregated_output", "")
        if sentinel in out:
            raw += 1
        if summary_mark in out:
            summ += 1
ok = (raw == 0 and summ >= 1)
print(f"raw_read={raw} (want 0)  summary_delivered={summ} (want >=1)", file=sys.stderr)
if ok:
    print("PASS: cat intercepted — raw file suppressed, summary reached the model", file=sys.stderr)
    sys.exit(0)
print("FAIL: hook did not intercept (raw file reached the model)", file=sys.stderr)
sys.exit(1)
PY
