#!/usr/bin/env python3
"""Tests for inject_context_usage.py hook.

Tests that the hook correctly calculates context percentage using the
actual token budget rather than a hardcoded value.
"""

import json
import subprocess
import tempfile
from pathlib import Path

# Debounce file path from inject_context_usage.py
DEBOUNCE_FILE = Path("/tmp/oro-context-warn-ts")


def test_calculates_percentage_with_1m_token_budget():
    """Hook should use actual budget (1M tokens) not hardcoded 200K.

    Given: 98K tokens used with 1M token budget
    When: Hook calculates context percentage
    Then: Should report ~10% (98K/1M), not 53% (98K/200K)
    """
    # Clean up debounce file to prevent test isolation issues
    DEBOUNCE_FILE.unlink(missing_ok=True)

    # Create a mock transcript with 98K tokens used and 1M budget
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

    try:
        # Hook input with 1M token budget
        hook_input = {
            "transcript_path": transcript_path,
            "budget": 1_000_000,  # 1M tokens
        }

        # Run the hook
        result = subprocess.run(
            ["python3", str(Path.home() / ".oro/hooks/inject_context_usage.py")],
            input=json.dumps(hook_input),
            capture_output=True,
            text=True,
            timeout=5,
        )

        # Hook should NOT output anything (98K/1M = 9.8% < 50% threshold)
        assert result.returncode == 0, f"Hook failed: {result.stderr}"

        if result.stdout.strip():
            output = json.loads(result.stdout)
            # If there's output, verify it shows correct percentage
            context_msg = output.get("hookSpecificOutput", {}).get("additionalContext", "")

            # Should NOT trigger warning at 9.8%
            assert "9%" in context_msg or "10%" in context_msg, f"Expected ~10% but got: {context_msg}"
            assert "53%" not in context_msg, f"Should not show 53% (that's the bug): {context_msg}"

    finally:
        Path(transcript_path).unlink(missing_ok=True)
        # Clean up debounce file after test
        DEBOUNCE_FILE.unlink(missing_ok=True)


def test_warns_correctly_at_45_percent_with_1m_budget():
    """Hook should warn when genuinely approaching limits.

    Given: 450K tokens used with 1M token budget (45%)
    When: Hook calculates context percentage
    Then: Should trigger warning at 45% threshold
    """
    # Clean up debounce file to prevent test isolation issues
    DEBOUNCE_FILE.unlink(missing_ok=True)

    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as f:
        transcript_path = f.name

        # Write a usage entry with 450K tokens (45% of 1M)
        entry = {
            "message": {
                "model": "claude-sonnet-4-5",
                "usage": {"input_tokens": 450000, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
            }
        }
        f.write(json.dumps(entry) + "\n")

    try:
        # Create thresholds.json with 45% threshold
        with tempfile.TemporaryDirectory() as project_root:
            thresholds_file = Path(project_root) / "thresholds.json"
            thresholds_file.write_text(json.dumps({"sonnet": 45}))

            # Hook input with 1M token budget
            hook_input = {"transcript_path": transcript_path, "budget": 1_000_000, "project_root": project_root}

            # Run the hook
            result = subprocess.run(
                ["python3", str(Path.home() / ".oro/hooks/inject_context_usage.py")],
                input=json.dumps(hook_input),
                capture_output=True,
                text=True,
                timeout=5,
                cwd=project_root,
            )

            assert result.returncode == 0, f"Hook failed: {result.stderr}"
            assert result.stdout.strip(), "Hook should output warning at 45%"

            output = json.loads(result.stdout)
            context_msg = output.get("hookSpecificOutput", {}).get("additionalContext", "")

            # Should show 45% (450K/1M)
            assert "45%" in context_msg, f"Expected 45% warning but got: {context_msg}"
            assert "450,000" in context_msg, f"Expected 450K tokens but got: {context_msg}"
            assert "1,000,000" in context_msg, f"Expected 1M budget but got: {context_msg}"

    finally:
        Path(transcript_path).unlink(missing_ok=True)
        # Clean up debounce file after test
        DEBOUNCE_FILE.unlink(missing_ok=True)


if __name__ == "__main__":
    test_calculates_percentage_with_1m_token_budget()
    test_warns_correctly_at_45_percent_with_1m_budget()
    print("All tests passed!")
