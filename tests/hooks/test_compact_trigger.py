#!/usr/bin/env python3
"""Tests for tier key lookup and legacy fallback in compact_trigger.load_threshold."""

import json
import sys
from pathlib import Path

HOOKS_DIR = str(Path(__file__).resolve().parent.parent.parent / "assets" / "hooks")

if HOOKS_DIR not in sys.path:
    sys.path.insert(0, HOOKS_DIR)


def _fresh_compact_trigger():
    if "compact_trigger" in sys.modules:
        del sys.modules["compact_trigger"]
    import compact_trigger

    return compact_trigger


def test_tier_key_lookup(tmp_path):
    """When thresholds.json has tier keys, tier key is preferred over legacy model key."""
    thresholds = tmp_path / "thresholds.json"
    thresholds.write_text(
        json.dumps(
            {
                "fast": 45,
                "balanced": 40,
                "deep": 40,
                "background": 40,
                "opus": 40,
                "sonnet": 40,
                "haiku": 40,
            }
        )
    )

    ct = _fresh_compact_trigger()
    # tier="fast" should be preferred over model_key="opus"
    result = ct.load_threshold("opus", thresholds, tier="fast")
    assert result == 45


def test_legacy_fallback(tmp_path):
    """When thresholds.json has only legacy keys, falls back to model_key lookup."""
    thresholds = tmp_path / "thresholds.json"
    thresholds.write_text(json.dumps({"opus": 40, "sonnet": 40, "haiku": 40}))

    ct = _fresh_compact_trigger()
    # No tier key "fast" in thresholds → fall back to legacy "opus"
    result = ct.load_threshold("opus", thresholds, tier="fast")
    assert result == 40
