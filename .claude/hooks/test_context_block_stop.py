#!/usr/bin/env python3
"""Tests for context_block_stop.py Stop hook decision logic."""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))

import context_block_stop


def test_decide_blocks_allows_handoff_and_reentry(monkeypatch) -> None:
    """Block Stop at the hard context threshold until a handoff marker exists."""

    assert context_block_stop.decide({}) == {}
    assert context_block_stop.decide(None) == {}
    assert context_block_stop.decide(["invalid"]) == {}
    assert context_block_stop.decide({"stop_hook_active": True}) == {}

    monkeypatch.setattr(context_block_stop, "handoff_exists", lambda: False)
    monkeypatch.setattr(
        context_block_stop,
        "read_context_pct",
        lambda: context_block_stop.hard_threshold() - 1,
    )
    assert context_block_stop.decide({"hook_event_name": "Stop"}) == {}

    monkeypatch.setattr(
        context_block_stop,
        "read_context_pct",
        lambda: context_block_stop.hard_threshold(),
    )
    decision = context_block_stop.decide({"hook_event_name": "Stop"})
    assert decision["decision"] == "block"
    reason = decision["reason"]
    assert f"{context_block_stop.hard_threshold()}%" in reason
    assert "create-handoff" in reason
    assert ".oro/handoff_done" in reason

    monkeypatch.setattr(context_block_stop, "handoff_exists", lambda: True)
    monkeypatch.setattr(context_block_stop, "read_context_pct", lambda: 99)
    assert context_block_stop.decide({"hook_event_name": "Stop"}) == {}
