# P3e Merge Conflict Recovery

- **Time:** 2026-06-01T10:45:44Z
- **Symptom:** final child task `oro-s08-p3e` reached integration with an unresolved merge conflict in `pkg/cards/promotion_test.go`.
- **Affected task/worker/branch:** `oro-s08-p3e`, `worker-1780299093114800000-0`, `agent/oro-s08-p3e`.
- **First bad observation:** `git status` in `.worktrees/oro-s08-p3e` showed `UU pkg/cards/promotion_test.go` while the dispatcher held the worker in reserved state.
- **Suspected root cause:** `oro-s08-p3f` and `oro-s08-p3e` both appended tests/helpers to `pkg/cards/promotion_test.go` after starting from adjacent epic revisions.
- **Evidence:** combined diff showed `TestPromotedLearning_EntersProposalQueue` from `p3f` and `TestCalibration_ReportsRates` plus calibration helpers from `p3e` in the same conflict region.
- **Corrective action:** kept both test blocks and helpers, staged the resolved file, verified affected packages, fast-forwarded `epic/oro-spec08` to `8991b08b`, and closed `oro-s08-p3e`.
- **Verification:** `go test ./pkg/cards/ -run 'TestCalibration_ReportsRates|TestPromotedLearning_EntersProposalQueue' -count=1 -v`, `go test ./pkg/ops/ -count=1`, and `go test ./pkg/dispatcher/ -run 'Test' -count=1 -timeout 180s` passed in `.worktrees/oro-s08-p3e`.
- **Prevention:** when two active workers append tests in the same file, expect a simple keep-both conflict; resolve by preserving both acceptance-test blocks and rerunning the touched packages.
