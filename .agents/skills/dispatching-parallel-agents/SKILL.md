---
name: dispatching-parallel-agents
description: Use when facing 2 or more independent tasks that can be worked on without shared state or sequential dependencies
---

# Dispatching Parallel Agents

Keep independent work moving concurrently while giving each task one owner. Use Oro's lifecycle instead of reimplementing worktree and Git integration in the coordinating session.

## Preconditions

Parallelize only when tasks have independent file scopes and no unresolved shared design decision. Keep overlapping files or dependency chains sequential.

Before launch, inspect:

```bash
git worktree list
oro task ready
oro task list --status=in_progress
```

Resolve stale claims. Preserve suspected abandoned worktrees until integration is proven or explicit cleanup approval is obtained.

## Launch

Prefer `oro work`, which owns the task lifecycle:

```bash
oro work <task-id> &
```

Use a raw subagent only for untracked research or when Oro is unavailable. Give it one bounded task, an isolated worktree, exact verification commands, and instructions to commit but not integrate its branch.

Never assign two workers to the same files. Keep available slots filled from the highest-priority unblocked tasks.

## Observe

- Trust completion notifications; do not poll or dump full transcripts.
- Use the task record and concise worker result as the durable output.
- If one result changes shared assumptions, pause dependent dispatches and update their context.
- Reduce concurrency after resource exhaustion or repeated timeouts.

## Integrate Results

`oro work` owns integration: it runs tests and review, merges the branch, closes the task, and cleans up its worktree on success.

On success:

1. Confirm the task is closed and the target branch contains the reported commit.
2. Record follow-up work discovered by the worker.
3. Launch the next independent ready task.

On failure, preserve the reported worktree and branch. Read the failure, classify it, and choose the relevant recovery workflow. Do not apply a generic Git cleanup or history-rewrite recipe. Manual integration must follow `finishing-work` and `destructive-command-safety` with the exact branch and target resolved first.

For raw subagents, review their committed branch and use the repository's normal integration workflow. The dispatcher skill does not merge, delete, or rewrite those branches itself.

## Finalize

After the queue drains, run the repository quality gate on the integrated target and push through the normal finishing workflow.

## Red Flags

- Parallel workers touching the same files
- Backfilling work whose assumptions were invalidated
- Treating a failed worker as successfully integrated
- Cleaning up a branch or worktree before integration is proven
- Reimplementing `oro work` lifecycle steps in the coordinator
