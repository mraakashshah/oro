"""Test that pyright configuration correctly declares pythonVersion 3.13."""

import json
import tomllib
from pathlib import Path

PROJECT_ROOT = Path(__file__).parent.parent


def test_pyright_python_version():
    """pyproject.toml and any pyrightconfig.json must declare pythonVersion 3.13.

    When pyrightconfig.json is present it takes precedence over pyproject.toml,
    so both files must set pythonVersion to 3.13 to prevent false
    'union syntax requires Python 3.10+' errors.
    """
    pyproject = PROJECT_ROOT / "pyproject.toml"
    with pyproject.open("rb") as f:
        config = tomllib.load(f)

    pyright_cfg = config.get("tool", {}).get("pyright", {})
    assert pyright_cfg.get("pythonVersion") == "3.13", (
        f"pyproject.toml [tool.pyright] pythonVersion must be '3.13', got {pyright_cfg.get('pythonVersion')!r}"
    )

    pyrightconfig = PROJECT_ROOT / "pyrightconfig.json"
    if pyrightconfig.exists():
        data = json.loads(pyrightconfig.read_text())
        assert data.get("pythonVersion") == "3.13", (
            f"pyrightconfig.json takes precedence over pyproject.toml but "
            f"pythonVersion is {data.get('pythonVersion')!r} — must be '3.13'"
        )
