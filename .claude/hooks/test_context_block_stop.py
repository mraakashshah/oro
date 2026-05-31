#!/usr/bin/env python3
"""Tests for context hard-stop hook helpers."""

import importlib
import json
import sys
from pathlib import Path

HOOKS_DIR = Path(__file__).resolve().parent


def _fresh_module(name: str):
    if str(HOOKS_DIR) not in sys.path:
        sys.path.insert(0, str(HOOKS_DIR))
    if name in sys.modules:
        del sys.modules[name]
    return importlib.import_module(name)


def test_read_context_pct_sources(monkeypatch, tmp_path):
    monkeypatch.chdir(tmp_path)
    home = tmp_path / "home"
    monkeypatch.setenv("HOME", str(home))

    hook = _fresh_module("context_block_stop")

    worker_pct = tmp_path / ".oro" / "context_pct"
    worker_pct.parent.mkdir(parents=True)
    worker_pct.write_text("42\n")

    role_pct = home / ".oro" / "panes" / "deep" / "context_pct"
    role_pct.parent.mkdir(parents=True)
    role_pct.write_text("64\n")

    monkeypatch.setenv("ORO_CONTEXT_BLOCK_PCT", "57")
    monkeypatch.setenv("ORO_WORKER", "1")
    monkeypatch.setenv("ORO_ROLE", "deep")
    assert hook.read_context_pct() == 57

    monkeypatch.setenv("ORO_CONTEXT_BLOCK_PCT", "garbage")
    assert hook.read_context_pct() is None

    monkeypatch.delenv("ORO_CONTEXT_BLOCK_PCT", raising=False)
    assert hook.read_context_pct() == 42

    monkeypatch.delenv("ORO_WORKER", raising=False)
    assert hook.read_context_pct() == 64

    role_pct.write_text("")
    assert hook.read_context_pct() is None

    role_pct.write_text("not-int")
    assert hook.read_context_pct() is None

    monkeypatch.delenv("ORO_ROLE", raising=False)
    assert hook.read_context_pct() is None


def test_hard_threshold_parity(monkeypatch, tmp_path):
    thresholds = tmp_path / "thresholds.json"
    thresholds.write_text(json.dumps({"fast": 35, "balanced": 45, "sonnet": 55}))

    compact_trigger = _fresh_module("compact_trigger")

    monkeypatch.setenv("ORO_ROLE", "fast")
    monkeypatch.setenv("ORO_MODEL", "claude-opus-4")
    assert compact_trigger.resolve_tier_threshold(thresholds) == 35
    assert compact_trigger.hard_threshold(thresholds) == 45

    monkeypatch.setenv("ORO_ROLE", "unknown")
    monkeypatch.setenv("ORO_MODEL", "claude-sonnet-4")
    assert compact_trigger.resolve_tier_threshold(thresholds) == 55
    assert compact_trigger.hard_threshold(thresholds) == 65

    monkeypatch.delenv("ORO_ROLE", raising=False)
    monkeypatch.delenv("ORO_MODEL", raising=False)
    assert compact_trigger.resolve_tier_threshold(thresholds) == 45
    assert compact_trigger.hard_threshold(thresholds) == 55

    thresholds.write_text(json.dumps({"fast": 35}))
    assert compact_trigger.resolve_tier_threshold(thresholds) == 40
    assert compact_trigger.hard_threshold(thresholds) == 50

    thresholds.write_text("{not-json")
    assert compact_trigger.resolve_tier_threshold(thresholds) == 40
    assert compact_trigger.hard_threshold(thresholds) == 50
