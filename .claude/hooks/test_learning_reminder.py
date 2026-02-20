"""Tests for learning_reminder.py — PostToolUse hook for learning reminders on git commit."""

import json
import subprocess
import sys
from pathlib import Path

HOOK_SCRIPT = str(Path(__file__).parent / "learning_reminder.py")

SAMPLE_ENTRIES = [
    {
        "key": "learned-use-absolute-paths",
        "type": "learned",
        "content": "Use absolute paths when working with worktrees",
        "bead": "oro-abc",
        "tags": ["git", "cli"],
        "ts": "2026-02-08T10:00:00+00:00",
    },
    {
        "key": "learned-pytest-fixtures",
        "type": "learned",
        "content": "Pytest fixtures are preferred over setup/teardown methods",
        "bead": "oro-abc",
        "tags": ["python", "pytest", "test"],
        "ts": "2026-02-08T11:00:00+00:00",
    },
    {
        "key": "learned-docker-layer-caching",
        "type": "learned",
        "content": "Docker layer caching speeds up builds significantly",
        "bead": "oro-def",
        "tags": ["docker", "performance"],
        "ts": "2026-02-09T12:00:00+00:00",
    },
]


def _run_hook(hook_input: dict, knowledge_file: str | None = None) -> dict | None:
    """Run the learning_reminder.py hook with given input, return parsed JSON output or None."""
    env = {}
    if knowledge_file:
        env["ORO_KNOWLEDGE_FILE"] = knowledge_file

    import os

    full_env = {**os.environ, **env}

    result = subprocess.run(
        [sys.executable, HOOK_SCRIPT],
        input=json.dumps(hook_input),
        capture_output=True,
        text=True,
        env=full_env,
    )

    stdout = result.stdout.strip()
    if not stdout:
        return None
    return json.loads(stdout)


def _write_knowledge(path: Path, entries: list[dict]) -> None:
    """Write sample entries to a knowledge JSONL file."""
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w") as f:
        for entry in entries:
            f.write(json.dumps(entry) + "\n")


# --- Tests ---


def test_commit_with_bead_surfaces_learnings(tmp_path: Path) -> None:
    """Git commit with bead ref injects reminder when LEARNED entries exist."""
    knowledge_file = tmp_path / "knowledge.jsonl"
    _write_knowledge(knowledge_file, SAMPLE_ENTRIES)

    hook_input = {
        "tool_name": "Bash",
        "tool_input": {
            "command": 'git commit -m "feat(hooks): add memory capture (bd-abc)"',
        },
    }

    output = _run_hook(hook_input, str(knowledge_file))

    assert output is not None
    context = output["hookSpecificOutput"]["additionalContext"]
    assert "2 undocumented learning" in context
    assert "oro-abc" in context
    assert "decisions&discoveries.md" in context or "oro remember" in context


def test_commit_with_bead_no_learnings(tmp_path: Path) -> None:
    """Git commit with bead ref but no matching knowledge entries produces no output."""
    knowledge_file = tmp_path / "knowledge.jsonl"
    _write_knowledge(knowledge_file, SAMPLE_ENTRIES)

    hook_input = {
        "tool_name": "Bash",
        "tool_input": {
            "command": 'git commit -m "fix(core): repair widget (bd-zzz)"',
        },
    }

    output = _run_hook(hook_input, str(knowledge_file))
    assert output is None


def test_non_commit_command() -> None:
    """Non-commit bash commands produce no output."""
    hook_input = {
        "tool_name": "Bash",
        "tool_input": {
            "command": "ls -la",
        },
    }

    output = _run_hook(hook_input)
    assert output is None


def test_non_bash_tool() -> None:
    """Non-Bash tool_name produces no output."""
    hook_input = {
        "tool_name": "Read",
        "tool_input": {
            "file_path": "/some/file.py",
        },
    }

    output = _run_hook(hook_input)
    assert output is None


def test_commit_with_bd_dash_prefix(tmp_path: Path) -> None:
    """Git commit with bd-xyz (no parens) also matches."""
    knowledge_file = tmp_path / "knowledge.jsonl"
    _write_knowledge(knowledge_file, SAMPLE_ENTRIES)

    hook_input = {
        "tool_name": "Bash",
        "tool_input": {
            "command": 'git commit -m "feat(hooks): add feature bd-abc"',
        },
    }

    output = _run_hook(hook_input, str(knowledge_file))

    assert output is not None
    context = output["hookSpecificOutput"]["additionalContext"]
    assert "2 undocumented learning" in context
    assert "oro-abc" in context


def test_commit_with_dotted_bead_id(tmp_path: Path) -> None:
    """Git commit with dotted bead id like bd-zw5.4 works."""
    knowledge_file = tmp_path / "knowledge.jsonl"
    entries = [
        {
            "key": "learned-something",
            "type": "learned",
            "content": "Something important",
            "bead": "oro-zw5.4",
            "tags": ["python"],
            "ts": "2026-02-10T10:00:00+00:00",
        },
    ]
    _write_knowledge(knowledge_file, entries)

    hook_input = {
        "tool_name": "Bash",
        "tool_input": {
            "command": 'git commit -m "feat(hooks): learning reminder (bd-zw5.4)"',
        },
    }

    output = _run_hook(hook_input, str(knowledge_file))

    assert output is not None
    context = output["hookSpecificOutput"]["additionalContext"]
    assert "1 undocumented learning" in context
    assert "oro-zw5.4" in context


def test_empty_knowledge_file(tmp_path: Path) -> None:
    """Empty knowledge file produces no output."""
    knowledge_file = tmp_path / "knowledge.jsonl"
    knowledge_file.write_text("")

    hook_input = {
        "tool_name": "Bash",
        "tool_input": {
            "command": 'git commit -m "feat: something (bd-abc)"',
        },
    }

    output = _run_hook(hook_input, str(knowledge_file))
    assert output is None


def test_missing_knowledge_file(tmp_path: Path) -> None:
    """Missing knowledge file produces no output."""
    knowledge_file = tmp_path / "nonexistent" / "knowledge.jsonl"

    hook_input = {
        "tool_name": "Bash",
        "tool_input": {
            "command": 'git commit -m "feat: something (bd-abc)"',
        },
    }

    output = _run_hook(hook_input, str(knowledge_file))
    assert output is None


def test_frequency_codification_proposal(tmp_path: Path) -> None:
    """When a tag appears 3+ times in knowledge.jsonl, additionalContext includes codification proposal."""
    knowledge_file = tmp_path / "knowledge.jsonl"
    # Create entries where "git" tag appears 3 times total across different beads
    entries = [
        {
            "key": "learned-git-worktree-paths",
            "type": "learned",
            "content": "Use absolute paths when working with git worktrees",
            "bead": "oro-aaa",
            "tags": ["git", "cli"],
            "ts": "2026-02-07T10:00:00+00:00",
        },
        {
            "key": "learned-git-rebase-worktree",
            "type": "learned",
            "content": "Git rebase fails if branch is checked out in any worktree",
            "bead": "oro-bbb",
            "tags": ["git"],
            "ts": "2026-02-08T10:00:00+00:00",
        },
        {
            "key": "learned-git-stash-worktree",
            "type": "learned",
            "content": "Git stash does not work across worktrees",
            "bead": "oro-ccc",
            "tags": ["git"],
            "ts": "2026-02-09T10:00:00+00:00",
        },
    ]
    _write_knowledge(knowledge_file, entries)

    hook_input = {
        "tool_name": "Bash",
        "tool_input": {
            "command": 'git commit -m "fix(worktree): handle stash edge case (bd-ccc)"',
        },
    }

    output = _run_hook(hook_input, str(knowledge_file))

    assert output is not None
    context = output["hookSpecificOutput"]["additionalContext"]
    # Must mention the bead's undocumented learnings
    assert "oro-ccc" in context
    # Must include a codification proposal for the high-frequency tag
    assert "codif" in context.lower()  # "codification" or "codify"
    assert "git" in context  # the tag that hit 3+
    assert "3" in context  # the frequency count
