"""Tests for inject_context_usage hook — threshold loading and compact/handoff logic."""

import json
from pathlib import Path
from unittest.mock import patch

# The module under test
from inject_context_usage import (
    CONTEXT_WINDOW,
    DEFAULT_THRESHOLD,
    load_thresholds,
    main,
)

# ---------------------------------------------------------------------------
# load_thresholds
# ---------------------------------------------------------------------------


class TestLoadThresholds:
    def test_reads_from_json_file(self, tmp_path):
        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text(json.dumps({"opus": 65, "sonnet": 50, "haiku": 40}))
        result = load_thresholds(str(tmp_path))
        assert result == {"opus": 0.65, "sonnet": 0.50, "haiku": 0.40}

    def test_returns_empty_when_file_missing(self, tmp_path):
        result = load_thresholds(str(tmp_path))
        assert result == {}

    def test_returns_empty_on_malformed_json(self, tmp_path):
        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text("not json{{{")
        result = load_thresholds(str(tmp_path))
        assert result == {}

    def test_converts_integer_percentages_to_fractions(self, tmp_path):
        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text(json.dumps({"opus": 70}))
        result = load_thresholds(str(tmp_path))
        assert result == {"opus": 0.70}


# ---------------------------------------------------------------------------
# Default fallback
# ---------------------------------------------------------------------------


class TestDefaultThreshold:
    def test_default_is_50_percent(self):
        assert DEFAULT_THRESHOLD == 0.50


# ---------------------------------------------------------------------------
# Compact vs Handoff message selection
# ---------------------------------------------------------------------------


class TestMainCompactHandoff:
    """Integration tests for main() exercising compact vs. handoff paths."""

    def _make_transcript(self, tmp_path, pct: float, model: str = "claude-opus-4-20250514"):
        """Create a transcript file with usage at `pct` of CONTEXT_WINDOW."""
        tokens = int(CONTEXT_WINDOW * pct)
        transcript = tmp_path / "transcript.jsonl"
        entry = {
            "message": {
                "model": model,
                "usage": {"input_tokens": tokens},
            }
        }
        transcript.write_text(json.dumps(entry) + "\n")
        return str(transcript)

    def _make_thresholds(self, tmp_path, thresholds: dict):
        tf = tmp_path / "thresholds.json"
        tf.write_text(json.dumps(thresholds))

    def _run_main(self, hook_input: dict) -> dict | None:
        """Run main() with stdin mocked and capture stdout."""
        import io

        stdin_data = json.dumps(hook_input)
        stdout_buf = io.StringIO()
        with (
            patch("sys.stdin", io.StringIO(stdin_data)),
            patch("sys.stdout", stdout_buf),
            patch("inject_context_usage.DEBOUNCE_FILE", "/tmp/oro-test-debounce-" + str(id(self))),
        ):
            # Remove debounce file to ensure we always fire
            debounce = Path(f"/tmp/oro-test-debounce-{id(self)}")
            debounce.unlink(missing_ok=True)
            main()
        output = stdout_buf.getvalue()
        if not output:
            return None
        return json.loads(output)

    def test_below_threshold_no_output(self, tmp_path):
        """Below threshold produces no output."""
        self._make_thresholds(tmp_path, {"opus": 65})
        transcript = self._make_transcript(tmp_path, 0.30)
        with patch("inject_context_usage._find_project_root", return_value=str(tmp_path)):
            result = self._run_main({"transcript_path": transcript})
        assert result is None

    def test_above_threshold_no_compacted_flag_injects_compact(self, tmp_path):
        """Above threshold with no .oro/compacted flag -> compact message."""
        self._make_thresholds(tmp_path, {"opus": 65})
        transcript = self._make_transcript(tmp_path, 0.70)
        # Ensure no .oro/compacted flag
        oro_dir = tmp_path / ".oro"
        oro_dir.mkdir(exist_ok=True)
        # no compacted file

        with patch("inject_context_usage._find_project_root", return_value=str(tmp_path)):
            result = self._run_main({"transcript_path": transcript})
        assert result is not None
        ctx = result["hookSpecificOutput"]["additionalContext"]
        assert "/compact" in ctx

    def test_above_threshold_with_compacted_flag_injects_handoff(self, tmp_path):
        """Above threshold with .oro/compacted flag -> handoff message."""
        self._make_thresholds(tmp_path, {"opus": 65})
        transcript = self._make_transcript(tmp_path, 0.70)
        # Create .oro/compacted flag
        oro_dir = tmp_path / ".oro"
        oro_dir.mkdir(exist_ok=True)
        (oro_dir / "compacted").touch()

        with patch("inject_context_usage._find_project_root", return_value=str(tmp_path)):
            result = self._run_main({"transcript_path": transcript})
        assert result is not None
        ctx = result["hookSpecificOutput"]["additionalContext"]
        assert "handoff" in ctx.lower() or "hand off" in ctx.lower()

    def test_default_threshold_when_file_missing(self, tmp_path):
        """When thresholds.json missing, default to 50%."""
        # No thresholds.json created
        transcript = self._make_transcript(tmp_path, 0.55)  # above 50%

        with patch("inject_context_usage._find_project_root", return_value=str(tmp_path)):
            result = self._run_main({"transcript_path": transcript})
        assert result is not None  # should trigger at default 50%

    def test_default_threshold_when_model_unknown(self, tmp_path):
        """When model not in thresholds.json, default to 50%."""
        self._make_thresholds(tmp_path, {"opus": 80})  # only opus defined
        # Use haiku model, not in thresholds -> falls back to 50%
        transcript = self._make_transcript(tmp_path, 0.55, model="claude-haiku-some-version")

        with patch("inject_context_usage._find_project_root", return_value=str(tmp_path)):
            result = self._run_main({"transcript_path": transcript})
        assert result is not None  # 55% > 50% default

    def test_single_threshold_no_warn_critical_split(self, tmp_path):
        """Verify there is only one threshold level, not warn + critical."""
        self._make_thresholds(tmp_path, {"opus": 65})
        # At exactly 65% -> should trigger
        transcript = self._make_transcript(tmp_path, 0.65)

        with patch("inject_context_usage._find_project_root", return_value=str(tmp_path)):
            result = self._run_main({"transcript_path": transcript})
        assert result is not None

        # At 64% -> should NOT trigger
        transcript2 = self._make_transcript(tmp_path, 0.64)
        with patch("inject_context_usage._find_project_root", return_value=str(tmp_path)):
            result2 = self._run_main({"transcript_path": transcript2})
        assert result2 is None
