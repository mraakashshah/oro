# QG Locale Bootstrap Warning

**Date:** 2026-05-24
**Component:** quality gate
**Severity:** high

## Symptom

Quality gate runs on workers could start with:

```text
bash: warning: setlocale: LC_ALL: cannot change locale (C.UTF-8): No such file or directory
```

The warning fingerprint was `qg:bd76ca36a57f46928f0bcdca`.

## Investigation

`LC_ALL=C.UTF-8 LANG=C.UTF-8 ./scripts/quality_gate.sh` reproduced the warning before the quality gate banner. `locale -a` on the host did not include `C.UTF-8`, so Bash emitted the warning while starting.

Earlier runner-side environment sanitization fixed subprocesses launched by Oro, but direct script execution still failed because `#!/usr/bin/env bash` starts Bash before script code can normalize `LC_ALL`.

## Root Cause

Locale normalization inside a Bash script is too late for Bash startup warnings. The script interpreter is launched from the shebang first; only after that does the script body run.

## Solution

`scripts/quality_gate.sh` and the generated template now start with a `/bin/sh` bootstrap. The bootstrap validates `LC_ALL`, falls back to `C` when the locale is unavailable, marks the script as bootstrapped, and re-execs Bash for the existing Bash implementation.

Regression coverage lives in `scripts/test_quality_gate.sh` and `cmd/oro/quality_gate_gen_test.go`.

## Prevention

When fixing inherited environment warnings from interpreters, sanitize before launching that interpreter. For scripts with Bash-specific bodies, use a POSIX `sh` prelude plus `exec /usr/bin/env bash "$0" "$@"` rather than putting the fix after `set -euo pipefail`.
