#!/usr/bin/env python3
"""SessionStart hook: surface stale beads, merged worktree cleanup, recent learnings.

Companion to enforce-skills.sh. Outputs additionalContext on SessionStart.

Pure functions for testability:
  - find_stale_beads(bd_output, days_threshold=3)
  - find_merged_worktrees(worktrees_dir, main_branch="main")
  - recent_learnings(knowledge_file, n=5)
"""

import contextlib
import json
import re
import subprocess
import sys
from datetime import UTC, datetime
from pathlib import Path

KNOWLEDGE_FILE = ".beads/memory/knowledge.jsonl"
WORKTREES_DIR = ".worktrees"

# Pattern: ◐ oro-xyz [● P2] [feature] - Title
_BEAD_LINE_RE = re.compile(r"^◐\s+([\w-]+)\s+\[")
# Pattern:   Updated: 2026-02-07
_UPDATED_RE = re.compile(r"Updated:\s*(\d{4}-\d{2}-\d{2})")


def find_stale_beads(bd_output: str, days_threshold: int = 3) -> list[dict]:
    """Parse bd list/show output and return beads not updated in >days_threshold days.

    Expects input where each bead header line is followed by a line containing
    'Updated: YYYY-MM-DD'. Returns list of dicts with id, title, days_stale.
    """
    if not bd_output.strip():
        return []

    stale = []
    now = datetime.now(UTC)
    current_id = None
    current_title = None

    for line in bd_output.splitlines():
        bead_match = _BEAD_LINE_RE.match(line)
        if bead_match:
            current_id = bead_match.group(1)
            # Extract title: everything after the last " - "
            title_parts = line.split(" - ", 1)
            current_title = title_parts[1].strip() if len(title_parts) > 1 else current_id
            continue

        if current_id:
            updated_match = _UPDATED_RE.search(line)
            if updated_match:
                updated_date = datetime.strptime(updated_match.group(1), "%Y-%m-%d").replace(tzinfo=UTC)
                days_old = (now - updated_date).days
                if days_old > days_threshold:
                    stale.append(
                        {
                            "id": current_id,
                            "title": current_title,
                            "days_stale": days_old,
                        }
                    )
                current_id = None
                current_title = None

    return stale


def find_merged_worktrees(worktrees_dir: str, main_branch: str = "main") -> list[dict]:
    """Check worktrees directory for branches already merged to main.

    Returns list of dicts with path, branch, and cleanup command.
    """
    wt_path = Path(worktrees_dir)
    if not wt_path.is_dir():
        return []

    merged = []
    for child in sorted(wt_path.iterdir()):
        if not child.is_dir():
            continue
        # Get the branch for this worktree
        try:
            result = subprocess.run(
                ["git", "rev-parse", "--abbrev-ref", "HEAD"],
                cwd=str(child),
                capture_output=True,
                text=True,
                timeout=5,
            )
            if result.returncode != 0:
                continue
            branch = result.stdout.strip()
            if not branch or branch == "HEAD":
                continue

            # Check if this branch is merged into main
            merged_result = subprocess.run(
                ["git", "branch", "--merged", main_branch],
                cwd=str(child),
                capture_output=True,
                text=True,
                timeout=5,
            )
            if merged_result.returncode != 0:
                continue

            merged_branches = [b.strip().lstrip("* ") for b in merged_result.stdout.splitlines()]
            if branch in merged_branches:
                merged.append(
                    {
                        "path": str(child),
                        "branch": branch,
                        "cleanup": f"git worktree remove {child.name} && git branch -d {branch}",
                    }
                )
        except (subprocess.TimeoutExpired, OSError):
            continue

    return merged


def recent_learnings(knowledge_file: str, n: int = 5) -> list[dict]:
    """Load the N most recent LEARNED entries from knowledge.jsonl.

    Deduplicates by key (latest wins). Returns list of dicts sorted by ts descending.
    """
    try:
        with open(knowledge_file) as f:
            lines = f.readlines()
    except OSError:
        return []

    # Parse all entries, dedup by key (latest wins)
    by_key: dict[str, dict] = {}
    for line in lines:
        line = line.strip()
        if not line:
            continue
        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue
        if entry.get("type") != "learned":
            continue
        by_key[entry["key"]] = entry

    # Sort by timestamp descending, take top n
    sorted_entries = sorted(by_key.values(), key=lambda e: e.get("ts", ""), reverse=True)
    return sorted_entries[:n]


def _format_output(stale: list[dict], merged: list[dict], learnings: list[dict]) -> str:
    """Format all findings into a single context string."""
    sections = []

    if stale:
        lines = ["## Stale Beads (no update in >3 days)"]
        for b in stale:
            lines.append(f"- **{b['id']}**: {b['title']} ({b['days_stale']} days stale)")
        lines.append("Consider: close, update status, or add a comment to keep active.")
        sections.append("\n".join(lines))

    if merged:
        lines = ["## Merged Worktrees (cleanup available)"]
        for w in merged:
            lines.append(f"- `{w['path']}` (branch: {w['branch']})")
            lines.append(f"  Cleanup: `{w['cleanup']}`")
        sections.append("\n".join(lines))

    if learnings:
        lines = ["## Recent Learnings"]
        for entry in learnings:
            tags = ", ".join(entry.get("tags", []))
            tag_str = f" ({tags})" if tags else ""
            lines.append(f"- [{entry['bead']}] {entry['content']}{tag_str}")
        sections.append("\n".join(lines))

    return "\n\n".join(sections)


def main() -> None:
    # Read hook input from stdin (SessionStart event)
    with contextlib.suppress(json.JSONDecodeError, ValueError):
        json.loads(sys.stdin.read())

    # 1. Stale bead detection
    stale = []
    try:
        bd_ids_result = subprocess.run(
            ["bd", "list", "--status=in_progress"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        if bd_ids_result.returncode == 0 and bd_ids_result.stdout.strip():
            # For each bead, get its show output to find Updated date
            bead_ids = _BEAD_LINE_RE.findall(bd_ids_result.stdout)
            show_lines = []
            for bead_id in bead_ids:
                show_result = subprocess.run(
                    ["bd", "show", bead_id],
                    capture_output=True,
                    text=True,
                    timeout=5,
                )
                if show_result.returncode == 0:
                    show_lines.append(show_result.stdout)
            combined = "\n".join(show_lines)
            stale = find_stale_beads(combined, days_threshold=3)
    except (subprocess.TimeoutExpired, OSError):
        pass

    # 2. Merged worktree cleanup
    merged = find_merged_worktrees(WORKTREES_DIR)

    # 3. Recent learnings
    learnings = recent_learnings(KNOWLEDGE_FILE)

    # Only output if there's something to report
    context = _format_output(stale, merged, learnings)
    if not context:
        return

    output = {
        "hookSpecificOutput": {
            "hookEventName": "SessionStart",
            "additionalContext": context,
        }
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
