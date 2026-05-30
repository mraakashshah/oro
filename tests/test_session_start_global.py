"""Tests for session_start_global hook (non-oro SessionStart)."""

import importlib.util
import io
import json
import sys
from pathlib import Path

# Load the hook module from source (assets/hooks/) for testing
_repo_root = Path(__file__).parent.parent
_spec = importlib.util.spec_from_file_location(
    "session_start_global",
    _repo_root / "assets" / "hooks" / "session_start_global.py",
)
_mod = importlib.util.module_from_spec(_spec)  # type: ignore[arg-type]
_spec.loader.exec_module(_mod)  # type: ignore[union-attr]

_auto_load_skills_silent = _mod._auto_load_skills_silent

# Load session_start_extras for project_state testing
_extras_spec = importlib.util.spec_from_file_location(
    "session_start_extras",
    _repo_root / "assets" / "hooks" / "session_start_extras.py",
)
_extras_mod = importlib.util.module_from_spec(_extras_spec)  # type: ignore[arg-type]
_extras_spec.loader.exec_module(_extras_mod)  # type: ignore[union-attr]

_project_state = _extras_mod.project_state


# --- _auto_load_skills_silent ---


class TestAutoLoadSkillsSilent:
    def test_valid_skill_file_returns_content(self, tmp_path):
        skill_dir = tmp_path / "using-skills"
        skill_dir.mkdir()
        f = skill_dir / "SKILL.md"
        f.write_text("# Using Skills\n\nAlways check skills first.")

        result = _auto_load_skills_silent(str(f))

        assert "# Auto-loaded Skill: using-skills" in result
        assert "Always check skills first." in result

    def test_missing_file_returns_empty_silently(self, tmp_path, capfd):
        result = _auto_load_skills_silent(str(tmp_path / "nonexistent.md"))

        assert result == ""
        # Critically: no warning printed (silent failure)
        captured = capfd.readouterr()
        assert captured.err == ""

    def test_empty_file_returns_empty(self, tmp_path):
        f = tmp_path / "empty.md"
        f.write_text("")

        result = _auto_load_skills_silent(str(f))

        assert result == ""

    def test_flat_md_uses_stem_as_name(self, tmp_path):
        f = tmp_path / "my-skill.md"
        f.write_text("# Content")

        result = _auto_load_skills_silent(str(f))

        assert "# Auto-loaded Skill: my-skill" in result


# --- main() integration ---


def _run_main(monkeypatch, tmp_path, *, stdin_data="{}", skills_content=None):
    """Helper: run main() with a fake home dir and optional skills file."""
    # Create ~/.claude/skills/using-skills/SKILL.md under tmp_path
    if skills_content is not None:
        skills_dir = tmp_path / ".claude" / "skills" / "using-skills"
        skills_dir.mkdir(parents=True)
        (skills_dir / "SKILL.md").write_text(skills_content)

    monkeypatch.setattr(Path, "home", staticmethod(lambda: tmp_path))
    monkeypatch.setattr(sys, "stdin", io.StringIO(stdin_data))

    captured = io.StringIO()
    monkeypatch.setattr(sys, "stdout", captured)

    _mod.main()
    return json.loads(captured.getvalue())


class TestMainIntegration:
    def test_superpowers_in_additional_context(self, monkeypatch, tmp_path):
        output = _run_main(monkeypatch, tmp_path)

        ctx = output["hookSpecificOutput"]["additionalContext"]
        assert "# Superpowers" in ctx

    def test_using_skills_injected_when_file_exists(self, monkeypatch, tmp_path):
        output = _run_main(
            monkeypatch,
            tmp_path,
            skills_content="# Using Skills\n\nAlways check skills first.",
        )

        ctx = output["hookSpecificOutput"]["additionalContext"]
        assert "# Auto-loaded Skill: using-skills" in ctx
        assert "Always check skills first." in ctx

    def test_superpowers_before_skills(self, monkeypatch, tmp_path):
        output = _run_main(
            monkeypatch,
            tmp_path,
            skills_content="# Skill content",
        )

        ctx = output["hookSpecificOutput"]["additionalContext"]
        sp_idx = ctx.find("# Superpowers")
        sk_idx = ctx.find("# Auto-loaded Skill")
        assert sp_idx < sk_idx, "Superpowers must appear before auto-loaded skill"

    def test_silent_when_skills_file_missing(self, monkeypatch, tmp_path, capfd):
        # No skills file created — should not warn
        _run_main(monkeypatch, tmp_path)

        captured = capfd.readouterr()
        assert captured.err == ""

    def test_no_dynamic_oro_sections(self, monkeypatch, tmp_path):
        """Output must not include dynamic oro state sections (stale beads, handoffs, worktrees)."""
        output = _run_main(monkeypatch, tmp_path)

        ctx = output["hookSpecificOutput"]["additionalContext"]
        # These are dynamic-state section headers injected by session_start_extras.py
        for forbidden_header in (
            "## Stale Beads",
            "## Merged Worktrees",
            "## Latest Handoff",
            "## Ready Work",
            "## Git State",
            "## current.md",
            "## Recent Learnings",
            "# Role Beacon",
        ):
            assert forbidden_header not in ctx, (
                f"Dynamic oro section '{forbidden_header}' must not appear in global hook output"
            )

    def test_no_inline_oro_references(self, monkeypatch, tmp_path):
        """Superpowers must not reference oro-specific tooling (AC #3)."""
        output = _run_main(monkeypatch, tmp_path)

        ctx = output["hookSpecificOutput"]["additionalContext"]
        # These are inline oro-specific terms — not just section headers
        for forbidden_term in (
            "bd ready",
            "bd close",
            "docs/handoffs/",
            "beads",
            "auto-syncs beads",
            "worktrees",
        ):
            assert forbidden_term.lower() not in ctx.lower(), (
                f"Inline oro reference '{forbidden_term}' must not appear in global hook output"
            )

    def test_commit_instruction_is_non_interactive(self, monkeypatch, tmp_path):
        output = _run_main(monkeypatch, tmp_path)

        ctx = output["hookSpecificOutput"]["additionalContext"]
        assert "git commit -m" in ctx
        assert "git add` → `git commit`" not in ctx

    def test_output_is_valid_json(self, monkeypatch, tmp_path):
        output = _run_main(monkeypatch, tmp_path)
        # Already parsed by _run_main — just assert structure
        assert "hookSpecificOutput" in output
        assert "additionalContext" in output["hookSpecificOutput"]

    def test_hook_event_name_is_session_start(self, monkeypatch, tmp_path):
        output = _run_main(monkeypatch, tmp_path)
        assert output["hookSpecificOutput"]["hookEventName"] == "SessionStart"

    def test_empty_stdin_handled_gracefully(self, monkeypatch, tmp_path):
        output = _run_main(monkeypatch, tmp_path, stdin_data="")
        assert "hookSpecificOutput" in output

    def test_invalid_stdin_handled_gracefully(self, monkeypatch, tmp_path):
        output = _run_main(monkeypatch, tmp_path, stdin_data="not json")
        assert "hookSpecificOutput" in output


# --- project_state() ---


class TestProjectState:
    def test_current_md_block_removed(self, tmp_path, monkeypatch):
        """project_state() must not include ## current.md even when file exists."""
        (tmp_path / "current.md").write_text("# Current task\nsome content")
        monkeypatch.chdir(tmp_path)

        result = _project_state()

        assert "## current.md" not in result
