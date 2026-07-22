# Daily Oro Disk-Containment Runbook

This is a temporary operational routine, not the product fix. It deliberately
uses a reboot as the liveness boundary because current Oro cannot reliably
identify every detached child process.

It deletes only two known scratch roots:

- /private/tmp/oro-subprocess
- ~/Library/Caches/oro/subprocess

Other /private/tmp/oro* paths and Git worktrees are report-only.

## 1. Before reboot: record and stop

Run from /Users/as21/codehouse/oro:

    mkdir -p "$HOME/.oro/logs/storage-cleanup"
    ORO_DAILY_LOG="$HOME/.oro/logs/storage-cleanup/$(date +%Y%m%d-%H%M%S).log"
    date | tee "$ORO_DAILY_LOG"
    df -h / | tee -a "$ORO_DAILY_LOG"
    gdu -sh /private/tmp/oro-subprocess "$HOME/Library/Caches/oro/subprocess" "$HOME/Library/Caches/go-build" 2>&1 | tee -a "$ORO_DAILY_LOG"
    find /private/tmp -maxdepth 1 -mindepth 1 -type d -name 'oro*' -print | tee -a "$ORO_DAILY_LOG"
    git worktree list --porcelain | tee -a "$ORO_DAILY_LOG"
    oro stop --all 2>&1 | tee -a "$ORO_DAILY_LOG"

Review the stop output. Keep the log: it is the list of projects to restart.

Do not use Oro pause. Pause prevents new assignments but lets current workers
continue.

## 2. Reboot macOS

Save unrelated work and restart the machine normally.

The reboot is required. Do not replace it with an age check, sleep, or process
name guess. Detached workers in this incident survived for hours and looked
stale.

## 3. After reboot: verify quiescence

Before starting Oro, an IDE, a quality gate, or a Go build, open Terminal and
run:

    ORO_PROCESS_SNAPSHOT="$(ps eww -axo pid,ppid,pgid,command)"
    print -r -- "$ORO_PROCESS_SNAPSHOT" | rg 'oro (start|worker|work|reviewer|ops)|(^|/)(go|compile|link)( |$)|quality_gate\.sh|golangci-lint|go-build.*/[^ /]+\.test|/private/tmp/oro-subprocess|/tmp/oro-subprocess'

    ORO_OPEN_FILE_SNAPSHOT="$(lsof -nP)"
    print -r -- "$ORO_OPEN_FILE_SNAPSHOT" | rg '/private/tmp/oro-subprocess|/Library/Caches/oro/subprocess|/Library/Caches/go-build'

Both commands must complete without errors and print nothing. If either prints
a real process/path match or fails to run, stop. Do not clean.

## 4. Delete only the known scratch roots

Paste this block as one unit. It checks exact paths, rejects symlinks and
wrong-owner directories, removes the roots, and recreates them empty.

    oro_delete_known_scratch() (
      local tmp_root=/private/tmp/oro-subprocess
      local legacy_root="$HOME/Library/Caches/oro/subprocess"

      delete_root() {
        local root="$1"
        [[ ! -e "$root" ]] && return 0
        [[ -d "$root" && ! -L "$root" ]] || {
          print -u2 -- "refusing unexpected path: $root"
          return 1
        }
        [[ "$(stat -f '%u' -- "$root")" == "$EUID" ]] || {
          print -u2 -- "refusing path owned by another user: $root"
          return 1
        }
        rm -rf -- "$root" || return 1
      }

      [[ "$tmp_root" == /private/tmp/oro-subprocess ]] || return 1
      [[ "$legacy_root" == "$HOME/Library/Caches/oro/subprocess" ]] || return 1
      delete_root "$tmp_root" || return 1
      delete_root "$legacy_root" || return 1
      install -d -m 0750 -- "$tmp_root" || return 1
      install -d -m 0750 -- "$legacy_root" || return 1
      print -- ORO_KNOWN_SCRATCH_CLEAN
    )

    oro_delete_known_scratch

The pass condition is exactly:

    ORO_KNOWN_SCRATCH_CLEAN

Do not run rm -rf /private/tmp/oro* or a find -mmin deletion.

## 5. Optionally clean Go build cache

Do this only before restarting Oro and only when the build cache is still at
least 20 GiB.

First verify the actual paths:

    ORO_GOCACHE="$(go env GOCACHE)"
    ORO_GOMODCACHE="$(go env GOMODCACHE)"
    printf 'GOCACHE=%s\nGOMODCACHE=%s\n' "$ORO_GOCACHE" "$ORO_GOMODCACHE"
    gdu -sh "$ORO_GOCACHE"

Only continue when GOCACHE is the exact path you measured and the quiescence
checks in step 3 still print nothing:

    go clean -cache -fuzzcache
    gdu -sh "$ORO_GOCACHE"

Any nonzero result, including unlinkat ... directory not empty, means a writer
raced the clean. Stop and record failure. Never add -modcache to the daily
routine. Because the current provider bug can place module content beneath the
build-cache root, cleaning GOCACHE may also remove that misplaced, rebuildable
module content.

## 6. Report, but do not delete, other Oro temp paths

    find /private/tmp -maxdepth 1 -mindepth 1 -type d -name 'oro*' ! -name 'oro-subprocess' -print

Names such as oro-config-test-*, oro-review-home.*, and quality-gate caches are
evidence, not ownership proof. Preserve them for a separate exact-path review.

## 7. Report, but do not remove, worktrees

    git worktree list --porcelain

Ignore the session-start cleanup banner as deletion authority. It does not prove
that a worktree is closed, clean, merged into its recorded target, unleased, or
outside recovery quarantine.

## 8. Record after-state and restart

    df -h /
    gdu -sh /private/tmp/oro-subprocess "$HOME/Library/Caches/oro/subprocess" "$HOME/Library/Caches/go-build"
    find /private/tmp -maxdepth 1 -mindepth 1 -type d -name 'oro*' -print
    git worktree list --porcelain

Review the log from step 1. Restart only the projects listed there, from each
project with its normal oro start workflow.

## Emergency rule

Disk pressure does not relax the boundary. Reboot first, delete only the two
known scratch roots, optionally clean the resolved Go build cache, and preserve
unknown temp paths and worktrees.
