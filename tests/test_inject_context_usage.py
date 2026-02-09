"""Tests for the inject_context_usage PreToolUse hook."""

import importlib.util
import json
import tempfile
from pathlib import Path

# Load the hook module directly from .claude/hooks/ (not a package)
_spec = importlib.util.spec_from_file_location(
    "inject_context_usage",
    Path(__file__).resolve().parent.parent / ".claude" / "hooks" / "inject_context_usage.py",
)
_mod = importlib.util.module_from_spec(_spec)  # type: ignore[arg-type]
_spec.loader.exec_module(_mod)  # type: ignore[union-attr]

CONTEXT_WINDOW = _mod.CONTEXT_WINDOW
DEFAULT_THRESHOLD = _mod.DEFAULT_THRESHOLD
calculate_context_pct = _mod.calculate_context_pct
get_last_usage = _mod.get_last_usage
detect_model = _mod.detect_model
load_thresholds = _mod.load_thresholds


def _make_transcript(entries: list[dict]) -> Path:
    """Write a list of dicts as JSONL to a temp file, return its path."""
    f = tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False)
    for entry in entries:
        f.write(json.dumps(entry) + "\n")
    f.close()
    return Path(f.name)


def _assistant_entry(
    input_tokens: int,
    cache_create: int = 0,
    cache_read: int = 0,
    output_tokens: int = 50,
) -> dict:
    """Build a minimal assistant transcript entry with usage data."""
    return {
        "type": "assistant",
        "message": {
            "role": "assistant",
            "usage": {
                "input_tokens": input_tokens,
                "cache_creation_input_tokens": cache_create,
                "cache_read_input_tokens": cache_read,
                "output_tokens": output_tokens,
            },
        },
    }


def _user_entry() -> dict:
    return {"type": "user", "message": {"role": "user"}}


class TestCalculateContextPct:
    def test_zero_usage(self):
        used, total, pct = calculate_context_pct({"input_tokens": 0})
        assert used == 0
        assert total == CONTEXT_WINDOW
        assert pct == 0.0

    def test_all_fields_sum(self):
        usage = {
            "input_tokens": 10_000,
            "cache_creation_input_tokens": 30_000,
            "cache_read_input_tokens": 60_000,
        }
        used, _, pct = calculate_context_pct(usage)
        assert used == 100_000
        assert pct == 100_000 / CONTEXT_WINDOW

    def test_missing_fields_default_to_zero(self):
        used, _, _ = calculate_context_pct({})
        assert used == 0


class TestGetLastUsage:
    def test_empty_file(self, tmp_path):
        p = tmp_path / "empty.jsonl"
        p.write_text("")
        assert get_last_usage(str(p)) is None

    def test_no_assistant_messages(self):
        transcript = _make_transcript([_user_entry(), _user_entry()])
        assert get_last_usage(str(transcript)) is None

    def test_returns_last_assistant_usage(self):
        transcript = _make_transcript(
            [
                _user_entry(),
                _assistant_entry(input_tokens=5_000, cache_create=10_000),
                _user_entry(),
                _assistant_entry(input_tokens=8_000, cache_create=50_000, cache_read=10_000),
            ]
        )
        usage = get_last_usage(str(transcript))
        assert usage["input_tokens"] == 8_000
        assert usage["cache_creation_input_tokens"] == 50_000
        assert usage["cache_read_input_tokens"] == 10_000

    def test_missing_file(self):
        assert get_last_usage("/nonexistent/path.jsonl") is None

    def test_handles_malformed_lines(self):
        f = tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False)
        f.write("not json\n")
        f.write(json.dumps(_assistant_entry(input_tokens=1_000)) + "\n")
        f.write("also bad {{\n")
        f.close()
        usage = get_last_usage(f.name)
        assert usage is not None
        assert usage["input_tokens"] == 1_000


class TestLoadThresholds:
    def test_loads_from_file(self, tmp_path):
        (tmp_path / "thresholds.json").write_text('{"opus": 65, "sonnet": 50}')
        result = load_thresholds(str(tmp_path))
        assert result == {"opus": 0.65, "sonnet": 0.50}

    def test_missing_file_returns_empty(self, tmp_path):
        result = load_thresholds(str(tmp_path))
        assert result == {}

    def test_malformed_json_returns_empty(self, tmp_path):
        (tmp_path / "thresholds.json").write_text("not json")
        result = load_thresholds(str(tmp_path))
        assert result == {}


class TestThresholds:
    def test_default_threshold_is_50(self):
        assert DEFAULT_THRESHOLD == 0.50

    def test_opus_below_threshold(self):
        """30% usage should produce no trigger for opus (threshold=65%)."""
        used = int(CONTEXT_WINDOW * 0.30)
        _, _, pct = calculate_context_pct({"input_tokens": used})
        assert pct < 0.65

    def test_opus_above_threshold(self):
        """70% usage should breach opus threshold (65%)."""
        used = int(CONTEXT_WINDOW * 0.70)
        _, _, pct = calculate_context_pct({"input_tokens": used})
        assert pct >= 0.65

    def test_sonnet_threshold(self):
        """55% usage should breach sonnet threshold (50%)."""
        used = int(CONTEXT_WINDOW * 0.55)
        _, _, pct = calculate_context_pct({"input_tokens": used})
        assert pct >= 0.50


class TestDetectModel:
    def test_opus(self):
        transcript = _make_transcript(
            [{"type": "assistant", "message": {"model": "claude-opus-4-6", "role": "assistant"}}]
        )
        assert detect_model(str(transcript)) == "opus"

    def test_sonnet(self):
        transcript = _make_transcript(
            [{"type": "assistant", "message": {"model": "claude-sonnet-4-5-20250929", "role": "assistant"}}]
        )
        assert detect_model(str(transcript)) == "sonnet"

    def test_haiku(self):
        transcript = _make_transcript(
            [{"type": "assistant", "message": {"model": "claude-haiku-4-5-20251001", "role": "assistant"}}]
        )
        assert detect_model(str(transcript)) == "haiku"

    def test_default_opus(self):
        transcript = _make_transcript([_user_entry()])
        assert detect_model(str(transcript)) == "opus"
