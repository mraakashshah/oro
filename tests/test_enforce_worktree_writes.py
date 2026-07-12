"""Tests for the enforce_worktree_writes PreToolUse hook.

The hook blocks Write/Edit/NotebookEdit whose target resolves inside a git
PRIMARY checkout, enforcing the "all code in worktrees" policy so concurrent
agents never edit the shared main working tree. Writes inside a linked
worktree, outside any git repo, to allow-listed paths, or with the
ORO_ALLOW_MAIN_WRITES escape hatch are permitted.
"""

# pylint: disable=import-error
import subprocess

import pytest  # type: ignore[import-not-found]
from enforce_worktree_writes import (
    build_decision,
    classify_checkout,
    is_allowlisted,
    is_escape_hatch_set,
    nearest_existing_dir,
    target_path_for,
)


def _git(cwd, *args):
    subprocess.run(["git", "-C", str(cwd), *args], check=True, capture_output=True, text=True)


@pytest.fixture
def primary_repo(tmp_path):
    """A git primary checkout with one commit and a gitignored .worktrees/."""
    root = tmp_path / "repo"
    root.mkdir()
    _git(root, "init", "-q")
    _git(root, "config", "user.email", "t@t.co")
    _git(root, "config", "user.name", "t")
    (root / ".gitignore").write_text(".worktrees/\n")
    (root / "seed.txt").write_text("seed\n")
    _git(root, "add", "-A")
    _git(root, "commit", "-q", "-m", "init")
    return root


@pytest.fixture
def linked_worktree(primary_repo):
    """A linked worktree at <root>/.worktrees/wt on branch wt."""
    wt = primary_repo / ".worktrees" / "wt"
    _git(primary_repo, "worktree", "add", "-q", str(wt), "-b", "wt")
    return wt


def _decide(tool_name, file_path, cwd=None):
    return build_decision(
        {
            "tool_name": tool_name,
            "tool_input": {"file_path": str(file_path)},
            "cwd": str(cwd) if cwd else str(file_path),
        }
    )


class TestBlocksPrimaryCheckout:
    def test_write_to_primary_src_is_blocked(self, primary_repo):
        decision = _decide("Write", primary_repo / "src" / "new.py")
        assert decision is not None
        assert decision["permissionDecision"] == "deny"

    def test_edit_in_primary_is_blocked(self, primary_repo):
        decision = _decide("Edit", primary_repo / "seed.txt")
        assert decision is not None
        assert decision["permissionDecision"] == "deny"

    def test_nonexistent_nested_parent_still_blocked(self, primary_repo):
        # Write can target a file whose parent dirs do not exist yet.
        decision = _decide("Write", primary_repo / "a" / "b" / "c.go")
        assert decision is not None
        assert decision["permissionDecision"] == "deny"


class TestAllowsIsolatedAndOutOfScope:
    def test_write_in_linked_worktree_is_allowed(self, linked_worktree):
        assert _decide("Write", linked_worktree / "src" / "new.py") is None

    def test_write_outside_any_repo_is_allowed(self, tmp_path):
        plain = tmp_path / "not-a-repo"
        plain.mkdir()
        assert _decide("Write", plain / "file.txt") is None

    def test_non_write_tool_passthrough(self, primary_repo):
        assert build_decision({"tool_name": "Bash", "tool_input": {"command": "ls"}, "cwd": str(primary_repo)}) is None


class TestAllowlist:
    def test_docs_in_primary_allowed(self, primary_repo):
        assert _decide("Write", primary_repo / "docs" / "note.md") is None

    def test_dotclaude_in_primary_allowed(self, primary_repo):
        # The control surface must stay editable so the hook can be disabled.
        assert _decide("Edit", primary_repo / ".claude" / "settings.json") is None

    def test_src_in_primary_not_allowlisted(self, primary_repo):
        assert _decide("Write", primary_repo / "src" / "app.py") is not None


class TestEscapeHatch:
    def test_env_override_allows_primary_write(self, primary_repo, monkeypatch):
        monkeypatch.setenv("ORO_ALLOW_MAIN_WRITES", "1")
        assert _decide("Write", primary_repo / "src" / "new.py") is None

    def test_env_off_values_do_not_override(self, primary_repo, monkeypatch):
        monkeypatch.setenv("ORO_ALLOW_MAIN_WRITES", "0")
        assert _decide("Write", primary_repo / "src" / "new.py") is not None


class TestUnits:
    def test_is_escape_hatch_set(self):
        assert is_escape_hatch_set({"ORO_ALLOW_MAIN_WRITES": "1"}) is True
        assert is_escape_hatch_set({"ORO_ALLOW_MAIN_WRITES": "true"}) is True
        assert is_escape_hatch_set({"ORO_ALLOW_MAIN_WRITES": ""}) is False
        assert is_escape_hatch_set({"ORO_ALLOW_MAIN_WRITES": "0"}) is False
        assert is_escape_hatch_set({}) is False

    def test_classify_primary_vs_linked(self, primary_repo, linked_worktree):
        assert classify_checkout(primary_repo) == "primary"
        assert classify_checkout(linked_worktree) == "linked"

    def test_classify_none_outside_repo(self, tmp_path):
        assert classify_checkout(tmp_path) == "none"

    def test_nearest_existing_dir(self, primary_repo):
        assert nearest_existing_dir(primary_repo / "x" / "y" / "z") == primary_repo

    def test_is_allowlisted(self, primary_repo):
        assert is_allowlisted(primary_repo / "docs" / "a.md", primary_repo) is True
        assert is_allowlisted(primary_repo / ".worktrees" / "w" / "f", primary_repo) is True
        assert is_allowlisted(primary_repo / "src" / "a.py", primary_repo) is False

    def test_target_path_for(self):
        assert target_path_for("Write", {"file_path": "/x"}) == "/x"
        assert target_path_for("NotebookEdit", {"notebook_path": "/n.ipynb"}) == "/n.ipynb"
        assert target_path_for("Edit", {}) is None
