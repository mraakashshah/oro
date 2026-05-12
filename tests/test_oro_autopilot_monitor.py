from __future__ import annotations

import importlib.util
from pathlib import Path


def load_monitor_module():
    path = Path("assets/skills/watching-oro/scripts/oro_autopilot_monitor.py")
    spec = importlib.util.spec_from_file_location("oro_autopilot_monitor", path)
    assert spec is not None
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def test_busy_workers_without_closures_reports_throughput_stall():
    monitor = load_monitor_module()
    state = monitor.MonitorState(no_close_check_limit=2, same_assignment_check_limit=99)
    snapshot = {
        "queue_depth": 5,
        "active_count": 2,
        "idle_count": 0,
        "assignments": {"w1": "oro-a", "w2": "oro-b"},
        "qg_failure_incidents_open": 0,
    }

    first = state.evaluate(snapshot, closed_ids={"closed-before"})
    second = state.evaluate(snapshot, closed_ids={"closed-before"})

    assert first.actions == []
    assert "THROUGHPUT_STALL" in second.actions
    assert second.reason == "no productive closures while workers are busy"


def test_new_closure_resets_throughput_stall_counter():
    monitor = load_monitor_module()
    state = monitor.MonitorState(no_close_check_limit=2, same_assignment_check_limit=99)
    snapshot = {
        "queue_depth": 5,
        "active_count": 2,
        "idle_count": 0,
        "assignments": {"w1": "oro-a", "w2": "oro-b"},
        "qg_failure_incidents_open": 0,
    }

    state.evaluate(snapshot, closed_ids={"closed-before"})
    reset = state.evaluate(snapshot, closed_ids={"closed-before", "closed-now"})
    after_reset = state.evaluate(snapshot, closed_ids={"closed-before", "closed-now"})

    assert "THROUGHPUT_CLOSED" in reset.actions
    assert "THROUGHPUT_STALL" not in after_reset.actions


def test_qg_incident_increase_reports_immediate_signal():
    monitor = load_monitor_module()
    state = monitor.MonitorState(no_close_check_limit=99, same_assignment_check_limit=99)
    snapshot = {
        "queue_depth": 5,
        "active_count": 2,
        "idle_count": 0,
        "assignments": {"w1": "oro-a"},
        "qg_failure_incidents_open": 1,
    }

    state.evaluate(snapshot, closed_ids=set())
    result = state.evaluate({**snapshot, "qg_failure_incidents_open": 2}, closed_ids=set())

    assert "QG_INCIDENT_INCREASE" in result.actions
    assert result.previous_qg_open == 1
    assert result.current_qg_open == 2
