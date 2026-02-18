#!/usr/bin/env python3
"""Audit closed beads: verify AC-specified tests exist on main and commits are reachable."""

import json
import re
import subprocess
import sys

REPO = "/Users/as21/codehouse/oro"
EXCLUDE = ["--exclude-dir=.worktrees", "--exclude-dir=archive"]


def get_closed_beads():
    result = subprocess.run(
        ["bd", "list", "--status=closed", "--json", "--limit", "0"],
        capture_output=True,
        text=True,
        check=True,
    )
    return json.loads(result.stdout)


def extract_test_names(text):
    """Extract Go test function names from text."""
    return list(set(re.findall(r"Test[A-Z][A-Za-z0-9_]+", text)))


def extract_commit_hashes(text):
    """Extract commit hashes (7-40 hex chars, word-bounded)."""
    return list(set(re.findall(r"\b([0-9a-f]{7,40})\b", text)))


def test_exists_on_main(name):
    """Check if a Go test function exists in the repo (excluding worktrees)."""
    r = subprocess.run(
        ["grep", "-r", f"func {name}", REPO, "--include=*.go", "-l", *EXCLUDE],
        capture_output=True,
        text=True,
    )
    return r.stdout.strip()


def commit_on_main(sha):
    """Check if a commit exists and is ancestor of HEAD (main)."""
    r1 = subprocess.run(
        ["git", "-C", REPO, "cat-file", "-t", sha],
        capture_output=True,
        text=True,
    )
    if r1.returncode != 0:
        return False, "NOT_FOUND"
    r2 = subprocess.run(
        ["git", "-C", REPO, "merge-base", "--is-ancestor", sha, "HEAD"],
        capture_output=True,
        text=True,
    )
    if r2.returncode != 0:
        return False, "NOT_ON_MAIN"
    return True, "OK"


def audit_bead(bead):
    """Audit a single bead, return verdict dict."""
    bid = bead["id"]
    title = bead.get("title", "")
    ac = bead.get("acceptance_criteria") or ""
    desc = bead.get("description") or ""
    close_reason = bead.get("close_reason") or ""
    combined = f"{ac}\n{desc}\n{close_reason}"

    test_names = extract_test_names(combined)
    commit_hashes = extract_commit_hashes(close_reason)

    missing_tests = []
    found_tests = []
    for name in test_names:
        loc = test_exists_on_main(name)
        if loc:
            found_tests.append(name)
        else:
            missing_tests.append(name)

    missing_commits = []
    found_commits = []
    for sha in commit_hashes:
        ok, reason = commit_on_main(sha)
        if ok:
            found_commits.append(sha)
        else:
            missing_commits.append((sha, reason))

    # Determine verdict
    if test_names and missing_tests:
        verdict = "FAIL"
    elif not test_names and not commit_hashes:
        verdict = "NO-EVIDENCE"
    elif not test_names and commit_hashes and not missing_commits:
        verdict = "PASS-COMMIT"
    elif not test_names and commit_hashes and missing_commits:
        verdict = "FAIL-COMMIT"
    elif test_names and not missing_tests:
        verdict = "PASS"
    else:
        verdict = "UNKNOWN"

    return {
        "id": bid,
        "title": title[:60],
        "verdict": verdict,
        "missing_tests": missing_tests,
        "found_tests": found_tests,
        "missing_commits": missing_commits,
        "found_commits": found_commits,
        "close_reason": close_reason[:100],
        "issue_type": bead.get("issue_type", ""),
    }


def main():
    beads = get_closed_beads()
    print(f"Auditing {len(beads)} closed beads...\n")

    results = []
    for i, b in enumerate(beads):
        r = audit_bead(b)
        results.append(r)
        if (i + 1) % 50 == 0:
            print(f"  ...processed {i + 1}/{len(beads)}", file=sys.stderr)

    # Group by verdict
    by_verdict = {}
    for r in results:
        by_verdict.setdefault(r["verdict"], []).append(r)

    # Print summary
    print("=" * 80)
    print("AUDIT SUMMARY")
    print("=" * 80)
    for v in ["PASS", "PASS-COMMIT", "NO-EVIDENCE", "FAIL", "FAIL-COMMIT", "UNKNOWN"]:
        items = by_verdict.get(v, [])
        print(f"  {v:<15} {len(items):>4}")
    print()

    # Print FAIL details
    fails = by_verdict.get("FAIL", [])
    if fails:
        print("=" * 80)
        print(f"FAIL — {len(fails)} beads with missing tests")
        print("=" * 80)
        for r in fails:
            print(f"\n  {r['id']} — {r['title']}")
            print(f"    Missing: {', '.join(r['missing_tests'])}")
            if r["found_tests"]:
                print(f"    Found:   {', '.join(r['found_tests'])}")
            print(f"    Close:   {r['close_reason']}")

    # Print FAIL-COMMIT details
    fail_commits = by_verdict.get("FAIL-COMMIT", [])
    if fail_commits:
        print()
        print("=" * 80)
        print(f"FAIL-COMMIT — {len(fail_commits)} beads with unreachable commits")
        print("=" * 80)
        for r in fail_commits:
            print(f"\n  {r['id']} — {r['title']}")
            for sha, reason in r["missing_commits"]:
                print(f"    Commit {sha[:10]}... — {reason}")
            print(f"    Close:   {r['close_reason']}")

    # Print NO-EVIDENCE summary (just IDs, not full detail)
    no_ev = by_verdict.get("NO-EVIDENCE", [])
    if no_ev:
        print()
        print("=" * 80)
        print(f"NO-EVIDENCE — {len(no_ev)} beads (no test names or commits in close_reason)")
        print("=" * 80)
        # Filter to non-epics (epics don't need direct evidence)
        non_epic = [r for r in no_ev if r["issue_type"] != "epic"]
        epics = [r for r in no_ev if r["issue_type"] == "epic"]
        if epics:
            print(f"  ({len(epics)} epics — skipped, epics close when children close)")
        for r in non_epic:
            print(f"  {r['id']:<15} {r['title']}")
            if r["close_reason"]:
                print(f"    {'':15} Close: {r['close_reason']}")

    # JSON output for machine consumption
    json_out = {
        "total": len(beads),
        "pass": len(by_verdict.get("PASS", [])),
        "pass_commit": len(by_verdict.get("PASS-COMMIT", [])),
        "no_evidence": len(by_verdict.get("NO-EVIDENCE", [])),
        "fail": len(fails),
        "fail_commit": len(fail_commits),
        "fail_details": fails,
        "fail_commit_details": fail_commits,
        "no_evidence_non_epic": [r for r in no_ev if r["issue_type"] != "epic"],
    }
    with open("/tmp/bead_audit_results.json", "w") as f:
        json.dump(json_out, f, indent=2, default=str)
    print("\nJSON results written to /tmp/bead_audit_results.json")


if __name__ == "__main__":
    main()
