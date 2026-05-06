# Dispatcher Scheduling Units Design

Date: 2026-05-06

## Summary

The dispatcher should stop treating every ready child bead as an equal global queue item. It should schedule coherent units:

1. Independent ready beads.
2. Full epic units, ordered by the epic bead's priority.

For an epic unit, the dispatcher assigns from that epic's ready descendant frontier. Child bead priority decides ordering inside the selected epic, but the parent epic priority decides which epic gets attention.

This preserves fast handling for standalone work while preventing the factory from randomly nibbling across many epics.

## Research Notes

Files read:

- `pkg/dispatcher/dispatcher.go:sortBeadsByPriority`
- `pkg/dispatcher/dispatcher.go:tryAssign`
- `pkg/dispatcher/dispatcher.go:filterExecutableBeads`
- `pkg/dispatcher/dispatcher.go:assignGeneralIdleWorkers`
- `pkg/dispatcher/dispatcher_test.go:TestSortBeadsByPriority_EpicFinishing`
- `pkg/beadstore/sqlite.go:Ready`
- `pkg/beadstore/migrations/migrate_v3.go:v3ViewsDDL`
- `docs/plans/notes/bd-ready-semantics.md`
- `docs/decisions&discoveries.md`

Current scheduler behavior:

- `Ready()` returns ready open beads ordered by bead priority and created time.
- `filterExecutableBeads` excludes already-decomposed epic beads because those epic beads are not directly executable.
- `sortBeadsByPriority` then mutates the flat ready child list into four groups:
  1. spawn-for priority beads
  2. focused epic descendants
  3. standalone beads where `Epic == ""`
  4. unfocused epic children grouped by oldest epic ID
- `TestSortBeadsByPriority_EpicFinishing` documents this old order explicitly.

The problem:

- Unfocused epic scheduling is based on epic ID/age, not the epic's priority.
- Child bead priority can make the dispatcher sample multiple epics instead of pushing one epic forward.
- The scheduler cannot express "this epic is the work unit; drain its ready frontier."

## Goals

- Prefer independent ready beads over epic work by default.
- Treat top-level epics as scheduling units once they have ready descendants.
- Sort epic units by the epic bead's priority.
- Sort ready descendants inside a selected epic by child priority and readiness order.
- Preserve focus behavior: focused epic descendants outrank normal backfill.
- Preserve spawn-for behavior: explicitly targeted beads still outrank normal scheduling.
- Keep existing readiness semantics intact; this is a dispatcher scheduling change, not a beadstore readiness rewrite.

## Non-Goals

- Changing dependency semantics.
- Changing `beads_ready` view rules.
- Changing epic decomposition or epic close behavior.
- Running one worker per epic forever.
- Adding a complex weighted fair scheduler in the first cut.
- Reprioritizing child beads automatically when parent epic priority changes.

## Terminology

Independent bead:

- A ready bead with no parent epic: `Epic == ""`.
- It can be any executable non-epic type such as task, bug, review, premortem, or chore.

Epic unit:

- A top-level epic bead with at least one ready executable descendant.
- The epic bead itself may not appear in the ready list after decomposition, so the dispatcher must derive epic units from ready descendant parent chains.

Ready frontier:

- The set of ready descendants of an epic that pass existing assignment filters.
- The dispatcher does not need to understand all blocked descendants; it only schedules what `Ready()` and `filterAssignable()` already allow.

Top-level epic:

- The root epic reached by walking `Bead.Epic` parent links until the parent has no `Epic`.
- Nested epic descendants inherit the root epic as their scheduling unit unless focus targets a nested epic directly.

## Scheduling Policy

### Override Layer

These preserve existing operational controls:

1. Spawn-for / explicit priority beads.
2. Focused epic descendants.

Spawn-for remains first because it is an explicit targeted worker contract. Focus remains second because the operator requested attention on a specific epic.

### Normal Layer

When no override applies, assign in this order:

1. Independent ready beads.
2. Epic units ordered by parent epic priority.

Independent beads are sorted by:

1. bead priority ascending
2. created time / store order as the stable tie-breaker
3. bead ID as deterministic final tie-breaker when available

Epic units are sorted by:

1. root epic priority ascending
2. root epic created time / ID as stable tie-breaker

Within a selected epic unit, ready descendants are sorted by:

1. child bead priority ascending
2. dependency-frontier/store order
3. bead ID as deterministic final tie-breaker

### Worker Fill Behavior

The dispatcher should fill idle workers by iterating scheduling units, not a single flat child list:

- Assign all available independent beads first, up to idle worker count.
- If idle workers remain, choose the highest-priority epic unit.
- Assign that epic's ready descendants to remaining idle workers.
- If the selected epic has fewer ready descendants than idle workers, move to the next epic unit.

This means a three-worker factory can make concentrated progress on one epic when independent work is empty, but still uses spare capacity when that epic's ready frontier is narrow.

### Focus Behavior

Focus keeps its current intent:

- `focus <epic>` moves descendants of that epic ahead of ordinary independent/backfill work.
- `focus --immediate <epic>` preempts non-focused active workers, then uses the same focused-first ordering.
- After focused descendants are exhausted or blocked, backfill follows the normal layer: independent beads, then epic units by epic priority.

Nested focus:

- If focus targets a nested epic, descendants of that nested epic are focused.
- Backfill epic-unit grouping still uses root epic units unless the focus is active.

## Data Requirements

The ready bead list does not include full parent details. To sort epic units by parent priority, the dispatcher needs parent metadata.

Required metadata:

- root epic ID
- root epic priority
- root epic created time or deterministic fallback
- descendant chain for focus and root lookup

Implementation approach:

- Build a per-`tryAssign` parent cache.
- For each ready bead with `Epic != ""`, walk parent links via `d.beads.Show(ctx, parentID)`.
- Stop at the first parent whose `Epic == ""`; that is the root epic.
- Cache both parent ID to parent detail and child bead ID to root epic info.
- If any parent lookup fails, keep the descendant assignable but place it in a conservative fallback epic unit sorted after known epic units of the same priority class.

Caching is per assignment tick only. Persistent caching risks stale priority after `oro task update <epic> --priority`.

## Proposed Code Shape

Replace `sortBeadsByPriority` with a scheduling plan builder:

```go
type schedulingUnitKind int

const (
    unitSpawnFor schedulingUnitKind = iota
    unitFocused
    unitIndependent
    unitEpic
)

type schedulingUnit struct {
    kind          schedulingUnitKind
    epicID        string
    epicPriority  int
    epicCreatedAt string
    beads         []protocol.Bead
}

func (d *Dispatcher) buildSchedulingPlan(ctx context.Context, beads []protocol.Bead) (schedulingPlan, prioritySnapshot map[string]bool, focusVersion uint64)
```

`assignGeneralIdleWorkers` should consume the plan in order and assign each unit's beads in order. This avoids flattening away the epic boundary.

The first implementation can still return a flattened ordered list if it preserves unit contiguity, but the plan type is preferred because it makes the invariant testable.

## Tests

### Unit Tests

`pkg/dispatcher/dispatcher_test.go:TestBuildSchedulingPlan_IndependentBeforeEpics`

- Input:
  - independent P2 bead
  - epic-root-a P0 with ready child P0
  - epic-root-b P1 with ready child P0
- Assert:
  - independent bead is scheduled before epic units despite epic-root-a being P0.
  - epic-root-a unit comes before epic-root-b.

`pkg/dispatcher/dispatcher_test.go:TestBuildSchedulingPlan_EpicPriorityBeatsEpicAge`

- Input:
  - older epic P2 with ready child
  - newer epic P0 with ready child
- Assert:
  - newer P0 epic unit is scheduled first.
  - This replaces the old "oldest epic first" behavior.

`pkg/dispatcher/dispatcher_test.go:TestBuildSchedulingPlan_EpicUnitKeepsFrontierContiguous`

- Input:
  - epic-a has children P0 and P2
  - epic-b has child P1
- Assert:
  - epic-a P0 and P2 remain contiguous when epic-a is selected before epic-b.
  - child priority sorts only inside the selected epic.

`pkg/dispatcher/dispatcher_test.go:TestBuildSchedulingPlan_FocusOverridesIndependent`

- Input:
  - focused epic child P2
  - independent P0
- Assert:
  - focused descendant comes first.
  - After focus group, normal backfill remains independent before epic units.

`pkg/dispatcher/dispatcher_test.go:TestBuildSchedulingPlan_NestedEpicUsesRootPriority`

- Input:
  - root epic P0
  - nested epic P3 under root
  - ready child under nested epic
- Assert:
  - child schedules under root epic P0 when no nested focus is active.

`pkg/dispatcher/dispatcher_test.go:TestTryAssign_FillsSelectedEpicBeforeNextEpic`

- Input:
  - two idle general workers
  - no independent beads
  - epic-a P0 with two ready descendants
  - epic-b P0 with one ready descendant
- Assert:
  - both workers receive epic-a descendants before epic-b.

### Integration/Regression Tests

`pkg/dispatcher/dispatcher_test.go:TestSortBeadsByPriority_EpicFinishing` should be replaced or renamed. It currently asserts the behavior this spec removes.

`pkg/beadstore/sqlite.go:Ready` should not need changes for this feature. Add no beadstore tests unless implementation touches readiness.

## Premortem

```yaml
premortem:
  mode: deep
  context: "dispatcher scheduling units: independent beads first, then full epics by epic priority"
  tigers:
    - risk: "Independent beads can starve epics if new standalone work keeps arriving."
      severity: high
      mitigation_checked: "Spec keeps MVP simple but requires visible epic queue ordering; follow-up aging can promote waiting epic units if starvation appears."
    - risk: "Parent priority lookup failure makes high-priority epics invisible."
      severity: medium
      mitigation_checked: "Spec requires conservative fallback ordering and event logging for parent lookup failures."
    - risk: "Flattened sorting loses the epic unit invariant."
      severity: high
      mitigation_checked: "Spec prefers a schedulingPlan type and tests frontier contiguity."
    - risk: "Focus and immediate focus regress."
      severity: high
      mitigation_checked: "Spec preserves override layer and adds focus override tests."
  elephants:
    - risk: "Independent-first means a P2 standalone bead can beat a P0 epic. This is intentional per operator preference, but it is a semantic choice worth watching."
  paper_tigers:
    - risk: "Root epic parent lookups add too much latency."
      reason: "Ready queue is small relative to worker runtime; per-tick cache avoids repeated lookups."
    - risk: "Child priority becomes irrelevant."
      reason: "Child priority still orders the selected epic's frontier; it just no longer chooses between epics."
```

## Adversarial Review

```yaml
verdict: PASS_WITH_NOTES
spec: docs/plans/2026-05-06-dispatcher-scheduling-units-design.md
acceptance_test:
  cmd: "go test ./pkg/dispatcher -run 'TestBuildSchedulingPlan|TestTryAssign_FillsSelectedEpicBeforeNextEpic' -count=1"
  assert: "Independent beads schedule before epic units; epic units sort by root epic priority; selected epic frontier stays contiguous; focus overrides normal ordering."
  adequate: true
requirements_traceability:
  - criterion: "Independent beads before epics"
    tests: ["TestBuildSchedulingPlan_IndependentBeforeEpics"]
    status: covered
  - criterion: "Epics have priorities"
    tests: ["TestBuildSchedulingPlan_EpicPriorityBeatsEpicAge"]
    status: covered
  - criterion: "Full epic unit, not random child sampling"
    tests: ["TestBuildSchedulingPlan_EpicUnitKeepsFrontierContiguous", "TestTryAssign_FillsSelectedEpicBeforeNextEpic"]
    status: covered
  - criterion: "Focus remains stronger than normal scheduling"
    tests: ["TestBuildSchedulingPlan_FocusOverridesIndependent"]
    status: covered
negative_space:
  - scenario: "All tasks pass but old flat `sortBeadsByPriority` still used by tryAssign"
    coverage: "Task requires tryAssign/assignGeneralIdleWorkers to consume schedulingPlan, not just helper tests."
  - scenario: "Nested epic child sorted by nested child priority instead of root epic priority"
    coverage: "Nested root-priority test covers parent-chain walking."
  - scenario: "Old test still asserts oldest epic first"
    coverage: "Spec explicitly requires replacing that regression test."
```

## Task Graph

Epic: Implement dispatcher scheduling units.

1. Add scheduling plan builder
   - Test: `pkg/dispatcher/dispatcher_test.go:TestBuildSchedulingPlan_IndependentBeforeEpics`
   - Cmd: `go test ./pkg/dispatcher -run TestBuildSchedulingPlan_IndependentBeforeEpics -count=1 -v`
   - Assert: independent ready beads schedule before epic units; epic units sort by root epic priority.
   - Read: `pkg/dispatcher/dispatcher.go:sortBeadsByPriority`, `pkg/dispatcher/dispatcher.go:focusedDescendants`, `pkg/dispatcher/dispatcher_test.go:TestSortBeadsByPriority_EpicFinishing`
   - Signature: `func (d *Dispatcher) buildSchedulingPlan(ctx context.Context, beads []protocol.Bead) (schedulingPlan, map[string]bool, uint64)`
   - Edges: no focused epic, empty queue, parent lookup failure.

2. Preserve epic unit contiguity and root-priority lookup
   - Test: `pkg/dispatcher/dispatcher_test.go:TestBuildSchedulingPlan_EpicUnitKeepsFrontierContiguous`
   - Cmd: `go test ./pkg/dispatcher -run 'TestBuildSchedulingPlan_EpicUnitKeepsFrontierContiguous|TestBuildSchedulingPlan_NestedEpicUsesRootPriority|TestBuildSchedulingPlan_EpicPriorityBeatsEpicAge' -count=1 -v`
   - Assert: selected epic descendants remain contiguous and nested descendants use root epic priority.
   - Read: `pkg/dispatcher/dispatcher.go:isFocusedDescendant`, `pkg/dispatcher/dispatcher.go:focusedDescendants`, `pkg/beadstore/testfake.go:Show`
   - Edges: nested epic chain, parent cycle, missing parent, equal epic priority.

3. Wire tryAssign to consume scheduling units
   - Test: `pkg/dispatcher/dispatcher_test.go:TestTryAssign_FillsSelectedEpicBeforeNextEpic`
   - Cmd: `go test ./pkg/dispatcher -run TestTryAssign_FillsSelectedEpicBeforeNextEpic -count=1 -v`
   - Assert: multiple idle workers drain the selected epic frontier before moving to another same/lower-priority epic.
   - Read: `pkg/dispatcher/dispatcher.go:tryAssign`, `pkg/dispatcher/dispatcher.go:assignGeneralIdleWorkers`, `pkg/dispatcher/dispatcher.go:assignTargetedIdleWorkers`
   - Edges: targeted idle workers, reserved spawn-for targets, fewer epic children than idle workers.

4. Preserve focus and immediate focus semantics
   - Test: `pkg/dispatcher/dispatcher_test.go:TestBuildSchedulingPlan_FocusOverridesIndependent`
   - Cmd: `go test ./pkg/dispatcher -run 'TestBuildSchedulingPlan_FocusOverridesIndependent|TestImmediateFocus' -count=1 -v`
   - Assert: focus descendants outrank independent work, and backfill after focused work uses independent-then-epic scheduling.
   - Read: `pkg/dispatcher/dispatcher.go:sortBeadsByPriority`, `pkg/dispatcher/dispatcher.go:applyFocusDirective`, existing focus tests.
   - Edges: nested focused epic, immediate focus preemption, backfill when focused frontier is empty.

5. Replace obsolete oldest-epic regression tests and docs
   - Test: `pkg/dispatcher/dispatcher_test.go:TestBuildSchedulingPlan_EpicPriorityBeatsEpicAge`
   - Cmd: `go test ./pkg/dispatcher -run 'TestBuildSchedulingPlan|TestTryAssign_FillsSelectedEpicBeforeNextEpic' -count=1 -v`
   - Assert: no test or comment still claims unfocused epics are ordered by oldest epic ID.
   - Read: `pkg/dispatcher/dispatcher_test.go:TestSortBeadsByPriority_EpicFinishing`, `docs/plans/2026-05-06-dispatcher-scheduling-units-design.md`
   - Edges: stale comments, stale test names, old helper still referenced.

## Acceptance

```bash
go test ./pkg/dispatcher -run 'TestBuildSchedulingPlan|TestTryAssign_FillsSelectedEpicBeforeNextEpic|TestImmediateFocus' -count=1
./scripts/quality_gate.sh
```

Operational acceptance:

- With independent beads ready, workers take independent beads before non-focused epic children.
- When independent queue is empty, workers select the highest-priority epic unit and assign its ready frontier.
- A P0 epic is selected before a P2 epic regardless of epic ID age.
- Focused epic work still outranks independent backfill.
