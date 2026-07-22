# Daily Oro Disk-Containment Runbook

Use this manual runbook once a day until Oro's storage lifecycle is enforced in
the product. It reclaims known scratch and optionally cold Go build cache while
protecting live workers, unknown temporary directories, and unmerged worktrees.

This runbook is intentionally stop-the-world. Directory age is not proof that a
worker is dead: the July 2026 incident left detached workers alive for roughly
eight hours with old-looking scratch.

## Safety contract

- Run this from `/Users/as21/codehouse/oro` as the normal Oro user.
- Do not run the destructive steps while Oro, a quality gate, a Go build, or a
  test binary is active.
- `/private/tmp/oro-subprocess` and
  `~/Library/Caches/oro/subprocess` are the only roots this runbook treats as
  known Oro scratch.
- A top-level name matching `/private/tmp/oro*` is not ownership proof. Handle
  those paths one at a time.
- Never use modification age (`find -mmin`) as the liveness test.
- Never automate worktree removal from the session-start “cleanup available”
  banner. It currently does not prove task closure, cleanliness, merge status,
  or absence of a live owner.

## 1. Record the before-state

```zsh
date
df -h /
gdu -sh /private/tmp/oro-subprocess 2>/dev/null || true
gdu -sh ~/Library/Caches/oro/subprocess 2>/dev/null || true
gdu -sh ~/Library/Caches/go-build 2>/dev/null || true
find /private/tmp -maxdepth 1 -mindepth 1 -type d -name 'oro*' -print
git worktree list --porcelain
oro status --json || true
oro storage status --json || true
```

`oro storage status` is evidence, not the source of truth on current `main`: it
may report zero managed cache bytes while the provider roots are large. The
filesystem measurements above are authoritative for this runbook.

If `gdu` is unavailable, install it or substitute `du -sh`. `gdu` is much
faster on the very wide `oro-subprocess` tree.

## 2. Drain every Oro project

An Oro `pause` is insufficient because current assignments keep running. Stop
all project daemons and wait for their graceful worker shutdown:

```zsh
oro stop --all
```

If the command asks for confirmation, review the listed projects and confirm.
Do not use `--force` as the daily default.

Then wait until these commands produce no relevant live process:

```zsh
ps -axo pid,ppid,pgid,lstart,command | rg \
  'oro (start|worker|work|reviewer|ops)|quality_gate\.sh|golangci-lint|go-build.*/[^ /]+\.test'

lsof -nP 2>/dev/null | rg \
  '/private/tmp/oro-subprocess|/Library/Caches/oro/subprocess|/Library/Caches/go-build'
```

No output is the pass condition. Inspect false positives rather than weakening
the patterns. If either command shows a real owner, stop here and resolve it by
its public lifecycle command when possible (`oro worker stop <id>`, then
`oro stop --all`). Do not delete underneath it.

For a detached Oro-owned process whose dispatcher no longer exists, record its
PID, PGID, command, working directory, and open files before terminating it.
Escalating from `TERM` to `KILL` is an incident action, not part of the routine
runbook.

## 3. Quarantine the two known scratch roots

Use a unique run identifier and exact guarded paths. Renaming first makes the
deletion target immutable and keeps a recovery boundary between inspection and
removal.

```zsh
ORO_CLEAN_RUN="$(date +%Y%m%d-%H%M%S)"
ORO_TMP_ROOT=/private/tmp/oro-subprocess
ORO_LEGACY_ROOT="$HOME/Library/Caches/oro/subprocess"
ORO_TMP_QUARANTINE="/private/tmp/oro-subprocess.quarantine.$ORO_CLEAN_RUN"
ORO_LEGACY_QUARANTINE="$HOME/Library/Caches/oro/subprocess.quarantine.$ORO_CLEAN_RUN"

[[ "$ORO_TMP_ROOT" == /private/tmp/oro-subprocess ]] || exit 1
[[ "$ORO_LEGACY_ROOT" == "$HOME/Library/Caches/oro/subprocess" ]] || exit 1

[[ -d "$ORO_TMP_ROOT" ]] && mv -- "$ORO_TMP_ROOT" "$ORO_TMP_QUARANTINE"
install -d -m 0750 "$ORO_TMP_ROOT"

[[ -d "$ORO_LEGACY_ROOT" ]] && mv -- "$ORO_LEGACY_ROOT" "$ORO_LEGACY_QUARANTINE"
install -d -m 0750 "$ORO_LEGACY_ROOT"
```

Do not restart Oro yet. Re-run the two liveness commands from step 2. Also
verify no process has the quarantines open:

```zsh
lsof -nP 2>/dev/null | rg \
  "oro-subprocess\.quarantine\.$ORO_CLEAN_RUN|subprocess\.quarantine\.$ORO_CLEAN_RUN"
```

No output is the pass condition.

## 4. Delete the quarantined known scratch

First print the exact targets and sizes:

```zsh
printf '%s\n%s\n' "$ORO_TMP_QUARANTINE" "$ORO_LEGACY_QUARANTINE"
gdu -sh "$ORO_TMP_QUARANTINE" "$ORO_LEGACY_QUARANTINE" 2>/dev/null || true
```

Confirm all four facts before continuing:

1. `oro stop --all` completed.
2. The step-2 process and open-file checks have no real matches.
3. The variables print only the two timestamped quarantine paths created in
   this run.
4. Neither path is a symlink.

Then remove only those exact quarantines:

```zsh
[[ "$ORO_TMP_QUARANTINE" == /private/tmp/oro-subprocess.quarantine."$ORO_CLEAN_RUN" ]] || exit 1
[[ "$ORO_LEGACY_QUARANTINE" == "$HOME/Library/Caches/oro/subprocess.quarantine.$ORO_CLEAN_RUN" ]] || exit 1
[[ ! -L "$ORO_TMP_QUARANTINE" && ! -L "$ORO_LEGACY_QUARANTINE" ]] || exit 1

rm -rf -- "$ORO_TMP_QUARANTINE" "$ORO_LEGACY_QUARANTINE"
```

This is the routine reclaim step. It does not touch top-level unknown
`/private/tmp/oro*` paths, worktrees, or module downloads.

## 5. Clean Go build cache only when oversized

Do not clean the Go cache every day merely because this runbook runs every day.
Cold rebuilding is expensive and immediately repopulates it. Use a 20 GiB
operator threshold unless disk pressure requires a different explicit choice.

```zsh
gdu -sh ~/Library/Caches/go-build
```

If it is below 20 GiB, skip this step. If it is at or above 20 GiB, verify that
no Go compiler, test, quality gate, or linter is active:

```zsh
ps -axo pid,ppid,pgid,lstart,command | rg \
  '(^|/)(go|compile|link|golangci-lint)( |$)|quality_gate\.sh|go-build.*/[^ /]+\.test'
lsof -nP 2>/dev/null | rg '/Library/Caches/go-build'
```

No output is the pass condition. Then use Go's native cleaner:

```zsh
go clean -cache -fuzzcache
```

Re-measure the root. Any nonzero exit, including `unlinkat ... directory not
empty`, means a writer raced the clean. Record it as a failed cleanup, return to
the liveness checks, and do not report the cache as cleaned.

Do not include `-modcache` in the daily run. Module downloads are separately
rebuildable but expensive, and the current Oro cache-provider bug can place a
module tree beneath `go-build`. The product fix must separate `GOCACHE` from
`GOMODCACHE`; this runbook does not pretend the provider metadata is correct.

## 6. Review other top-level `/private/tmp/oro*` paths

Inventory everything except the known runtime root and its quarantine:

```zsh
find /private/tmp -maxdepth 1 -mindepth 1 -type d -name 'oro*' \
  ! -name 'oro-subprocess' \
  ! -name 'oro-subprocess.quarantine.*' \
  -print
```

For each path, inspect it individually:

```zsh
ORO_CANDIDATE=/private/tmp/REPLACE_WITH_ONE_EXACT_BASENAME
[[ "$ORO_CANDIDATE" == /private/tmp/oro* && "${ORO_CANDIDATE:h}" == /private/tmp ]] || exit 1
[[ -d "$ORO_CANDIDATE" && ! -L "$ORO_CANDIDATE" ]] || exit 1
gdu -sh "$ORO_CANDIDATE"
find "$ORO_CANDIDATE" -maxdepth 2 -mindepth 1 -print | head -n 50
lsof +D "$ORO_CANDIDATE" 2>/dev/null
```

Do not bulk-delete these candidates. Names such as `oro-config-test-*`,
`oro-review-home.*`, and quality-gate caches are strong incident clues but not
durable ownership records. If inspection proves a specific path is disposable,
move that exact path into a timestamped quarantine, repeat the liveness check,
and require explicit operator approval before deleting it. Preserve anything
unknown.

## 7. Retire worktrees one at a time

The daily default is inventory, not automatic removal:

```zsh
git worktree list --porcelain
```

For one candidate, set exact values and require every proof below:

```zsh
ORO_WT=/Users/as21/codehouse/oro/.worktrees/REPLACE_WITH_EXACT_DIRECTORY
ORO_TASK=oro-REPLACE
ORO_TARGET=main
ORO_BRANCH="$(git -C "$ORO_WT" branch --show-current)"

git worktree list --porcelain | rg -F "worktree $ORO_WT"
oro task show "$ORO_TASK" --json | jq -e '.status == "closed"'
git -C "$ORO_WT" status --porcelain
git merge-base --is-ancestor "$ORO_BRANCH" "$ORO_TARGET"
lsof +D "$ORO_WT" 2>/dev/null
```

All commands must pass; `git status --porcelain` and `lsof` must print nothing.
Also confirm the task is not in recovery quarantine and that `ORO_TARGET` is the
task's recorded integration target, not merely an assumed `main`.

Only then, from the primary checkout—not from inside the worktree—run:

```zsh
git worktree remove "$ORO_WT"
git branch -d "$ORO_BRANCH"
git worktree prune
```

If any proof fails or is uncertain, preserve the worktree. Never use `--force`,
`git branch -D`, or filesystem deletion in this routine.

## 8. Record the after-state and restart deliberately

```zsh
df -h /
gdu -sh /private/tmp/oro-subprocess 2>/dev/null || true
gdu -sh ~/Library/Caches/oro/subprocess 2>/dev/null || true
gdu -sh ~/Library/Caches/go-build 2>/dev/null || true
find /private/tmp -maxdepth 1 -mindepth 1 -type d -name 'oro*' -print
git worktree list --porcelain
```

Record bytes reclaimed, paths preserved, worktrees removed, and any failed
liveness or cleanup check. Restart Oro through the project's normal `oro start`
workflow only after the after-state is captured.

## Emergency disk pressure

If the filesystem is critically full, do not weaken ownership or liveness
checks. The safe escalation order is:

1. stop every Oro project;
2. terminate verified Oro-owned orphan process groups and confirm exit;
3. quarantine and delete the two known scratch roots;
4. clean the Go build cache only with zero writers;
5. inspect and explicitly approve exact top-level temp candidates;
6. preserve uncertain paths and dirty/unmerged worktrees.

Never use `rm -rf /private/tmp/oro*`, `find ... -mmin ... -delete`, or a blanket
worktree-removal loop.
