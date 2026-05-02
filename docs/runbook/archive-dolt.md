# Runbook: Archive `.beads/dolt/` on Operator Machines

## Context

Oro Phase 10 migrates bead storage away from the in-repo `.beads/` directory.
The Dolt database previously lived at `.beads/dolt/` inside each project repo.
After migrating to the new storage backend, this directory is no longer read by
oro and can be safely archived or deleted.

## When to run this

Run this runbook after confirming that `oro` is operating correctly without the
legacy `.beads/dolt/` data (i.e., all beads are visible via `oro bead list` and
the new backend is healthy).

## Steps

### 1. Confirm the dispatcher is not running

```sh
oro status
```

If the dispatcher is still running, stop it first:

```sh
oro stop
```

### 2. Identify projects with legacy Dolt data

```sh
find ~ -type d -name dolt -path '*/.beads/*' 2>/dev/null
```

### 3. Archive each `.beads/dolt/` directory

For each project root `<REPO>` found above:

```sh
# Create a timestamped tarball in the project root
tar -czf <REPO>/beads-dolt-archive-$(date +%Y%m%d).tar.gz -C <REPO>/.beads dolt

# Verify the archive is readable
tar -tzf <REPO>/beads-dolt-archive-$(date +%Y%m%d).tar.gz | head

# Remove the now-archived directory
rm -rf <REPO>/.beads/dolt
```

### 4. (Optional) Remove the entire `.beads/` directory

If the project is fully migrated and no other tooling reads `.beads/`:

```sh
rm -rf <REPO>/.beads
```

Keep the tarball for at least 30 days before permanent deletion.

## Rollback

To restore from the archive:

```sh
mkdir -p <REPO>/.beads
tar -xzf <REPO>/beads-dolt-archive-<DATE>.tar.gz -C <REPO>/.beads
```

## Reference

- `LegacyBeadsDir` constant in `cmd/oro/paths.go` defines the legacy directory name (`.beads`).
- Dolt subdirectory within it is always named `dolt/`.
