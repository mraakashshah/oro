#!/usr/bin/env bash
# Stop hook: inject landing-the-plane checklist before agent finishes
set -euo pipefail

cat <<'EOF'
{"hookSpecificOutput":{"hookEventName":"Stop","additionalContext":"<IMPORTANT>\nBefore declaring done, verify the landing-the-plane checklist:\n\n1. [ ] All tests pass (pytest, go test)\n2. [ ] Lint clean (ruff check, go vet, shellcheck)\n3. [ ] Format clean (ruff format --check, gofmt -l, biome check)\n4. [ ] Filed bd issues for discovered/remaining work\n5. [ ] Committed with conventional commit message\n6. [ ] bd sync --flush-only\n7. [ ] git push succeeded\n\nWork is NOT complete until git push succeeds.\n</IMPORTANT>"}}
EOF
