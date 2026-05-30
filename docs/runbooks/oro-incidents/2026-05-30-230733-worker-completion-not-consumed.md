# 2026-05-30 23:07Z Delayed Worker Completion Consumption

## Symptom

Two dispatcher-managed workers finished their assigned task verification and printed final summaries, but the dispatcher continued to show both assignments as active/busy for longer than expected and did not immediately advance to review, merge, close, or next assignment.

Affected assignments:

- `worker-1780181900344581000-0` -> `oro-v6k5`
- `worker-1780181900363486000-1` -> `oro-vnhr`

## First Bad Observation

At `2026-05-30T23:07:33Z`, after a 70-second grace interval following completed worker output, `oro status` still reported:

- dispatcher state: `running`
- health: `healthy`
- workers: `2 active, 0 idle`
- queue: `7 ready`
- `worker-1780181900344581000-0 -> oro-v6k5`
- `worker-1780181900363486000-1 -> oro-vnhr`

Health did not flag this condition:

```json
{"state":"healthy","posture":"no findings","metrics":{"active_workers":2,"ready_queue":7,"active_assignments":2,"recovery_quarantines_open":0}}
```

## Initial Evidence

`oro-v6k5` worker output showed the task was verified:

- `go test ./cmd/oro -run "TestTask.*(Help|Unsupported|Stub)" -count=1` passed.
- `./quality_gate.sh` passed with `Passed: 22`, `Failed: 0`.
- Worktree status was clean except untracked `quality_gate.sh`.
- Branch: `agent/oro-v6k5` at `15d3c727 fix(oro): hide unsupported task stubs`.

`oro-vnhr` worker output showed the task was verified:

- `go mod tidy -diff && go test ./...` exited 0.
- `./scripts/quality_gate.sh` passed with `Passed: 22`, `Failed: 0`.
- Worktree status was clean except untracked `quality_gate.sh`.
- Branch: `agent/oro-vnhr` at `f3083e39 chore(deps): tidy unused Go modules`.

Initial dispatcher event log sampling showed no corresponding completion/review/merge events after assignment:

- `2026-05-30 22:58:20 assign oro-v6k5`
- `2026-05-30 22:58:21 assign oro-vnhr`
- later events in the sampled window were recovery skip/directive status checks only.

## Follow-Up Observation

After additional monitoring and dispatcher pause/resume cycles, both workers eventually emitted `ready_for_review`:

- `oro-v6k5`: `2026-05-30 23:07:40 ready_for_review`
- `oro-vnhr`: `2026-05-30 23:10:28 ready_for_review`

`oro-v6k5` then received `review_rejected` at `2026-05-30 23:09:36`. The stored review output was noisy because it included session hook JSON and tool transcript content, but the final reviewer text ended with `VERDICT: APPROVED`. The retry was still productive: the worker tightened the unsupported-command test, found that `task note list` returned a custom non-Cobra error, changed the `task note` parent command to use `cobra.NoArgs`, and reran the focused regression successfully.

`oro-vnhr` advanced to review after the delay, so it no longer supports a hard "completion not consumed" conclusion.

## Suspected Root Cause

The worker runtime finished useful work and emitted final text, but dispatcher lifecycle advancement lagged behind the visible worker output. The likely failure boundary is worker runtime completion/result parsing, delayed process lifecycle observation, or review-result parsing under noisy agent output.

This differs from a pure task implementation failure because both task worktrees had passing acceptance checks and quality gates before review. However, `oro-v6k5` retry evidence shows review/retry may still uncover missing edge assertions even when the review text's terminal verdict is ambiguous.

Follow-up source investigation found a concrete parser bug in `pkg/ops.reviewOutputText`: valid stream-json events with uninteresting types such as `{"type":"system", ...}` were parsed as `ActivityUnknown`, which caused the parser to abandon stream-json extraction and scan raw JSON lines. In that raw mode, the final non-empty line is the JSON `result` envelope instead of the embedded `VERDICT: APPROVED` text, so approved reviews were classified as failed/rejected.

Regression coverage was added in `pkg/ops/ops_test.go` for system hook noise before a stream-json result verdict. The parser now skips valid stream-json envelopes with uninteresting event types while preserving the raw-text fallback for malformed/non-stream output.

## Corrective Action

1. Preserve this incident note and the completed task branch evidence.
2. Continue monitoring rather than immediately stopping the factory when worker heartbeats remain fresh and review progression appears delayed.
3. If a worker remains active with completed final output and no lifecycle event past the progress timeout, stop/restart the factory cleanly with `--web` enabled so dashboard behavior remains consistent with operator preference.
4. Inspect whether completed branches are reprocessed, reviewed, merged, or need manual recovery.

## Verification

Pending. Verify:

- `oro status --json` has no stuck completed workers beyond the progress timeout.
- `oro health --json` remains healthy.
- `oro-v6k5` and `oro-vnhr` either close/merge or are explicitly requeued with preserved branches.
- Dashboard `/healthz` returns `200`.

Parser fix verification:

- `go test ./pkg/ops -run TestParseReviewOutputStreamJSON/system_hook_noise_does_not_hide_result_verdict -count=1` first failed with `verdict = "failed", want "approved"`.
- After the fix, `go test ./pkg/ops -run 'TestParseReviewOutput(StreamJSON|RequiresVerdictPrefix)|TestParseResultNonZeroExit' -count=1` passed.
- `go test ./pkg/ops -count=1` passed.

## Prevention / Memory

Factory health should detect workers that have emitted final summaries and stopped producing dispatcher lifecycle events while still heartbeating as active. Completed-but-unconsumed worker output should become a health finding or an ops recovery path.
