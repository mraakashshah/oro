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
legacy `.beads/` data: all beads are visible via `oro bead list`, native
commands work, and the new backend is healthy.

## Steps

### 1. Confirm no writers are running

```sh
oro status
scripts/check-phase8-no-writers.py
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
    legacy_beads_dir=$(dirname "$LEGACY_DOLT_PATH")
    archive_item_rel="dolt"
    ;;
  */.beads/beads_*/.dolt|*/.beads/embeddeddolt/*/.dolt)
    legacy_beads_dir=${LEGACY_DOLT_PATH%%/.beads/*}/.beads
    db_dir=${LEGACY_DOLT_PATH%/.dolt}
    archive_item_rel=${db_dir#"$legacy_beads_dir"/}
    ;;
  *)
    echo "unexpected legacy Dolt path: $LEGACY_DOLT_PATH" >&2
    exit 1
    ;;
esac

repo_root=$(dirname "$legacy_beads_dir")
safe_name=$(printf '%s' "$archive_item_rel" | tr '/.' '__')
archive="$repo_root/beads-dolt-${safe_name}-archive-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"

if [ -e "$archive" ]; then
  echo "archive already exists: $archive" >&2
  exit 1
fi

# Create a timestamped tarball in the project root. The archive stores the path
# relative to .beads so rollback can restore the exact legacy layout.
tar -czf "$archive" -C "$legacy_beads_dir" "$archive_item_rel"

# Verify the archive is readable and contains the expected relative path.
tar -tzf "$archive" | head

# Remove the now-archived directory.
rm -rf "$legacy_beads_dir/$archive_item_rel"
```

### 4. Optional: remove the entire `.beads/` directory

If the project is fully migrated and no other tooling reads `.beads/`:

```sh
rm -rf <REPO>/.beads
```

Keep the tarballs for at least 30 days before permanent deletion.

## Rollback

Each archive stores a path relative to `.beads/`, such as `dolt/`,
`beads_oro/`, or `embeddeddolt/<project>/`. To restore from an archive:

```sh
mkdir -p <REPO>/.beads
tar -xzf <REPO>/beads-dolt-<SAFE_NAME>-archive-<DATE>.tar.gz -C <REPO>/.beads
```

Verify the expected layout exists before restarting legacy tooling:

```sh
find <REPO>/.beads -maxdepth 3 -type d \( \
  -path '*/.beads/dolt' -o \
  -path '*/.beads/beads_*/.dolt' -o \
  -path '*/.beads/embeddeddolt/*/.dolt' \
\)
```

## Reference

- `LegacyBeadsDir` in `cmd/oro/paths.go` defines the legacy directory name
  (`.beads`).
- Known legacy Dolt layouts are `.beads/dolt/`, `.beads/beads_<project>/.dolt`,
  and `.beads/embeddeddolt/<project>/.dolt`.
