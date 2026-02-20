#!/usr/bin/env python3
"""PostToolUse hook: capture LEARNED entries from bd comments add into knowledge base.

Intercepts `bd comments add <id> "LEARNED: ..."` commands run via Bash tool.
Extracts structured entries into .beads/memory/knowledge.jsonl with
auto-tagging, deduplication by key, and timestamp.

Input: JSON on stdin with tool_name, tool_input, etc.
Output: JSON with additionalContext confirming capture, or nothing.
"""

import json
import re
import sys
from datetime import UTC, datetime
from pathlib import Path

KNOWLEDGE_FILE = ".beads/memory/knowledge.jsonl"
MAX_CONTENT_LEN = 2048

TAG_KEYWORDS = [
    "async",
    "auth",
    "cache",
    "cli",
    "concurrency",
    "css",
    "database",
    "docker",
    "git",
    "go",
    "graphql",
    "hook",
    "html",
    "http",
    "javascript",
    "json",
    "jwt",
    "kubernetes",
    "linux",
    "macos",
    "markdown",
    "node",
    "npm",
    "performance",
    "postgres",
    "python",
    "pytest",
    "react",
    "redis",
    "rest",
    "rust",
    "security",
    "sql",
    "sqlite",
    "swift",
    "test",
    "typescript",
    "uv",
    "wasm",
    "websocket",
]

# Pattern: bd comments add <bead_id> "LEARNED: <content>"
# Supports double quotes, single quotes, or no quotes
_BD_COMMENT_RE = re.compile(
    r"""bd\s+comments\s+add\s+([\w-]+)\s+"""
    r"""(?:["']LEARNED:\s*(.+?)["']|LEARNED:\s*(.+))""",
    re.DOTALL,
)


def extract_learned(command: str) -> tuple[str, str] | None:
    """Extract bead_id and content from a bd comments add LEARNED command."""
    m = _BD_COMMENT_RE.search(command)
    if not m:
        return None
    bead_id = m.group(1)
    content = (m.group(2) or m.group(3) or "").strip()
    if not content:
        return None
    return bead_id, content[:MAX_CONTENT_LEN]


def auto_tag(content: str) -> list[str]:
    """Auto-detect technology tags from content."""
    if not content:
        return []
    lower = content.lower()
    return [kw for kw in TAG_KEYWORDS if re.search(rf"\b{kw}\b", lower)]


def slugify(text: str) -> str:
    """Convert text to a URL-safe slug for dedup keys."""
    slug = re.sub(r"[^a-z0-9\s-]", "", text.lower())
    slug = re.sub(r"[\s-]+", "-", slug).strip("-")
    return slug[:80]


def append_entry(
    knowledge_file: str,
    bead_id: str,
    content: str,
    tags: list[str],
) -> dict:
    """Append a LEARNED entry to the knowledge JSONL file. Returns the entry."""
    path = Path(knowledge_file)
    path.parent.mkdir(parents=True, exist_ok=True)

    entry = {
        "key": f"learned-{slugify(content)}",
        "type": "learned",
        "content": content,
        "bead": bead_id,
        "tags": tags,
        "ts": datetime.now(UTC).isoformat(),
    }

    with open(path, "a") as f:
        f.write(json.dumps(entry) + "\n")

    return entry


def recall(knowledge_file: str, query: str) -> list[dict]:
    """Search knowledge base by keyword. Deduplicates by key, keeping latest."""
    try:
        with open(knowledge_file) as f:
            lines = f.readlines()
    except OSError:
        return []

    # Parse all entries, dedup by key (latest wins)
    by_key: dict[str, dict] = {}
    for line in lines:
        line = line.strip()
        if not line:
            continue
        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue
        by_key[entry["key"]] = entry

    # Search content and tags
    query_lower = query.lower()
    return [
        e
        for e in by_key.values()
        if query_lower in e.get("content", "").lower() or query_lower in " ".join(e.get("tags", [])).lower()
    ]


def main() -> None:
    hook_input = json.loads(sys.stdin.read())

    if hook_input.get("tool_name") != "Bash":
        return

    command = hook_input.get("tool_input", {}).get("command", "")
    result = extract_learned(command)
    if not result:
        return

    bead_id, content = result
    tags = auto_tag(content)
    append_entry(KNOWLEDGE_FILE, bead_id, content, tags)

    output = {
        "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
            "additionalContext": (
                f"Captured LEARNED entry for {bead_id}: "
                f"{content[:80]}{'...' if len(content) > 80 else ''} "
                f"[tags: {', '.join(tags) or 'none'}]"
            ),
        }
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
