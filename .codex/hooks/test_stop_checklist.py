#!/usr/bin/env python3
"""Tests for stop-checklist.sh non-blocking hook output."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

HOOKS_DIR = Path(__file__).parent


def _run_stop_checklist(fixture: dict[str, object]) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run(
        ["bash", str(HOOKS_DIR / "stop-checklist.sh")],
        input=json.dumps(fixture).encode(),
        capture_output=True,
        check=False,
        timeout=10,
    )


def test_codex_stop_input() -> None:
    result = _run_stop_checklist(
        {
            "hook_event_name": "Stop",
            "cwd": "/tmp/project",
            "session_id": "sess-codex-stop",
            "transcript_path": "/tmp/project/.codex/sessions/sess-codex-stop.jsonl",
        }
    )

    assert result.returncode == 0
    assert json.loads(result.stdout) == {"continue": True}


def test_user_prompt_submit_input() -> None:
    result = _run_stop_checklist(
        {
            "hook_event_name": "UserPromptSubmit",
            "cwd": "/tmp/project",
            "session_id": "sess-codex-prompt",
            "prompt": "continue",
            "transcript_path": "/tmp/project/.codex/sessions/sess-codex-prompt.jsonl",
            "turn_id": "turn-001",
        }
    )

    assert result.returncode == 0
    assert json.loads(result.stdout) == {"continue": True}
