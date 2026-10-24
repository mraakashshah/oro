# Experimental autonomous factory core

`oro factory` runs an objective through implementation, independent acceptance,
independent review, and promotion of the exact verified commit. Rejected attempts
feed diagnostics into the next worker automatically. There is no human approval
step. Attempts and the absolute deadline persist across restarts.

This is the first replacement-core increment. Use trusted command adapters in a
disposable or externally isolated repository. It does not provide a sandbox,
restart a killed controller, deploy services, or replace the existing dispatcher.
The [design](../plans/2026-10-23-factory-core-design.md) describes the remaining
requirements for a fully autonomous production factory.

## Run an objective

Use the repository's Git working-tree root as `repository`. Create a dedicated
target branch that is not checked out in any worktree. Keep the three executable
policy programs and the state directory outside that repository. For example:

```json
{
  "id": "calculator",
  "objective": "Implement integer addition, including negative inputs",
  "repository": "/absolute/path/to/repository",
  "target_branch": "factory/calculator",
  "worker": {"path": "/absolute/policies/worker", "args": []},
  "acceptance": {"path": "/absolute/policies/acceptance", "args": []},
  "review": {"path": "/absolute/policies/review", "args": []},
  "required_checks": ["positive-addition", "negative-addition"],
  "max_attempts": 3,
  "timeout_seconds": 300,
  "step_timeout_seconds": 60
}
```

Save this as a specification file. Use a binary built from the checkout containing
this increment; for a local build and invocation:

```sh
make stage-assets
go build -o ./oro ./cmd/oro
./oro factory run --spec /absolute/path/to/spec.json --state-dir /absolute/factory-state
./oro factory status --id calculator --state-dir /absolute/factory-state
```

`run` prints terminal status JSON and succeeds only for `completed`. Exhaustion
returns a durable `failed` result and a nonzero exit. Infrastructure errors also
return nonzero. `status` reads existing state without executing adapters; a
successful read exits zero even when the objective failed.

Reissue the same `run` command after a controller interruption. The original
budget and deadline remain in force. A completed objective returns its recorded
commit without executing more work. Changing the specification or source policy
bytes under an existing ID is rejected. A new objective requires a new ID.

## Adapter contract

Each executable receives one JSON object on stdin with `objective`, `attempt`,
`worktree`, `candidate_sha`, `base_sha`, and `feedback`. Arguments are literal
argv. The worker edits its current working directory and must not commit; the
core creates the candidate commit. Worker stdout is diagnostic only.

Acceptance and review each run in a new detached checkout of the candidate.
Worker build outputs do not carry into either checker, and checker outputs do
not carry into the next stage. Tracked edits or movement of HEAD invalidate a
checker result. Acceptance must test actual delivered behavior and return one
JSON object, with no additional stdout:

```json
{"candidate_sha":"<exact candidate SHA>","passed":true,"checks":["positive-addition","negative-addition"],"feedback":""}
```

Review returns:

```json
{"candidate_sha":"<exact candidate SHA>","approved":true,"feedback":""}
```

Both programs must exit zero. Missing checks, mismatched identities, malformed
receipts, negative verdicts, errors, timeouts, and output overflow fail the
attempt. Put useful repair instructions in `feedback`. Each output stream is
limited to 1 MiB; durable retry diagnostics are also bounded.

Policy files are frozen at admission and checked again before execution and
promotion. Interpreters, tools and external dependencies remain part of the
trusted execution environment. Separate processes do not guarantee that the
acceptance criteria or reviewer judgment are correct.

## Recovery and evidence

One controller can own an objective at a time. State and transition events commit
atomically to the objective's SQLite database. Promotion uses Git's expected-old
ref comparison, so an intervening target change cannot be overwritten. The core
never rebases a verified candidate, pushes it, or cleans up its worktrees.

An interrupted worker or incomplete verification consumes its reserved attempt.
Valid approval can resume promotion. If Git promotion succeeded before the
completion record was committed, restart recognizes the exact target and records
completion, even if the deadline has since expired. An externally restarted
controller performs this reconciliation without human repair of its state.

To verify the implementation in a development checkout:

```sh
go test ./pkg/factory -race -cover -count=1 -timeout=300s
make stage-assets
go test ./cmd/oro -run '^TestFactoryCLI$' -count=1 -timeout=120s
```

The tests use real Git, SQLite and subprocesses. They demonstrate rejection of a
wrong runnable artifact despite misleading ignored worker output, repair from
feedback, fresh-checkout delivery, concurrent-controller exclusion, bounded
termination, and recovery around the promotion/completion crash window. These
are lifecycle proofs; real-model reliability and unattended production operation
still require separate trials and the design's cutover prerequisites.
