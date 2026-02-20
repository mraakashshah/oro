"""Tests for learning_analysis.py — frequency analysis library for knowledge.jsonl."""

import json
import tempfile
from pathlib import Path

from learning_analysis import (
    content_similarity,
    cross_reference_docs,
    filter_by_bead,
    filter_by_session,
    frequency_level,
    load_knowledge,
    tag_frequency,
)

# --- Fixtures / helpers ---


def _write_jsonl(path: Path, entries: list[dict]) -> None:
    """Write entries as JSONL to a file."""
    with open(path, "w") as f:
        for entry in entries:
            f.write(json.dumps(entry) + "\n")


SAMPLE_ENTRIES = [
    {
        "key": "learned-use-absolute-paths",
        "type": "learned",
        "content": "Use absolute paths when working with worktrees",
        "bead": "oro-abc",
        "tags": ["git", "cli"],
        "ts": "2026-02-08T10:00:00+00:00",
    },
    {
        "key": "learned-pytest-fixtures",
        "type": "learned",
        "content": "Pytest fixtures are preferred over setup/teardown methods",
        "bead": "oro-abc",
        "tags": ["python", "pytest", "test"],
        "ts": "2026-02-08T11:00:00+00:00",
    },
    {
        "key": "learned-docker-layer-caching",
        "type": "learned",
        "content": "Docker layer caching speeds up builds significantly",
        "bead": "oro-def",
        "tags": ["docker", "performance"],
        "ts": "2026-02-09T12:00:00+00:00",
    },
    {
        "key": "learned-use-absolute-paths",  # Duplicate key — should keep this (latest)
        "type": "learned",
        "content": "Always use absolute paths, never cd into worktrees",
        "bead": "oro-ghi",
        "tags": ["git", "cli"],
        "ts": "2026-02-09T14:00:00+00:00",
    },
]


# --- Tests ---


def test_load_knowledge():
    """load_knowledge reads JSONL and deduplicates by key (latest wins)."""
    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as f:
        for entry in SAMPLE_ENTRIES:
            f.write(json.dumps(entry) + "\n")
        tmp_path = Path(f.name)

    try:
        entries = load_knowledge(tmp_path)
        # 4 lines but 3 unique keys (one duplicate)
        assert len(entries) == 3
        # The duplicate key should have the latest content
        abs_entry = [e for e in entries if e["key"] == "learned-use-absolute-paths"]
        assert len(abs_entry) == 1
        assert "never cd into worktrees" in abs_entry[0]["content"]
    finally:
        tmp_path.unlink()


def test_load_knowledge_missing_file():
    """load_knowledge returns empty list for missing file."""
    entries = load_knowledge(Path("/nonexistent/knowledge.jsonl"))
    assert entries == []


def test_filter_by_bead():
    """filter_by_bead returns only entries matching the given bead_id."""
    entries = [
        {"key": "a", "bead": "oro-abc", "content": "aaa"},
        {"key": "b", "bead": "oro-def", "content": "bbb"},
        {"key": "c", "bead": "oro-abc", "content": "ccc"},
    ]
    result = filter_by_bead(entries, "oro-abc")
    assert len(result) == 2
    assert all(e["bead"] == "oro-abc" for e in result)


def test_filter_by_bead_no_match():
    """filter_by_bead returns empty list when no entries match."""
    entries = [{"key": "a", "bead": "oro-abc", "content": "aaa"}]
    assert filter_by_bead(entries, "oro-zzz") == []


def test_filter_by_session():
    """filter_by_session returns entries with ts >= since_ts."""
    entries = [
        {"key": "a", "ts": "2026-02-08T10:00:00+00:00"},
        {"key": "b", "ts": "2026-02-09T12:00:00+00:00"},
        {"key": "c", "ts": "2026-02-09T14:00:00+00:00"},
    ]
    result = filter_by_session(entries, "2026-02-09T00:00:00+00:00")
    assert len(result) == 2
    assert result[0]["key"] == "b"
    assert result[1]["key"] == "c"


def test_filter_by_session_none_match():
    """filter_by_session returns empty list when all entries are older."""
    entries = [{"key": "a", "ts": "2026-02-07T10:00:00+00:00"}]
    assert filter_by_session(entries, "2026-02-09T00:00:00+00:00") == []


def test_tag_frequency():
    """tag_frequency counts tag occurrences across entries."""
    entries = [
        {"tags": ["git", "cli"]},
        {"tags": ["python", "pytest", "test"]},
        {"tags": ["git", "performance"]},
    ]
    freq = tag_frequency(entries)
    assert freq["git"] == 2
    assert freq["cli"] == 1
    assert freq["python"] == 1
    assert freq["pytest"] == 1
    assert freq["test"] == 1
    assert freq["performance"] == 1


def test_tag_frequency_empty():
    """tag_frequency returns empty dict for empty entries."""
    assert tag_frequency([]) == {}


def test_content_similarity():
    """content_similarity clusters near-duplicates using Jaccard > 0.5."""
    entries = [
        {"key": "a", "content": "use absolute paths for worktree commands"},
        {"key": "b", "content": "always use absolute paths for worktree operations"},
        {"key": "c", "content": "docker layer caching speeds up builds"},
    ]
    clusters = content_similarity(entries)
    # a and b should be in the same cluster (high Jaccard overlap)
    # c should be in its own cluster
    assert len(clusters) == 2
    # Find the cluster containing "a"
    cluster_with_a = [c for c in clusters if "a" in c]
    assert len(cluster_with_a) == 1
    assert "b" in cluster_with_a[0]
    # c is alone
    cluster_with_c = [c for c in clusters if "c" in c]
    assert len(cluster_with_c) == 1
    assert cluster_with_c[0] == ["c"]


def test_content_similarity_no_duplicates():
    """content_similarity returns each entry in its own cluster when no overlap."""
    entries = [
        {"key": "a", "content": "apples oranges bananas"},
        {"key": "b", "content": "kubernetes docker containers"},
    ]
    clusters = content_similarity(entries)
    assert len(clusters) == 2


def test_frequency_level():
    """frequency_level maps 1=note, 2=consider, 3+=create."""
    assert frequency_level(1) == "note"
    assert frequency_level(2) == "consider"
    assert frequency_level(3) == "create"
    assert frequency_level(5) == "create"
    assert frequency_level(100) == "create"


def test_frequency_level_zero():
    """frequency_level treats 0 or negative as note."""
    assert frequency_level(0) == "note"
    assert frequency_level(-1) == "note"


def test_cross_reference_docs():
    """cross_reference_docs identifies entries NOT in discoveries doc."""
    entries = [
        {"key": "a", "content": "Use absolute paths for worktree commands"},
        {"key": "b", "content": "Docker layer caching speeds up builds"},
        {"key": "c", "content": "Pytest fixtures preferred over setup methods"},
    ]
    with tempfile.NamedTemporaryFile(mode="w", suffix=".md", delete=False) as f:
        f.write("# Decisions\n\n")
        f.write("## 2026-02-08: Worktree paths\n")
        f.write("Use absolute paths for worktree commands\n\n")
        f.write("## 2026-02-09: Docker caching\n")
        f.write("Docker layer caching speeds up builds\n")
        doc_path = Path(f.name)

    try:
        undocumented = cross_reference_docs(entries, doc_path)
        # Only "c" should be undocumented
        assert len(undocumented) == 1
        assert undocumented[0]["key"] == "c"
    finally:
        doc_path.unlink()


def test_cross_reference_docs_missing_file():
    """cross_reference_docs returns all entries if doc file is missing."""
    entries = [{"key": "a", "content": "something"}]
    result = cross_reference_docs(entries, Path("/nonexistent/doc.md"))
    assert len(result) == 1
