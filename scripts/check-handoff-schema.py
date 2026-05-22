#!/usr/bin/env python3
"""Validate the minimal handoff YAML schema used by oro workers."""
# pylint: disable=import-error

from __future__ import annotations

import pathlib
import sys
from typing import Any

import yaml

REQUIRED_TASK_KEYS = ("completed", "in_progress", "remaining")


def validate_handoff(path: pathlib.Path) -> list[str]:
    """Return schema validation errors for a handoff YAML file."""
    if not path.is_file():
        return [f"file not found: {path}"]

    try:
        content = path.read_text(encoding="utf-8")
    except OSError as exc:
        return [f"cannot read {path}: {exc}"]

    try:
        data = yaml.safe_load(content)
    except yaml.YAMLError as exc:
        return [f"malformed YAML: {exc}"]

    if not isinstance(data, dict):
        return ["handoff must be a YAML mapping"]

    tasks = data.get("tasks")
    if not isinstance(tasks, dict):
        return ["missing required key: tasks"]

    errors: list[str] = []
    for key in REQUIRED_TASK_KEYS:
        value: Any = tasks.get(key)
        if key not in tasks:
            errors.append(f"missing required key: tasks.{key}")
        elif not isinstance(value, list):
            errors.append(f"tasks.{key} must be a list")
    return errors


def main(argv: list[str] | None = None) -> int:
    """Run the handoff schema linter CLI."""
    args = sys.argv[1:] if argv is None else argv
    if len(args) != 1:
        print("usage: check-handoff-schema.py HANDOFF.yaml", file=sys.stderr)
        return 2

    errors = validate_handoff(pathlib.Path(args[0]))
    for error in errors:
        print(error, file=sys.stderr)
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
