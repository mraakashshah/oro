#!/usr/bin/env python3
"""SessionStart hook: surface stale beads, merged worktree cleanup, recent learnings.

Also injects role-specific beacon context when ORO_ROLE is set.
The full role prompt is loaded from .claude/hooks/beacons/{role}.md, so send-keys
only needs to send a short nudge — the hook handles the heavy context injection.

Outputs additionalContext on SessionStart.

Pure functions for testability:
  - find_stale_beads(bd_output, days_threshold=3)
  - find_merged_worktrees(worktrees_dir, main_branch="main")
  - recent_memories_db(n=5)
  - role_beacon(role, beacons_dir)
"""

import contextlib
import json
import os
import re
import sqlite3
import subprocess
import sys
import time
from datetime import UTC, datetime
from pathlib import Path

import yaml


def oro_home():
    """Return ORO_HOME or default ~/.oro."""
    return os.environ.get("ORO_HOME", os.path.expanduser("~/.oro"))


def oro_project_dir():
    """Return $ORO_HOME/projects/$ORO_PROJECT or None if ORO_PROJECT not set."""
    home = oro_home()
    project = os.environ.get("ORO_PROJECT", "")
    if not project:
        return None
    return os.path.join(home, "projects", project)


WORKTREES_DIR = ".worktrees"
BEACONS_DIR = os.path.join(oro_home(), "beacons") if os.environ.get("ORO_PROJECT") else ".claude/hooks/beacons"
STATE_DB = os.path.join(oro_home(), "state.db")

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
- **Decompose early**: At 45% context, create tasks for remaining work and start handing off.
- **Commit often**: Small, atomic commits. Never batch unrelated changes.

## Efficiency
- **Parallel agents**: Use Task tool for independent work. Launch multiple agents simultaneously.
- **Don't repeat work**: If an agent is doing something, don't also do it yourself.
- **Read before edit**: Always read a file before modifying it.
- **Functional first**: Pure functions, immutability, early returns. Impure edges only.

## Session Protocol
- Start: `oro task ready` to find work. Check latest handoff in `docs/handoffs/`.
- End: `oro task close` → `git add` → `git commit` → `git push` (pre-commit hook auto-syncs tasks).
- **Never say "ready to push" — just push.**

## Anti-Patterns (STOP if you catch yourself)
- Calling TaskOutput with block=true on long-running agents
- Starting new multi-step work past 45% context
- Skipping skills because "this is simple"
- Amending commits instead of creating new ones
- Using cd into worktrees
"""


def role_beacon(role: str, beacons_dir: str = BEACONS_DIR) -> str:
    """Load the beacon markdown for the given ORO_ROLE.

    Returns the beacon content as a string, or empty string if the role is
    unknown or the beacon file does not exist.
    """
    if not role:
        return ""
    beacon_file = Path(beacons_dir) / f"{role}.md"
    try:
        return beacon_file.read_text()
    except OSError:
        return ""


# Pattern: ◐ oro-xyz [● P2] [feature] - Title
_BEAD_LINE_RE = re.compile(r"^◐\s+([\w.-]+)\s+\[")
# Pattern:   Updated: 2026-02-07
_UPDATED_RE = re.compile(r"Updated:\s*(\d{4}-\d{2}-\d{2})")


def find_stale_beads(bd_output: str, days_threshold: int = 3) -> list[dict]:
    """Parse bead list/show output and return beads not updated in >days_threshold days.

    Expects input where each bead header line is followed by a line containing
    'Updated: YYYY-MM-DD'. Returns list of dicts with id, title, days_stale.
    """
    if not bd_output.strip():
        return []

    stale = []
    now = datetime.now(UTC)
    current_id = None
    current_title = None

    for line in bd_output.splitlines():
        bead_match = _BEAD_LINE_RE.match(line)
        if bead_match:
            current_id = bead_match.group(1)
            # Extract title: everything after the last " - "
            title_parts = line.split(" - ", 1)
            current_title = title_parts[1].strip() if len(title_parts) > 1 else current_id
            continue

        if current_id:
            updated_match = _UPDATED_RE.search(line)
            if updated_match:
                updated_date = datetime.strptime(updated_match.group(1), "%Y-%m-%d").replace(tzinfo=UTC)
                days_old = (now - updated_date).days
                if days_old > days_threshold:
                    stale.append(
                        {
                            "id": current_id,
                            "title": current_title,
                            "days_stale": days_old,
                        }
                    )
                current_id = None
                current_title = None

    return stale


def find_stale_beads_from_json(items: list[dict], days_threshold: int = 3) -> list[dict]:
    """Return beads whose updated_at timestamp is older than ``days_threshold`` days."""
    stale = []
    now = datetime.now(UTC)
    for item in items:
        if not isinstance(item, dict):
            continue
        updated_raw = str(item.get("updated_at", ""))
        if not updated_raw:
            continue
        with contextlib.suppress(ValueError):
            updated_at = datetime.fromisoformat(updated_raw.replace("Z", "+00:00"))
            days_old = (now - updated_at).days
            if days_old > days_threshold:
                stale.append(
                    {
                        "id": str(item.get("id", "")),
                        "title": str(item.get("title", item.get("id", ""))).strip(),
                        "days_stale": days_old,
                    }
                )
    return [b for b in stale if b["id"]]


def find_merged_worktrees(worktrees_dir: str, main_branch: str = "main") -> list[dict]:
    """Check worktrees directory for branches already merged to main.

    Returns list of dicts with path, branch, and cleanup command.
    """
    wt_path = Path(worktrees_dir)
    if not wt_path.is_dir():
        return []

    merged = []
    for child in sorted(wt_path.iterdir()):
        if not child.is_dir():
            continue
        # Get the branch for this worktree
        try:
            result = subprocess.run(
                ["git", "rev-parse", "--abbrev-ref", "HEAD"],
                cwd=str(child),
                capture_output=True,
                text=True,
                timeout=5,
                start_new_session=True,
            )
            if result.returncode != 0:
                continue
            branch = result.stdout.strip()
            if not branch or branch == "HEAD":
                continue

            # Check if this branch is merged into main
            merged_result = subprocess.run(
                ["git", "branch", "--merged", main_branch],
                cwd=str(child),
                capture_output=True,
                text=True,
                timeout=5,
                start_new_session=True,
            )
            if merged_result.returncode != 0:
                continue

            merged_branches = [b.strip().lstrip("* ") for b in merged_result.stdout.splitlines()]
            if branch in merged_branches:
                merged.append(
                    {
                        "path": str(child),
                        "branch": branch,
                        "cleanup": f"git worktree remove {child.name} && git branch -d {branch}",
                    }
                )
        except (subprocess.TimeoutExpired, OSError):
            continue

    return merged


def recent_memories_db(n: int = 5) -> list[dict]:
    """Fetch recent memories from oro's SQLite store via the CLI.

    Calls ``oro memories list --format=json --limit=N`` and parses the JSON output.
    Returns empty list when oro is not on PATH, exits non-zero, or outputs invalid JSON.
    """
    try:
        result = subprocess.run(
            ["oro", "memories", "list", "--format=json", f"--limit={n}"],
            capture_output=True,
            text=True,
            timeout=10,
            start_new_session=True,
        )
        if result.returncode != 0:
            return []
    except (subprocess.TimeoutExpired, OSError):
        return []

    try:
        entries = json.loads(result.stdout)
    except (json.JSONDecodeError, ValueError):
        return []

    return entries if isinstance(entries, list) else []


_CLOSED_LINE_RE = re.compile(r"^✓\s+([\w.-]+)\s+\[.*?\]\s+\[.*?\]\s+-\s+(.+)$")


def recently_closed_beads(limit: int = 3) -> list[dict]:
    """Get the most recently closed beads (sorted by close date, descending).

    Uses the oro task command and falls back to an empty list on failures.
    """
    try:
        result = subprocess.run(
            ["oro", "task", "closed", f"--limit={limit}", "--json"],
            capture_output=True,
            text=True,
            timeout=10,
            start_new_session=True,
        )
        if result.returncode != 0:
            return []
    except (subprocess.TimeoutExpired, OSError):
        return []

    try:
        items = json.loads(result.stdout)
    except (json.JSONDecodeError, ValueError):
        return []
    if not isinstance(items, list):
        return []
    beads = []
    for item in items[:limit]:
        if isinstance(item, dict):
            beads.append({"id": str(item.get("id", "")), "title": str(item.get("title", "")).strip()})
    return [b for b in beads if b["id"]]


def ready_beads(limit: int = 4) -> list[dict]:
    """Get beads that are ready to work on (no blockers)."""
    try:
        result = subprocess.run(
            ["oro", "task", "ready", "--json"],
            capture_output=True,
            text=True,
            timeout=10,
            start_new_session=True,
        )
        if result.returncode != 0:
            return []
    except (subprocess.TimeoutExpired, OSError):
        return []

    try:
        items = json.loads(result.stdout)
    except (json.JSONDecodeError, ValueError):
        return []
    if not isinstance(items, list):
        return []
    beads = []
    for item in items[:limit]:
        if isinstance(item, dict):
            beads.append({"id": str(item.get("id", "")), "title": str(item.get("title", "")).strip()})
    return [b for b in beads if b["id"]]


def session_banner(closed: list[dict], ready: list[dict]) -> str:
    """Format a user-visible session banner with recently closed and up-next beads."""
    lines = []

    if closed:
        lines.append("  Just finished:")
        for b in closed:
            lines.append(f"    ✓ {b['id']}: {b['title']}")

    if ready:
        lines.append("  Up next:")
        for b in ready:
            lines.append(f"    → {b['id']}: {b['title']}")

    if not lines:
        return ""

    return "\n".join(lines)


_project_dir = oro_project_dir()
HANDOFFS_DIR = os.path.join(_project_dir, "handoffs") if _project_dir else "docs/handoffs"
PANES_DIR = os.path.join(oro_home(), "panes")


def archive_handoff(handoff_file: Path) -> None:
    """Archive an existing handoff file to a timestamped backup.

    Creates a copy of the handoff file with a timestamp suffix (e.g.,
    handoff.2026-02-23_14-30-45.yaml) in the same directory as the original.
    If an archive with the same timestamp already exists, appends a counter
    (e.g., handoff.2026-02-23_14-30-45.1.yaml).
    The original file remains unchanged. Does nothing if the file doesn't exist.

    Best-effort: silently skips on errors (e.g., permission issues).
    """
    if not handoff_file.is_file():
        return

    try:
        # Generate timestamp: YYYY-MM-DD_HH-MM-SS
        now = datetime.now(UTC)
        timestamp = now.strftime("%Y-%m-%d_%H-%M-%S")

        # Create archive filename: original_name.TIMESTAMP.yaml
        # e.g., "handoff.yaml" -> "handoff.2026-02-23_14-30-45.yaml"
        stem = handoff_file.stem
        suffix = handoff_file.suffix
        archive_name = f"{stem}.{timestamp}{suffix}"
        archive_path = handoff_file.parent / archive_name

        # If archive already exists, append a counter to make it unique
        counter = 1
        while archive_path.exists():
            archive_name = f"{stem}.{timestamp}.{counter}{suffix}"
            archive_path = handoff_file.parent / archive_name
            counter += 1

        # Copy file content to archive
        archive_path.write_text(handoff_file.read_text())
    except OSError:
        # Best-effort: log warning and continue
        print(f"warning: could not archive handoff file {handoff_file}", file=sys.stderr)


def pane_handoff(role: str, panes_dir: str = PANES_DIR) -> str:
    """Read per-role handoff from panes_dir/<role>/handoff.yaml.

    Returns formatted handoff string, or empty string if:
    - role is empty
    - panes dir or handoff file doesn't exist
    - YAML is malformed (logs warning to stderr)

    Content is truncated to 2000 chars.
    """
    if not role:
        return ""
    handoff_file = Path(panes_dir) / role / "handoff.yaml"
    if not handoff_file.is_file():
        return ""
    try:
        content = handoff_file.read_text()
    except OSError:
        return ""

    # Validate YAML structure (must parse without error)
    try:
        list(yaml.safe_load_all(content))
    except Exception:
        print(f"warning: malformed YAML in {handoff_file}, skipping pane handoff", file=sys.stderr)
        return ""

    # Archive the handoff file before returning (memory persistence)
    archive_handoff(handoff_file)

    label = "## Latest Handoff (Auto-Recovery)\n```yaml\n"
    return label + content[:2000] + ("\n...(truncated)" if len(content) > 2000 else "") + "\n```"


def latest_handoff(handoffs_dir: str) -> str:
    """Read the latest handoff YAML file and return its contents (truncated to 2000 chars).

    Archives the handoff file to a timestamped backup before returning (memory persistence).
    """
    hd = Path(handoffs_dir)
    if not hd.is_dir():
        return ""
    files = sorted(hd.glob("*.yaml"))
    if not files:
        return ""
    try:
        handoff_file = files[-1]
        content = handoff_file.read_text()

        # Archive the handoff file before returning (memory persistence)
        archive_handoff(handoff_file)

        label = f"## Latest Handoff ({handoff_file.name})\n```yaml\n"
        return label + content[:2000] + ("\n...(truncated)" if len(content) > 2000 else "") + "\n```"
    except OSError:
        return ""


def update_pane_activity(role: str, state_db: str = STATE_DB) -> None:
    """Update pane_activity table with current timestamp for the given role.

    Best-effort: logs warning on error, doesn't block session.
    Skips update if role is empty (not in oro context).
    """
    if not role:
        return

    try:
        conn = sqlite3.connect(state_db, timeout=1.0)
        try:
            ts = int(time.time())
            conn.execute("INSERT OR REPLACE INTO pane_activity (pane, last_seen) VALUES (?, ?)", (role, ts))
            conn.commit()
        finally:
            conn.close()
    except sqlite3.Error as e:
        print(f"warning: could not update pane_activity: {e}", file=sys.stderr)


def auto_load_skills(skills_file: str) -> str:
    """Load skill content from the given file path.

    Returns formatted content with header when file is valid and non-empty.
    Returns empty string and logs warning when file is missing.
    Returns empty string when file is empty.
    """
    skills_path = Path(skills_file)

    # Check if file exists
    if not skills_path.is_file():
        print(f"warning: skills file not found: {skills_file}", file=sys.stderr)
        return ""

    # Read file content
    try:
        content = skills_path.read_text().strip()
    except OSError as e:
        print(f"warning: could not read skills file {skills_file}: {e}", file=sys.stderr)
        return ""

    # Return empty string if content is empty
    if not content:
        return ""

    # Format and return content with header
    # Skills use <name>/SKILL.md convention — derive name from parent dir
    skill_name = skills_path.parent.name if skills_path.name == "SKILL.md" else skills_path.stem
    return f"# Auto-loaded Skill: {skill_name}\n\n{content}"


def project_state() -> str:
    """Gather ready tasks, git status, and git log into a context string."""
    sections = []

    # ready tasks
    try:
        result = subprocess.run(
            ["oro", "task", "ready"], capture_output=True, text=True, timeout=10, start_new_session=True
        )
        if result.returncode == 0 and result.stdout.strip():
            sections.append(f"## Ready Work\n```\n{result.stdout.strip()}\n```")
    except (subprocess.TimeoutExpired, OSError):
        pass

    # git status + log
    try:
        status = subprocess.run(
            ["git", "status", "--short"], capture_output=True, text=True, timeout=5, start_new_session=True
        )
        log = subprocess.run(
            ["git", "log", "--oneline", "-5"], capture_output=True, text=True, timeout=5, start_new_session=True
        )
        git_lines = []
        if status.returncode == 0:
            git_lines.append(f"Status:\n{status.stdout.strip() or '(clean)'}")
        if log.returncode == 0:
            git_lines.append(f"Recent commits:\n{log.stdout.strip()}")
        if git_lines:
            sections.append("## Git State\n```\n" + "\n".join(git_lines) + "\n```")
    except (subprocess.TimeoutExpired, OSError):
        pass

    return "\n\n".join(sections)


def _format_learning_entry(entry: dict) -> str:
    """Format a single learning entry from memories.db."""
    content = entry.get("content", "")
    mem_type = entry.get("type", "memory")
    return f"- [{mem_type}] {content}"


def _format_output(stale: list[dict], merged: list[dict], learnings: list[dict]) -> str:
    """Format all findings into a single context string."""
    sections = []

    if stale:
        lines = ["## Stale Tasks (no update in >3 days)"]
        for b in stale:
            lines.append(f"- **{b['id']}**: {b['title']} ({b['days_stale']} days stale)")
        lines.append("Consider: close, update status, or add a comment to keep active.")
        sections.append("\n".join(lines))

    if merged:
        lines = ["## Merged Worktrees (cleanup available)"]
        for w in merged:
            lines.append(f"- `{w['path']}` (branch: {w['branch']})")
            lines.append(f"  Cleanup: `{w['cleanup']}`")
        sections.append("\n".join(lines))

    if learnings:
        lines = ["## Recent Learnings"]
        for entry in learnings:
            lines.append(_format_learning_entry(entry))
        sections.append("\n".join(lines))

    return "\n\n".join(sections)


def main() -> None:
    # Worker agents get minimal context: superpowers + skills only.
    # Check first to skip all subprocess calls in worker sessions.
    is_worker = os.environ.get("ORO_WORKER") == "1"
    if is_worker:
        with contextlib.suppress(json.JSONDecodeError, ValueError):
            sys.stdin.read()
        skills_file = Path(oro_home()) / ".claude" / "skills" / "using-skills" / "SKILL.md"
        auto_skills = auto_load_skills(str(skills_file))
        parts = [_SUPERPOWERS]
        if auto_skills:
            parts.append(auto_skills)
        json.dump(
            {
                "hookSpecificOutput": {
                    "hookEventName": "SessionStart",
                    "additionalContext": "\n\n".join(parts),
                }
            },
            sys.stdout,
        )
        return

    # Read hook input from stdin (SessionStart event)
    hook_input = {}
    with contextlib.suppress(json.JSONDecodeError, ValueError):
        hook_input = json.loads(sys.stdin.read())

    # Determine session source: "startup", "resume", "clear", "compact"
    session_source = hook_input.get("source", "startup")

    # 1. Stale bead detection
    stale = []
    try:
        bd_ids_result = subprocess.run(
            ["oro", "task", "list", "--status=in_progress", "--json"],
            capture_output=True,
            text=True,
            timeout=10,
            start_new_session=True,
        )
        if bd_ids_result.returncode == 0 and bd_ids_result.stdout.strip():
            # Parse timestamps directly from oro task JSON output.
            items = []
            with contextlib.suppress(json.JSONDecodeError, ValueError):
                parsed = json.loads(bd_ids_result.stdout)
                if isinstance(parsed, list):
                    items = parsed
            stale = find_stale_beads_from_json(items, days_threshold=3)
    except (subprocess.TimeoutExpired, OSError):
        pass

    # 2. Merged worktree cleanup
    merged = find_merged_worktrees(WORKTREES_DIR)

    # 3. Recent memories from memories.db
    learnings = recent_memories_db(n=5)

    # 4. Update pane activity (if ORO_ROLE is set)
    oro_role = os.environ.get("ORO_ROLE", "")
    role_hooks_enabled = bool(oro_role)
    removed_planning_role = "arch" + "itect"
    if oro_role == removed_planning_role:
        print(
            f"[oro] ORO_ROLE={removed_planning_role} is no longer supported — "
            "this value was removed. See release notes.",
            file=sys.stderr,
        )
        role_hooks_enabled = False
    if role_hooks_enabled:
        update_pane_activity(oro_role)

    # 4a. Clean stale handoff signals from previous session
    if oro_role:
        panes_dir = os.path.join(oro_home(), "panes")
        role_dir = Path(panes_dir) / oro_role
        for signal_name in ("handoff_requested", "handoff_complete"):
            with contextlib.suppress(OSError):
                (role_dir / signal_name).unlink(missing_ok=True)

    # 5. Latest handoff + project state (skip if .no-reprime exists, unless /clear)
    handoff = ""
    state = ""
    is_priming = not Path(".no-reprime").is_file() or session_source == "clear"
    if is_priming:
        # Per-role pane handoff takes priority over directory handoff
        handoff = pane_handoff(oro_role) or latest_handoff(HANDOFFS_DIR)
        state = project_state()

    # 6. Role-specific beacon injection (ORO_ROLE env var set by oro start)
    beacon = role_beacon(oro_role) if role_hooks_enabled else ""

    # 7. Auto-load skills (inject using-skills content between Superpowers and Role Beacon)
    skills_file = Path(oro_home()) / ".claude" / "skills" / "using-skills" / "SKILL.md"
    auto_skills = auto_load_skills(str(skills_file))

    # Full injection for role sessions
    situational = _format_output(stale, merged, learnings)
    parts = [_SUPERPOWERS]
    if auto_skills:
        parts.append(auto_skills)
    if beacon:
        parts.append(f"# Role Beacon ({oro_role})\n\n{beacon}")
    for section_content in (handoff, state, situational):
        if section_content:
            parts.append(section_content)
    context = "\n\n".join(parts)

    output: dict = {
        "hookSpecificOutput": {
            "hookEventName": "SessionStart",
            "additionalContext": context,
        }
    }

    # 8. User-visible banner (only when priming)
    if is_priming:
        closed = recently_closed_beads(limit=3)
        ready = ready_beads(limit=4)
        banner = session_banner(closed, ready)
        if banner:
            output["systemMessage"] = banner

    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
