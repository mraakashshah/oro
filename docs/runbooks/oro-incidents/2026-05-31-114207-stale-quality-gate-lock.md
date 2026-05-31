# Stale Quality Gate Lock

- Symptom: Oro workers stayed assigned to reviewed tasks while the dispatcher stopped closing work.
- Affected tasks: `oro-eg25`, `oro-4y2c`; `oro-2ad7` remained actively busy.
- First bad observation: `oro-eg25` stayed `reserved` with no close event while `.oro-quality-gate.lock/owner` pointed to PID `37453`.
- Evidence: at `2026-05-31T11:42:07Z`, lock `created_at=2026-05-31T10:31:55Z`; `ps` showed `37453` running `.worktrees/oro-eg25/scripts/quality_gate.sh` for `06:08:05`, `57423` running `.worktrees/oro-4y2c/scripts/quality_gate.sh` for `03:13:38`, and child `64424` for `01:10:12`.
- Suspected root cause: stale quality-gate wrapper processes retained the global gate lock after review/merge flow diverged behind advanced epic branches.
- Corrective action: terminated stale quality-gate wrappers; the lock was released. Manually rebased `agent/oro-eg25` onto `epic/oro-q628`, resolved `pkg/web/server.go` to preserve epics, worker-title, and event-title data contracts, fast-forwarded `epic/oro-q628` to `5ccd36cb`, closed `oro-eg25`, and resolved recovery quarantine `#96`.
- Verification: `go test ./pkg/web -run TestEpicsFragment -count=1` and `go test ./pkg/web -count=1` passed in `.worktrees/oro-eg25`. `oro health --json` showed `recovery_quarantines_open=0`; remaining degraded state was only the rolling 30-minute quality-gate occurrence warning.
- Prevention: monitor lock owner age against active worker progress; treat reviewed branches that are behind their epic as manual rebase candidates before waiting on another full gate.
