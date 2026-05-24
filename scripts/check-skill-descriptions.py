#!/usr/bin/env python3
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

DASH_SUMMARY_RE = re.compile(r"\s[-\u2013\u2014]\s")
MANDATORY_RULE_RE = re.compile(r"\b(?:must|always|no exceptions)\b", re.IGNORECASE)
WORKFLOW_SUMMARY_ERROR = "description must contain triggering conditions only, not a workflow summary"
ISSUE_CODES = {
    WORKFLOW_SUMMARY_ERROR: "workflow-summary",
}


def _frontmatter(content: str) -> dict[str, object] | None:
    if not content.startswith("---\n"):
        return None

    raw_yaml, separator, _body = content[4:].partition("\n---\n")
    if not separator:
        return None

    data: dict[str, object] = {}
    for line in raw_yaml.splitlines():
        key, separator, value = line.partition(":")
        if separator:
            data[key.strip()] = value.strip()
    return data


def _is_trigger_only_description(description: str) -> bool:
    return description.startswith("Use when ") and "\n" not in description


def check_skill_description(path: Path) -> list[str]:
    content = path.read_text(encoding="utf-8")
    frontmatter = _frontmatter(content)
    if frontmatter is None:
        return ["missing YAML frontmatter"]

    description = frontmatter.get("description")
    if not isinstance(description, str) or not description.strip():
        return ["missing description"]

    description = description.strip()
    if DASH_SUMMARY_RE.search(description) or MANDATORY_RULE_RE.search(description):
        return [WORKFLOW_SUMMARY_ERROR]

    if _is_trigger_only_description(description):
        return []

    return []


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("paths", nargs="+", type=Path)
    args = parser.parse_args()

    failed = False
    for path in args.paths:
        for issue in check_skill_description(path):
            failed = True
            code = ISSUE_CODES.get(issue)
            if code is None:
                print(f"{path}: {issue}", file=sys.stderr)
            else:
                print(f"{path}: {code}: {issue}", file=sys.stderr)

    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
