"""pytest configuration for oro tests."""

import os
from pathlib import Path

import pytest


def pytest_collection_modifyitems(config, items):
    """Skip tests that require ORO_HOME when hooks are missing."""
    oro_home = Path(os.environ.get("ORO_HOME", Path.home() / ".oro"))
    hooks_dir = oro_home / "hooks"

    # Tests that require oro hooks to exist
    hook_dependent_tests = [
        "test_inject_context_usage.py",
        "test_memory_capture.py",
        "test_session_start_extras.py",
        "test_validate_agent_completion.py",
        "test_worktree_guard.py",
    ]

    # Skip hook-dependent tests if hooks directory doesn't exist
    if not hooks_dir.exists():
        skip_oro = pytest.mark.skip(reason="requires ~/.oro/hooks/ directory")
        for item in items:
            if any(test_file in str(item.fspath) for test_file in hook_dependent_tests):
                item.add_marker(skip_oro)
