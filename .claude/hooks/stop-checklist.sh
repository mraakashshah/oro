#!/usr/bin/env bash
# Stop hook: landing-the-plane checklist
# Stop hooks cannot inject context (fires after response).
# Output empty JSON to satisfy hook schema without errors.
set -euo pipefail

echo '{}'
