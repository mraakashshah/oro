"""Tests for the post-commit discovery reminder PostToolUse hook."""

import importlib.util
from pathlib import Path

# Load the hook module directly from .claude/hooks/ (not a package)
_spec = importlib.util.spec_from_file_location(
    "post_commit_reminder",
    Path(__file__).resolve().parent.parent / ".claude" / "hooks" / "post_commit_reminder.py",
)
_mod = importlib.util.module_from_spec(_spec)  # type: ignore[arg-type]
_spec.loader.exec_module(_mod)  # type: ignore[union-attr]

is_feat_or_fix_commit = _mod.is_feat_or_fix_commit
mentions_decisions_doc = _mod.mentions_decisions_doc
build_reminder = _mod.build_reminder
process_hook = _mod.process_hook


class TestIsFeatOrFixCommit:
    def test_feat_commit_detected(self):
        cmd = 'git commit -m "feat(hooks): add new reminder hook"'
        assert is_feat_or_fix_commit(cmd) is True

    def test_fix_commit_detected(self):
        cmd = 'git commit -m "fix(parser): handle edge case in regex"'
        assert is_feat_or_fix_commit(cmd) is True

    def test_feat_no_scope(self):
        cmd = 'git commit -m "feat: initial implementation"'
        assert is_feat_or_fix_commit(cmd) is True

    def test_fix_no_scope(self):
        cmd = 'git commit -m "fix: correct off-by-one error"'
        assert is_feat_or_fix_commit(cmd) is True

    def test_chore_commit_not_detected(self):
        cmd = 'git commit -m "chore: update dependencies"'
        assert is_feat_or_fix_commit(cmd) is False

    def test_docs_commit_not_detected(self):
        cmd = 'git commit -m "docs: update README"'
        assert is_feat_or_fix_commit(cmd) is False

    def test_refactor_commit_not_detected(self):
        cmd = 'git commit -m "refactor(core): simplify logic"'
        assert is_feat_or_fix_commit(cmd) is False

    def test_non_commit_command(self):
        cmd = "git status"
        assert is_feat_or_fix_commit(cmd) is False

    def test_non_git_command(self):
        cmd = "ls -la"
        assert is_feat_or_fix_commit(cmd) is False

    def test_empty_command(self):
        assert is_feat_or_fix_commit("") is False

    def test_heredoc_commit(self):
        cmd = """git commit -m "$(cat <<'EOF'
feat(hooks): post-commit discovery reminder
EOF
)"
"""
        assert is_feat_or_fix_commit(cmd) is True

    def test_fix_with_bang(self):
        """fix! indicates a breaking change fix -- still a fix."""
        cmd = 'git commit -m "fix!: breaking change in API"'
        assert is_feat_or_fix_commit(cmd) is True


class TestMentionsDecisionsDoc:
    def test_present_in_tool_output(self):
        assert mentions_decisions_doc("Updated decisions-and-discoveries.md with new entry") is True

    def test_absent_from_output(self):
        assert mentions_decisions_doc("1 file changed, 5 insertions(+)") is False

    def test_empty_string(self):
        assert mentions_decisions_doc("") is False

    def test_none_input(self):
        assert mentions_decisions_doc(None) is False

    def test_partial_match(self):
        assert mentions_decisions_doc("see docs/decisions-and-discoveries.md") is True


class TestBuildReminder:
    def test_returns_string(self):
        result = build_reminder()
        assert isinstance(result, str)
        assert "decisions-and-discoveries.md" in result


class TestProcessHook:
    def _make_input(self, tool_name="Bash", command="", tool_output=""):
        return {
            "tool_name": tool_name,
            "tool_input": {"command": command},
            "tool_output": tool_output,
        }

    def test_feat_commit_triggers_reminder(self):
        hook_input = self._make_input(
            command='git commit -m "feat(hooks): new hook"',
            tool_output="[main abc1234] feat(hooks): new hook\n 1 file changed",
        )
        result = process_hook(hook_input)
        assert result is not None
        assert "decisions-and-discoveries.md" in result["hookSpecificOutput"]["additionalContext"]

    def test_fix_commit_triggers_reminder(self):
        hook_input = self._make_input(
            command='git commit -m "fix(parser): handle edge case"',
            tool_output="[main def5678] fix(parser): handle edge case\n 2 files changed",
        )
        result = process_hook(hook_input)
        assert result is not None
        assert "decisions-and-discoveries.md" in result["hookSpecificOutput"]["additionalContext"]

    def test_chore_commit_no_reminder(self):
        hook_input = self._make_input(
            command='git commit -m "chore: update deps"',
            tool_output="[main 1234567] chore: update deps\n 1 file changed",
        )
        result = process_hook(hook_input)
        assert result is None

    def test_docs_commit_no_reminder(self):
        hook_input = self._make_input(
            command='git commit -m "docs: update README"',
            tool_output="[main 1234567] docs: update README\n 1 file changed",
        )
        result = process_hook(hook_input)
        assert result is None

    def test_refactor_commit_no_reminder(self):
        hook_input = self._make_input(
            command='git commit -m "refactor(core): simplify"',
            tool_output="[main 1234567] refactor(core): simplify\n 3 files changed",
        )
        result = process_hook(hook_input)
        assert result is None

    def test_non_commit_bash_command_no_reminder(self):
        hook_input = self._make_input(
            command="git status",
            tool_output="On branch main\nnothing to commit",
        )
        result = process_hook(hook_input)
        assert result is None

    def test_non_git_bash_command_no_reminder(self):
        hook_input = self._make_input(
            command="uv run pytest tests/ -v",
            tool_output="5 passed",
        )
        result = process_hook(hook_input)
        assert result is None

    def test_suppressed_when_output_mentions_decisions_doc(self):
        hook_input = self._make_input(
            command='git commit -m "feat(hooks): new hook"',
            tool_output="Updated docs/decisions-and-discoveries.md\n[main abc1234] feat(hooks): new hook",
        )
        result = process_hook(hook_input)
        assert result is None

    def test_non_bash_tool_no_reminder(self):
        hook_input = {
            "tool_name": "Read",
            "tool_input": {"file_path": "/some/file.py"},
            "tool_output": "file contents",
        }
        result = process_hook(hook_input)
        assert result is None

    def test_empty_input_fails_open(self):
        result = process_hook({})
        assert result is None

    def test_malformed_input_fails_open(self):
        result = process_hook(None)
        assert result is None

    def test_missing_tool_input_fails_open(self):
        result = process_hook({"tool_name": "Bash"})
        assert result is None

    def test_missing_command_fails_open(self):
        result = process_hook({"tool_name": "Bash", "tool_input": {}})
        assert result is None
