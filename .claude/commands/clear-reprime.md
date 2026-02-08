# Clear and Reprime Context

You just started a fresh session after /clear. Recover full project context now.

## Steps (do all of these, in order)

1. Run `bd prime --full` and internalize the output
2. Find and read the latest file in `docs/handoffs/` (sort by name, take last)
3. Run `bd ready` to see available work
4. Run `git status --short` and `git log --oneline -5` to understand current state
5. Read `current.md` if it exists in the project root
6. Read any referenced plans or specs from the handoff

## Then present a summary

```
**Recovered context from [handoff date]:**
- Completed: [what's done]
- In progress: [what's active]
- Ready work: [from bd ready]
- Git state: [clean/dirty, branch, unpushed commits]

**Recommended next actions:**
1. [highest priority]
2. [second priority]

Shall I proceed?
```

## Rules
- Do NOT skip any step — each one provides critical context
- Read files directly, don't delegate to agents
- If current.md exists, its contents take priority over handoff for "what to do next"
- After presenting the summary, wait for user confirmation before starting work
