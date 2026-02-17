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


if __name__ == "__main__":
    test_writes_correct_percentage_with_1m_budget()
    test_clamps_percentage_at_100()
    print("All tests passed!")
