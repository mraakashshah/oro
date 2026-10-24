# Factory pause checkpoint

Paused at the user's request on 2026-10-24. Work is preserved on
`feature/factory-core`. This checkpoint is unfinished implementation work;
the autonomous factory has not been qualified end to end.

For an extended-break handoff, start with the tracked
[resume guide](2026-10-24-factory-resume-guide.md). It preserves the subsequent
Astra/Terra role clarification, proposed delivery experiment, code entry points,
task inventory, and research/evidence that previously lived only in temporary
storage. Implementation remains paused.

## Accepted evidence

- `bc4dca68`: actual production image-builder and produced watchdog qualification
  passed on the dedicated Docker backend. The namespace expired at its original
  deadline, UID1000 source writes succeeded, the sentinel remained unchanged and
  all owned fixture resources were removed. Root independently checked absence.
- Source-preparation implementation passed its affected recording-runtime race
  suite in 86.339 seconds and independent verification before the label repair.
  Its actual production native path remains unqualified.

## Work saved in this checkpoint

The source-preparation path connects keeper creation, READY, first running
inspection, one-shot stdin-tar seeding and a final continuity inspection. Its
journal preserves original deadlines and denies uncertain start/copy replay.

Actual native evidence exposed inherited image labels that the six-label keeper
fixture omitted. The repair now preserves six mandatory ownership values while
binding the bounded complete observed label map with CreatedAt. Subsequent
inspection, replay and cleanup retain exact full-map comparison.

The narrow inherited-label race regression passed in 5.410 seconds. Root's final
checkpoint regression passed in 1.410 seconds, and package lint reported zero
issues. The complete combined regression, coverage and independent repair review
have not been completed.

## Resume in this order

1. Check malformed raw label values, including JSON `null`, before accepting the
   map decoder. Preserve exact ownership and full-map cleanup checks.
2. Review the inherited-label repair and run the combined source, keeper, volume
   and cleanup regressions with coverage. Tasks are `oro-mf-keeper-image-labels`
   and `oro-mf-source-prepare`; neither is closed.
3. Qualify the actual managed source path through the trusted HostRuntime and
   integrate candidate execution, stopped export and the managed controller.
4. Preserve the full goal: automatic recovery, real restricted coding agents,
   full repository checks, exact-commit acceptance and sustained fault trials.

The current Go/watchdog image does not yet supply the full Oro check environment.
Offline tools/dependencies, frozen independent checker inputs, synthetic Git
metadata and a bounded generated-assets layout remain required. Do not replace
the full repository qualification with smaller fixture results.

## Evidence and handoff

The verification directory is `/private/tmp/oro-factory-verify-ldhnhnvl`.
Native evidence includes `guard-image-native-result.json`,
`guard-image-native-independent.json` and the persistent fixture
`guard-image-native-73164a1597a3e2e24c8119d060a6ddbf/`.
Checkpoint checks are `pause-checkpoint-test.log` and
`pause-checkpoint-lint.log`; earlier source and repair evidence use
`source-prepare-*` and `keeper-image-labels-*`.

The local detailed handoff is
`docs/handoffs/2026-10-24_14-42_paused-factory.yaml`. That directory is intentionally
ignored. Remaining task acceptance and the tracked plans preserve the work for
review; no production-readiness claim is attached to this checkpoint.
