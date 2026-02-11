---
name: bead-craft
description: Use when decomposing a spec, design, or feature description into a bead dependency graph with self-evaluating acceptance criteria
---

# Bead Craft

## Overview

Unified skill for bead quality. Defines bead anatomy, quality standards, and size heuristics ONCE, serving three modes:

- **Decompose**: Spec/design → bead dependency graph (replaces spec-to-beads)
- **Create**: Ad-hoc single bead with full quality applied
- **Review**: Audit existing beads, flag smells

**Core principle:** Every bead must answer "how do I know this is done?" with a test command. Every bead passes the Rule of Five before being emitted.

---

## Bead Quality Core

All three modes apply this shared quality standard.

### Bead Anatomy — Required Fields

Every bead must include:

```
Test: path:FnName | Cmd: test_cmd | Assert: expected
Read: file1.go:Symbol1, file2.go:Symbol2
Signature: func Name(ctx context.Context, arg Type) (Result, error)
Edges: nil input → ErrInvalid; timeout → context.DeadlineExceeded
```

| Field | Purpose | Required? |
|-------|---------|-----------|
| `Test:` | Test file path and function name | Always |
| `Cmd:` | Command to run verification | Always |
| `Assert:` | What "pass" looks like | Always |
| `Read:` | Exact files and symbols the worker must read | Always |
| `Signature:` | Function/method signatures being implemented | When adding functions |
| `Edges:` | Error conditions and boundary behavior | When non-trivial |

### Rule of Five — Critique Loop

Run 5 passes on every bead before emitting. Each pass asks one question:

| Pass | Name | Question |
|------|------|----------|
| P1 | **Zero Ambiguity** | Are signatures, types, and error conditions exact? No "should handle errors appropriately." |
| P2 | **TDD Inputs/Outputs** | Does acceptance have concrete test data — specific inputs and expected outputs — not vague assertions? |
| P3 | **Context Minimization** | Does `Read:` list the exact files and symbols the worker must look at? Nothing more, nothing less? |
| P4 | **Boundary Check** | If this crosses IPC/RPC/package boundaries, are protocol details and both sides specified? |
| P5 | **Adversarial** | What would a zero-context worker misunderstand? Fix it. |

If any pass fails, revise the bead and re-run from P1.

### Size Heuristics

A bead is too large if ANY of these apply:

- Estimate >7 minutes
- Needs >1 test file or >4 source files
- Title contains "and" (doing two things)
- Acceptance needs multiple unrelated assertions

A bead is small enough when ALL of these apply:

- One test function in acceptance criteria
- <=7 min estimate
- 1-3 source files
- Single-purpose title

### Smell Catalog

Used by Review mode, but also as a checklist for Decompose and Create:

| Smell | Description |
|-------|-------------|
| No acceptance | Bead has no `Test:` / `Cmd:` / `Assert:` |
| Vague title | "improve X", "handle edge cases", "clean up" |
| Missing `Read:` | Worker doesn't know what files to look at |
| Stale in_progress | >3 days in `in_progress` without activity |
| Missing estimate | No time estimate attached |
| Stale dependency | Depends on a closed bead (dep should be removed) |
| Oversized | Fails size heuristics but hasn't been decomposed |
| Missing `Edges:` | Non-trivial logic with no error conditions specified |

---

## Mode: Decompose

Takes a spec (markdown doc, user description, or design output from `brainstorming`) and decomposes it into a bead dependency graph.

### Step 1: Parse Spec

Extract from the spec:
- **Goal** — what the feature does
- **Components** — distinct modules/packages/files
- **Seams** — boundaries between components (APIs, interfaces, data contracts)
- **Constraints** — tech choices, performance requirements, compatibility

If spec is vague: ask the user to clarify before decomposing. Don't guess.

### Step 2: Create Epic Bead

```bash
bd create "<feature name>" --type epic \
  --acceptance "All child beads closed. Full quality gate passes." \
  --description "<goal from spec>"
```

### Step 3: Decompose to Task Beads

For each seam/component, create a task bead. Apply the full Bead Anatomy format:

```bash
bd create "<specific task>" \
  --parent <epic-id> \
  --type task \
  --acceptance "Test: <path>:<FnName> | Cmd: <test_cmd> | Assert: <expected>
Read: <file1>:<Symbol1>, <file2>:<Symbol2>
Signature: <func signature if applicable>
Edges: <error conditions if applicable>" \
  --estimate <minutes>
```

### Step 4: Rule of Five (Apply to Each Bead)

Run all 5 passes (P1-P5) on every bead before emitting. Revise until all pass.

### Step 5: Size Test (Recursive)

Check every task bead against size heuristics. If too large, decompose:

1. Promote: `bd update <id> --type epic`
2. Create child tasks with `--parent <id>`
3. Re-apply size test + Rule of Five to children

### Step 6: Wire Dependencies

```bash
bd dep add <later-bead> <earlier-bead>
```

Common patterns:
- Types/interfaces before implementations
- Core logic before integration
- Setup/config before features
- Lower layers before higher layers

### Step 7: Present Tree

```bash
bd show <epic-id>
```

Present the full tree: epic → children, dependencies, estimates, acceptance criteria.

Ask: "Does this decomposition look right? Any beads to add, remove, or re-scope?"

Wait for user confirmation before proceeding to execution.

### Example Decomposition

Spec: "Add JWT authentication to the API"

```
Epic: Implement JWT authentication (bd-001)
├── Task: Define auth types and interfaces (bd-002, 5min)
│   Test: internal/auth/types_test.go:TestTokenClaims | Cmd: go test ./internal/auth/... | Assert: Claims struct has required fields
│   Read: internal/auth/types.go:TokenClaims
│   Signature: type TokenClaims struct { Sub string; Exp time.Time; Iss string }
├── Task: Implement token generation (bd-003, 7min, depends: bd-002)
│   Test: internal/auth/token_test.go:TestGenerateToken | Cmd: go test ./internal/auth/... -run TestGenerateToken | Assert: returns signed JWT with correct claims
│   Read: internal/auth/token.go:GenerateToken, internal/auth/types.go:TokenClaims
│   Signature: func GenerateToken(claims TokenClaims, secret []byte) (string, error)
│   Edges: nil secret → ErrNoSecret; expired claims → ErrExpiredClaims
├── Task: Implement token validation (bd-004, 7min, depends: bd-002)
│   Test: internal/auth/token_test.go:TestValidateToken | Cmd: go test ./internal/auth/... -run TestValidateToken | Assert: validates signature, expiry, issuer
│   Read: internal/auth/token.go:ValidateToken
│   Signature: func ValidateToken(tokenStr string, secret []byte) (*TokenClaims, error)
│   Edges: invalid signature → ErrInvalidSignature; expired → ErrExpired
├── Task: Add auth middleware (bd-005, 7min, depends: bd-003, bd-004)
│   Test: internal/middleware/auth_test.go:TestAuthMiddleware | Cmd: go test ./internal/middleware/... | Assert: rejects invalid tokens with 401, passes valid tokens
│   Read: internal/middleware/auth.go:AuthMiddleware, internal/auth/token.go:ValidateToken
│   Signature: func AuthMiddleware(secret []byte) func(http.Handler) http.Handler
└── Task: Wire middleware to routes (bd-006, 5min, depends: bd-005)
    Test: internal/api/routes_test.go:TestProtectedRoutes | Cmd: go test ./internal/api/... | Assert: protected endpoints require valid JWT
    Read: internal/api/routes.go:SetupRoutes
```

---

## Mode: Create

For creating a single ad-hoc bead with full quality applied.

### Step 1: Gather Context

From the user request, extract:
- What needs to be done (title)
- Where in the codebase (files/packages)
- What type (task, bug, feature)
- Priority

### Step 2: Draft Bead

Write the full Bead Anatomy:
- Title (single-purpose, imperative)
- Acceptance with all fields (Test, Cmd, Assert, Read, Signature, Edges)
- Estimate
- Dependencies (check `bd list` for related work)

### Step 3: Rule of Five

Run all 5 passes. Revise until clean.

### Step 4: Size Check

If too large → switch to Decompose mode.

### Step 5: Create

```bash
bd create "<title>" --type <type> --priority <0-4> \
  --acceptance "<full anatomy>" \
  --estimate <minutes>
```

Wire dependencies if needed:
```bash
bd dep add <this-bead> <depends-on>
```

---

## Mode: Review

Audit existing beads for quality issues.

### Step 1: Gather Beads

```bash
bd list --status=open
bd list --status=in_progress
```

### Step 2: Audit Each Bead

For each bead, check against:

1. **Smell Catalog** — flag any matching smells
2. **Bead Anatomy** — are required fields present?
3. **Rule of Five** — would it pass all 5 passes?
4. **Size Heuristics** — is it too large?

### Step 3: Report

Present findings grouped by severity:

**Critical** (blocks execution):
- No acceptance criteria
- Oversized without decomposition

**Warning** (reduces quality):
- Missing `Read:` field
- Vague title
- Missing estimate

**Info** (housekeeping):
- Stale in_progress
- Stale dependencies

### Step 4: Fix

For each finding, suggest the fix command:

```bash
# Add missing acceptance
bd update <id> --acceptance "Test: ... | Cmd: ... | Assert: ..."

# Decompose oversized bead
bd update <id> --type epic
bd create "<child1>" --parent <id> ...

# Clean stale dep
bd dep remove <id> <stale-dep>
```

---

## Red Flags

- Creating beads without running Rule of Five
- Acceptance criteria that can't be verified by a command
- Beads with estimates >7min that haven't been decomposed
- Missing dependencies (later bead assumes earlier bead's output)
- Proceeding to execution without user confirmation of the tree (Decompose mode)
- Vague titles like "implement feature" or "handle edge cases"
- Skipping `Read:` field — workers waste time exploring
- Emitting beads after only 1-2 Rule of Five passes
