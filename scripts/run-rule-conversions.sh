#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

run_check() {
	local label="$1"
	shift
	printf '==> %s\n' "$label"
	"$@"
}

run_check "H.2a R048 skill description contract (scripts/check-skill-descriptions.py)" \
	uv run --with pytest pytest tests/test_skill_description_lint.py -q

run_check "H.2b R030 handoff schema contract (scripts/check-handoff-schema.py)" \
	uv run --with pytest pytest tests/test_handoff_schema_lint.py -q

run_check "H.2c R013/R015 destructive command guard" \
	uv run --with pytest pytest tests/test_destructive_command_guard.py -q

run_check "H.2d R019 epic child blocker invariant (scripts/check-native-beadstore-invariants.py)" \
	uv run --with pytest pytest scripts/test_native_beadstore_invariants.py -q

run_check "rules conversion ledger includes converted rule IDs" \
	python - <<'PY'
from pathlib import Path

ledger = Path("assets/rules-converted.md").read_text(encoding="utf-8")
missing = [
    rule_id
    for rule_id in ("R013", "R015", "R019", "R030", "R048")
    if f"| {rule_id} |" not in ledger
]
if missing:
    raise SystemExit(f"missing converted rule IDs in assets/rules-converted.md: {', '.join(missing)}")
PY
