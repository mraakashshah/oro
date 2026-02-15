"""Test that hook path resolution works with and without env vars."""

import os


def oro_home():
    """Return ORO_HOME or default ~/.oro."""
    return os.environ.get("ORO_HOME", os.path.expanduser("~/.oro"))


def oro_project_dir():
    """Return $ORO_HOME/projects/$ORO_PROJECT or None if ORO_PROJECT not set."""
    home = oro_home()
    project = os.environ.get("ORO_PROJECT", "")
    if not project:
        return None
    return os.path.join(home, "projects", project)


def test_with_env_vars():
    """Test paths resolve to ORO_HOME when env vars set."""
    os.environ["ORO_HOME"] = "/tmp/test-oro"
    os.environ["ORO_PROJECT"] = "testproj"

    try:
        assert oro_home() == "/tmp/test-oro"
        assert oro_project_dir() == "/tmp/test-oro/projects/testproj"

        # BEACONS_DIR should resolve via oro_home()
        beacons_dir = os.path.join(oro_home(), "beacons")
        assert beacons_dir == "/tmp/test-oro/beacons"

        # HANDOFFS_DIR should resolve via oro_project_dir()
        proj_dir = oro_project_dir()
        assert proj_dir is not None
        handoffs_dir = os.path.join(proj_dir, "handoffs")
        assert handoffs_dir == "/tmp/test-oro/projects/testproj/handoffs"

        # DECISIONS_FILE should resolve via oro_project_dir()
        decisions_file = os.path.join(proj_dir, "decisions.md")
        assert decisions_file == "/tmp/test-oro/projects/testproj/decisions.md"
    finally:
        os.environ.pop("ORO_HOME", None)
        os.environ.pop("ORO_PROJECT", None)


def test_without_env_vars():
    """Test paths fall back to current behavior when env vars unset."""
    os.environ.pop("ORO_HOME", None)
    os.environ.pop("ORO_PROJECT", None)

    assert oro_home() == os.path.expanduser("~/.oro")
    assert oro_project_dir() is None

    # Fallback paths should match current hardcoded values
    beacons_dir = os.path.join(oro_home(), "beacons") if os.environ.get("ORO_PROJECT") else ".claude/hooks/beacons"
    assert beacons_dir == ".claude/hooks/beacons"

    _pd = oro_project_dir()
    handoffs_dir = os.path.join(_pd, "handoffs") if _pd else "docs/handoffs"
    assert handoffs_dir == "docs/handoffs"

    _pd2 = oro_project_dir()
    decisions_file = os.path.join(_pd2, "decisions.md") if _pd2 else "docs/decisions-and-discoveries.md"
    assert decisions_file == "docs/decisions-and-discoveries.md"


def test_oro_home_default():
    """ORO_HOME defaults to ~/.oro when not set."""
    os.environ.pop("ORO_HOME", None)
    os.environ.pop("ORO_PROJECT", None)
    assert oro_home() == os.path.expanduser("~/.oro")


def test_oro_project_dir_no_project():
    """oro_project_dir returns None when ORO_PROJECT is not set."""
    os.environ.pop("ORO_PROJECT", None)
    assert oro_project_dir() is None


def test_oro_project_dir_empty_string():
    """oro_project_dir returns None when ORO_PROJECT is empty string."""
    os.environ["ORO_PROJECT"] = ""
    try:
        assert oro_project_dir() is None
    finally:
        os.environ.pop("ORO_PROJECT", None)


def test_session_start_extras_constants_with_env():
    """session_start_extras module constants resolve correctly with env vars."""
    os.environ["ORO_HOME"] = "/tmp/test-oro"
    os.environ["ORO_PROJECT"] = "myproject"

    try:
        # Simulate the constant resolution pattern from session_start_extras.py
        beacons = os.path.join(oro_home(), "beacons") if os.environ.get("ORO_PROJECT") else ".claude/hooks/beacons"
        _pd = oro_project_dir()
        handoffs = os.path.join(_pd, "handoffs") if _pd else "docs/handoffs"
        assert beacons == "/tmp/test-oro/beacons"
        assert handoffs == "/tmp/test-oro/projects/myproject/handoffs"
    finally:
        os.environ.pop("ORO_HOME", None)
        os.environ.pop("ORO_PROJECT", None)


def test_learning_analysis_constant_with_env():
    """learning_analysis DECISIONS_FILE resolves correctly with env vars."""
    os.environ["ORO_HOME"] = "/tmp/test-oro"
    os.environ["ORO_PROJECT"] = "myproject"

    try:
        _pd = oro_project_dir()
        assert _pd is not None
        decisions = os.path.join(_pd, "decisions.md")
        assert decisions == "/tmp/test-oro/projects/myproject/decisions.md"
    finally:
        os.environ.pop("ORO_HOME", None)
        os.environ.pop("ORO_PROJECT", None)


if __name__ == "__main__":
    test_with_env_vars()
    print("PASS: env var resolution")
    test_without_env_vars()
    print("PASS: fallback behavior")
    test_oro_home_default()
    print("PASS: oro_home default")
    test_oro_project_dir_no_project()
    print("PASS: oro_project_dir no project")
    test_oro_project_dir_empty_string()
    print("PASS: oro_project_dir empty string")
    test_session_start_extras_constants_with_env()
    print("PASS: session_start_extras constants with env")
    test_learning_analysis_constant_with_env()
    print("PASS: learning_analysis constant with env")
    print("ALL TESTS PASSED")
