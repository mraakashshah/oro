"""Tests for enforce_skills.py PreToolUse hook."""

import json
import os
import sys
import tempfile
from io import StringIO
from pathlib import Path
from unittest import mock

# ---------------------------------------------------------------------------
# Import the module under test (it lives alongside this file)
# ---------------------------------------------------------------------------
sys.path.insert(0, str(Path(__file__).parent))
import enforce_skills as es

# ---------------------------------------------------------------------------
# should_remind
# ---------------------------------------------------------------------------


class TestShouldRemind:
    def test_fires_at_zero(self):
        assert es.should_remind(0) is True

    def test_fires_at_window_boundary(self):
        assert es.should_remind(12) is True

    def test_fires_at_double_window(self):
        assert es.should_remind(24) is True

    def test_silent_between_boundaries(self):
        for i in range(1, 12):
            assert es.should_remind(i) is False, f"should be silent at counter={i}"

    def test_silent_just_before_second_boundary(self):
        assert es.should_remind(11) is False

    def test_respects_custom_window(self):
        assert es.should_remind(5, window=5) is True
        assert es.should_remind(3, window=5) is False


# ---------------------------------------------------------------------------
# state_file / read_counter / write_counter
# ---------------------------------------------------------------------------


class TestStateIO:
    def test_state_file_uses_ppid(self, tmp_path):
        p = es.state_file.__wrapped__(99) if hasattr(es.state_file, "__wrapped__") else es.state_file(99)  # pyright: ignore[reportFunctionMemberAccess]
        assert "99" in str(p)

    def test_read_missing_returns_zero(self, tmp_path):
        assert es.read_counter(tmp_path / "missing") == 0

    def test_read_invalid_returns_zero(self, tmp_path):
        bad = tmp_path / "bad"
        bad.write_text("not-a-number")
        assert es.read_counter(bad) == 0

    def test_write_then_read(self, tmp_path):
        p = tmp_path / "counter"
        es.write_counter(p, 7)
        assert es.read_counter(p) == 7

    def test_write_failure_is_silent(self, tmp_path):
        # Write to a path whose parent doesn't exist — should not raise
        es.write_counter(tmp_path / "nonexistent" / "counter", 1)


# ---------------------------------------------------------------------------
# build_reminder
# ---------------------------------------------------------------------------


class TestBuildReminder:
    def test_returns_hook_specific_output(self):
        r = es.build_reminder()
        assert "hookSpecificOutput" in r
        assert r["hookSpecificOutput"]["hookEventName"] == "PreToolUse"
        assert "using-skills" in r["hookSpecificOutput"]["additionalContext"]


# ---------------------------------------------------------------------------
# build_decision
# ---------------------------------------------------------------------------


class TestBuildDecision:
    def _make_input(self, tool: str) -> dict:
        return {"tool_name": tool, "tool_input": {}}

    def test_suppressed_for_worker(self, tmp_path, monkeypatch):
        monkeypatch.setenv("ORO_WORKER", "1")
        # Patch state_file to use tmp_path so tests are isolated
        monkeypatch.setattr(es, "state_file", lambda ppid: tmp_path / f"s-{ppid}")
        result = es.build_decision(self._make_input("Edit"), ppid=1, window=12)
        assert result is None

    def test_suppressed_for_non_qualifying_tool(self, tmp_path, monkeypatch):
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setattr(es, "state_file", lambda ppid: tmp_path / f"s-{ppid}")
        result = es.build_decision(self._make_input("Bash"), ppid=2, window=12)
        assert result is None

    def test_fires_at_first_qualifying_call(self, tmp_path, monkeypatch):
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setattr(es, "state_file", lambda ppid: tmp_path / f"s-{ppid}")
        result = es.build_decision(self._make_input("Edit"), ppid=3, window=12)
        assert result is not None
        assert "hookSpecificOutput" in result

    def test_silent_on_second_call(self, tmp_path, monkeypatch):
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setattr(es, "state_file", lambda ppid: tmp_path / f"s-{ppid}")
        es.build_decision(self._make_input("Write"), ppid=4, window=12)  # counter 0 → fires
        result = es.build_decision(self._make_input("Write"), ppid=4, window=12)  # counter 1 → silent
        assert result is None

    def test_fires_again_at_window(self, tmp_path, monkeypatch):
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setattr(es, "state_file", lambda ppid: tmp_path / f"s-{ppid}")
        # Pre-set counter to 12: build_decision reads 12, checks should_remind(12)=True
        state = tmp_path / "s-5"
        es.write_counter(state, 12)
        result = es.build_decision(self._make_input("Agent"), ppid=5, window=12)
        assert result is not None

    def test_all_qualifying_tools_trigger(self, tmp_path, monkeypatch):
        monkeypatch.delenv("ORO_WORKER", raising=False)
        for i, tool in enumerate(sorted(es.QUALIFYING_TOOLS), start=10):
            monkeypatch.setattr(es, "state_file", lambda ppid, _t=tmp_path, _i=i: _t / f"s-{ppid}-{_i}")
            result = es.build_decision(self._make_input(tool), ppid=i, window=12)
            assert result is not None, f"Expected reminder for tool={tool}"

    def test_bash_does_not_trigger(self, tmp_path, monkeypatch):
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setattr(es, "state_file", lambda ppid: tmp_path / f"s-bash-{ppid}")
        result = es.build_decision(self._make_input("Bash"), ppid=20, window=12)
        assert result is None

    def test_read_does_not_trigger(self, tmp_path, monkeypatch):
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setattr(es, "state_file", lambda ppid: tmp_path / f"s-read-{ppid}")
        result = es.build_decision(self._make_input("Read"), ppid=21, window=12)
        assert result is None

    def test_increments_counter_even_when_silent(self, tmp_path, monkeypatch):
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setattr(es, "state_file", lambda ppid: tmp_path / f"s-{ppid}")
        ppid = 30
        # Call 3 times — first fires, next two are silent but counter should increment
        es.build_decision(self._make_input("Edit"), ppid=ppid, window=12)
        es.build_decision(self._make_input("Edit"), ppid=ppid, window=12)
        es.build_decision(self._make_input("Edit"), ppid=ppid, window=12)
        state = tmp_path / f"s-{ppid}"
        assert es.read_counter(state) == 3

    def test_worker_env_unset_does_not_suppress(self, tmp_path, monkeypatch):
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setattr(es, "state_file", lambda ppid: tmp_path / f"s-{ppid}")
        result = es.build_decision(self._make_input("Task"), ppid=40, window=12)
        assert result is not None


# ---------------------------------------------------------------------------
# main() — integration via stdin/stdout
# ---------------------------------------------------------------------------


class TestMain:
    def _run_main(self, hook_input: dict, env: dict | None = None) -> str:
        stdin_data = json.dumps(hook_input)
        with (
            mock.patch("sys.stdin", StringIO(stdin_data)),
            mock.patch("sys.stdout", new_callable=StringIO) as mock_stdout,
            mock.patch("os.getppid", return_value=99999),
            mock.patch.dict(os.environ, env or {}, clear=False),
        ):
            # Ensure ORO_WORKER is unset unless explicitly provided
            if env is None or "ORO_WORKER" not in (env or {}):
                os.environ.pop("ORO_WORKER", None)
            # Use a fresh tmp state file for each test
            with (
                tempfile.TemporaryDirectory() as td,
                mock.patch.object(es, "state_file", return_value=Path(td) / "state"),
            ):
                es.main()
        return mock_stdout.getvalue()

    def test_edit_first_call_produces_output(self):
        out = self._run_main({"tool_name": "Edit", "tool_input": {}})
        assert out != ""
        parsed = json.loads(out)
        assert "hookSpecificOutput" in parsed

    def test_bash_produces_no_output(self):
        out = self._run_main({"tool_name": "Bash", "tool_input": {"command": "git status"}})
        assert out == ""

    def test_worker_suppression_via_env(self):
        out = self._run_main({"tool_name": "Edit", "tool_input": {}}, env={"ORO_WORKER": "1"})
        assert out == ""

    def test_invalid_json_produces_no_output(self):
        with (
            mock.patch("sys.stdin", StringIO("not-json")),
            mock.patch("sys.stdout", new_callable=StringIO) as mock_stdout,
        ):
            es.main()
        assert mock_stdout.getvalue() == ""
