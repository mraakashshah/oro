# Script Catalog

The quality gate and Make targets invoke most scripts automatically. The
entries below document scripts intended for direct developer or operator use,
including focused regression checks that are not discovered by `go test`.

Run commands from the repository root unless noted otherwise.

| Script | Purpose | Invocation |
|---|---|---|
| `check-merge-content.sh` | Reject a tree-neutral merge that drops files newly added by a non-first parent. | `scripts/check-merge-content.sh . <merge-commit>` |
| `check-beadstore-shadow-monitor.sh` | Verify that a beadstore shadow-mode observation window has run for at least 24 hours. | `ORO_BEADSOURCE_MODE=shadow ORO_DB_PATH=/path/to/state.db scripts/check-beadstore-shadow-monitor.sh` |
| `check_parity_docs.sh` | Check that Codex parity documentation retains required commands, paths, and terminology. | `scripts/check_parity_docs.sh` |
| `install_test.sh` | Exercise platform detection, URL construction, and installation behavior in `install.sh`. | `scripts/install_test.sh` |
| `nilaway_lint_wiring_test.sh` | Regression-test the Makefile and quality-gate wiring for blocking NilAway checks. | `bash scripts/nilaway_lint_wiring_test.sh` |
| `quality_gate_test.sh` | Regression-test complete output capture from parallel quality-gate checks. | `scripts/quality_gate_test.sh` |
| `run-edit-corpus.sh` | Run the focused edit-package corpus tests without the full repository gate. | `scripts/run-edit-corpus.sh` |
| `test-quality-gate-parallel.sh` | Exercise concurrent quality-gate runs from isolated worktrees. | `scripts/test-quality-gate-parallel.sh` |
| `test_check_no_claude_workers_phrasing.sh` | Test detection of disallowed Claude-worker phrasing in public-facing files. | `scripts/test_check_no_claude_workers_phrasing.sh` |
| `test_run_rule_conversions.py` | Pytest coverage for the rule-conversion runner and conversion ledger. | `pytest -q scripts/test_run_rule_conversions.py` |
| `verify-mg-doc-cleanup.sh` | Verify that removed `oro mg` documentation stays removed or clearly marked historical. | `scripts/verify-mg-doc-cleanup.sh` |
