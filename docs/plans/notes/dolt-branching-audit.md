# Dolt Branching Audit

Date: 2026-04-28
Bead: oro-wgks

## Finding

Dolt branching and merging are unused in oro. No source call sites invoke Dolt
branch, merge, or checkout operations, and the checked local Dolt repositories
have no non-main branches.

## Repository State

Commands run from this checkout:

```sh
dolt branch --list
```

Results:

- `.beads/beads_oro`: `* main`
- `.beads/beads_oro/.dolt/stats`: `* main`
- `.beads/.dolt`: not a valid Dolt repository; it only contains server state
  such as `sql-server.info`.

The tracked `repo_state.json` for `.beads/beads_oro` reports:

```json
{
  "head": "refs/heads/main",
  "remotes": {},
  "backups": {},
  "branches": {}
}
```

## Source Search

The audit searched source paths for Dolt branch/merge/checkout usage:

```sh
rg -n "dolt.*(branch|merge|checkout)|dolt\\s+\"?,\\s*\"(branch|merge|checkout)|\"(branch|merge|checkout)\"" \
  cmd pkg scripts assets/hooks --glob '*.go' --glob '*.sh' --glob '*.py'

rg -n "bd dolt (branch|merge|checkout)|dolt (branch|merge|checkout)" \
  cmd pkg scripts assets/hooks --glob '*.go' --glob '*.sh' --glob '*.py'
```

Result: none found for Dolt branch, merge, or checkout operations. Matches for
`branch`, `merge`, and `checkout` are Git worktree/branch operations, prompt
tests, or UI labels, not Dolt storage operations.

## Conclusion

Oro does not rely on Dolt's version-control features. The current Dolt use is as
a SQL storage backend/server for bd, not as a branching or merging layer. This
supports the replatform assumption in §11.1 that the SQLite replacement does
not need to preserve Dolt branch/merge semantics.
