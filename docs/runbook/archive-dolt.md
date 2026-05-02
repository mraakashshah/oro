# Runbook: Archive legacy Dolt data on operator machines

## Context

Oro Phase 10 migrates bead storage away from the in-repo `.beads/` directory.
The Dolt database previously lived under `.beads/`, most commonly at
`.beads/dolt/`. Older bd fallback modes may also have written databases at
`.beads/beads_<project>/.dolt` or `.beads/embeddeddolt/<project>/.dolt`.
After migrating to the new storage backend, these directories are no longer
read by oro and can be safely archived or deleted.

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

### 2. Identify legacy Dolt directories

```sh
find ~ -type d \( \
  -path '*/.beads/dolt' -o \
  -path '*/.beads/beads_*/.dolt' -o \
  -path '*/.beads/embeddeddolt/*/.dolt' \
\) 2>/dev/null
```

### 3. Archive each legacy Dolt directory

For each `<LEGACY_DOLT_PATH>` found above:

```sh
case "$LEGACY_DOLT_PATH" in
  */.beads/dolt)
    archive_item="$LEGACY_DOLT_PATH"
    legacy_beads_dir=$(dirname "$LEGACY_DOLT_PATH")
    ;;
  */.beads/beads_*/.dolt|*/.beads/embeddeddolt/*/.dolt)
    archive_item=$(dirname "$LEGACY_DOLT_PATH")
    legacy_beads_dir=${LEGACY_DOLT_PATH%%/.beads/*}/.beads
    ;;
  *)
    echo "unexpected legacy Dolt path: $LEGACY_DOLT_PATH" >&2
    exit 1
    ;;
esac

repo_root=$(dirname "$legacy_beads_dir")
archive="$repo_root/beads-dolt-archive-$(date +%Y%m%d).tar.gz"

# Create a timestamped tarball in the project root.
tar -czf "$archive" -C "$(dirname "$archive_item")" "$(basename "$archive_item")"

# Verify the archive is readable.
tar -tzf "$archive" | head

# Remove the now-archived directory.
rm -rf "$archive_item"
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
- Known legacy Dolt layouts are `.beads/dolt/`, `.beads/beads_<project>/.dolt`,
  and `.beads/embeddeddolt/<project>/.dolt`.
