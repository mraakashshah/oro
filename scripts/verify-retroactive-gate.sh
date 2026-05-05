#!/usr/bin/env bash
# verify-retroactive-gate.sh — §11.4 retroactive premortem gate smoke test.
# Runs the four retroactive gate unit tests and exits non-zero on any failure.
# Usage: scripts/verify-retroactive-gate.sh
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

echo "==> §11.4 retroactive premortem gate smoke tests"
go test -C "$repo_root" ./pkg/dispatcher/... \
  -run "TestSixthChildSetsEligible|TestExecuteRefusedOnEligibleParent|TestPremortemAutoSpawnedOnEligible|TestVerdictTransitionsGateState" \
  -v -timeout 120s
