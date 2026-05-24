# Oro Converted Rule Ledger

This ledger records level-6 rules from `assets/rules-audit.md` that have a
published deterministic verifier after the H.2 conversion pass.

| Rule ID | Converted By | Verification |
| --- | --- | --- |
| R013 | H.2c destructive command guard | `uv run --with pytest pytest tests/test_destructive_command_guard.py -q` verifies dangerous Bash payloads are denied. |
| R015 | H.2c destructive command guard | `uv run --with pytest pytest tests/test_destructive_command_guard.py -q` verifies `git branch -D` is denied while `git branch -d` is allowed. |
| R019 | H.2d epic child blocker invariant | `uv run --with pytest pytest scripts/test_native_beadstore_invariants.py -q` verifies open epic children require explicit blocker edges. |
| R030 | H.2b handoff schema linter | `uv run --with pytest pytest tests/test_handoff_schema_lint.py -q` verifies handoff YAML requires `tasks.completed`, `tasks.in_progress`, and `tasks.remaining`. |
| R048 | H.2a skill description linter | `uv run --with pytest pytest tests/test_skill_description_lint.py -q` verifies `SKILL.md` descriptions stay trigger-only. |

Run `scripts/run-rule-conversions.sh` to execute the conversion verifier set.
