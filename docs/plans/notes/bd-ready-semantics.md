# bd ready semantics audit

Bead: `oro-r6ql`

Source audited:

- Installed command: `/opt/homebrew/bin/bd`
- Installed version: `bd version 1.0.2 (Homebrew)`
- Upstream source: `github.com/gastownhall/beads`, tag `v1.0.2`
- Source commit: `a3f834b31fe9d250f21120f8d4d56e7221bcd8b4`

The replatform spec section 6.3 defines a compact `beads_ready` view:

```sql
SELECT b.*
FROM beads b
WHERE b.deleted = 0
  AND b.status = 'open'
  AND (b.deferred_until IS NULL OR datetime(b.deferred_until) <= datetime('now'))
  AND NOT EXISTS (
    SELECT 1 FROM bead_deps d
    JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
    WHERE d.bead_id = b.id
      AND d.type IN ('blocks','conditional-blocks')
      AND parent.status != 'closed'
  );
```

That captures the core case, but it is not byte-for-byte compatible with `bd ready`.

## Side-by-side table

| Behavior | bd 1.0.2 source | Current replatform spec | Disposition |
| --- | --- | --- | --- |
| Candidate status | CLI sets `WorkFilter.Status = "open"` for `bd ready`; lower-level `GetReadyWork` defaults to `open` plus `in_progress` only when no status is passed. | Requires `status = 'open'`. | Correct for CLI parity. Store-level API must be explicit about whether it implements CLI semantics or raw `GetReadyWork` semantics. |
| Deleted rows | Dolt source queries the active `issues` table; deleted handling is storage-schema dependent. | Requires `deleted = 0`. | Correct for SQLite-native schema. |
| Pinned rows | Excluded by `(pinned = 0 OR pinned IS NULL)`. | Not represented. | Add to native ready semantics, either in `beads_ready` or in the Store query layer. |
| Ephemeral/wisp rows | Excluded unless `--include-ephemeral`; when included, wisps are appended from the wisp table. | Not represented. | If native Oro has no wisp table, document as intentionally unsupported. If imported, add an `include_ephemeral` query path. |
| Default excluded types | Excludes `merge-request`, `gate`, `molecule`, `message`, `agent`, `role`, and `rig` when no explicit type filter is provided. User `--exclude-type` values are appended. | Not represented. | Add to CLI/Store query layer. Do not hard-code this into the base view if other callers need raw readiness. |
| Explicit type filter | `--type` replaces the default excluded-type list. | Not represented. | Add to CLI/Store query layer. |
| Assignee filters | Supports assignee and unassigned filters. | Not represented. | Add to query layer, not the base view. |
| Priority filter | Supports exact priority filter. | Not represented. | Add to query layer. |
| Label filters | Supports label AND and label OR, including directory-aware labels when no labels are passed. | Not represented. | Add to query layer; directory scoping is CLI/config behavior. |
| Parent/molecule filters | Supports direct explicit `parent-child` deps and dotted-ID fallback for parent/molecule filtering. | Not represented. | Add to query layer if Oro exposes parent-scoped ready work. |
| Metadata filters | Supports metadata equality and metadata-key-exists filters. | Not represented. | Add to query layer. |
| Future-deferred candidate | Excluded unless `--include-deferred`. | Excluded by `deferred_until` check. | Correct in principle, but column naming differs: bd uses `defer_until`; spec uses `deferred_until`. Migration/query code must normalize this intentionally. |
| Child of future-deferred parent | Excluded unless `--include-deferred`; source computes direct children of future-deferred parents via `parent-child`. | Not represented. | Add to native ready semantics. This is a material readiness divergence. |
| Direct blocking dependency types | Computes blockers from `blocks`, `conditional-blocks`, and `waits-for`. `blocks` and `conditional-blocks` block when both sides are active. | Only `blocks` and `conditional-blocks`. | Add `waits-for` semantics or explicitly defer unsupported molecule/gate behavior. |
| Active blocker statuses | Active IDs are all issues whose status is not `closed` and not `pinned`; external/missing blockers are ignored because they are not in the active-ID set. | Blocks if joined parent exists and `parent.status != 'closed'`. | Change native logic to treat `pinned` as non-blocking. Missing blockers are already ignored by the join. Decide whether custom frozen/done statuses map into this layer. |
| `waits-for` gate behavior | `waits-for` can unblock based on child state. Default gate blocks while any direct child of the spawner is active; `any_children` blocks until at least one direct child closes, while active children remain. | Not represented. | Keep out of the simple view unless gate workflows are in scope. Before cutover, either implement a helper query/function or mark `waits-for` unsupported in imported data. |
| Child of blocked parent | Direct children of blocked parents are also excluded from ready. | Not represented. | Add to native ready semantics. Current bd propagation is direct child propagation, not an unbounded recursive CTE in `GetReadyWork`. |
| Sorting | CLI default is `--sort priority`: `priority ASC, created_at DESC, id ASC`. Store default with empty sort is hybrid. `--sort oldest` and `--sort hybrid` are supported. | Store sketch says `priority ASC, created_at ASC`. | Change sketch. For CLI parity, default should be `priority ASC, created_at DESC, id ASC`. |
| Limit | CLI default limit is 10. Store applies a limit only when `filter.Limit > 0`; `--explain` uses no limit. | View has no limit. | Correct for base view; CLI should enforce the limit. |
| Explain mode | `bd ready --explain --json` returns an object with `ready`, `blocked`, and `summary`, not the normal ready array. | Not represented. | Add only if Oro intends to reproduce explain output. |

## Divergences to resolve

1. `waits-for` is a blocker in bd, but the current spec omits it.

   This is the highest-risk semantic gap. If any existing beads use `waits-for`, a native Store that only checks `blocks` and `conditional-blocks` can dispatch work early. The SQLite implementation should either implement the bd gate logic or fail/import-gate workflows explicitly before the cutover.

2. Children of future-deferred parents are not ready in bd.

   The spec only checks the candidate bead's own defer timestamp. Native readiness should also exclude direct `parent-child` children whose parent has a future defer timestamp, unless the query is intentionally implementing an `include_deferred` mode.

3. Children of blocked parents are not ready in bd.

   The spec only excludes the blocked item itself. Native readiness should propagate blocked-parent state to direct children to match `bd ready`.

4. Pinned issues do not block in bd and are not ready candidates.

   The spec's `parent.status != 'closed'` would treat pinned parents as blockers. It should match bd's active blocker definition: active means not `closed` and not `pinned`.

5. Default type filtering is CLI behavior, not pure readiness.

   `bd ready` hides workflow and identity types by default. The base view should stay simple if other callers need raw semantics, but Oro's claim/dispatch path must apply the same default exclusions before selecting worker tasks.

6. Sort order in the spec sketch differs from CLI parity.

   The sketch uses oldest-first within priority. `bd ready --sort priority` uses newest-first within priority. Use the bd order for dispatcher compatibility unless Oro intentionally changes prioritization.

7. `include-deferred`, `include-ephemeral`, and explain mode are query modes.

   They do not belong in a single static view. The native Store can expose a base readiness predicate and layer these flags in methods.

## Recommended native shape

Use a base readiness predicate for candidate status, deleted, pinned, own defer time, active blockers, child-of-deferred-parent, and child-of-blocked-parent. Keep user-facing filters such as type, labels, assignee, metadata, parent, sort, and limit in the Store method that implements ready work.

For cutover safety, add side-by-side tests that seed a native SQLite store with:

- A `blocks` dependency whose blocker is open, closed, pinned, and missing.
- A `conditional-blocks` dependency with the same blocker statuses.
- A `waits-for` dependency with no children, active children, and closed children.
- A future-deferred parent with an otherwise-ready child.
- A blocked parent with an otherwise-ready child.
- Hidden workflow types and an explicit `--type` override.

The native result should match `bd ready --json --limit 0` for ordinary ready output before any dispatcher uses it.
