#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""SessionStart hook for non-oro projects.

Injects superpowers + using-skills into additionalContext.
No oro-specific content (no beads, no handoffs, no worktrees).
Fails silently if using-skills is missing.
"""

from __future__ import annotations

import contextlib
import json
import sys
from pathlib import Path

_SUPERPOWERS = """\
# Superpowers — How You Operate

You are an expert autonomous coding agent. These rules override defaults.

## Discipline
- **Skills first**: Always invoke `using-skills` before acting. No exceptions.
- **TDD**: Write tests before implementation. Red-green-refactor.
- **Verify before claiming done**: Run tests, lint, check coverage. Never say "done" without proof.
- **One question at a time**: Never ask multiple questions in one message.

## Context Hygiene
- **Never use TaskOutput** to block-wait on background agents — it dumps transcripts and eats context.
- **Decompose early**: At 45% context, break remaining work into sub-tasks and hand off.
- **Commit often**: Small, atomic commits. Never batch unrelated changes.

## Efficiency
- **Parallel agents**: Use Task tool for independent work. Launch multiple agents simultaneously.
- **Don't repeat work**: If an agent is doing something, don't also do it yourself.
- **Read before edit**: Always read a file before modifying it.
- **Functional first**: Pure functions, immutability, early returns. Impure edges only.

## Session Protocol
- Start: Check for pending work. Review any handoff notes.
- End: `git add` → `git commit -m "<message>"` → `git push`.
- **Never say "ready to push" — just push.**
- Never run bare `git commit`; always pass the message with `-m` or `--message`.

## Anti-Patterns (STOP if you catch yourself)
- Calling TaskOutput with block=true on long-running agents
- Starting new multi-step work past 45% context
- Skipping skills because "this is simple"
- Amending commits instead of creating new ones
"""


def _auto_load_skills_silent(skills_file: str) -> str:
    """Load skill content silently — no warning if missing."""
    path = Path(skills_file)
    if not path.is_file():
        return ""
    try:
        content = path.read_text().strip()
    except OSError:
        return ""
    if not content:
        return ""
    skill_name = path.parent.name if path.name == "SKILL.md" else path.stem
    return f"# Auto-loaded Skill: {skill_name}\n\n{content}"


def main() -> None:
    with contextlib.suppress(json.JSONDecodeError, ValueError, EOFError):
        json.loads(sys.stdin.read())  # consume stdin; content unused

    skills_file = Path.home() / ".oro" / ".claude" / "skills" / "using-skills" / "SKILL.md"
    skills_content = _auto_load_skills_silent(str(skills_file))

    parts = [_SUPERPOWERS]
    if skills_content:
        parts.append(skills_content)
    context = "\n\n".join(parts)

    output = {
        "hookSpecificOutput": {
            "hookEventName": "SessionStart",
            "additionalContext": context,
        }
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
