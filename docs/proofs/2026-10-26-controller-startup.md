# Controller grants, first supervisor lifecycle and snapshot ownership

Date: 2026-10-26. These are implementation prerequisites for the managed factory,
not an unattended delivery or recovery qualification.

## Integrated changes

`9bdcf40a` enforces typed counted controller authority in existing HELLO/GO
transactions. It denies generic controller scope creation before mutation,
rechecks active runtime/event and exact controller/supervisor reservations before
idempotent replay, and requires controller PID=PGID. Ordinary host peer and
cleanup behavior remain unchanged. Source matches reviewed Terra `7c8d69fe`.

`6b374d62` supplies the first actual
`Supervise → reserveManagedController → StartController` path. One lifecycle
retains the supervisor lock, spends the original counted authority, verifies GO,
observes durable outcomes, and revokes and retires exact launchd scopes within
the original bounds. This deliberately does not retry, reconcile promotion or
Docker uncertainty, release capacity, or provide the concrete CLI/backend.
Source matches reviewed Terra `5c9a4f8f`.

Astra's supervisor review found three Important defects in the first draft:

- A NUL in a backend error prevented durable revocation and all retirement.
- An earlier Docker scope or failed retirement skipped later launchd peers.
- Monitoring missed superseded ownership and could wait until deadline.

Terra reproduced all three as compiling behavioral REDs, then sanitized bounded
persisted diagnostics, retired launchd scopes deterministically with accumulated
errors, and checked ownership before further monitoring or terminal handling.
The supersession test preserves successor state. Astra accepted the final
revision with no remaining findings. New complexity suppressions were removed.

`055e56f8` adds a concrete optional owner binding to managed snapshot saves. In
the existing write transaction, before snapshot or event writes, it verifies
exact manifest/cached/persisted admission bytes, canonical granted controller,
actual current process group, frozen runtime, and immutable identity of both
current and proposed snapshots. Legacy/admission ownerless saves retain their
behavior. Source matches reviewed Terra `287a4d27`. **The actual managed
controller constructor does not yet bind this owner**; that consumer remains
required before claiming production protection.

## Verification

| Check | Result |
| --- | --- |
| Grant worker final focused race / lint | PASS, 33.084 s / zero issues |
| Root grant broader race before test-only followup | PASS, 33.700 s |
| Root final grant-only race | PASS, 10.787 s |
| Full shuffled factory race at grant commit `9bdcf40a` | PASS, 453.366 s; 89.0% statements |
| Full gate at `9bdcf40a` | FAIL, 179.074 s; 15 passed, one failed |
| Root combined supervisor/grant/admission race | PASS, 33.029 s |
| Root supervisor package lint / production NilAway | PASS, zero issues |
| Owner worker focused race / lint | PASS, 26.622 s / zero issues |
| Root owner/grant/store/admission race | PASS, 23.671 s |
| Independent Astra final reviews | No remaining findings for these scoped leaves |
| Full gate at `055e56f8` | FAIL, 172.627 s; 15 passed, one failed |
| Combined full race at `055e56f8` | FAIL, 481.003 s; supervisor cancellation cause |

The grant gate failed only on five APIs lacking production callers. Its later
Go runtime/security/build tiers were not qualified. The new `Supervise` export
also awaits its CLI caller, bringing the latest count to six. No dummy references
or test-only annotations were added. NilAway and lint passed in the latest gate.
The grant full-package coverage predates supervisor and owner changes.

The failed full run reached a cancellation race in
`TestSuperviseControllerOutcomes/cancel`: SQLite returned `interrupted (9)`
while checking grant acknowledgment, and the returned error lost the context
cancellation identity. It exited one after 488.537 seconds harness time. Do not
report this run or its 88.1% profile as passing qualification.

Terra reproduced the original case four times in twenty runs, plus a
deterministic post-grant backend cancellation regression. `6269d528` now joins
the work-context cause only when startup/monitoring fails, retaining both error
causes, bounded retirement and nil terminal success. Astra found no defects;
root's supervisor race passed in 11.173 seconds. Worker repeated cancellation
race passed twenty runs in 16.523 seconds, with zero package lint issues.
Evidence is `/tmp/oro-supervisor-cancel-evidence-20261026/`.

## Workspace and interruption prerequisites

`e3df6dde` matches reviewed Terra `da175d5a`: worker target/checkout/execute/commit
interruptions preserve the spent attempt and existing workspace rather than
rejecting and burning more attempts. Execution retains joined exit/stdout/stderr
diagnostics. A behavioral RED reproduced three consumed attempts and false
terminal failure; a second RED reproduced dropped diagnostics. Astra accepted;
root final race passed in 9.819 seconds. Evidence is
`/tmp/oro-interruption-evidence-20261026/`.

`c627f6c3` matches reviewed Terra `ea56d695`: canonical bounded workspace identity
is retained through reserved, preparing and ready phases, with original
deadline, frozen seed, exact keeper/source association and real controller
ownership. Rows fit the existing per-source metadata allowance. Tests cover
separate connections, missing/corrupt history, exact event-cap and COMMIT rollback
at all three transitions. Astra accepted; root broader race passed in 83.266
seconds before the test-only followup, and final workspace race in 14.477
seconds. Evidence is `/tmp/oro-workspace-state-20261026/`.

At `c627f6c3`, the full gate passed fifteen checks and failed only six dead
exports, in 175.878 seconds. Lint and production NilAway passed. The new full
shuffled factory race passed in mode `workspace-interruption-integrated-full`:
480.631 seconds package, 487.817 seconds harness, 85.2% statement coverage.
Its frozen Go source is `c627f6c3`; final HEAD `fc635415` adds checkpoint docs only.
Both verification handles are terminal. None of these APIs is a substitute
for the actual managed controller consumer, currently under implementation.

Root used Go 1.26.10, race detection and count one. Supervisor selection was
`^TestSupervise|^TestManagedControllerGrant$|^TestManagedControllerReservation$|^TestManagedAdmission$`.
Owner selection was
`^TestManagedControllerOwner$|^TestManagedControllerGrant$|^TestFactoryStore$|^TestStoreFailures$|^TestManagedAdmission$`.
Owner tests use a real process-group-leader subprocess; controller HELLO fixtures
still simulate trusted authenticated peers and do not prove kernel/socket
authentication or native launch.

Archived temporary evidence:

- `/tmp/oro-controller-grant-evidence-20261026/`: behavioral `red.log`, final
  `green-followup.log` and `lint-followup.log`.
- `/tmp/oro-supervisor-start-20261026/`: `nul-red.log`, `generation-red.log`,
  `retirement-red.log`, `final-review-race.log` and `final-review-lint.log`.
  Initial producer-only RED is limited evidence, not a full API baseline.
- `/tmp/oro-owner-evidence-20261026/`: revoked-write behavioral `red.log`,
  `green.log` and `lint.log`.
- `/private/tmp/oro-factory-verify-ldhnhnvl/`: environment/log/result files with
  prefixes `controller-grant-integrated-*` and `supervisor-owner-integrated-*`.

No native Docker, VM or launchd effects, coding-agent delivery, recovery fault
trial, or sustained unattended queue ran in these increments. The full autonomy
objective remains active and unachieved.
