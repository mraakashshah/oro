#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""PreCompact hook: extract structured state from transcript before compaction.

Parses the transcript JSONL to capture in-progress work state, then saves it
to ~/.oro/compaction-state/<session_id>.yaml. The companion SessionStart hook
(session_start_compact.py) reads this state after compaction completes.

Input:  JSON with session_id, transcript_path, trigger, cwd
Output: JSON with continue: true, systemMessage: "..."
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any


def parse_transcript(transcript_path: Path) -> dict[str, Any]:
    """Parse transcript JSONL and extract structured state."""
    state: dict[str, Any] = {
        "last_tool_calls": [],
        "files_modified": [],
        "bead_id": None,
        "errors": [],
        "last_assistant_message": "",
    }

    if not transcript_path.exists():
        return state

    try:
        content = transcript_path.read_text()
    except Exception:
        return state

    all_tool_calls: list[dict[str, str]] = []
    modified_files: set[str] = set()
    errors: list[str] = []
    last_assistant = ""
    bead_id: str | None = None

    for line in content.split("\n"):
        line = line.strip()
        if not line:
            continue

        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue

        # Extract last assistant message
        if entry.get("role") == "assistant" and isinstance(entry.get("content"), str):
            last_assistant = entry["content"]

        # Extract tool calls
        tool_name = entry.get("tool_name") or (entry.get("name") if entry.get("type") == "tool_use" else None)
        if tool_name:
            tool_input = entry.get("tool_input", {})

            all_tool_calls.append({"name": tool_name, "input_summary": _summarize_input(tool_input)})

            # Track file modifications
            if tool_name.lower() in ("edit", "write"):
                file_path = tool_input.get("file_path") or tool_input.get("path")
                if file_path and isinstance(file_path, str):
                    modified_files.add(file_path)

            # Track bead status updates (bd update --status=in_progress)
            if tool_name.lower() == "bash":
                cmd = tool_input.get("command", "")
                if "bd update" in cmd and "in_progress" in cmd:
                    # Extract bead ID from command like "bd update oro-xxx --status in_progress"
                    parts = cmd.split()
                    for i, p in enumerate(parts):
                        if p == "update" and i + 1 < len(parts):
                            bead_id = parts[i + 1]
                            break

        # Extract errors from tool results
        if entry.get("type") == "tool_result" or entry.get("tool_result") is not None:
            result = entry.get("tool_result", {})
            if isinstance(result, dict):
                exit_code = result.get("exit_code") or result.get("exitCode")
                if exit_code is not None and exit_code != 0:
                    error_msg = result.get("stderr") or result.get("error") or "Command failed"
                    errors.append(str(error_msg)[:200])

    state["last_tool_calls"] = [{"name": tc["name"], "input": tc["input_summary"]} for tc in all_tool_calls[-5:]]
    state["files_modified"] = sorted(modified_files)
    state["bead_id"] = bead_id
    state["errors"] = errors[-5:]
    state["last_assistant_message"] = last_assistant[:500]

    return state


def _summarize_input(tool_input: dict[str, Any]) -> str:
    """Produce a short summary of tool input."""
    if not tool_input:
        return ""
    # For Bash, show the command
    if "command" in tool_input:
        return str(tool_input["command"])[:100]
    # For Read/Edit/Write, show the file path
    if "file_path" in tool_input:
        return str(tool_input["file_path"])
    # Fallback: truncated JSON
    return json.dumps(tool_input)[:100]


def save_state(session_id: str, state: dict[str, Any]) -> Path:
    """Save extracted state to ~/.oro/compaction-state/<session_id>.json."""
    state_dir = Path.home() / ".oro" / "compaction-state"
    state_dir.mkdir(parents=True, exist_ok=True)
    state_path = state_dir / f"{session_id}.json"
    state_path.write_text(json.dumps(state, indent=2))
    return state_path


def main() -> None:
    """Main hook entry point."""
    try:
        input_data = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        print(json.dumps({"continue": True}))
        return

    session_id = input_data.get("session_id", "")
    transcript_path_str = input_data.get("transcript_path", "")

    if not session_id or not transcript_path_str:
        print(json.dumps({"continue": True}))
        return

    transcript_path = Path(transcript_path_str)
    state = parse_transcript(transcript_path)

    # Save state to disk for session_start_compact.py to read
    state_path = save_state(session_id, state)

    # Build a summary for the post-compaction agent
    files = ", ".join(state["files_modified"][:5]) or "none"
    bead = state["bead_id"] or "none"
    msg = (
        f"Session was compacted. State saved to {state_path}.\n"
        f"Bead in progress: {bead}\n"
        f"Files modified: {files}\n"
        f"Run `bd ready` to check for continuation work."
    )

    print(json.dumps({"continue": True, "systemMessage": msg}))


if __name__ == "__main__":
    main()
