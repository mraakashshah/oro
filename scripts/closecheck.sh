#!/usr/bin/env bash
# closecheck.sh — flag direct Store.Close calls that should be Dispatcher.CloseBead.
# Usage: scripts/closecheck.sh <dir> [<dir>...]
# Exits 0 if clean, 1 if violations, 2 on error.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

exec go run "$repo_root/pkg/lint/closecheck/cmd" "$@"
