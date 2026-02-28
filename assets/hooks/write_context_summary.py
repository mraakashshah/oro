#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""Utility: write a compact context summary to .oro/context_summary.txt.

Called during the create-handoff skill, BEFORE touching .oro/handoff_done.
The dispatcher reads this file to populate ContextSummary in continuation beads
(see pkg/worker/worker.go handleHandoffExhaustion).
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path


def write_context_summary(goal: str, now: str, worktree_root: Path) -> Path:
    """Write a compact context summary to <worktree_root>/.oro/context_summary.txt.

    Args:
        goal: What this session accomplished (from handoff YAML 'goal:' field).
        now: What next session should do first (from handoff YAML 'now:' field).
        worktree_root: Root of the worktree (the directory containing .oro/).

    Returns:
        Path to the written context_summary.txt file.

    Edges:
        - Creates .oro/ if it does not exist.
        - Overwrites an existing context_summary.txt.
    """
    oro_dir = worktree_root / ".oro"
    oro_dir.mkdir(parents=True, exist_ok=True)

    summary = f"Goal: {goal}\nNext: {now}"
    summary_path = oro_dir / "context_summary.txt"
    summary_path.write_text(summary, encoding="utf-8")
    return summary_path


def main() -> None:
    """CLI entry point: python3 write_context_summary.py --goal '...' --now '...'"""
    parser = argparse.ArgumentParser(description="Write .oro/context_summary.txt for continuation bead context")
    parser.add_argument("--goal", required=True, help="Current session goal (handoff 'goal:' field)")
    parser.add_argument("--now", required=True, help="What next session should do first (handoff 'now:' field)")
    parser.add_argument(
        "--worktree",
        default=".",
        help="Worktree root directory (default: current directory)",
    )

    args = parser.parse_args()
    path = write_context_summary(args.goal, args.now, Path(args.worktree))
    print(f"Context summary written to {path}", file=sys.stderr)


if __name__ == "__main__":
    main()
