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


def _relative_files(path: Path, suffixes: tuple[str, ...] | None = None) -> set[Path]:
    return {p.relative_to(path) for p in _files_under(path, suffixes)}


def _is_allowed_extra(rel: Path, allowed_paths: set[Path], allowed_prefixes: tuple[Path, ...]) -> bool:
    return rel in allowed_paths or any(rel == prefix or prefix in rel.parents for prefix in allowed_prefixes)


def _assert_mirror(
    source_root: Path,
    mirror_root: Path,
    *,
    exclude_names: set[str] | None = None,
    allowed_extra_paths: set[Path] | None = None,
    allowed_extra_prefixes: tuple[Path, ...] = (),
    suffixes: tuple[str, ...] | None = None,
) -> None:
    exclude_names = exclude_names or set()
    allowed_extra_paths = allowed_extra_paths or set()
    assert source_root.exists(), f"Missing source asset directory: {source_root}"
    assert mirror_root.exists(), f"Missing mirror asset directory: {mirror_root}"

    source_files = {rel for rel in _relative_files(source_root, suffixes) if rel.name not in exclude_names}
    mirror_files = _relative_files(mirror_root, suffixes)
    extra_files = {
        rel
        for rel in mirror_files - source_files
        if rel not in allowed_extra_paths and not _is_allowed_extra(rel, allowed_extra_paths, allowed_extra_prefixes)
    }
    assert not extra_files, f"Unexpected mirrored assets in {mirror_root}: {sorted(extra_files)}"

    for rel in sorted(source_files):
        mirror_file = mirror_root / rel
        assert mirror_file.exists(), f"Missing mirrored asset: {mirror_file}"
        assert (source_root / rel).read_bytes() == mirror_file.read_bytes(), (
            f"Asset mirror drift: {source_root / rel} != {mirror_file}"
        )


def _assert_hook_root_contains_only_staged_file_types(
    root: Path,
    *,
    allowed_extra_paths: set[Path] | None = None,
    allowed_extra_prefixes: tuple[Path, ...] = (),
) -> None:
    allowed_extra_paths = allowed_extra_paths or set()
    unexpected = {
        rel
        for rel in _relative_files(root)
        if rel.suffix not in {".py", ".sh"} and not _is_allowed_extra(rel, allowed_extra_paths, allowed_extra_prefixes)
    }
    assert not unexpected, f"Unexpected hook artifacts in {root}: {sorted(unexpected)}"


def _ensure_staged_assets() -> None:
    if (REPO_ROOT / "cmd" / "oro" / "_assets").exists():
        return
    subprocess.run(["make", "stage-assets"], cwd=REPO_ROOT, check=True)


def test_check_agent_asset_mirrors_script_exists() -> None:
    script = REPO_ROOT / "scripts" / "check-agent-asset-mirrors.sh"
    assert script.exists(), "scripts/check-agent-asset-mirrors.sh is missing"
    assert "make stage-assets" in script.read_text()


def test_agent_asset_mirrors() -> None:
    """Active agent asset sources are mirrored to dogfood and embedded surfaces."""
    _ensure_staged_assets()

    assets_hooks = REPO_ROOT / "assets" / "hooks"
    _assert_mirror(
        assets_hooks,
        REPO_ROOT / ".claude" / "hooks",
        exclude_names={"test_enforce_skills.py", "test_hook_schemas.py", "test_parity.py"},
        allowed_extra_paths={
            Path("test_architect_router.py"),
            Path("test_architect_router_new.py"),
            Path("test_context_pct_writer.py"),
            Path("test_hook_paths.py"),
            Path("test_notify_manager_on_bead_create.py"),
            Path("test_pane_handoff_reminder.py"),
            Path("test_prompt_injection_guard.py"),
            Path("test_rebase_worktree_guard.py"),
            Path("test_session_start_extras.py"),
        },
        suffixes=(".py", ".sh"),
    )
    _assert_mirror(assets_hooks, REPO_ROOT / "cmd" / "oro" / "_assets" / "hooks", suffixes=(".py", ".sh"))
    _assert_hook_root_contains_only_staged_file_types(assets_hooks)
    _assert_hook_root_contains_only_staged_file_types(
        REPO_ROOT / ".claude" / "hooks",
        allowed_extra_paths={Path(".DS_Store"), Path("oro-search-hook")},
        allowed_extra_prefixes=(Path("beacons"),),
    )
    _assert_hook_root_contains_only_staged_file_types(REPO_ROOT / "cmd" / "oro" / "_assets" / "hooks")

    _assert_mirror(REPO_ROOT / "assets" / "beacons", REPO_ROOT / ".claude" / "hooks" / "beacons")
    _assert_mirror(REPO_ROOT / "assets" / "beacons", REPO_ROOT / "cmd" / "oro" / "_assets" / "beacons")

    _assert_mirror(REPO_ROOT / "assets" / "commands", REPO_ROOT / ".claude" / "commands")
    _assert_mirror(REPO_ROOT / "assets" / "commands", REPO_ROOT / "cmd" / "oro" / "_assets" / "commands")

    _assert_mirror(
        REPO_ROOT / "assets" / "skills",
        REPO_ROOT / ".claude" / "skills",
        allowed_extra_paths={Path(".DS_Store"), Path("restart-oro")},
        allowed_extra_prefixes=(Path("oro"), Path("watching-oro")),
    )
    _assert_mirror(REPO_ROOT / "assets" / "skills", REPO_ROOT / "cmd" / "oro" / "_assets" / "skills")


def test_hooks_are_mirrored_to_claude_and_embedded_assets() -> None:
    test_agent_asset_mirrors()


def test_beacons_and_commands_are_mirrored_to_agent_surfaces() -> None:
    test_agent_asset_mirrors()
