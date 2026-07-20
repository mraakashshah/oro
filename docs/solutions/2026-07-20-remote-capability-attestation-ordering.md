# Remote Capability Attestation Ordering

**Date:** 2026-07-20
**Component:** `cmd/oro` setup and startup
**Severity:** high

## Symptom

The focused capability test passed, but the real setup path produced no
`.oro/remote-capabilities.json`. Startup could also reconnect to an existing
daemon before checking persisted evidence drift.

## Investigation

Tracing `executeBootstrap` showed that `bootstrapProject` rewrote
`.oro/config.yaml` before `persistSetupRemoteCapabilities` loaded it. The
remote gate therefore appeared local by the time attestation ran. Tracing
`newStartCmd` showed verification only inside dispatcher construction, after
preflight mutations and outside the reconnect path.

The initial quality-gate rerun also reported:

```text
cmd/oro/cmd_start.go:862:6: Function 'newStartCmd' is too long (62 > 60) (funlen)
```

Keeping more preflight logic inline was therefore not a viable correction.

## Root Cause

Capability persistence and verification were attached to downstream helpers
instead of the lifecycle boundaries that own configuration and side effects.
The unit test called those helpers directly, masking the ordering defects in
the production setup and start flows.

## Solution

- `cmd/oro/cmd_init.go:createProjectAnchor` preserves an existing config on
  normal and forced setup, then merges only the project name.
- Both `oro start` and `oro dispatcher start` verify configured capability
  evidence before preflight, PID handling, reconnect, or daemon launch.
- Integration regressions invoke `executeBootstrap` and `newStartCmd` rather
  than only the attestation helpers.
- The startup boundary delegates to a small helper so lint complexity remains
  within the repository gate.

## Prevention

For persisted preflight evidence, test the public lifecycle entry point and
assert both the durable artifact and absence of side effects on failure. A
helper-only test is necessary for decoding details but cannot prove ordering or
wiring.

## Related

- `docs/plans/2026-07-18-dispatcher-owned-github-pr-quality-gates-design.md`
- Bead `oro-bym3`
