#!/usr/bin/env python3
"""Regression tests for SessionStart hook wiring in the dogfood configs.

Codex strictly requires every SessionStart-registered hook to emit valid JSON
on stdout; empty output is rejected with "hook returned invalid session start
JSON output". Claude Code tolerates empty output, so a hook that is silent on
SessionStart (e.g. the PreToolUse-only ``enforce_skills.py`` guard) fails only
on Codex.

These tests pin the invariant across both committed dogfood configs
(``.codex/hooks.json`` and ``.claude/settings.json``): every command wired
under ``SessionStart`` must produce non-empty, parseable JSON for a SessionStart
event. This is what regressed when ``enforce_skills.py`` was left in the
SessionStart group.

Run: uv run pytest tests/test_sessionstart_hook_wiring.py -v
"""

from __future__ import annotations

import json
import shlex
import subprocess
import sys
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parent.parent

# (config path, hooks dir) for each runtime's committed dogfood config.
_CONFIGS = [
    (REPO_ROOT / ".codex" / "hooks.json", REPO_ROOT / ".codex" / "hooks"),
    (REPO_ROOT / ".claude" / "settings.json", REPO_ROOT / ".claude" / "hooks"),
]


def _session_start_fixture(source: str) -> dict:
    """Codex-shaped SessionStart event (superset of the Claude shape)."""
    return {
        "session_id": "sess-regression",
        "source": source,
        "turn_id": "turn-001",
        "transcript_path": "/nonexistent/transcript.jsonl",
    }


def _resolve_command(command: str, hooks_dir: Path) -> list[str]:
    """Build an executable argv that runs the hook from the repo copy.

    Command strings vary (absolute vs. relative paths, python3 vs. shell), so we
    match on the script basename and run the copy that lives next to the config
    under test rather than trusting the embedded path.
    """
    tokens = shlex.split(command)
    script_token = next((t for t in reversed(tokens) if "/" in t or t.endswith((".py", ".sh"))), tokens[-1])
    script = hooks_dir / Path(script_token).name
    if script.suffix == ".py":
        return [sys.executable, str(script)]
    return ["bash", str(script)]


def _sessionstart_hooks() -> list[tuple[str, str, list[str]]]:
    """Yield (config_name, source, argv) for every SessionStart-wired hook."""
    cases: list[tuple[str, str, list[str]]] = []
    for config_path, hooks_dir in _CONFIGS:
        if not config_path.is_file():
            continue
        config = json.loads(config_path.read_text())
        for group in config.get("hooks", {}).get("SessionStart", []):
            # "compact" matcher fires on a compact SessionStart; others on startup.
            source = "compact" if group.get("matcher") == "compact" else "startup"
            for entry in group.get("hooks", []):
                argv = _resolve_command(entry["command"], hooks_dir)
                cases.append((config_path.name, source, argv))
    return cases


_CASES = _sessionstart_hooks()


def test_sessionstart_hooks_are_wired() -> None:
    """The configs must actually register SessionStart hooks (guards empty parse)."""
    assert _CASES, "no SessionStart hooks found in dogfood configs"


@pytest.mark.parametrize(
    ("config_name", "source", "argv"),
    _CASES,
    ids=[f"{c}:{argv[-1].split('/')[-1]}" for c, _, argv in _CASES],
)
def test_sessionstart_hook_emits_valid_json(config_name: str, source: str, argv: list[str]) -> None:
    """Every SessionStart-registered hook emits non-empty, parseable JSON.

    Empty stdout is what Codex rejects as "invalid session start JSON output".
    """
    result = subprocess.run(
        argv,
        input=json.dumps(_session_start_fixture(source)).encode(),
        capture_output=True,
        timeout=20,
        cwd=str(REPO_ROOT),
    )
    assert result.returncode == 0, f"{config_name}:{argv[-1]} exited {result.returncode}: {result.stderr!r}"
    assert result.stdout.strip(), (
        f"{config_name} wires {argv[-1]} under SessionStart but it emits empty stdout — "
        "Codex rejects this as invalid session start JSON output. "
        "Only hooks that emit valid JSON on a SessionStart event belong here."
    )
    json.loads(result.stdout)  # raises if not valid JSON


def test_enforce_skills_not_wired_under_sessionstart() -> None:
    """enforce_skills.py is a PreToolUse guard and must not be a SessionStart hook.

    It returns early (empty stdout) for any event without a qualifying tool_name,
    which breaks Codex SessionStart. session_start_extras.py already injects the
    using-skills content on SessionStart, so this wiring is redundant too.
    """
    for config_path, _ in _CONFIGS:
        if not config_path.is_file():
            continue
        config = json.loads(config_path.read_text())
        commands = [
            entry["command"]
            for group in config.get("hooks", {}).get("SessionStart", [])
            for entry in group.get("hooks", [])
        ]
        assert not any("enforce_skills" in c for c in commands), (
            f"{config_path.name} wires enforce_skills under SessionStart — it emits empty "
            "stdout on SessionStart and breaks Codex. Remove it from the SessionStart group."
        )
