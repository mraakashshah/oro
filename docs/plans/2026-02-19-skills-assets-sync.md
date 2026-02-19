# Skills & Assets Sync Implementation Plan

> **For Claude:** Use executing-plans skill to implement this plan task-by-task.

**Goal:** Ensure sub-agents (Task tool) and new Oro workers reliably receive and use skills.

**Architecture:** Two mechanisms deliver skills — Claude Code's `.claude/skills/` directory (read for every session including sub-agents) and `session_start_extras.py` (injects `using-skills` on SessionStart for workers). The sub-agent gap is that `_SUPERPOWERS` is only in the hook's `additionalContext`, not in `CLAUDE.md` — sub-agents skip it. The assets gap is that `assets/` (the git-tracked source that gets embedded into the binary and deployed by `oro init`) is missing 2 production hooks and has 2 test files misplaced inside it (they'd get embedded into the binary).

**Tech Stack:** Python (hooks + tests), pytest, GNU make, Go (binary rebuild)

---

## Context

### How skills reach each agent type

```
assets/skills/  ─── git-tracked source of truth
     │ stage-assets
     ▼
cmd/oro/_assets/ ── embedded in binary
     │ oro init
     ▼
<project>/.claude/skills/  ──→  Skill tool (all sessions in that project)
                                │
              session_start_extras.py reads:
              ~/.oro/.claude/skills/using-skills/SKILL.md
                                │
                           additionalContext on SessionStart
                           (main sessions + new oro workers)
                           ✗ Task tool sub-agents (no SessionStart)
```

**Task tool sub-agents** have the Skill tool and see the skills list, but don't get the "always invoke using-skills" mandate because they don't trigger SessionStart. The fix: put it in `CLAUDE.md`.

### Gap inventory

**Assets missing 2 production hooks** (in `~/.oro/hooks/` but not `assets/hooks/`):
- `bd_create_notifier.py`
- `enforce-skills.sh`

**Assets has 2 misplaced test files** (should be in `tests/`, not embedded in binary):
- `assets/hooks/test_notify_manager_on_bead_create.py`
- `assets/hooks/test_session_start_extras.py`

**`tests/` missing 8 test files** (exist only in `~/.oro/hooks/`):
- `test_architect_router.py`
- `test_architect_router_new.py`
- `test_hook_paths.py`
- `test_learning_analysis.py`
- `test_learning_reminder.py`
- `test_notify_manager_on_bead_create.py`
- `test_prompt_injection_guard.py`
- `test_rebase_worktree_guard.py`

---

## Task 1: Add skills mandate to CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`
- Modify: `assets/CLAUDE.md`

Both files currently contain only:
```
# Oro Project Instructions
- **Reference**: Remember, you also have global ~/.claude/CLAUDE.md and any files in ~/.claude/rules/
```

**Step 1: Edit project CLAUDE.md**

Replace the entire contents of `CLAUDE.md` with:

```markdown
# Oro Project Instructions

- **Reference**: Remember, you also have global ~/.claude/CLAUDE.md and any files in ~/.claude/rules/

# Skills

Always invoke `using-skills` before any action. This applies in all contexts: main sessions, sub-agents spawned via Task tool, and oro workers. No exceptions.
```

**Step 2: Edit assets/CLAUDE.md identically**

Apply the same change to `assets/CLAUDE.md`.

**Step 3: Verify**

```bash
cat CLAUDE.md && echo "---" && cat assets/CLAUDE.md
```
Expected: both files contain the `# Skills` section with the mandate.

---

## Task 2: Update conftest.py to import from assets/hooks/

**Files:**
- Modify: `tests/conftest.py`

**Context:** Tests being moved from `~/.oro/hooks/` use direct imports like `import architect_router`. This works when run from `~/.oro/hooks/` because the module is on sys.path. After moving to `tests/`, they need `assets/hooks/` on sys.path so they import from the git-tracked source.

**Step 1: Add sys.path insertion to conftest.py**

Edit `tests/conftest.py` — add after the existing imports (after line 5 `import pytest`):

```python
import sys

# Add assets/hooks/ to sys.path so hook tests can import hook modules directly.
# This makes tests run against the git-tracked source (assets/), not the deployed ~/.oro/.
_ASSETS_HOOKS = Path(__file__).parent.parent / "assets" / "hooks"
if str(_ASSETS_HOOKS) not in sys.path:
    sys.path.insert(0, str(_ASSETS_HOOKS))
```

**Step 2: Run existing tests to verify nothing broke**

```bash
python -m pytest tests/ -x -q 2>&1 | head -40
```
Expected: same results as before (existing tests pass or are skipped, none newly broken).

---

## Task 3: Remove misplaced test files from assets/hooks/

**Files:**
- Delete: `assets/hooks/test_notify_manager_on_bead_create.py`
- Delete: `assets/hooks/test_session_start_extras.py`

These files are tracked in git inside `assets/hooks/`, meaning `stage-assets` copies them into `cmd/oro/_assets/hooks/` and they get embedded in the binary. Tests have no business being in the binary.

Note: `tests/` already has `test_session_start_extras.py`. `test_notify_manager_on_bead_create.py` will be added in Task 5.

**Step 1: Remove from assets**

```bash
git rm assets/hooks/test_notify_manager_on_bead_create.py
git rm assets/hooks/test_session_start_extras.py
```

**Step 2: Verify assets/hooks/ is clean**

```bash
ls assets/hooks/test_* 2>/dev/null && echo "ERROR: test files remain" || echo "OK: no test files in assets/hooks/"
```
Expected: `OK: no test files in assets/hooks/`

---

## Task 4: Copy 2 missing production hooks into assets/hooks/

**Files:**
- Create: `assets/hooks/bd_create_notifier.py` (copy from `~/.oro/hooks/bd_create_notifier.py`)
- Create: `assets/hooks/enforce-skills.sh` (copy from `~/.oro/hooks/enforce-skills.sh`)

**Step 1: Copy hooks**

```bash
cp ~/.oro/hooks/bd_create_notifier.py assets/hooks/bd_create_notifier.py
cp ~/.oro/hooks/enforce-skills.sh assets/hooks/enforce-skills.sh
```

**Step 2: Verify**

```bash
ls -la assets/hooks/ | grep -E "bd_create|enforce-skills"
```
Expected: both files present with non-zero size.

**Step 3: Quick sanity check on the sh file**

```bash
bash -n assets/hooks/enforce-skills.sh && echo "OK: syntax valid" || echo "ERROR: syntax error"
```
Expected: `OK: syntax valid`

---

## Task 5: Copy 8 missing test files into tests/

**Files to create** (copy from `~/.oro/hooks/`):
- `tests/test_architect_router.py`
- `tests/test_architect_router_new.py`
- `tests/test_hook_paths.py`
- `tests/test_learning_analysis.py`
- `tests/test_learning_reminder.py`
- `tests/test_notify_manager_on_bead_create.py`
- `tests/test_prompt_injection_guard.py`
- `tests/test_rebase_worktree_guard.py`

**Step 1: Copy all 8 files**

```bash
for f in test_architect_router.py test_architect_router_new.py test_hook_paths.py \
          test_learning_analysis.py test_learning_reminder.py \
          test_notify_manager_on_bead_create.py test_prompt_injection_guard.py \
          test_rebase_worktree_guard.py; do
  cp ~/.oro/hooks/$f tests/$f
  echo "copied: $f"
done
```

**Step 2: Verify files exist**

```bash
ls tests/test_*.py | wc -l
```
Expected: 15 (7 existing + 8 new).

**Step 3: Attempt test collection (expect some failures — document them)**

```bash
python -m pytest tests/ --collect-only -q 2>&1 | tail -20
```

Look for import errors. If tests import modules not in `assets/hooks/`, they'll fail collection. Document which ones fail and why.

**Step 4: Fix import errors**

If any test file uses `import module_name` directly and the module isn't in `assets/hooks/`, check whether the module is a hook that needs to be in `assets/hooks/` (Task 4 may need expanding) or whether the test needs its import path corrected.

For any test that tries to load from `~/.oro/hooks/` via importlib (Pattern A), update the path to use `assets/hooks/`:

Old pattern (Pattern A — loads from deployed location):
```python
_oro_home = Path(os.environ.get("ORO_HOME", Path.home() / ".oro"))
_spec = importlib.util.spec_from_file_location("module", _oro_home / "hooks" / "module.py")
```

New pattern (Pattern A — loads from assets):
```python
_assets = Path(__file__).parent.parent / "assets" / "hooks"
_spec = importlib.util.spec_from_file_location("module", _assets / "module.py")
```

---

## Task 6: Update conftest.py skip list

**Files:**
- Modify: `tests/conftest.py`

The conftest has a `hook_dependent_tests` list for tests that require `~/.oro/hooks/` to exist. The new tests should NOT be in this list — they should import from `assets/hooks/` and run anywhere. Verify the list doesn't need updating for the new tests.

**Step 1: Check conftest's hook_dependent_tests list**

The current list is:
```python
hook_dependent_tests = [
    "test_inject_context_usage.py",
    "test_memory_capture.py",
    "test_session_start_extras.py",
    "test_validate_agent_completion.py",
    "test_worktree_guard.py",
]
```

After Task 2 adds `assets/hooks/` to sys.path, none of the NEW test files should be hook-dependent (they import from assets/, not ~/.oro/). If any of the 8 new tests still load from `~/.oro/hooks/` after fixup, add them to this list.

**Step 2: Run full test suite**

```bash
python -m pytest tests/ -x -q 2>&1
```
Expected: all tests pass or are properly skipped. Zero failures.

If failures: investigate import errors, missing modules, or path issues. Fix in place — don't move on until green.

---

## Task 7: Run make dev-sync

Now that `assets/` is the authoritative source, redeploy to `~/.oro/`.

**Step 1:**

```bash
make dev-sync
```
Expected output ends with `✓ dev-sync complete`.

This overwrites `~/.oro/hooks/` and `~/.oro/.claude/skills/` from `assets/`. Any hooks that were edited directly in `~/.oro/` are now replaced with the authoritative versions from git.

**Step 2: Verify deployed hooks match assets**

```bash
diff <(ls assets/hooks/*.py assets/hooks/*.sh 2>/dev/null | xargs -I{} basename {} | sort) \
     <(ls ~/.oro/hooks/*.py ~/.oro/hooks/*.sh 2>/dev/null | grep -v test_ | xargs -I{} basename {} | sort)
```
Expected: no diff (only test files are excluded from both sides).

---

## Task 8: Rebuild binary

The binary embeds assets via `go:embed`. Must rebuild after assets change.

**Step 1:**

```bash
make build
```
Expected: builds `./oro` successfully.

**Step 2: Smoke test**

```bash
./oro version 2>/dev/null || ./oro --version 2>/dev/null || ./oro help | head -3
```
Expected: version or help output (no crash).

---

## Task 9: Run gate and commit

**Step 1: Run quality gate**

```bash
make gate
```
Expected: all checks pass.

If `make gate` fails on specific checks, fix them before committing.

**Step 2: Stage changes**

```bash
git add CLAUDE.md assets/CLAUDE.md
git add assets/hooks/bd_create_notifier.py assets/hooks/enforce-skills.sh
git add tests/test_architect_router.py tests/test_architect_router_new.py \
        tests/test_hook_paths.py tests/test_learning_analysis.py \
        tests/test_learning_reminder.py tests/test_notify_manager_on_bead_create.py \
        tests/test_prompt_injection_guard.py tests/test_rebase_worktree_guard.py
git add tests/conftest.py
```

**Step 3: Commit**

```bash
git commit -m "fix(assets): sync skills mandate to CLAUDE.md, move tests, add missing hooks

- Add 'always invoke using-skills' mandate to CLAUDE.md and assets/CLAUDE.md
  so Task tool sub-agents receive the skills directive (no SessionStart hook)
- Copy bd_create_notifier.py and enforce-skills.sh into assets/hooks/
- git rm test files from assets/hooks/ (should not be embedded in binary)
- Copy 8 marooned test files from ~/.oro/hooks/ into tests/
- Add assets/hooks/ to conftest sys.path so tests run against git-tracked source"
```

**Step 4: Verify clean**

```bash
git status
```
Expected: clean working tree.

**Step 5: Push**

```bash
git push
```

---

## Verification Checklist

After completing all tasks:

- [ ] `cat CLAUDE.md` shows skills mandate
- [ ] `cat assets/CLAUDE.md` shows skills mandate
- [ ] `ls assets/hooks/test_*` returns nothing
- [ ] `ls assets/hooks/bd_create_notifier.py assets/hooks/enforce-skills.sh` — both exist
- [ ] `ls tests/test_*.py | wc -l` = 15
- [ ] `python -m pytest tests/ -q` — all pass or properly skipped
- [ ] `make dev-sync` succeeds
- [ ] `make build` succeeds
- [ ] `git push` succeeds
