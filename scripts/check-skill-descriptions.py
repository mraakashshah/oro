#!/usr/bin/env python3
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

DASH_SUMMARY_RE = re.compile(r"\s[-\u2013\u2014]\s")
MANDATORY_RULE_RE = re.compile(
    r"\b(?:must|always|required|before any action|invoke relevant skills|no exceptions)\b",
    re.IGNORECASE,
)
WORKFLOW_SUMMARY_ERROR = "description must contain triggering conditions only, not a workflow summary"
DUPLICATE_SKILL_NAME_ERROR = "duplicate skill name"
ISSUE_CODES = {
    WORKFLOW_SUMMARY_ERROR: "workflow-summary",
    DUPLICATE_SKILL_NAME_ERROR: "duplicate-skill-name",
}
ALLOWED_DUPLICATE_SKILL_NAME_PATHS = frozenset(
    {
        Path("assets/skills/agent-browser/SKILL.md"),
        Path("assets/skills/agent-browser/agent-browser/SKILL.md"),
    }
)


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


def _display_path(path: Path) -> Path:
    try:
        return path.resolve().relative_to(Path.cwd().resolve())
    except ValueError:
        return path


def _iter_skill_paths(paths: list[Path]) -> list[Path]:
    skill_paths: list[Path] = []
    for path in paths:
        if path.is_dir():
            skill_paths.extend(sorted(path.rglob("SKILL.md")))
        else:
            skill_paths.append(path)
    return skill_paths


def _skill_name(path: Path) -> str | None:
    frontmatter = _frontmatter(path.read_text(encoding="utf-8"))
    if frontmatter is None:
        return None

    name = frontmatter.get("name")
    if not isinstance(name, str):
        return None

    name = name.strip()
    return name or None


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

    if not _is_trigger_only_description(description):
        return [WORKFLOW_SUMMARY_ERROR]

    return []


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("paths", nargs="+", type=Path)
    args = parser.parse_args()

    failed = False
    paths = _iter_skill_paths(args.paths)
    names: dict[str, list[Path]] = {}
    for path in paths:
        for issue in check_skill_description(path):
            failed = True
            code = ISSUE_CODES.get(issue)
            if code is None:
                print(f"{path}: {issue}", file=sys.stderr)
            else:
                print(f"{path}: {code}: {issue}", file=sys.stderr)

        name = _skill_name(path)
        if name is not None:
            names.setdefault(name, []).append(path)

    for name, duplicate_paths in names.items():
        if len(duplicate_paths) < 2:
            continue

        display_paths = frozenset(_display_path(path) for path in duplicate_paths)
        if display_paths == ALLOWED_DUPLICATE_SKILL_NAME_PATHS:
            continue

        failed = True
        code = ISSUE_CODES[DUPLICATE_SKILL_NAME_ERROR]
        for path in duplicate_paths:
            print(f"{path}: {code}: {DUPLICATE_SKILL_NAME_ERROR}: {name}", file=sys.stderr)

    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
