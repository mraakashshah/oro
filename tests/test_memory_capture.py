"""Tests for the LEARNED memory capture PostToolUse hook."""

import importlib.util
import json
import os
from pathlib import Path

# Load the hook module from ORO_HOME/hooks/ (externalized config)
_oro_home = Path(os.environ.get("ORO_HOME", Path.home() / ".oro"))
_spec = importlib.util.spec_from_file_location(
    "memory_capture",
    _oro_home / "hooks" / "memory_capture.py",
)
_mod = importlib.util.module_from_spec(_spec)  # type: ignore[arg-type]
_spec.loader.exec_module(_mod)  # type: ignore[union-attr]

extract_learned = _mod.extract_learned
auto_tag = _mod.auto_tag
slugify = _mod.slugify
append_entry = _mod.append_entry
recall = _mod.recall


class TestExtractLearned:
    def test_extracts_learned_prefix(self):
        cmd = 'bd comment oro-abc "LEARNED: TaskGroup requires @Sendable closures in Swift 6"'
        bead_id, content = extract_learned(cmd)
        assert bead_id == "oro-abc"
        assert content == "TaskGroup requires @Sendable closures in Swift 6"

    def test_returns_none_for_non_learned(self):
        cmd = 'bd comment oro-abc "Fixed the build issue"'
        result = extract_learned(cmd)
        assert result is None

    def test_returns_none_for_non_bd_command(self):
        cmd = "git status"
        result = extract_learned(cmd)
        assert result is None

    def test_handles_single_quotes(self):
        cmd = "bd comment oro-xyz 'LEARNED: Use uv sync instead of pip install'"
        bead_id, content = extract_learned(cmd)
        assert bead_id == "oro-xyz"
        assert content == "Use uv sync instead of pip install"

    def test_handles_no_quotes(self):
        cmd = "bd comment oro-123 LEARNED: simple note"
        bead_id, content = extract_learned(cmd)
        assert bead_id == "oro-123"
        assert content == "simple note"

    def test_truncates_long_content(self):
        long_text = "x" * 3000
        cmd = f'bd comment oro-abc "LEARNED: {long_text}"'
        _, content = extract_learned(cmd)
        assert len(content) <= 2048


class TestAutoTag:
    def test_detects_python_tags(self):
        tags = auto_tag("Use pytest fixtures instead of unittest classes")
        assert "pytest" in tags

    def test_detects_go_tags(self):
        tags = auto_tag("SQLite WAL mode in Go with concurrent access")
        assert "sqlite" in tags
        assert "go" in tags

    def test_detects_multiple_tags(self):
        tags = auto_tag("React hooks with TypeScript generics and async fetch")
        assert "react" in tags
        assert "typescript" in tags
        assert "async" in tags

    def test_empty_content_returns_empty(self):
        tags = auto_tag("")
        assert tags == []

    def test_case_insensitive(self):
        tags = auto_tag("PYTEST fixtures are great")
        assert "pytest" in tags


class TestSlugify:
    def test_basic_slugify(self):
        slug = slugify("TaskGroup requires @Sendable closures")
        assert slug == "taskgroup-requires-sendable-closures"

    def test_strips_special_chars(self):
        slug = slugify("Use `uv sync` instead of pip!")
        assert "`" not in slug
        assert "!" not in slug


class TestAppendEntry:
    def test_creates_file_and_writes_entry(self, tmp_path):
        knowledge_file = tmp_path / "knowledge.jsonl"
        append_entry(
            knowledge_file=str(knowledge_file),
            bead_id="oro-abc",
            content="TaskGroup requires @Sendable closures",
            tags=["swift", "async"],
        )
        lines = knowledge_file.read_text().strip().split("\n")
        assert len(lines) == 1
        entry = json.loads(lines[0])
        assert entry["content"] == "TaskGroup requires @Sendable closures"
        assert entry["bead"] == "oro-abc"
        assert entry["tags"] == ["swift", "async"]
        assert entry["type"] == "learned"
        assert "ts" in entry
        assert "key" in entry

    def test_appends_multiple_entries(self, tmp_path):
        knowledge_file = tmp_path / "knowledge.jsonl"
        append_entry(str(knowledge_file), "oro-1", "First insight", ["go"])
        append_entry(str(knowledge_file), "oro-2", "Second insight", ["python"])
        lines = knowledge_file.read_text().strip().split("\n")
        assert len(lines) == 2

    def test_creates_parent_directory(self, tmp_path):
        knowledge_file = tmp_path / "memory" / "knowledge.jsonl"
        append_entry(str(knowledge_file), "oro-1", "Test", [])
        assert knowledge_file.exists()

    def test_dedup_key_is_deterministic(self, tmp_path):
        knowledge_file = tmp_path / "knowledge.jsonl"
        append_entry(str(knowledge_file), "oro-1", "Same content", [])
        append_entry(str(knowledge_file), "oro-2", "Same content", [])
        lines = knowledge_file.read_text().strip().split("\n")
        e1 = json.loads(lines[0])
        e2 = json.loads(lines[1])
        assert e1["key"] == e2["key"]  # same content → same key


class TestRecall:
    def _write_entries(self, path, entries):
        with open(path, "w") as f:
            for e in entries:
                f.write(json.dumps(e) + "\n")

    def test_search_by_keyword(self, tmp_path):
        knowledge_file = tmp_path / "knowledge.jsonl"
        self._write_entries(
            str(knowledge_file),
            [
                {
                    "key": "a",
                    "content": "SQLite WAL mode is fast",
                    "tags": ["sqlite"],
                    "ts": "2026-01-01T00:00:00",
                    "bead": "oro-1",
                    "type": "learned",
                },
                {
                    "key": "b",
                    "content": "React hooks are composable",
                    "tags": ["react"],
                    "ts": "2026-01-02T00:00:00",
                    "bead": "oro-2",
                    "type": "learned",
                },
            ],
        )
        results = recall(str(knowledge_file), "sqlite")
        assert len(results) == 1
        assert results[0]["key"] == "a"

    def test_dedup_keeps_latest(self, tmp_path):
        knowledge_file = tmp_path / "knowledge.jsonl"
        self._write_entries(
            str(knowledge_file),
            [
                {
                    "key": "same",
                    "content": "Old version",
                    "tags": [],
                    "ts": "2026-01-01T00:00:00",
                    "bead": "oro-1",
                    "type": "learned",
                },
                {
                    "key": "same",
                    "content": "New version",
                    "tags": [],
                    "ts": "2026-01-02T00:00:00",
                    "bead": "oro-2",
                    "type": "learned",
                },
            ],
        )
        results = recall(str(knowledge_file), "version")
        assert len(results) == 1
        assert results[0]["content"] == "New version"

    def test_empty_file_returns_empty(self, tmp_path):
        knowledge_file = tmp_path / "knowledge.jsonl"
        knowledge_file.write_text("")
        results = recall(str(knowledge_file), "anything")
        assert results == []

    def test_missing_file_returns_empty(self):
        results = recall("/nonexistent/path.jsonl", "anything")
        assert results == []

    def test_search_matches_tags_and_content(self, tmp_path):
        knowledge_file = tmp_path / "knowledge.jsonl"
        self._write_entries(
            str(knowledge_file),
            [
                {
                    "key": "a",
                    "content": "Use fixtures",
                    "tags": ["pytest"],
                    "ts": "2026-01-01T00:00:00",
                    "bead": "oro-1",
                    "type": "learned",
                },
            ],
        )
        # Search by tag
        results = recall(str(knowledge_file), "pytest")
        assert len(results) == 1
        # Search by content
        results = recall(str(knowledge_file), "fixtures")
        assert len(results) == 1
