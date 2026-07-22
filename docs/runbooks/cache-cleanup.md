# Daily Oro Disk-Containment Runbook

This is a temporary operational routine, not the product fix. A reboot is the
strongest liveness boundary, but this runbook also supports a no-reboot pass
when Oro, its tmux server, and every checked process/open-file reference have
been stopped. Do not substitute an age check, a sleep, or a process-name guess
for that boundary: detached workers can survive for hours.

It deletes these disposable Oro scratch roots:

- /private/tmp/oro-subprocess
- ~/Library/Caches/oro/subprocess
- top-level /private/tmp/oro-config-test-*
- top-level /private/tmp/oro*cache*
- top-level /private/tmp/oro*qg* and /private/tmp/qg-*

It never deletes review/home/history roots, arbitrary /private/tmp paths, or
Git worktrees.

## 1. Record and stop

Run from /Users/as21/codehouse/oro:

    mkdir -p "$HOME/.oro/logs/storage-cleanup"
    ORO_DAILY_LOG="$HOME/.oro/logs/storage-cleanup/$(date +%Y%m%d-%H%M%S).log"
    date | tee "$ORO_DAILY_LOG"
    df -h / | tee -a "$ORO_DAILY_LOG"
    gdu -sh /private/tmp/oro-subprocess "$HOME/Library/Caches/oro/subprocess" "$HOME/Library/Caches/go-build" 2>&1 | tee -a "$ORO_DAILY_LOG"
    find /private/tmp -maxdepth 1 -mindepth 1 -type d \( -name 'oro*' -o -name 'qg-*' \) -print | tee -a "$ORO_DAILY_LOG"
    git worktree list --porcelain | tee -a "$ORO_DAILY_LOG"
    oro stop --all --force 2>&1 | tee -a "$ORO_DAILY_LOG"

Review the stop output. Keep the log: it is the list of projects to restart.

Do not use Oro pause. Pause prevents new assignments but lets current workers
continue. Do not start Oro, an IDE task, a quality gate, or a Go build again
until the cleanup is complete.

## 2. Stop Oro tmux (no-reboot pass only)

If you are not rebooting, list Oro's dedicated tmux sockets:

    find /tmp/oro-tmux-sockets -maxdepth 1 -type s -name 'oro-*.sock' -print 2>/dev/null

For each socket listed, inspect its sessions and then stop that exact server:

    tmux -S /tmp/oro-tmux-sockets/<exact-socket-name> list-sessions
    tmux -S /tmp/oro-tmux-sockets/<exact-socket-name> kill-server

Do not kill unrelated tmux servers. A normal reboot makes this step unnecessary.

## 3. Verify quiescence

Before deleting anything, run these commands. Do not use `ps eww`: environment
output can expose credentials in the terminal or log.

    ps -axo pid,ppid,pgid,command | rg 'oro (start|worker|work|reviewer|ops)|(^|/)(go|compile|link)( |$)|quality_gate\.sh|golangci-lint|go-build.*/[^ /]+\.test|/private/tmp/oro-(subprocess|config-test-)|/private/tmp/(oro[^ ]*(cache|qg)|qg-)'

    lsof -nP 2>/dev/null | rg '/private/tmp/oro-(subprocess|config-test-)|/private/tmp/(oro[^ ]*(cache|qg)|qg-)|/Library/Caches/oro/subprocess|/Library/Caches/go-build'

Both commands must print nothing. An `rg` exit status of 1 means no matches and
is expected; another error, or a real process/path match, means stop. Do not
clean.

## 4. Delete the known subprocess scratch roots

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
        [[ "$(stat -f '%Su' "$root")" == "$USER" ]] || {
          print -u2 -- "refusing path owned by another user: $root"
          return 1
        }
        rm -rf "$root" || return 1
      }

      [[ "$tmp_root" == /private/tmp/oro-subprocess ]] || return 1
      [[ "$legacy_root" == "$HOME/Library/Caches/oro/subprocess" ]] || return 1
      delete_root "$tmp_root" || return 1
      delete_root "$legacy_root" || return 1
      install -d -m 0750 "$tmp_root" || return 1
      install -d -m 0750 "$legacy_root" || return 1
      print -- ORO_KNOWN_SCRATCH_CLEAN
    )

    oro_delete_known_scratch

The pass condition is exactly:

    ORO_KNOWN_SCRATCH_CLEAN

Do not run rm -rf /private/tmp/oro* or a find -mmin deletion.

## 5. Delete the confirmed disposable test/cache/QG roots

This is intentionally limited to direct children of /private/tmp with the four
patterns listed at the top of this document. The function validates every
candidate before modifying it, makes Go module-cache trees writable, then
deletes them in a *separate* pass. On macOS, combining `chmod -R` and `rm` as
two `find -exec ... +` actions can run the delete before the deferred chmod;
BSD `chmod` also does not accept `--`.

    oro_delete_disposable_tmp() (
      local tmp_root=/private/tmp
      local candidate
      local -a targets=()

      while IFS= read -r -d '' candidate; do
        [[ -d "$candidate" && ! -L "$candidate" ]] || {
          print -u2 -- "refusing unexpected path: $candidate"
          return 1
        }
        [[ "$(stat -f '%Su' "$candidate")" == "$USER" ]] || {
          print -u2 -- "refusing path owned by another user: $candidate"
          return 1
        }
        targets+=("$candidate")
      done < <(
        find "$tmp_root" -maxdepth 1 -mindepth 1 -type d \
          \( -name 'oro-config-test-*' -o -name 'oro*cache*' \
             -o -name 'oro*qg*' -o -name 'qg-*' \) -print0
      )

      (( ${#targets[@]} == 0 )) && {
        print -- ORO_DISPOSABLE_TMP_ALREADY_EMPTY
        return 0
      }

      chmod -R u+w "${targets[@]}" || return 1
      rm -rf "${targets[@]}" || return 1
      print -- ORO_DISPOSABLE_TMP_CLEAN
    )

    oro_delete_disposable_tmp

The pass condition is exactly one of:

    ORO_DISPOSABLE_TMP_CLEAN
    ORO_DISPOSABLE_TMP_ALREADY_EMPTY

The permission adjustment is confined to paths which have already passed the
exact-name, direct-child, non-symlink, and owner checks. It is necessary because
Go deliberately stores module-cache directories read-only.

## 6. Optionally clean Go build cache

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

## 7. Report, but do not delete, other Oro temp paths

    find /private/tmp -maxdepth 1 -mindepth 1 -type d \( -name 'oro*' -o -name 'qg-*' \) ! -name 'oro-subprocess' -print

The disposable config-test/cache/QG paths from step 5 are expected to be gone.
Preserve paths such as oro-review-home.*, oro-review-history.*, and any name
outside the exact patterns for a separate review. `oro storage clean` does not
currently account for or reclaim these /private/tmp roots.

## 8. Report, but do not remove, worktrees

    git worktree list --porcelain

Ignore the session-start cleanup banner as deletion authority. It does not prove
that a worktree is closed, clean, merged into its recorded target, unleased, or
outside recovery quarantine.

## 9. Record after-state and restart

    df -h /
    gdu -sh /private/tmp/oro-subprocess "$HOME/Library/Caches/oro/subprocess" "$HOME/Library/Caches/go-build"
    find /private/tmp -maxdepth 1 -mindepth 1 -type d \( -name 'oro*' -o -name 'qg-*' \) -print
    git worktree list --porcelain

Review the log from step 1. Restart only the projects listed there, from each
project with its normal oro start workflow.

## Emergency rule

Disk pressure does not relax the boundary. Stop Oro and its dedicated tmux
server, require empty process and open-file checks, delete only the named
scratch/test/cache/QG roots, optionally clean the resolved Go build cache, and
preserve unknown temp paths and worktrees. Reboot instead whenever quiescence
cannot be proven.
