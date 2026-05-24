# SQLite Busy During Assignment Completion

**Date:** 2026-05-24
**Component:** Dispatcher assignment tracking
**Severity:** high

## Symptom

After a worker merged successfully, assignment completion could fail with:

```text
complete assignment: database is locked (5) (SQLITE_BUSY)
```

The bead remained `in_progress` and the assignment row stayed `active` until operator repair.

## Investigation

`pkg/dispatcher/dispatcher.go:completeAssignment` performed a single `UPDATE assignments`
statement. Even though dispatcher databases are opened with WAL and `busy_timeout=5000`,
a short write lock from another connection can still surface as `SQLITE_BUSY` after the
connection's busy timeout expires.

## Root Cause

Assignment completion treated every SQL execution error as terminal. Transient SQLite
write-lock errors were not retried, so a successful merge could leave dispatcher tracking
state inconsistent when completion raced with another writer.

## Solution

`completeAssignment` now retries only transient SQLite busy or locked errors around the
existing single-attempt update path. The original row-count validation and quarantined
assignment no-op behavior remain in the single-attempt function.

The regression test `TestCompleteAssignmentRetriesTransientSQLiteBusy` uses a file-backed
dispatcher database with `PRAGMA busy_timeout=1`, holds an `IMMEDIATE` write lock from a
second connection, releases it shortly after completion starts, and verifies the assignment
ends `completed` with `completed_at` set.

## Prevention

When a dispatcher write is required to close out already-successful work, test it with a
real file-backed SQLite database and a competing write transaction. In-memory shared-cache
tests do not exercise the same lock behavior as the production state database.
