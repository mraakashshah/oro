"""Frequency analysis library for knowledge.jsonl.

Provides functions to load, filter, analyze, and cross-reference
learnings stored in the knowledge JSONL format.

Entry format: {key, type, content, bead, tags, ts}
"""

import json
import os
from collections import Counter
from pathlib import Path

from memory_capture import slugify


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


_project_dir = oro_project_dir()
DECISIONS_FILE = (
    os.path.join(_project_dir, "decisions&discoveries.md") if _project_dir else "docs/decisions&discoveries.md"
)


def load_knowledge(path: Path) -> list[dict]:
    """Read knowledge.jsonl, deduplicate by key (latest wins).

    Returns a list of unique entries, ordered by first appearance of each key.
    If a key appears multiple times, the last occurrence's data is used.
    """
    try:
        with open(path) as f:
            lines = f.readlines()
    except OSError:
        return []

    by_key: dict[str, dict] = {}
    for line in lines:
        line = line.strip()
        if not line:
            continue
        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue
        by_key[entry["key"]] = entry

    return list(by_key.values())


def filter_by_bead(entries: list[dict], bead_id: str) -> list[dict]:
    """Filter entries by bead field."""
    return [e for e in entries if e.get("bead") == bead_id]


def filter_by_session(entries: list[dict], since_ts: str) -> list[dict]:
    """Filter entries with ts >= since_ts.

    Uses lexicographic ISO-8601 string comparison (works for UTC offsets).
    """
    return [e for e in entries if e.get("ts", "") >= since_ts]


def tag_frequency(entries: list[dict]) -> dict[str, int]:
    """Count tag occurrences across entries, return dict[str, int]."""
    counter: Counter[str] = Counter()
    for entry in entries:
        for tag in entry.get("tags", []):
            counter[tag] += 1
    return dict(counter)


def content_similarity(entries: list[dict], threshold: float = 0.5) -> list[list[str]]:
    """Cluster near-duplicates using Jaccard similarity on slugify tokens.

    Returns a list of clusters, where each cluster is a list of entry keys.
    Entries with Jaccard similarity > threshold are placed in the same cluster.
    Uses single-linkage clustering: if any member of a cluster is similar
    to a new entry, the entry joins that cluster.
    """
    if not entries:
        return []

    # Precompute token sets for each entry
    token_sets: list[tuple[str, set[str]]] = []
    for entry in entries:
        key = entry["key"]
        slug = slugify(entry.get("content", ""))
        tokens = set(slug.split("-")) - {""}
        token_sets.append((key, tokens))

    # Build clusters via single-linkage
    clusters: list[list[str]] = []
    cluster_tokens: list[set[str]] = []

    for key, tokens in token_sets:
        merged_into = None
        for i, ct in enumerate(cluster_tokens):
            # Jaccard similarity
            if not tokens and not ct:
                similarity = 1.0
            elif not tokens or not ct:
                similarity = 0.0
            else:
                intersection = len(tokens & ct)
                union = len(tokens | ct)
                similarity = intersection / union

            if similarity > threshold:
                clusters[i].append(key)
                cluster_tokens[i] = ct | tokens
                merged_into = i
                break

        if merged_into is None:
            clusters.append([key])
            cluster_tokens.append(tokens)

    return clusters


def frequency_level(count: int) -> str:
    """Map frequency count to action level.

    1 (or less) = note
    2 = consider
    3+ = create
    """
    if count <= 1:
        return "note"
    if count == 2:
        return "consider"
    return "create"


def cross_reference_docs(entries: list[dict], discoveries_path: Path) -> list[dict]:
    """Check which learnings are NOT in the decisions&discoveries doc.

    Uses simple substring match on content against the full document text.
    Returns entries whose content is not found in the document.
    If the document doesn't exist, all entries are considered undocumented.
    """
    try:
        doc_text = discoveries_path.read_text()
    except OSError:
        return list(entries)

    return [e for e in entries if e.get("content", "") not in doc_text]
