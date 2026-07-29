# Early Compact Trigger for Architect/Manager Panes — Implementation Plan

> **For Claude:** Use executing-plans skill to implement this plan task-by-task.

**Goal:** Trigger `/compact` proactively at soft threshold for architect/manager panes, before CC's natural compaction at ~59%.
**Architecture:** Six changes: (1) create `assets/thresholds.json`; (2) Makefile stages it + new `extractThresholdsJSON()` deploys it to `~/.oro/`; (3) new `compact_trigger.py` PostToolUse hook; (4) updated `session_start_compact.py` for role recovery; (5) `cmd_init.go` wiring; (6) new `regenerateProjectSettings()` helper + call in `cmd_start.go` on version bump.
**Tech Stack:** Python 3.10+, pytest, tmux, Go (cmd_init.go, cmd_start.go, Makefile).

---

## Adversarial review fixes applied

| Gap | Fix location |
|-----|-------------|
| `assets/thresholds.json` doesn't exist — nothing to embed | Task 0 (create `assets/thresholds.json`) |
| Makefile cp source was repo root, not `assets/` | Task 1 (use `assets/thresholds.json`) |
| `THRESHOLDS_FILE` resolves to non-existent `~/thresholds.json` | Task 1 + Task 3 |
| `extractAssets` has no path for FS-root files except CLAUDE.md | Task 1 (`extractThresholdsJSON` mirrors `extractClaudeMD`) |
| `settings.json` not regenerated on `oro start` | Task 7 (`regenerateProjectSettings` helper + call site in `cmd_start.go`) |
| Regeneration block has no `os.MkdirAll` before `os.WriteFile` | Task 7 (helper does `MkdirAll` first) |
| Task 7 test only checked `generateSettings()` output, not file write | Task 7 (test exercises full helper including disk write) |
| Test patches `os.path.expanduser` after import (no-op) | Task 5 (patch `session_start_compact.PANES_DIR` directly) |
| Debounce written before tmux call — persists on tmux failure | Task 3 (write debounce only after `returncode=0`) |
| `./oro status` breaks when CWD ≠ project root | Task 5 (`['oro', 'status']`) |

---

## Task 0: Create `assets/thresholds.json`

`thresholds.json` currently lives only at the repo root (used by Python hooks via local import).
We need a copy in `assets/` so the Makefile can stage it and `go:embed` can bundle it.

**Files:**
- Create: `assets/thresholds.json`

**Step 1: Create the file**

Create `assets/thresholds.json` with the same content as the repo-root `thresholds.json`:

```json
{
    "opus": 65,
    "sonnet": 50,
    "haiku": 40
}
```

**Step 2: Verify repo-root `thresholds.json` matches**

```bash
diff thresholds.json assets/thresholds.json
```

Expected: no diff. ✓

---

## Task 1: Stage and deploy `thresholds.json`

`compact_trigger.py` needs `thresholds.json` at `~/.oro/thresholds.json` at runtime.
`extractAssets()` only handles directory mappings and `CLAUDE.md`. Add `extractThresholdsJSON()`.

**Files:**
- Modify: `Makefile` (stage-assets target)
- Modify: `cmd/oro/cmd_init.go` (new function + call in `extractAssets`)

**Step 1: Add to Makefile stage-assets**

In `Makefile`, after the line:
```makefile
    @test -f assets/CLAUDE.md && cp assets/CLAUDE.md cmd/oro/_assets/ || true
```
Add:
```makefile
    @test -f assets/thresholds.json && cp assets/thresholds.json cmd/oro/_assets/ || true
```

**Step 2: Add `dev-sync` support (minor — keeps local dev in sync)**

In `Makefile`, in the `dev-sync` target, after the line that copies CLAUDE.md, add:
```makefile
    @test -f assets/thresholds.json && cp assets/thresholds.json $(ORO_HOME)/ || true
    @test -f $(ORO_HOME)/thresholds.json && echo "  ✓ ~/.oro/thresholds.json ok" || (echo "  ✗ ~/.oro/thresholds.json FAILED" && exit 1)
```

**Step 3: Add `extractThresholdsJSON` to `cmd_init.go`**

After the `extractClaudeMD` function (~line 759), add:

```go
// extractThresholdsJSON extracts thresholds.json from assets to dest/thresholds.json.
// Skips writing if force is false and the file already exists.
func extractThresholdsJSON(dest string, assets fs.FS, force bool) error {
    data, err := fs.ReadFile(assets, "thresholds.json")
    if err != nil {
        return nil //nolint:nilerr // thresholds.json is optional in assets
    }
    destPath := filepath.Join(dest, "thresholds.json")
    if !force && fileExists(destPath) {
        return nil
    }
    return os.WriteFile(destPath, data, 0o644) //nolint:gosec // needs to be readable
}
```

**Step 4: Call `extractThresholdsJSON` from `extractAssets`**

In `extractAssets()` (~line 794), after the `extractClaudeMD` call:

```go
func extractAssets(dest string, assets fs.FS, force bool) error {
    if err := extractClaudeMD(dest, assets, force); err != nil {
        return err
    }
    if err := extractThresholdsJSON(dest, assets, force); err != nil {  // ← add
        return err
    }
    // ... rest unchanged (assetMapping loop, version stamp)
```

**Step 5: Build and verify**

```bash
make build
ls cmd/oro/_assets/ | grep thresholds
```

Expected: `thresholds.json` listed. Build succeeds. ✓

---

## Task 2: Write failing tests for `compact_trigger.py`

**Files:**
- Create: `tests/test_compact_trigger.py`

**Step 1: Write the failing tests**

Create `tests/test_compact_trigger.py`:

```python
"""Tests for compact_trigger.py hook."""

from __future__ import annotations

import json
import sys
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / ".claude" / "hooks"))


class TestCompactTrigger:
    """Tests for the compact_trigger PostToolUse hook."""

    def _make_input(self, model_key: str = "sonnet") -> str:
        return json.dumps({"model_key": model_key, "transcript_path": "/fake/transcript"})

    def _run_main(self, stdin_json: str) -> None:
        """Import and run main() fresh (no module cache between tests)."""
        import io

        if "compact_trigger" in sys.modules:
            del sys.modules["compact_trigger"]
        import compact_trigger

        sys.stdin = io.StringIO(stdin_json)
        compact_trigger.main()

    def test_below_threshold_no_tmux(self, tmp_path: Path, monkeypatch) -> None:
        """pct < threshold → no tmux call."""
        pane_dir = tmp_path / "architect"
        pane_dir.mkdir(parents=True)
        (pane_dir / "context_pct").write_text("45\n")
        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text(json.dumps({"sonnet": 50}))

        monkeypatch.setenv("ORO_ROLE", "architect")
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setenv("TMUX_PANE", "%42")

        with (
            patch("compact_trigger.PANES_DIR", str(tmp_path)),
            patch("compact_trigger.THRESHOLDS_FILE", thresholds_file),
            patch("compact_trigger.subprocess") as mock_subp,
        ):
            self._run_main(self._make_input())

        mock_subp.run.assert_not_called()

    def test_at_threshold_triggers_compact(self, tmp_path: Path, monkeypatch) -> None:
        """pct >= threshold, no debounce → tmux called, debounce file written."""
        pane_dir = tmp_path / "architect"
        pane_dir.mkdir(parents=True)
        (pane_dir / "context_pct").write_text("51\n")
        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text(json.dumps({"sonnet": 50}))

        monkeypatch.setenv("ORO_ROLE", "architect")
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setenv("TMUX_PANE", "%42")

        with (
            patch("compact_trigger.PANES_DIR", str(tmp_path)),
            patch("compact_trigger.THRESHOLDS_FILE", thresholds_file),
            patch("compact_trigger.subprocess") as mock_subp,
        ):
            mock_subp.run.return_value = MagicMock(returncode=0)
            self._run_main(self._make_input())

        mock_subp.run.assert_called_once()
        args = mock_subp.run.call_args[0][0]
        assert "tmux" in args
        assert "/compact" in args
        assert (pane_dir / "compact_triggered").exists()

    def test_debounce_written_only_after_tmux_success(self, tmp_path: Path, monkeypatch) -> None:
        """If tmux send-keys fails (returncode != 0), debounce file is NOT written."""
        pane_dir = tmp_path / "architect"
        pane_dir.mkdir(parents=True)
        (pane_dir / "context_pct").write_text("55\n")
        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text(json.dumps({"sonnet": 50}))

        monkeypatch.setenv("ORO_ROLE", "architect")
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setenv("TMUX_PANE", "%42")

        with (
            patch("compact_trigger.PANES_DIR", str(tmp_path)),
            patch("compact_trigger.THRESHOLDS_FILE", thresholds_file),
            patch("compact_trigger.subprocess") as mock_subp,
        ):
            mock_subp.run.return_value = MagicMock(returncode=1)
            self._run_main(self._make_input())

        assert not (pane_dir / "compact_triggered").exists()

    def test_debounce_prevents_double_trigger(self, tmp_path: Path, monkeypatch) -> None:
        """pct >= threshold, debounce file exists → no-op."""
        pane_dir = tmp_path / "architect"
        pane_dir.mkdir(parents=True)
        (pane_dir / "context_pct").write_text("55\n")
        (pane_dir / "compact_triggered").touch()
        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text(json.dumps({"sonnet": 50}))

        monkeypatch.setenv("ORO_ROLE", "architect")
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setenv("TMUX_PANE", "%42")

        with (
            patch("compact_trigger.PANES_DIR", str(tmp_path)),
            patch("compact_trigger.THRESHOLDS_FILE", thresholds_file),
            patch("compact_trigger.subprocess") as mock_subp,
        ):
            self._run_main(self._make_input())

        mock_subp.run.assert_not_called()

    def test_no_tmux_pane_is_noop(self, tmp_path: Path, monkeypatch) -> None:
        """TMUX_PANE absent → no-op."""
        pane_dir = tmp_path / "architect"
        pane_dir.mkdir(parents=True)
        (pane_dir / "context_pct").write_text("60\n")
        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text(json.dumps({"sonnet": 50}))

        monkeypatch.setenv("ORO_ROLE", "architect")
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.delenv("TMUX_PANE", raising=False)

        with (
            patch("compact_trigger.PANES_DIR", str(tmp_path)),
            patch("compact_trigger.THRESHOLDS_FILE", thresholds_file),
            patch("compact_trigger.subprocess") as mock_subp,
        ):
            self._run_main(self._make_input())

        mock_subp.run.assert_not_called()

    def test_worker_is_noop(self, tmp_path: Path, monkeypatch) -> None:
        """ORO_WORKER=1 → no-op (workers have their own Go hard-stop)."""
        monkeypatch.setenv("ORO_WORKER", "1")
        monkeypatch.setenv("TMUX_PANE", "%42")

        with patch("compact_trigger.subprocess") as mock_subp:
            self._run_main(self._make_input())

        mock_subp.run.assert_not_called()

    def test_no_role_is_noop(self, tmp_path: Path, monkeypatch) -> None:
        """ORO_ROLE absent → no-op."""
        monkeypatch.delenv("ORO_ROLE", raising=False)
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setenv("TMUX_PANE", "%42")

        with patch("compact_trigger.subprocess") as mock_subp:
            self._run_main(self._make_input())

        mock_subp.run.assert_not_called()

    def test_missing_thresholds_file_uses_fallback(self, tmp_path: Path, monkeypatch) -> None:
        """thresholds.json missing → fallback threshold of 50."""
        pane_dir = tmp_path / "architect"
        pane_dir.mkdir(parents=True)
        (pane_dir / "context_pct").write_text("51\n")

        monkeypatch.setenv("ORO_ROLE", "architect")
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setenv("TMUX_PANE", "%42")

        with (
            patch("compact_trigger.PANES_DIR", str(tmp_path)),
            patch("compact_trigger.THRESHOLDS_FILE", tmp_path / "no-such-file.json"),
            patch("compact_trigger.subprocess") as mock_subp,
        ):
            mock_subp.run.return_value = MagicMock(returncode=0)
            self._run_main(self._make_input())

        mock_subp.run.assert_called_once()

    def test_pct_file_absent_is_noop(self, tmp_path: Path, monkeypatch) -> None:
        """context_pct file absent → skip."""
        pane_dir = tmp_path / "architect"
        pane_dir.mkdir(parents=True)
        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text(json.dumps({"sonnet": 50}))

        monkeypatch.setenv("ORO_ROLE", "architect")
        monkeypatch.delenv("ORO_WORKER", raising=False)
        monkeypatch.setenv("TMUX_PANE", "%42")

        with (
            patch("compact_trigger.PANES_DIR", str(tmp_path)),
            patch("compact_trigger.THRESHOLDS_FILE", thresholds_file),
            patch("compact_trigger.subprocess") as mock_subp,
        ):
            self._run_main(self._make_input())

        mock_subp.run.assert_not_called()
```

**Step 2: Run tests to verify they fail**

```bash
uv run pytest tests/test_compact_trigger.py -v
```

Expected: `ModuleNotFoundError: No module named 'compact_trigger'`. All 8 tests FAIL. ✓

---

## Task 3: Implement `compact_trigger.py`

**Files:**
- Create: `assets/hooks/compact_trigger.py`
- Create: `.claude/hooks/compact_trigger.py` (synced copy)

**Step 1: Write the implementation**

Create `assets/hooks/compact_trigger.py`:

```python
#!/usr/bin/env python3
"""PostToolUse hook: trigger /compact early for architect/manager panes.

Reads the context percentage written by context_pct_writer.py (which runs
first in the same PostToolUse chain) and sends /compact via tmux if the
soft threshold is crossed and no trigger is already pending.

Trigger conditions (all must be true):
  - ORO_ROLE is set (architect/manager)
  - ORO_WORKER != 1 (workers have a Go hard-stop)
  - TMUX_PANE env var is present
  - ~/.oro/panes/<role>/context_pct >= threshold
  - ~/.oro/panes/<role>/compact_triggered does NOT exist (debounce)

On trigger:
  1. tmux send-keys -t $TMUX_PANE "/compact" Enter
  2. Write ~/.oro/panes/<role>/compact_triggered  (only on tmux success)

Input: JSON on stdin with model_key.
Output: Silent. Best-effort.
"""

from __future__ import annotations

import contextlib
import json
import os
import subprocess
import sys
from pathlib import Path

PANES_DIR = os.path.expanduser("~/.oro/panes")
THRESHOLDS_FILE = Path(os.path.expanduser("~/.oro")) / "thresholds.json"
DEFAULT_THRESHOLD = 50


def load_threshold(model_key: str, thresholds_file: Path | None = None) -> int:
    """Load soft threshold % for model_key from ~/.oro/thresholds.json."""
    if thresholds_file is None:
        thresholds_file = THRESHOLDS_FILE
    try:
        thresholds = json.loads(thresholds_file.read_text())
        return int(thresholds.get(model_key, DEFAULT_THRESHOLD))
    except (OSError, json.JSONDecodeError, ValueError):
        return DEFAULT_THRESHOLD


def main() -> None:
    """Main entry point."""
    if os.getenv("ORO_WORKER") == "1":
        return

    role = os.getenv("ORO_ROLE")
    if not role:
        return

    tmux_pane = os.getenv("TMUX_PANE")
    if not tmux_pane:
        return

    hook_input = json.loads(sys.stdin.read())
    model_key = hook_input.get("model_key", "sonnet")

    pane_dir = Path(PANES_DIR) / role
    pct_file = pane_dir / "context_pct"
    debounce_file = pane_dir / "compact_triggered"

    try:
        pct = int(pct_file.read_text().strip())
    except (OSError, ValueError):
        return

    if pct < load_threshold(model_key):
        return

    if debounce_file.exists():
        return

    result = subprocess.run(
        ["tmux", "send-keys", "-t", tmux_pane, "/compact", "Enter"],
        capture_output=True,
        check=False,
    )

    if result.returncode == 0:
        with contextlib.suppress(OSError):
            pane_dir.mkdir(parents=True, exist_ok=True)
            debounce_file.touch()


if __name__ == "__main__":
    main()
```

**Step 2: Sync to `.claude/hooks/`**

```bash
cp assets/hooks/compact_trigger.py .claude/hooks/compact_trigger.py
```

**Step 3: Run tests**

```bash
uv run pytest tests/test_compact_trigger.py -v
```

Expected: All 8 tests PASS. ✓

---

## Task 4: Write failing tests for updated `session_start_compact.py`

**Files:**
- Modify: `tests/test_session_start_compact.py`

**Step 1: Add `MagicMock` to imports**

The current file imports `from unittest.mock import patch`. Change to:
```python
from unittest.mock import MagicMock, patch
```

**Step 2: Append `TestSessionStartCompactRole` class to end of file**

```python
class TestSessionStartCompactRole:
    """Tests for architect/manager (ORO_ROLE) paths added in compact trigger feature."""

    def test_clears_debounce_flag_on_compact(self, tmp_path: Path, monkeypatch, capsys) -> None:
        """debounce flag cleared when compact session starts."""
        pane_dir = tmp_path / "architect"
        pane_dir.mkdir(parents=True)
        debounce = pane_dir / "compact_triggered"
        debounce.touch()

        monkeypatch.setenv("ORO_ROLE", "architect")
        monkeypatch.delenv("ORO_WORKER", raising=False)

        import io

        sys.stdin = io.StringIO(json.dumps({"session_id": ""}))

        # Patch PANES_DIR directly — it's a module-level constant computed at import time.
        # Do NOT patch os.path.expanduser (already evaluated; patch would be a no-op).
        with (
            patch("session_start_compact.PANES_DIR", str(tmp_path)),
            patch("session_start_compact.subprocess.run", return_value=MagicMock(returncode=0, stdout="")),
        ):
            session_start_main()

        assert not debounce.exists()

    def test_role_session_injects_live_state(self, tmp_path: Path, monkeypatch, capsys) -> None:
        """ORO_ROLE set → oro status + bd list injected as additionalContext."""
        pane_dir = tmp_path / "manager"
        pane_dir.mkdir(parents=True)

        monkeypatch.setenv("ORO_ROLE", "manager")
        monkeypatch.delenv("ORO_WORKER", raising=False)

        import io

        sys.stdin = io.StringIO(json.dumps({"session_id": ""}))

        with (
            patch("session_start_compact.PANES_DIR", str(tmp_path)),
            patch("session_start_compact.subprocess.run") as mock_run,
        ):
            mock_run.side_effect = [
                MagicMock(returncode=0, stdout="workers: 2 running\nbead: oro-abc"),
                MagicMock(returncode=0, stdout="[● P1] oro-abc: Fix something"),
            ]
            session_start_main()

        output = json.loads(capsys.readouterr().out)
        ctx = output["additionalContext"]
        assert "workers: 2 running" in ctx
        assert "oro-abc" in ctx

    def test_oro_status_failure_suppressed(self, tmp_path: Path, monkeypatch, capsys) -> None:
        """oro status failure → suppressed, still returns additionalContext."""
        pane_dir = tmp_path / "architect"
        pane_dir.mkdir(parents=True)

        monkeypatch.setenv("ORO_ROLE", "architect")
        monkeypatch.delenv("ORO_WORKER", raising=False)

        import io

        sys.stdin = io.StringIO(json.dumps({"session_id": ""}))

        with (
            patch("session_start_compact.PANES_DIR", str(tmp_path)),
            patch("session_start_compact.subprocess.run", side_effect=OSError("not found")),
        ):
            session_start_main()

        output = json.loads(capsys.readouterr().out)
        assert isinstance(output, dict)
        assert "additionalContext" in output

    def test_worker_path_unchanged(self, tmp_path: Path, monkeypatch, capsys) -> None:
        """ORO_WORKER=1 → existing transcript-state path unchanged."""
        state = {
            "bead_id": "oro-work",
            "files_modified": ["pkg/worker/worker.go"],
            "errors": [],
            "last_assistant_message": "Running tests",
            "last_tool_calls": [{"name": "Bash"}],
        }
        state_dir = tmp_path / ".oro" / "compaction-state"
        state_dir.mkdir(parents=True)
        (state_dir / "session-w1.json").write_text(json.dumps(state))

        monkeypatch.setenv("ORO_WORKER", "1")
        monkeypatch.delenv("ORO_ROLE", raising=False)

        import io

        sys.stdin = io.StringIO(json.dumps({"session_id": "session-w1"}))

        with patch("session_start_compact.Path.home", return_value=tmp_path):
            with patch("session_start_compact.subprocess.run"):
                session_start_main()

        output = json.loads(capsys.readouterr().out)
        ctx = output["additionalContext"]
        assert "oro-work" in ctx
        assert "pkg/worker/worker.go" in ctx
```

**Step 3: Run to verify they fail**

```bash
uv run pytest tests/test_session_start_compact.py::TestSessionStartCompactRole -v
```

Expected: `AttributeError: module 'session_start_compact' has no attribute 'PANES_DIR'`. All FAIL. ✓

---

## Task 5: Update `session_start_compact.py`

**Files:**
- Modify: `assets/hooks/session_start_compact.py`
- Modify: `.claude/hooks/session_start_compact.py` (synced copy)

Key changes: adds `PANES_DIR` constant, `_clear_debounce()`, `_live_swarm_context()`, role-branch in `main()`.
Uses `['oro', 'status']` (NOT `['./oro', 'status']` — binary is on PATH after `make install`).

**Step 1: Overwrite `assets/hooks/session_start_compact.py`**

```python
#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""SessionStart hook (matcher: compact): recover context after compaction.

Two paths:
  1. ORO_ROLE set (architect/manager): clear debounce flag, inject live swarm
     state from `oro status` + `bd list --status=in_progress`.
  2. ORO_WORKER=1 (worker): read saved transcript state, inject, create continuation bead.

Input:  JSON with session_id, source ("compact")
Output: JSON with additionalContext: "..."
"""

from __future__ import annotations

import contextlib
import json
import os
import subprocess
import sys
from pathlib import Path

PANES_DIR = os.path.expanduser("~/.oro/panes")


def _clear_debounce(role: str) -> None:
    """Delete ~/.oro/panes/<role>/compact_triggered if it exists."""
    debounce = Path(PANES_DIR) / role / "compact_triggered"
    with contextlib.suppress(OSError):
        debounce.unlink()


def _live_swarm_context() -> str:
    """Return live swarm state string from oro status + bd list."""
    lines = ["Resuming after compaction. Live swarm state:"]
    for cmd, label in [
        (["oro", "status"], "Swarm status"),
        (["bd", "list", "--status=in_progress"], "In-progress beads"),
    ]:
        try:
            result = subprocess.run(cmd, capture_output=True, text=True, timeout=10, check=False)
            out = result.stdout.strip()
            if out:
                lines.append(f"\n{label}:\n{out}")
        except (OSError, subprocess.TimeoutExpired):
            pass
    return "\n".join(lines)


def main() -> None:
    """Main hook entry point."""
    try:
        input_data = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        print(json.dumps({}))
        return

    role = os.environ.get("ORO_ROLE")
    is_worker = os.environ.get("ORO_WORKER") == "1"

    if role:
        _clear_debounce(role)

    if role and not is_worker:
        print(json.dumps({"additionalContext": _live_swarm_context()}))
        return

    # Worker path — existing transcript-state injection
    session_id = input_data.get("session_id", "")
    if not session_id:
        print(json.dumps({}))
        return

    state_path = Path.home() / ".oro" / "compaction-state" / f"{session_id}.json"
    if not state_path.exists():
        print(json.dumps({}))
        return

    try:
        state = json.loads(state_path.read_text())
    except (json.JSONDecodeError, OSError):
        print(json.dumps({}))
        return

    lines = ["Resuming after compaction. Previous state:"]
    bead_id = state.get("bead_id")
    if bead_id:
        lines.append(f"  Bead in progress: {bead_id}")
    files = state.get("files_modified", [])
    if files:
        lines.append(f"  Files modified: {', '.join(files[:10])}")
    errors = state.get("errors", [])
    if errors:
        lines.append(f"  Recent errors: {'; '.join(errors[:3])}")
    last_msg = state.get("last_assistant_message", "")
    if last_msg:
        lines.append(f"  Last context: {last_msg[:200]}")
    tool_calls = state.get("last_tool_calls", [])
    if tool_calls:
        lines.append(f"  Recent tools: {', '.join(tc.get('name', '?') for tc in tool_calls)}")

    if bead_id and is_worker:
        _create_continuation_bead(bead_id, state)

    with contextlib.suppress(OSError):
        state_path.unlink()

    print(json.dumps({"additionalContext": "\n".join(lines)}))


def _create_continuation_bead(bead_id: str, state: dict) -> None:
    """Create a continuation bead for the dispatcher to pick up."""
    files = ", ".join(state.get("files_modified", [])[:5])
    last_msg = state.get("last_assistant_message", "")[:200]
    description = f"Continue work from compacted session.\nFiles: {files}\nContext: {last_msg}"
    with contextlib.suppress(OSError, subprocess.TimeoutExpired):
        subprocess.run(
            [
                "bd",
                "create",
                f"--title=Continue: {bead_id}",
                "--type=task",
                f"--parent={bead_id}",
                f"--description={description}",
            ],
            capture_output=True,
            timeout=10,
            check=False,
        )


if __name__ == "__main__":
    main()
```

**Step 2: Sync**

```bash
cp assets/hooks/session_start_compact.py .claude/hooks/session_start_compact.py
```

**Step 3: Run all session_start_compact tests**

```bash
uv run pytest tests/test_session_start_compact.py -v
```

Expected: All tests PASS (old `TestSessionStartCompact` + new `TestSessionStartCompactRole`). ✓

---

## Task 6: Wire `compact_trigger.py` into `cmd_init.go` + add wiring test

**Files:**
- Modify: `cmd/oro/cmd_init.go` (PostToolUse group)
- Modify: `cmd/oro/cmd_init_test.go` (new wiring test)

**Step 1: Add compact_trigger.py to PostToolUse blank-matcher group**

Find (~line 663):
```go
        {Matcher: "", Hooks: []hookEntry{
            {Type: "command", Command: py("context_pct_writer.py")},
            {Type: "command", Command: py("context_pruner.py")},
        }},
```

Change to:
```go
        {Matcher: "", Hooks: []hookEntry{
            {Type: "command", Command: py("context_pct_writer.py")},
            {Type: "command", Command: py("compact_trigger.py")},
            {Type: "command", Command: py("context_pruner.py")},
        }},
```

**Step 2: Add test to `cmd_init_test.go`**

`strings` is already imported in `cmd_init_test.go` (line 9 — do NOT add a duplicate import).

Add this test function:

```go
func TestBuildHookConfigContainsCompactTrigger(t *testing.T) {
    hooks := buildHookConfig("$HOME/.oro/hooks")
    postToolUse, ok := hooks["PostToolUse"]
    if !ok {
        t.Fatal("PostToolUse key missing from hook config")
    }

    var blankGroup *hookGroup
    for i := range postToolUse {
        if postToolUse[i].Matcher == "" {
            blankGroup = &postToolUse[i]
            break
        }
    }
    if blankGroup == nil {
        t.Fatal("no blank-matcher PostToolUse group found")
    }

    for _, h := range blankGroup.Hooks {
        if strings.Contains(h.Command, "compact_trigger.py") {
            return // found
        }
    }
    t.Error("compact_trigger.py not found in blank-matcher PostToolUse hooks")
}
```

**Step 3: Build + run wiring test**

```bash
make build
go test ./cmd/oro/... -run TestBuildHookConfigContainsCompactTrigger -v
```

Expected: PASS. ✓

---

## Task 7: Regenerate `settings.json` on version bump

Extract regeneration logic into a testable helper function in `cmd_start.go`.
Call it from `preflightAndCheckRunning` when `reExtracted=true`.
Write tests that exercise the actual file-write path.

**Files:**
- Modify: `cmd/oro/cmd_start.go` (`preflightAndCheckRunning` + new helper)
- Create or modify: `cmd/oro/cmd_start_test.go` (new tests)

**Step 1: Add helper function to `cmd_start.go`**

Add after `preflightAndCheckRunning` (~line 255):

```go
// regenerateProjectSettings writes a fresh settings.json to the project directory.
// Creates the project directory if it does not exist (non-fatal: errors logged to w).
// Called after asset re-extraction so new hooks take effect without 'oro init' re-run.
func regenerateProjectSettings(w io.Writer, oroHome, projectName string) {
    if projectName == "" {
        return
    }
    projectDir := filepath.Join(oroHome, "projects", projectName)
    if err := os.MkdirAll(projectDir, 0o755); err != nil { //nolint:gosec // user-owned dir
        fmt.Fprintf(w, "warning: could not create project dir for settings update: %v\n", err)
        return
    }
    settingsData, err := generateSettings("$HOME/.oro")
    if err != nil {
        fmt.Fprintf(w, "warning: could not generate settings.json after asset update: %v\n", err)
        return
    }
    settingsPath := filepath.Join(projectDir, "settings.json")
    if err := os.WriteFile(settingsPath, settingsData, 0o644); err != nil { //nolint:gosec // settings file needs to be readable
        fmt.Fprintf(w, "warning: could not update settings.json after asset update: %v\n", err)
    }
}
```

**Step 2: Call helper in `preflightAndCheckRunning`**

Replace:
```go
    // Re-extract assets if the binary's embedded version differs from the on-disk stamp.
    if _, err := checkAssetVersion(paths.OroHome, EmbeddedAssets); err != nil {
        return "", err
    }
```

With:
```go
    // Re-extract assets if the binary's embedded version differs from the on-disk stamp.
    reExtracted, err := checkAssetVersion(paths.OroHome, EmbeddedAssets)
    if err != nil {
        return "", err
    }
    if reExtracted {
        regenerateProjectSettings(w, paths.OroHome, readProjectName())
    }
```

**Step 3: Write tests**

Find or create `cmd/oro/cmd_start_test.go`. Add:

```go
func TestRegenerateProjectSettings_WritesFile(t *testing.T) {
    tmpHome := t.TempDir()
    var buf strings.Builder

    regenerateProjectSettings(&buf, tmpHome, "myproject")

    settingsPath := filepath.Join(tmpHome, "projects", "myproject", "settings.json")
    data, err := os.ReadFile(settingsPath)
    if err != nil {
        t.Fatalf("settings.json not written: %v", err)
    }
    if !strings.Contains(string(data), "compact_trigger.py") {
        t.Errorf("settings.json missing compact_trigger.py\ngot: %s", string(data))
    }
    if buf.String() != "" {
        t.Errorf("unexpected warning output: %s", buf.String())
    }
}

func TestRegenerateProjectSettings_EmptyProjectName_Noop(t *testing.T) {
    tmpHome := t.TempDir()
    var buf strings.Builder

    regenerateProjectSettings(&buf, tmpHome, "") // should be no-op

    entries, _ := os.ReadDir(tmpHome)
    if len(entries) != 0 {
        t.Errorf("expected no files written for empty project name, got: %v", entries)
    }
}

func TestRegenerateProjectSettings_CreatesProjectDir(t *testing.T) {
    tmpHome := t.TempDir()
    var buf strings.Builder

    // projectDir does NOT exist yet
    projectDir := filepath.Join(tmpHome, "projects", "newproject")
    if _, err := os.Stat(projectDir); !os.IsNotExist(err) {
        t.Fatal("precondition: projectDir should not exist")
    }

    regenerateProjectSettings(&buf, tmpHome, "newproject")

    // Verify the directory was created and settings.json exists
    settingsPath := filepath.Join(projectDir, "settings.json")
    if _, err := os.Stat(settingsPath); err != nil {
        t.Errorf("settings.json not created: %v", err)
    }
}
```

Note: `strings`, `os`, and `filepath` are standard imports — verify they're present in `cmd_start_test.go`.

**Step 4: Build and run**

```bash
make build
go test ./cmd/oro/... -run TestRegenerateProjectSettings -v
```

Expected: All 3 new tests PASS. ✓

---

## Task 8: Run full test suite

```bash
uv run pytest tests/ -v
go test ./cmd/oro/... -timeout 60s
```

Expected: All PASS. ✓

---

## Task 9: Commit and push

**Step 1: Stage**

```bash
git add \
  assets/thresholds.json \
  Makefile \
  cmd/oro/cmd_init.go \
  cmd/oro/cmd_init_test.go \
  cmd/oro/cmd_start.go \
  cmd/oro/cmd_start_test.go \
  assets/hooks/compact_trigger.py \
  .claude/hooks/compact_trigger.py \
  assets/hooks/session_start_compact.py \
  .claude/hooks/session_start_compact.py \
  tests/test_compact_trigger.py \
  tests/test_session_start_compact.py
```

**Step 2: Commit** (use `git-commits` skill)

```
feat(hooks): proactive /compact trigger for architect/manager panes

- compact_trigger.py: fires /compact at soft threshold via tmux send-keys;
  debounce written only after tmux returncode=0
- session_start_compact.py: clears debounce on compact start; injects live
  oro status + bd list for role sessions; worker path unchanged
- extractThresholdsJSON: deploys assets/thresholds.json to ~/.oro/ at
  extraction time so compact_trigger.py reads correct per-model thresholds
- regenerateProjectSettings: regenerates settings.json on asset version bump
  so new hooks wire in without requiring oro init re-run
- Makefile: stage assets/thresholds.json; dev-sync deploys to ~/.oro/
```

**Step 3: Push**

```bash
git push
```

---

## Machine-verifiable acceptance checks

```bash
# 1. thresholds.json staged correctly
make build && ls cmd/oro/_assets/ | grep thresholds

# 2. compact_trigger.py in blank-matcher PostToolUse
go test ./cmd/oro/... -run TestBuildHookConfigContainsCompactTrigger -v

# 3. settings.json regeneration writes file with correct content + handles missing dir
go test ./cmd/oro/... -run TestRegenerateProjectSettings -v

# 4. compact_trigger.py unit tests
uv run pytest tests/test_compact_trigger.py -v

# 5. session_start_compact.py unit tests
uv run pytest tests/test_session_start_compact.py -v

# 6. Full suite
go test ./cmd/oro/... -timeout 60s && uv run pytest tests/ -v
```
