# Detached Worker Process Cleanup Design

## Goal

When replacing or stopping a managed worker, terminate only detached
descendants that carry the complete, exact ownership tuple for that worker and
its dispatcher socket.

## Ownership contract

Ownership is established by the complete environment entries:

- `ORO_SOCKET_PATH=<dispatcher socket>`
- `ORO_WORKER_ID=<worker id>`

The ownership matcher accepts typed `[]string` environment entries, not a
rendered process description. `processenv.WorkerOwnershipMarkers` produces the
complete tuple, and `processenv.CommandContainsAllMarkers(entries, markers)`
compares each required entry by exact equality. A partial tuple, a duplicate
name with a different value, argv text, a role value, a tool name, or a
worktree path is not ownership evidence.

`processenv.WithWorkerOwnership` removes inherited ownership entries and adds
the exact socket and worker entries to every managed worker command. This makes
the tuple stable across child processes while preventing an inherited parent
scope from claiming a detached descendant.

## Process environment readers

Residual cleanup calls `processenv.ReadEntries(pid)` and makes decisions only
from its typed `[]string` result. Reader implementations preserve the original
entry delimiters:

- On Darwin, `processenv.ReadEntries` obtains `kern.procargs2` with
  `unix.SysctlRaw` and parses its NUL-delimited executable, argv, and
  environment payload without whitespace tokenization.
- On Linux, `processenv.ReadEntries` reads `/proc/<pid>/environ` and splits its
  NUL-delimited contents into complete environment entries.
- On unsupported operating systems, and for an unreadable or malformed
  process environment, the reader returns an error. Cleanup fails closed: it
  does not infer ownership and does not kill that process.

The reader boundary deliberately excludes argv inspection and whitespace
tokenization. Values may contain spaces, quotes, or marker-shaped text; only a
complete environment entry can satisfy an ownership marker.

## Cleanup sequence

1. Serialize `Spawn` and `Kill` for a worker ID.
2. Terminate the tracked worker process group.
3. Enumerate candidate process IDs using normal process metadata only.
4. For each candidate, read exact entries through `processenv.ReadEntries`.
5. Kill a residual process only if every marker in the complete ownership tuple
   is present as an exact entry.
6. Complete residual cleanup before a same-ID replacement can start.

Unknown worker IDs remain errors and do not start a residual scan. Residual
termination is bounded and uses the existing graceful-then-force process-group
cleanup path.

## Verification

Tests must cover a managed detached descendant, a same-worker/different-socket
process, and a same-socket/different-worker process. Only the exact complete
tuple is eligible for cleanup. Reader tests must preserve environment values
containing whitespace and must verify Darwin `kern.procargs2` parsing, Linux
NUL-delimited parsing, and fail-closed errors for unsupported or unreadable
processes.
