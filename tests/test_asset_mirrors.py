#!/usr/bin/env python3
"""Tests for generated and local agent asset mirrors."""

import subprocess
from pathlib import Path

REPO_ROOT = Path(__file__).parent.parent


def _files_under(path: Path, suffixes: tuple[str, ...] | None = None) -> list[Path]:
    files = sorted(p for p in path.rglob("*") if p.is_file() and "__pycache__" not in p.parts)
    if suffixes is None:
        return files
    return [p for p in files if p.suffix in suffixes]


def _assert_mirror(
    source_root: Path,
    mirror_root: Path,
    *,
    exclude_names: set[str] | None = None,
    suffixes: tuple[str, ...] | None = None,
) -> None:
    exclude_names = exclude_names or set()
    assert source_root.exists(), f"Missing source asset directory: {source_root}"
    assert mirror_root.exists(), f"Missing mirror asset directory: {mirror_root}"

    for source_file in _files_under(source_root, suffixes):
        if source_file.name in exclude_names:
            continue
        rel = source_file.relative_to(source_root)
        mirror_file = mirror_root / rel
        assert mirror_file.exists(), f"Missing mirrored asset: {mirror_file}"
        assert source_file.read_bytes() == mirror_file.read_bytes(), (
            f"Asset mirror drift: {source_file} != {mirror_file}"
        )


def _ensure_staged_assets() -> None:
    if (REPO_ROOT / "cmd" / "oro" / "_assets").exists():
        return
    subprocess.run(["make", "stage-assets"], cwd=REPO_ROOT, check=True)


def test_check_agent_asset_mirrors_script_exists() -> None:
    script = REPO_ROOT / "scripts" / "check-agent-asset-mirrors.sh"
    assert script.exists(), "scripts/check-agent-asset-mirrors.sh is missing"
    assert "make stage-assets" in script.read_text()


def test_hooks_are_mirrored_to_claude_and_embedded_assets() -> None:
    _ensure_staged_assets()
    assets_hooks = REPO_ROOT / "assets" / "hooks"
    _assert_mirror(
        assets_hooks,
        REPO_ROOT / ".claude" / "hooks",
        exclude_names={"test_enforce_skills.py"},
        suffixes=(".py", ".sh"),
    )
    _assert_mirror(assets_hooks, REPO_ROOT / "cmd" / "oro" / "_assets" / "hooks", suffixes=(".py", ".sh"))


def test_beacons_and_commands_are_mirrored_to_agent_surfaces() -> None:
    _ensure_staged_assets()
    _assert_mirror(REPO_ROOT / "assets" / "beacons", REPO_ROOT / ".claude" / "hooks" / "beacons")
    _assert_mirror(REPO_ROOT / "assets" / "beacons", REPO_ROOT / "cmd" / "oro" / "_assets" / "beacons")
    _assert_mirror(REPO_ROOT / "assets" / "commands", REPO_ROOT / ".claude" / "commands")
    _assert_mirror(REPO_ROOT / "assets" / "commands", REPO_ROOT / "cmd" / "oro" / "_assets" / "commands")
