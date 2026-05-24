from __future__ import annotations

import re
import subprocess
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
RUNNER = REPO_ROOT / "scripts" / "run-rule-conversions.sh"
LEDGER = REPO_ROOT / "assets" / "rules-converted.md"

CONVERSION_CHECKS = {
    "H.2a": "scripts/check-skill-descriptions.py",
    "H.2b": "scripts/check-handoff-schema.py",
    "H.2c": "tests/test_destructive_command_guard.py",
    "H.2d": "scripts/check-native-beadstore-invariants.py",
}

CONVERTED_RULE_IDS = ("R013", "R015", "R019", "R030", "R048")


def test_run_rule_conversions_executes_all_checks() -> None:
    runner_text = RUNNER.read_text()

    for label, check_path in CONVERSION_CHECKS.items():
        assert check_path in runner_text, f"{label} missing from conversion runner"

    ledger_text = LEDGER.read_text()
    assert "| Rule ID | Converted By | Verification |" in ledger_text
    for rule_id in CONVERTED_RULE_IDS:
        assert re.search(rf"^\| {rule_id} \|", ledger_text, re.MULTILINE), f"{rule_id} missing from conversion ledger"

    result = subprocess.run(
        [str(RUNNER)],
        cwd=REPO_ROOT,
        text=True,
        capture_output=True,
        check=False,
    )
    assert result.returncode == 0, result.stdout + result.stderr
