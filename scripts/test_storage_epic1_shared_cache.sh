#!/usr/bin/env bash
# End-to-end acceptance for Storage Lifecycle Epic 1. The named Go fixtures
# create their own temporary home, cache, and worktree roots; this wrapper
# never reads from or writes to the operator's real cache roots.

set -euo pipefail

if [[ "$(git branch --show-current)" != "main" ]]; then
	echo "STORAGE_EPIC1 requires the main branch" >&2
	exit 1
fi

acceptance_root="$(mktemp -d "${TMPDIR:-/tmp}/oro-storage-epic1.XXXXXX")"
cleanup() {
	chmod -R u+w "$acceptance_root" 2>/dev/null || true
	rm -rf -- "$acceptance_root"
}
trap cleanup EXIT

export HOME="$acceptance_root/home"
export XDG_CACHE_HOME="$acceptance_root/cache"
export GOCACHE="$acceptance_root/cache/go-build"
export GOMODCACHE="$acceptance_root/cache/go-mod"
export UV_CACHE_DIR="$acceptance_root/cache/uv"
export GOLANGCI_LINT_CACHE="$acceptance_root/cache/golangci-lint"
export NPM_CONFIG_CACHE="$acceptance_root/cache/npm"
export TMPDIR="$acceptance_root/tmp"
mkdir -p "$HOME" "$GOCACHE" "$GOMODCACHE" "$UV_CACHE_DIR" "$GOLANGCI_LINT_CACHE" "$NPM_CONFIG_CACHE" "$TMPDIR"

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
