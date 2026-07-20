#!/usr/bin/env bash
# End-to-end acceptance for Storage Lifecycle Epic 1. The named Go fixtures
# create their own temporary home, cache, and worktree roots; this wrapper
# never reads from or writes to the operator's real cache roots.

set -euo pipefail

if [[ "$(git branch --show-current)" != "main" ]]; then
	echo "STORAGE_EPIC1 requires the main branch" >&2
	exit 1
fi

tests=(
	"TestStorageSharedCacheEndToEnd"
	"TestStorageStandalonePolicyParity"
	"TestStorageCLIAndHealthWiring"
)

for test_name in "${tests[@]}"; do
	output="$(go test -v ./pkg/integration -run "^${test_name}$" -count=1 -timeout=10m 2>&1)"
	printf '%s\n' "$output"
	if ! grep -Fq "=== RUN   ${test_name}" <<<"$output"; then
		echo "named integration test did not run: ${test_name}" >&2
		exit 1
	fi
done

echo "STORAGE_EPIC1_PASS"
