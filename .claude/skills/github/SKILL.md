---
name: github
description: Use when interacting with GitHub issues, PRs, CI runs, or the GitHub API via the gh CLI
---

# GitHub

## Overview

Use the `gh` CLI to interact with GitHub. Always specify `--repo owner/repo` when not in a git directory.

## Pull Requests

### Check CI status on a PR

```bash
gh pr checks <pr-number> --repo owner/repo
```

### View PR details

```bash
gh pr view <pr-number> --repo owner/repo
```

### Review PR diff

```bash
gh pr diff <pr-number> --repo owner/repo
```

### Create a PR

```bash
gh pr create --title "title" --body "$(cat <<'EOF'
## Summary
- Change description

## Test plan
- [ ] Tests pass
EOF
)"
```

## Issues

### List open issues

```bash
gh issue list --repo owner/repo
```

### View issue details

```bash
gh issue view <issue-number> --repo owner/repo
```

### Create an issue

```bash
gh issue create --title "title" --body "description" --repo owner/repo
```

## CI / Workflow Runs

### List recent workflow runs

```bash
gh run list --repo owner/repo --limit 10
```

### View a run (see which steps failed)

```bash
gh run view <run-id> --repo owner/repo
```

### View logs for failed steps only

```bash
gh run view <run-id> --repo owner/repo --log-failed
```

## API for Advanced Queries

The `gh api` command accesses data not available through other subcommands:

```bash
# Get PR with specific fields
gh api repos/owner/repo/pulls/55 --jq '.title, .state, .user.login'

# Get PR comments
gh api repos/owner/repo/pulls/55/comments

# Get PR review comments
gh api repos/owner/repo/pulls/55/reviews
```

## JSON Output

Most commands support `--json` for structured output with `--jq` filtering:

```bash
gh issue list --repo owner/repo --json number,title --jq '.[] | "\(.number): \(.title)"'
gh pr list --json number,title,state --jq '.[] | "\(.number): \(.title) [\(.state)]"'
```

## Tips

- Use `gh auth status` to verify authentication
- Prefer `gh pr view`/`gh pr diff` over checking out PR branches for reviews
- Use `--jq` for precise field extraction from JSON output
- `gh api` supports pagination with `--paginate`

## Red Flags

- Running `gh pr merge` without user confirmation
- Closing issues/PRs without explicit instruction
- Posting comments without user review
