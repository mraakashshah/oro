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
THRESHOLDS = _mod.THRESHOLDS
DEFAULT_THRESHOLDS = _mod.DEFAULT_THRESHOLDS
calculate_context_pct = _mod.calculate_context_pct
get_last_usage = _mod.get_last_usage
detect_model = _mod.detect_model


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


class TestThresholds:
    def test_opus_below_warn(self):
        """30% usage should produce no warning for opus."""
        warn, _ = THRESHOLDS["opus"]
        used = int(CONTEXT_WINDOW * 0.30)
        _, _, pct = calculate_context_pct({"input_tokens": used})
        assert pct < warn

    def test_opus_at_warn(self):
        """45% usage should trigger warn for opus."""
        warn, critical = THRESHOLDS["opus"]
        used = int(CONTEXT_WINDOW * warn)
        _, _, pct = calculate_context_pct({"input_tokens": used})
        assert pct >= warn
        assert pct < critical

    def test_opus_at_critical(self):
        """60% usage should trigger critical for opus."""
        _, critical = THRESHOLDS["opus"]
        used = int(CONTEXT_WINDOW * critical)
        _, _, pct = calculate_context_pct({"input_tokens": used})
        assert pct >= critical

    def test_sonnet_no_warn_zone(self):
        """Sonnet has no warn threshold."""
        warn, _ = THRESHOLDS["sonnet"]
        assert warn is None

    def test_haiku_critical_lower(self):
        """Haiku critical is 35%."""
        _, critical = THRESHOLDS["haiku"]
        assert critical == 0.35
