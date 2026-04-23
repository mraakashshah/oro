#!/usr/bin/env bash
# Test script: Verify quality_gate.sh works when invoked in parallel from multiple worktrees.
# Reproduces the issue where two concurrent QG runs emit "parallel golangci-lint is running".

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

repo_root="$(cd "$(git rev-parse --git-common-dir)/.." && pwd)"
cd "$repo_root"

echo "Setting up test worktrees..."

# Create two temporary worktrees
wt1=$(mktemp -d "${TMPDIR:-/tmp}/oro-test-wt1-XXXXXX")
wt2=$(mktemp -d "${TMPDIR:-/tmp}/oro-test-wt2-XXXXXX")
log1=$(mktemp "${TMPDIR:-/tmp}/oro-test-log1-XXXXXX")
log2=$(mktemp "${TMPDIR:-/tmp}/oro-test-log2-XXXXXX")
trap 'rm -rf "$wt1" "$wt2" "$log1" "$log2"' EXIT

# Initialize both worktrees by cloning the repo root (worktree pattern)
git -C "$repo_root" worktree add "$wt1" HEAD 2>/dev/null || true
git -C "$repo_root" worktree add "$wt2" HEAD 2>/dev/null || true

echo "Running quality_gate.sh concurrently in both worktrees..."

# Run QG in both worktrees in parallel, capturing output to files
"$wt1/scripts/quality_gate.sh" >"$log1" 2>&1 &
pid1=$!
"$wt2/scripts/quality_gate.sh" >"$log2" 2>&1 &
pid2=$!

# Wait for both to complete (exit code is ignored — we assert on log contents below)
wait "$pid1" 2>/dev/null || true
wait "$pid2" 2>/dev/null || true

# Check if either output contains the "parallel golangci-lint is running" error
if grep -q "parallel golangci-lint is running" "$log1" 2>/dev/null; then
	printf '%bFAIL: Worktree 1 got parallel lock error:%b\n' "$RED" "$NC"
	head -20 "$log1"
	exit 1
fi

if grep -q "parallel golangci-lint is running" "$log2" 2>/dev/null; then
	printf '%bFAIL: Worktree 2 got parallel lock error:%b\n' "$RED" "$NC"
	head -20 "$log2"
	exit 1
fi

printf '%bPASS: Both concurrent QG runs completed without parallel lock error%b\n' "$GREEN" "$NC"
exit 0
