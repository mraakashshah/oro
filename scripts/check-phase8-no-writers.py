#!/usr/bin/env python3
"""Fail if Phase 8 migration would race an active bead writer."""

from __future__ import annotations

import os
import shlex
import subprocess

MUTATING_BEAD_COMMANDS = {
    "create",
    "update",
    "close",
    "reopen",
    "dep",
    "deps",
    "tag",
    "defer",
    "undefer",
    "comment",
    "note",
    "meta",
    "import",
    "migrate-from-dolt",
}

GLOBAL_FLAGS_WITH_VALUE = {
    "--config",
    "--home",
    "--project",
    "--socket",
    "--state-db",
    "--log-level",
}


def process_rows() -> list[str]:
    return subprocess.check_output(["ps", "-axo", "pid=,comm=,args="], text=True).splitlines()


def split_args(args: str) -> list[str]:
    try:
        return shlex.split(args)
    except ValueError:
        return args.split()


def is_oro_writer(tokens: list[str]) -> bool:
    try:
        idx = next(i for i, token in enumerate(tokens) if os.path.basename(token).startswith("oro"))
    except StopIteration:
        idx = 0
    else:
        idx += 1

    while idx < len(tokens) and tokens[idx].startswith("-"):
        flag = tokens[idx]
        idx += 1
        if flag in GLOBAL_FLAGS_WITH_VALUE and idx < len(tokens):
            idx += 1

    if idx >= len(tokens):
        return False
    if tokens[idx] == "start":
        return True
    if tokens[idx] == "dispatcher" and idx + 1 < len(tokens) and tokens[idx + 1] == "start":
        return True
    if tokens[idx] in {"work", "worker", "worker-launch"}:
        return True
    if tokens[idx] != "bead":
        return False

    idx += 1
    while idx < len(tokens) and tokens[idx].startswith("-"):
        idx += 1
    return idx < len(tokens) and tokens[idx] in MUTATING_BEAD_COMMANDS


def find_writer_matches(rows: list[str], self_pid: int) -> list[tuple[str, str]]:
    matches: list[tuple[str, str]] = []
    for row in rows:
        parts = row.strip().split(None, 2)
        if len(parts) < 2:
            continue
        pid, comm = parts[0], os.path.basename(parts[1])
        try:
            if int(pid) == self_pid:
                continue
        except ValueError:
            continue
        args = parts[2] if len(parts) > 2 else ""
        tokens = split_args(args)
        first = os.path.basename(tokens[0]) if tokens else comm
        first_base = os.path.basename(first)
        program = comm if comm in {"bd", "oro"} else first_base
        if program.startswith("oro"):
            program = "oro"

        if program == "bd" or (program == "oro" and is_oro_writer(tokens)):
            matches.append((pid, args))
    return matches


def main() -> int:
    matches = find_writer_matches(process_rows(), os.getpid())
    print(f"active_writer_count={len(matches)}")
    for pid, args in matches:
        print(f"{pid} {args}")
    return 1 if matches else 0


if __name__ == "__main__":
    raise SystemExit(main())
