#!/usr/bin/env bash
# Test: verify that make install-git-hooks installs working hooks
# RED/GREEN test for oro-ozbz acceptance criteria

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"
PASS=0
FAIL=0

ok() {
    echo "  ✓ $1"
    PASS=$((PASS + 1))
}

fail() {
    echo "  ✗ $1"
    FAIL=$((FAIL + 1))
}

echo "=== git hooks test ==="

# AC1: canonical location exists at git/hooks/
echo ""
echo "--- AC1: canonical location ---"

if [ -d "$REPO_ROOT/git/hooks" ]; then
    ok "git/hooks/ directory exists"
else
    fail "git/hooks/ directory does NOT exist"
fi

if [ -f "$REPO_ROOT/git/hooks/pre-commit" ]; then
    ok "git/hooks/pre-commit exists"
else
    fail "git/hooks/pre-commit does NOT exist"
fi

if [ -f "$REPO_ROOT/git/hooks/pre-push" ]; then
    ok "git/hooks/pre-push exists"
else
    fail "git/hooks/pre-push does NOT exist"
fi

if [ -x "$REPO_ROOT/git/hooks/pre-commit" ]; then
    ok "git/hooks/pre-commit is executable"
else
    fail "git/hooks/pre-commit is NOT executable"
fi

if [ -x "$REPO_ROOT/git/hooks/pre-push" ]; then
    ok "git/hooks/pre-push is executable"
else
    fail "git/hooks/pre-push is NOT executable"
fi

# AC3: pre-push contains the "all checks" string
echo ""
echo "--- AC3: pre-push check count string ---"

if grep -q "all checks" "$REPO_ROOT/git/hooks/pre-push" 2>/dev/null; then
    ok "pre-push contains 'all checks' string"
else
    fail "pre-push does NOT contain 'all checks' string"
fi

# AC2: Makefile has install-git-hooks target
echo ""
echo "--- AC2: Makefile target ---"

if grep -q "install-git-hooks" "$REPO_ROOT/Makefile"; then
    ok "Makefile has install-git-hooks target"
else
    fail "Makefile does NOT have install-git-hooks target"
fi

# AC2: make install-git-hooks installs symlinks into a temp .git/hooks/
# Simulate a fresh clone by creating a temp git repo, symlinking in our Makefile+git/hooks,
# then running make install-git-hooks to verify it works end-to-end.
echo ""
echo "--- AC2: make install-git-hooks installs hooks ---"

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

# Init a minimal git repo in tmp
git init "$TMP_DIR/test-repo" --quiet 2>/dev/null

# Symlink our git/hooks/ and Makefile in
ln -s "$REPO_ROOT/git" "$TMP_DIR/test-repo/git"
ln -s "$REPO_ROOT/Makefile" "$TMP_DIR/test-repo/Makefile"

# Run make install-git-hooks from within the simulated repo
if make install-git-hooks --no-print-directory -C "$TMP_DIR/test-repo" 2>/dev/null; then
    ok "make install-git-hooks succeeded"
else
    fail "make install-git-hooks FAILED"
fi

# Verify hooks were installed (as symlinks)
if [ -e "$TMP_DIR/test-repo/.git/hooks/pre-commit" ]; then
    ok ".git/hooks/pre-commit installed"
else
    fail ".git/hooks/pre-commit NOT installed"
fi

if [ -e "$TMP_DIR/test-repo/.git/hooks/pre-push" ]; then
    ok ".git/hooks/pre-push installed"
else
    fail ".git/hooks/pre-push NOT installed"
fi

# AC4: check count string persists after install
if grep -q "all checks" "$TMP_DIR/test-repo/.git/hooks/pre-push" 2>/dev/null; then
    ok "pre-push 'all checks' string persists after install"
else
    fail "pre-push 'all checks' string does NOT persist after install"
fi

# AC4: installed hooks are executable
if [ -x "$TMP_DIR/test-repo/.git/hooks/pre-push" ]; then
    ok ".git/hooks/pre-push is executable after install"
else
    fail ".git/hooks/pre-push is NOT executable after install"
fi

if [ -x "$TMP_DIR/test-repo/.git/hooks/pre-commit" ]; then
    ok ".git/hooks/pre-commit is executable after install"
else
    fail ".git/hooks/pre-commit is NOT executable after install"
fi

# Summary
echo ""
echo "Passed: $PASS"
echo "Failed: $FAIL"
echo ""

if [ "$FAIL" -gt 0 ]; then
    echo "FAILED"
    exit 1
else
    echo "PASSED"
    exit 0
fi
