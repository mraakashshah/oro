# Beadstore Recovery Drill - 2026-05-02

Purpose: exercise the native SQLite recovery runbook before marking the legacy
Dolt recovery memories superseded.

Scope: non-destructive drill against a temporary copy of
`/Users/as21/.oro/projects/oro/state.db`. The live state database was not
modified.

Runbook section exercised:
`docs/runbooks/beadstore-recovery.md` "Bad initial import" recovery path for a
non-empty pre-migration SQLite snapshot, including backup, integrity check,
native beadstore table clearing, and dry-run retry.

Command evidence:

```text
drill_workdir=/tmp/oro-recovery-drill-20260502.2LK5Au
snapshot_count=1757
ok
beads|0
bead_deps|0
bead_tags|0
bead_labels|0
bead_metadata|0
bead_notes|0
verified_beads_count=0
verified_bead_deps_count=0
verified_bead_tags_count=0
verified_bead_labels_count=0
verified_bead_metadata_count=0
verified_bead_notes_count=0
Migration plan
source: fixture (/Users/as21/codehouse/oro/testdata/dolt-100/export.jsonl)
beads: 100
dependencies: 3
tags: 5
labels: 2
metadata entries: 4
notes: 3
DRY RUN -- no writes performed
post_dry_run_beads_count=0
```

Result: pass. The copied database backup passed `PRAGMA integrity_check`, the
reviewed native beadstore clear sequence left all native bead tables empty, and
`migrate-from-dolt --dry-run --from-fixture` succeeded without mutating the
copied database.

Residual risk: this drill validates the recovery mechanics on a copied database
and fixture source. It does not approve a future live destructive recovery; live
recovery still requires the runbook gates, explicit target paths, and fresh
operator evidence.
