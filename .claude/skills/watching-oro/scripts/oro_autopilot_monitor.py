#!/usr/bin/env python3
"""Outcome-based Oro factory monitor.

This monitor treats a live dispatcher and fresh worker heartbeats as necessary
but not sufficient. A healthy factory must also close tasks or shrink the
queue within a bounded number of checks.
"""

import argparse
import dataclasses
import datetime as dt
import json
import subprocess
import time
from pathlib import Path
from typing import Any


@dataclasses.dataclass(frozen=True)
class MonitorDecision:
    actions: list[str]
    reason: str = ""
    new_closed: set[str] = dataclasses.field(default_factory=set)
    previous_qg_open: int | None = None
    current_qg_open: int | None = None


class MonitorState:
    def __init__(
        self,
        *,
        no_close_check_limit: int = 4,
        same_assignment_check_limit: int = 4,
        stall_restart_limit: int = 2,
    ) -> None:
        self.no_close_check_limit = no_close_check_limit
        self.same_assignment_check_limit = same_assignment_check_limit
        self.stall_restart_limit = stall_restart_limit
        self.no_close_checks = 0
        self.same_assignment_checks = 0
        self.stall_restarts = 0
        self.last_closed: set[str] | None = None
        self.last_queue: int | None = None
        self.last_assigned_beads: set[str] = set()
        self.last_qg_open: int | None = None

    def evaluate(self, snapshot: dict[str, Any], closed_ids: set[str]) -> MonitorDecision:
        actions: list[str] = []
        reason = ""
        queue = int(snapshot.get("queue_depth", 0) or 0)
        active = int(snapshot.get("active_count", 0) or 0)
        assignments = snapshot.get("assignments", {}) or {}
        assigned_beads = set(assignments.values())
        qg_open = int(snapshot.get("qg_failure_incidents_open", 0) or 0)

        new_closed = self._evaluate_closures(closed_ids, queue, active)
        if new_closed:
            actions.append("THROUGHPUT_CLOSED")

        if assigned_beads and active > 0 and assigned_beads == self.last_assigned_beads:
            self.same_assignment_checks += 1
        else:
            self.same_assignment_checks = 0

        previous_qg_open = self.last_qg_open
        if previous_qg_open is not None and qg_open > previous_qg_open:
            actions.append("QG_INCIDENT_INCREASE")

        if self._is_throughput_stalled(queue):
            actions.append("THROUGHPUT_STALL")
            reason = self._stall_reason()
            self.no_close_checks = 0
            self.same_assignment_checks = 0
            self.stall_restarts += 1
            if self.stall_restarts >= self.stall_restart_limit:
                actions.append("RESTART_FACTORY")
                self.stall_restarts = 0

        self.last_queue = queue
        self.last_assigned_beads = assigned_beads
        self.last_qg_open = qg_open

        return MonitorDecision(
            actions=actions,
            reason=reason,
            new_closed=new_closed,
            previous_qg_open=previous_qg_open,
            current_qg_open=qg_open,
        )

    def _evaluate_closures(self, closed_ids: set[str], queue: int, active: int) -> set[str]:
        if self.last_closed is None:
            self.last_closed = set(closed_ids)
            if queue > 0 and active > 0:
                self.no_close_checks += 1
            return set()

        new_closed = closed_ids - self.last_closed
        if new_closed:
            self.last_closed = set(closed_ids)
            self.no_close_checks = 0
            self.same_assignment_checks = 0
            self.stall_restarts = 0
            return new_closed

        if queue > 0 and active > 0 and (self.last_queue is None or queue >= self.last_queue):
            self.no_close_checks += 1
        else:
            self.no_close_checks = 0
        return set()

    def _is_throughput_stalled(self, queue: int) -> bool:
        if queue <= 0:
            return False
        return (
            self.no_close_checks >= self.no_close_check_limit
            or self.same_assignment_checks >= self.same_assignment_check_limit
        )

    def _stall_reason(self) -> str:
        if self.no_close_checks >= self.no_close_check_limit:
            return "no productive closures while workers are busy"
        return "same tasks assigned for too many checks"


class OroAutopilot:
    def __init__(self, *, oro: str, repo: Path, log_path: Path, target: int, max_workers: int) -> None:
        self.oro = oro
        self.repo = repo
        self.log_path = log_path
        self.target = target
        self.max_workers = max_workers
        self.state = MonitorState()

    def run_forever(self, interval_secs: int) -> None:
        self.log(f"autopilot_started target={self.target} max_workers={self.max_workers} interval={interval_secs}s")
        while True:
            try:
                self.check_once()
            except Exception as exc:
                self.log(f"monitor_exception type={type(exc).__name__} error={exc}")
            time.sleep(interval_secs)

    def check_once(self) -> None:
        snapshot = self.status()
        if snapshot is None:
            self.restart_factory("status_unavailable")
            return

        closed = self.closed_ids()
        decision = self.state.evaluate(snapshot, closed)
        assignments = snapshot.get("assignments", {}) or {}
        self.log(
            "state={state} active={active} idle={idle} managed={managed}/{target} "
            "queue={queue} qg_open={qg_open} assignments={assignments}".format(
                state=snapshot.get("state"),
                active=snapshot.get("active_count", 0),
                idle=snapshot.get("idle_count", 0),
                managed=snapshot.get("managed_count", 0),
                target=self.target,
                queue=snapshot.get("queue_depth", 0),
                qg_open=snapshot.get("qg_failure_incidents_open", 0),
                assignments=assignments,
            )
        )
        self.apply_liveness_policy(snapshot)
        self.apply_decision(decision)

    def apply_liveness_policy(self, snapshot: dict[str, Any]) -> None:
        state = snapshot.get("state")
        if state != "running":
            proc = self.run([self.oro, "directive", "resume"], timeout=30)
            self.log(f"ACTION resume state={state} rc={proc.returncode} tail={tail(proc.stdout, 300)!r}")

        managed = int(snapshot.get("managed_count", 0) or 0)
        target = int(snapshot.get("target_count", self.target) or self.target)
        if managed < self.target or target != self.target:
            proc = self.run([self.oro, "directive", "scale", str(self.target)], timeout=30)
            self.log(
                f"ACTION scale target={self.target} managed={managed} "
                f"rc={proc.returncode} tail={tail(proc.stdout, 300)!r}"
            )

    def apply_decision(self, decision: MonitorDecision) -> None:
        if "THROUGHPUT_CLOSED" in decision.actions:
            self.log(f"throughput closed={sorted(decision.new_closed)}")
        if "QG_INCIDENT_INCREASE" in decision.actions:
            self.log(f"QG_INCIDENT_INCREASE previous={decision.previous_qg_open} current={decision.current_qg_open}")
            self.capture_snapshot("qg_incident_increase")
        if "THROUGHPUT_STALL" in decision.actions:
            self.log(f"THROUGHPUT_STALL reason={decision.reason}")
            self.capture_snapshot("throughput_stall")
        if "RESTART_FACTORY" in decision.actions:
            self.restart_factory("throughput_stall")

    def status(self) -> dict[str, Any] | None:
        proc = self.run([self.oro, "directive", "status"], timeout=20)
        if proc.returncode != 0:
            self.log(f"status_failed rc={proc.returncode} output={tail(proc.stdout, 500)!r}")
            return None
        try:
            return json.loads(proc.stdout)
        except json.JSONDecodeError as exc:
            self.log(f"status_json_failed error={exc} output={tail(proc.stdout, 500)!r}")
            return None

    def closed_ids(self) -> set[str]:
        proc = self.run([self.oro, "task", "closed", "--limit", "25", "--json"], timeout=30)
        if proc.returncode != 0:
            self.log(f"closed_failed rc={proc.returncode} output={tail(proc.stdout, 500)!r}")
            return set()
        try:
            rows = json.loads(proc.stdout)
        except json.JSONDecodeError as exc:
            self.log(f"closed_json_failed error={exc} output={tail(proc.stdout, 500)!r}")
            return set()
        return {row.get("id", "") for row in rows if row.get("id")}

    def capture_snapshot(self, reason: str) -> None:
        commands = [
            ("ready", [self.oro, "task", "ready"]),
            ("closed", [self.oro, "task", "closed", "--limit", "10"]),
            ("logs", [self.oro, "logs", "--tail", "80"]),
        ]
        for label, args in commands:
            proc = self.run(args, timeout=45)
            self.log(f"snapshot reason={reason} label={label} rc={proc.returncode} tail={tail(proc.stdout, 2500)!r}")

    def restart_factory(self, reason: str) -> None:
        self.log(f"ACTION restart reason={reason}")
        stop = self.run(["env", "ORO_HUMAN_CONFIRMED=1", self.oro, "stop", "--force"], timeout=120)
        self.log(f"stop rc={stop.returncode} tail={tail(stop.stdout, 500)!r}")
        start = self.run(
            [self.oro, "start", "--workers", str(self.target), "--max-workers", str(self.max_workers), "--detach"],
            timeout=120,
        )
        self.log(f"start rc={start.returncode} tail={tail(start.stdout, 500)!r}")

    def run(self, args: list[str], timeout: int) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            args,
            cwd=self.repo,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=timeout,
            check=False,
        )

    def log(self, message: str) -> None:
        self.log_path.parent.mkdir(parents=True, exist_ok=True)
        with self.log_path.open("a", encoding="utf-8") as handle:
            handle.write(f"{now()} {message}\n")


def now() -> str:
    return dt.datetime.now(dt.UTC).isoformat()


def tail(text: str, limit: int) -> str:
    return text.strip()[-limit:]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Monitor an Oro swarm for liveness and throughput.")
    parser.add_argument("--oro", default="./oro", help="path to oro binary")
    parser.add_argument("--repo", default=".", help="repository directory")
    parser.add_argument("--log", default=str(Path.home() / ".oro" / "autopilot-monitor.log"), help="log file path")
    parser.add_argument("--target", type=int, default=2, help="target managed worker count")
    parser.add_argument("--max-workers", type=int, default=2, help="maximum managed worker count")
    parser.add_argument("--interval", type=int, default=120, help="seconds between checks")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    monitor = OroAutopilot(
        oro=args.oro,
        repo=Path(args.repo),
        log_path=Path(args.log),
        target=args.target,
        max_workers=args.max_workers,
    )
    monitor.run_forever(args.interval)


if __name__ == "__main__":
    main()
