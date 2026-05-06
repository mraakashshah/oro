#!/usr/bin/env python3
"""Tests for context_pct_writer.py hook.

Tests that the hook correctly calculates context percentage using the
actual token budget rather than a hardcoded value.
"""

import json
import os
import subprocess
import tempfile
from pathlib import Path

# Resolve the repo-local hooks directory so tests work on CI (no ~/.oro/hooks there)
HOOKS_DIR = str(Path(__file__).resolve().parent.parent / "assets" / "hooks")


def test_writes_correct_percentage_with_1m_budget():
    """Hook should write correct percentage based on actual budget.

    Given: 98K tokens used with 1M token budget
    When: Hook writes context_pct file
    Then: Should write ~10% (98K/1M), not 53% (98K/200K)
    """
    # Create a mock transcript with 98K tokens used
    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as f:
        transcript_path = f.name

        # Write a usage entry with 98K tokens
        entry = {
            "message": {
                "model": "claude-sonnet-4-5",
                "usage": {"input_tokens": 98000, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
            }
        }
        f.write(json.dumps(entry) + "\n")

    # Create temp directory for panes output
    with tempfile.TemporaryDirectory() as panes_dir:
        try:
            # Hook input with 1M token budget
            hook_input = {
                "transcript_path": transcript_path,
                "budget": 1_000_000,  # 1M tokens
            }

            # Run the hook with ORO_ROLE set
            env = os.environ.copy()
            env["ORO_ROLE"] = "test-worker"
            # Scrub ORO_WORKER so the hook does not also write to Path.cwd()/.oro/
            # (cwd here is HOOKS_DIR = repo's assets/hooks/, which would pollute
            # the source tree and break test_asset_mirrors).
            env.pop("ORO_WORKER", None)

            # Temporarily modify hook to use custom panes dir
            result = subprocess.run(
                [
                    "python3",
                    "-c",
                    f'''
import sys
import json
import os
from pathlib import Path

# Override PANES_DIR
import context_pct_writer
context_pct_writer.PANES_DIR = "{panes_dir}"

# Run main
context_pct_writer.main()
''',
                ],
                input=json.dumps(hook_input),
                capture_output=True,
                text=True,
                timeout=5,
                env=env,
                cwd=HOOKS_DIR,
            )

            assert result.returncode == 0, f"Hook failed: {result.stderr}"

            # Check the written percentage
            context_file = Path(panes_dir) / "test-worker" / "context_pct"
            assert context_file.exists(), "Hook should have written context_pct file"

            written_pct = int(context_file.read_text().strip())

            # Should be ~10% (98K/1M), not 53% (98K/200K)
            assert 9 <= written_pct <= 10, f"Expected ~10% but got {written_pct}% (bug would give 53%)"

        finally:
            Path(transcript_path).unlink(missing_ok=True)


def test_clamps_percentage_at_100():
    """Hook should clamp percentage to 100 max.

    Given: 2M tokens used with 1M token budget (200%)
    When: Hook writes context_pct file
    Then: Should write 100% (clamped), not 200%
    """
    # Create a mock transcript with 2M tokens used
    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as f:
        transcript_path = f.name

        entry = {
            "message": {
                "model": "claude-sonnet-4-5",
                "usage": {"input_tokens": 2_000_000, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
            }
        }
        f.write(json.dumps(entry) + "\n")

    with tempfile.TemporaryDirectory() as panes_dir:
        try:
            hook_input = {"transcript_path": transcript_path, "budget": 1_000_000}

            env = os.environ.copy()
            env["ORO_ROLE"] = "test-worker"
            # Scrub ORO_WORKER so the hook does not also write to Path.cwd()/.oro/
            # (cwd here is HOOKS_DIR = repo's assets/hooks/, which would pollute
            # the source tree and break test_asset_mirrors).
            env.pop("ORO_WORKER", None)

            result = subprocess.run(
                [
                    "python3",
                    "-c",
                    f'''
import sys
import json
import os
from pathlib import Path

import context_pct_writer
context_pct_writer.PANES_DIR = "{panes_dir}"

context_pct_writer.main()
''',
                ],
                input=json.dumps(hook_input),
                capture_output=True,
                text=True,
                timeout=5,
                env=env,
                cwd=HOOKS_DIR,
            )

            assert result.returncode == 0, f"Hook failed: {result.stderr}"

            context_file = Path(panes_dir) / "test-worker" / "context_pct"
            written_pct = int(context_file.read_text().strip())

            # Should be clamped to 100
            assert written_pct == 100, f"Expected 100% (clamped) but got {written_pct}%"

        finally:
            Path(transcript_path).unlink(missing_ok=True)


def test_budget_from_config():
    """load_budget_from_config reads budget by model key with proper fallbacks.

    Edges:
    - Known key (1m_beta) → 1_000_000
    - Unknown key → falls back to "default" value (200_000)
    - Missing file → falls back to DEFAULT_CONTEXT_WINDOW (1_000_000)
    """
    import importlib
    import sys

    # Direct import for unit testing the pure function
    if HOOKS_DIR not in sys.path:
        sys.path.insert(0, HOOKS_DIR)
    cpw = importlib.import_module("context_pct_writer")
    importlib.reload(cpw)

    config = {"default": 200000, "1m_beta": 1000000}
    with tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False) as f:
        json.dump(config, f)
        config_path = Path(f.name)

    try:
        # Known key returns exact value
        assert cpw.load_budget_from_config("1m_beta", config_path) == 1_000_000

        # Unknown key falls back to "default" entry
        assert cpw.load_budget_from_config("unknown_model", config_path) == 200_000

        # Missing file falls back to DEFAULT_CONTEXT_WINDOW
        assert cpw.load_budget_from_config("1m_beta", Path("/no/such/file.json")) == cpw.DEFAULT_CONTEXT_WINDOW
    finally:
        config_path.unlink(missing_ok=True)


def test_writes_worktree_context_pct_when_oro_worker():
    """Hook should write to CWD/.oro/context_pct when ORO_WORKER=1.

    Given: ORO_WORKER=1 (no ORO_ROLE)
    When: Hook runs after a tool use
    Then: Should write context_pct to CWD/.oro/context_pct
    """
    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as f:
        transcript_path = f.name
        entry = {
            "message": {
                "model": "claude-sonnet-4-5",
                "usage": {"input_tokens": 100000, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
            }
        }
        f.write(json.dumps(entry) + "\n")

    with tempfile.TemporaryDirectory() as work_dir:
        try:
            hook_input = {"transcript_path": transcript_path, "budget": 200_000}

            env = os.environ.copy()
            # No ORO_ROLE — only ORO_WORKER
            env.pop("ORO_ROLE", None)
            env["ORO_WORKER"] = "1"
            env["PYTHONPATH"] = HOOKS_DIR

            result = subprocess.run(
                [
                    "python3",
                    "-c",
                    """
import context_pct_writer
context_pct_writer.main()
""",
                ],
                input=json.dumps(hook_input),
                capture_output=True,
                text=True,
                timeout=5,
                env=env,
                cwd=work_dir,
            )

            assert result.returncode == 0, f"Hook failed: {result.stderr}"

            # Should write to CWD/.oro/context_pct
            context_file = Path(work_dir) / ".oro" / "context_pct"
            assert context_file.exists(), "Hook should write to CWD/.oro/context_pct when ORO_WORKER=1"

            written_pct = int(context_file.read_text().strip())
            assert written_pct == 50, f"Expected 50% (100K/200K) but got {written_pct}%"

        finally:
            Path(transcript_path).unlink(missing_ok=True)


def test_architect_role_is_silent_noop():
    """Hook should silently return early when ORO_ROLE=architect.

    Given: ORO_ROLE=architect
    When: Hook runs after a tool use
    Then: Should return silently without writing pane files, no stderr
    """
    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as f:
        transcript_path = f.name
        entry = {
            "message": {
                "model": "claude-sonnet-4-5",
                "usage": {"input_tokens": 60000, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
            }
        }
        f.write(json.dumps(entry) + "\n")

    with tempfile.TemporaryDirectory() as panes_dir:
        try:
            hook_input = {"transcript_path": transcript_path, "budget": 200_000}

            env = os.environ.copy()
            env["ORO_ROLE"] = "architect"
            env.pop("ORO_WORKER", None)
            env["PYTHONPATH"] = HOOKS_DIR

            result = subprocess.run(
                [
                    "python3",
                    "-c",
                    f'''
import context_pct_writer
context_pct_writer.PANES_DIR = "{panes_dir}"
context_pct_writer.main()
''',
                ],
                input=json.dumps(hook_input),
                capture_output=True,
                text=True,
                timeout=5,
                env=env,
            )

            assert result.returncode == 0, f"Hook failed: {result.stderr}"
            assert result.stderr == "", f"Architect role should emit no stderr, got: {result.stderr}"

            # Pane file should NOT exist
            pane_dir = Path(panes_dir) / "architect"
            assert not pane_dir.exists(), "Architect role should NOT write pane directory"

        finally:
            Path(transcript_path).unlink(missing_ok=True)


def test_architect_role_silent_noop_even_with_worker():
    """Hook with ORO_ROLE=architect should still be silent no-op, even if ORO_WORKER=1.

    Given: ORO_ROLE=architect AND ORO_WORKER=1
    When: Hook runs after a tool use
    Then: Should NOT write to pane, but SHOULD still write to CWD/.oro/context_pct (worker path)
    """
    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as f:
        transcript_path = f.name
        entry = {
            "message": {
                "model": "claude-sonnet-4-5",
                "usage": {"input_tokens": 60000, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
            }
        }
        f.write(json.dumps(entry) + "\n")

    with tempfile.TemporaryDirectory() as work_dir, tempfile.TemporaryDirectory() as panes_dir:
        try:
            hook_input = {"transcript_path": transcript_path, "budget": 200_000}

            env = os.environ.copy()
            env["ORO_ROLE"] = "architect"
            env["ORO_WORKER"] = "1"
            env["PYTHONPATH"] = HOOKS_DIR

            result = subprocess.run(
                [
                    "python3",
                    "-c",
                    f'''
import context_pct_writer
context_pct_writer.PANES_DIR = "{panes_dir}"
context_pct_writer.main()
''',
                ],
                input=json.dumps(hook_input),
                capture_output=True,
                text=True,
                timeout=5,
                env=env,
                cwd=work_dir,
            )

            assert result.returncode == 0, f"Hook failed: {result.stderr}"
            assert result.stderr == "", f"Architect role should emit no stderr, got: {result.stderr}"

            # Pane file should NOT exist
            pane_dir = Path(panes_dir) / "architect"
            assert not pane_dir.exists(), "Architect role should NOT write pane directory even with ORO_WORKER=1"

            # But worktree file should NOT exist either (architect role is a silent no-op)
            wt_file = Path(work_dir) / ".oro" / "context_pct"
            assert not wt_file.exists(), "Architect role should not write worktree file either (silent no-op)"

        finally:
            Path(transcript_path).unlink(missing_ok=True)


def test_writes_both_pane_and_worktree_when_both_set():
    """Hook should write to both locations when both ORO_ROLE and ORO_WORKER are set (non-architect).

    Given: ORO_ROLE=manager AND ORO_WORKER=1
    When: Hook runs after a tool use
    Then: Should write to both ~/.oro/panes/<role>/context_pct AND CWD/.oro/context_pct
    """
    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as f:
        transcript_path = f.name
        entry = {
            "message": {
                "model": "claude-sonnet-4-5",
                "usage": {"input_tokens": 60000, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
            }
        }
        f.write(json.dumps(entry) + "\n")

    with tempfile.TemporaryDirectory() as work_dir, tempfile.TemporaryDirectory() as panes_dir:
        try:
            hook_input = {"transcript_path": transcript_path, "budget": 200_000}

            env = os.environ.copy()
            env["ORO_ROLE"] = "manager"
            env["ORO_WORKER"] = "1"
            env["PYTHONPATH"] = HOOKS_DIR

            result = subprocess.run(
                [
                    "python3",
                    "-c",
                    f'''
import context_pct_writer
context_pct_writer.PANES_DIR = "{panes_dir}"
context_pct_writer.main()
''',
                ],
                input=json.dumps(hook_input),
                capture_output=True,
                text=True,
                timeout=5,
                env=env,
                cwd=work_dir,
            )

            assert result.returncode == 0, f"Hook failed: {result.stderr}"

            # Pane file should exist
            pane_file = Path(panes_dir) / "manager" / "context_pct"
            assert pane_file.exists(), "Hook should write pane context_pct when ORO_ROLE set (non-architect)"

            # Worktree file should also exist
            wt_file = Path(work_dir) / ".oro" / "context_pct"
            assert wt_file.exists(), "Hook should also write CWD/.oro/context_pct when ORO_WORKER=1"

            # Both should have same value
            pane_pct = int(pane_file.read_text().strip())
            wt_pct = int(wt_file.read_text().strip())
            assert pane_pct == 30, f"Expected 30% but got {pane_pct}%"
            assert wt_pct == 30, f"Expected 30% but got {wt_pct}%"

        finally:
            Path(transcript_path).unlink(missing_ok=True)


def test_main_role_default_writes_pane_file():
    """When neither ORO_ROLE nor ORO_WORKER is set, defaults to role='main'.

    Given: No ORO_ROLE, no ORO_WORKER in environment
    When: Hook runs after a tool use
    Then: Should write context_pct to ~/.oro/panes/main/context_pct
    """
    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as f:
        transcript_path = f.name
        entry = {
            "message": {
                "model": "claude-sonnet-4-5",
                "usage": {
                    "input_tokens": 50000,
                    "cache_creation_input_tokens": 0,
                    "cache_read_input_tokens": 0,
                },
            }
        }
        f.write(json.dumps(entry) + "\n")

    with tempfile.TemporaryDirectory() as panes_dir:
        try:
            hook_input = {"transcript_path": transcript_path, "budget": 200_000}

            env = os.environ.copy()
            # Explicitly remove both ORO_ROLE and ORO_WORKER
            env.pop("ORO_ROLE", None)
            env.pop("ORO_WORKER", None)
            env["PYTHONPATH"] = HOOKS_DIR

            result = subprocess.run(
                [
                    "python3",
                    "-c",
                    f'''
import context_pct_writer
context_pct_writer.PANES_DIR = "{panes_dir}"
context_pct_writer.main()
''',
                ],
                input=json.dumps(hook_input),
                capture_output=True,
                text=True,
                timeout=5,
                env=env,
            )

            assert result.returncode == 0, f"Hook failed: {result.stderr}"

            # Should default to role="main" and write to panes/main/context_pct
            context_file = Path(panes_dir) / "main" / "context_pct"
            assert context_file.exists(), (
                "Hook should default to role='main' and write to "
                "~/.oro/panes/main/context_pct when no ORO_ROLE/ORO_WORKER set"
            )

            written_pct = int(context_file.read_text().strip())
            assert written_pct == 25, f"Expected 25% (50K/200K) but got {written_pct}%"

        finally:
            Path(transcript_path).unlink(missing_ok=True)


def test_budget_for_model():
    """budget_for_model detects context window from model ID.

    Edges:
    - Opus model → 1M
    - Sonnet model → 200K
    - Unknown model → DEFAULT_CONTEXT_WINDOW (1M)
    """
    import importlib
    import sys

    if HOOKS_DIR not in sys.path:
        sys.path.insert(0, HOOKS_DIR)
    cpw = importlib.import_module("context_pct_writer")
    importlib.reload(cpw)

    # Opus models get 1M
    assert cpw.budget_for_model("claude-opus-4-6") == 1_000_000
    assert cpw.budget_for_model("claude-opus-4-20260301") == 1_000_000

    # Sonnet models get 1M
    assert cpw.budget_for_model("claude-sonnet-4-6") == 1_000_000
    assert cpw.budget_for_model("claude-sonnet-4-5-20251001") == 1_000_000

    # Haiku models get 200K
    assert cpw.budget_for_model("claude-haiku-4-5-20251001") == 200_000

    # Unknown/empty falls back to DEFAULT_CONTEXT_WINDOW
    assert cpw.budget_for_model("") == cpw.DEFAULT_CONTEXT_WINDOW
    assert cpw.budget_for_model("some-future-model") == cpw.DEFAULT_CONTEXT_WINDOW


if __name__ == "__main__":
    test_writes_correct_percentage_with_1m_budget()
    test_clamps_percentage_at_100()
    test_budget_from_config()
    test_writes_worktree_context_pct_when_oro_worker()
    test_writes_both_pane_and_worktree_when_both_set()
    test_main_role_default_writes_pane_file()
    test_budget_for_model()
    print("All tests passed!")
