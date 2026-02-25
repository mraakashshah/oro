#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""SessionStart hook (matcher: compact): read state saved by pre_compact.py.

After compaction completes, this hook reads the saved state from
~/.oro/compaction-state/<session_id>.json and injects it as additionalContext
so the post-compaction agent knows what was in progress.

Input:  JSON with session_id, source ("compact")
Output: JSON with additionalContext: "..."
"""

from __future__ import annotations

import contextlib
import json
import os
import subprocess
import sys
from pathlib import Path


def main() -> None:
    """Main hook entry point."""
    try:
        input_data = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        print(json.dumps({}))
        return

    session_id = input_data.get("session_id", "")
    if not session_id:
        print(json.dumps({}))
        return

    state_path = Path.home() / ".oro" / "compaction-state" / f"{session_id}.json"
    if not state_path.exists():
        print(json.dumps({}))
        return

    try:
        state = json.loads(state_path.read_text())
    except (json.JSONDecodeError, OSError):
        print(json.dumps({}))
        return

    # Build continuation context
    lines = ["Resuming after compaction. Previous state:"]

    bead_id = state.get("bead_id")
    if bead_id:
        lines.append(f"  Bead in progress: {bead_id}")

    files = state.get("files_modified", [])
    if files:
        lines.append(f"  Files modified: {', '.join(files[:10])}")

    errors = state.get("errors", [])
    if errors:
        lines.append(f"  Recent errors: {'; '.join(errors[:3])}")

    last_msg = state.get("last_assistant_message", "")
    if last_msg:
        lines.append(f"  Last context: {last_msg[:200]}")

    tool_calls = state.get("last_tool_calls", [])
    if tool_calls:
        tc_summary = ", ".join(tc.get("name", "?") for tc in tool_calls)
        lines.append(f"  Recent tools: {tc_summary}")

    # Create continuation bead if bead_id present and ORO_WORKER is set
    if bead_id and os.environ.get("ORO_WORKER") == "1":
        _create_continuation_bead(bead_id, state)

    # Clean up state file
    with contextlib.suppress(OSError):
        state_path.unlink()

    context = "\n".join(lines)
    print(json.dumps({"additionalContext": context}))


def _create_continuation_bead(
    bead_id: str,
    state: dict,
) -> None:
    """Create a continuation bead for the dispatcher to pick up."""
    files = ", ".join(state.get("files_modified", [])[:5])
    last_msg = state.get("last_assistant_message", "")[:200]
    description = f"Continue work from compacted session.\nFiles: {files}\nContext: {last_msg}"

    with contextlib.suppress(OSError, subprocess.TimeoutExpired):
        subprocess.run(
            [
                "bd",
                "create",
                f"--title=Continue: {bead_id}",
                "--type=task",
                f"--parent={bead_id}",
                f"--description={description}",
            ],
            capture_output=True,
            timeout=10,
            check=False,
        )


if __name__ == "__main__":
    main()
