#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("check-phase8-no-writers.py")
SPEC = importlib.util.spec_from_file_location("check_phase8_no_writers", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
CHECKER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECKER)


class Phase8NoWritersTest(unittest.TestCase):
    def matches(self, rows: list[str], self_pid: int = 99999) -> list[tuple[str, str]]:
        return CHECKER.find_writer_matches(rows, self_pid)

    def test_ignores_daemons_and_shell_carried_commands(self) -> None:
        matches = self.matches(
            [
                "1224 com.apple.sbd /System/Library/PrivateFrameworks/CloudServices.framework/Helpers/com.apple.sbd",
                "10427 donotdisturbd "
                "/System/Library/PrivateFrameworks/DoNotDisturbServer.framework/Support/donotdisturbd",
                "92360 zsh /bin/zsh -c ./oro bead migrate-from-dolt --help",
                "92361 zsh zsh -c 'bd update oro-x --status=open'",
            ]
        )
        self.assertEqual(matches, [])

    def test_detects_direct_bd_and_oro_mutators(self) -> None:
        matches = self.matches(
            [
                "123 bd /usr/local/bin/bd update oro-x --status=open",
                "124 bd bd update oro-x",
                "125 oro ./oro --json bead migrate-from-dolt",
                "126 oro ./oro bead create --title x",
                "127 oro /Users/as21/codehouse/oro/oro dispatcher start",
                "128 oro ./oro worker --socket /tmp/oro.sock --id w-1",
            ]
        )
        self.assertEqual(len(matches), 6, matches)

    def test_detects_temp_named_oro_binary_migration(self) -> None:
        matches = self.matches(
            [
                "701 oro-main-ect4.1 /tmp/oro-main-ect4.1 bead migrate-from-dolt",
                "702 oro-review /tmp/oro-review bead create --title x",
                "703 oro-local /tmp/oro-local dispatcher start",
            ]
        )
        self.assertEqual(len(matches), 3, matches)

    def test_detects_global_flags_and_start_commands(self) -> None:
        matches = self.matches(
            [
                "801 oro ./oro --config /tmp/cfg --home /tmp/home --project /tmp/project "
                "--socket /tmp/oro.sock --state-db /tmp/state.db --log-level debug "
                "bead create --title x",
                "802 oro ./oro --project=/tmp/project --log-level=debug bead migrate-from-dolt",
                "803 oro ./oro start",
                "804 oro ./oro work oro-123",
                "805 oro ./oro worker-launch --socket /tmp/oro.sock --id worker-1",
            ]
        )
        self.assertEqual(len(matches), 5, matches)

    def test_skips_own_process_id(self) -> None:
        matches = self.matches(
            [
                "99999 oro ./oro bead migrate-from-dolt",
                "901 bd bd update oro-x",
            ],
            self_pid=99999,
        )
        self.assertEqual(matches, [("901", "bd update oro-x")])

    def test_ignores_read_only_or_non_oro_commands(self) -> None:
        matches = self.matches(
            [
                "401 bdx bdx update oro-123",
                "402 zsh zsh -c 'bd update oro-123'",
                "403 oro ./oro bead list --json",
                "404 oro ./oro bead closed --limit 5",
            ]
        )
        self.assertEqual(matches, [])


if __name__ == "__main__":
    unittest.main()
