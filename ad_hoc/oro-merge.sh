#!/bin/sh
#
# oro-merge: gate + rebase + ff-only merge for agent branches
#
# Usage: ./ad_hoc/oro-merge.sh <branch-name>
#
# Runs the quality gate on the branch BEFORE merging to main.
# If the gate fails, nothing touches main.

set -e

BRANCH="${1:?Usage: oro-merge.sh <branch-name>}"
REPO_ROOT="$(git rev-parse --show-toplevel)"
GATE="$REPO_ROOT/quality_gate.sh"

if [ ! -x "$GATE" ]; then
    echo "ERROR: quality_gate.sh not found or not executable at $GATE" >&2
    exit 1
fi

# Ensure we're on main
CURRENT=$(git branch --show-current)
if [ "$CURRENT" != "main" ]; then
    echo "ERROR: must be on main (currently on $CURRENT)" >&2
    exit 1
fi

# Ensure working tree is clean
if ! git diff --quiet || ! git diff --cached --quiet; then
    echo "ERROR: working tree is dirty. Stash or commit first." >&2
    exit 1
fi

echo "=== Step 1/4: Rebase $BRANCH onto main ==="
git rebase main "$BRANCH"

echo ""
echo "=== Step 2/4: Quality gate on $BRANCH ==="
if ! "$GATE"; then
    echo ""
    echo "GATE FAILED on $BRANCH. Merge aborted."
    echo "Fix the branch, then re-run."
    git checkout main
    exit 1
fi

echo ""
echo "=== Step 3/4: FF-only merge to main ==="
git checkout main
git merge --ff-only "$BRANCH"

echo ""
echo "=== Step 4/4: Verify main ==="
echo "Merged $BRANCH to main at $(git rev-parse --short HEAD)"
echo "Run 'git push' when ready."
