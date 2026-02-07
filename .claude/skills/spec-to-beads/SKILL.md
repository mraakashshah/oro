---
name: spec-to-beads
description: Use when decomposing a spec, design, or feature description into a bead dependency graph with self-evaluating acceptance criteria
---

# Spec to Beads

## Overview

Takes a spec (markdown doc, user description, or design output from `brainstorming`) and decomposes it into a bead dependency graph where every bead has concrete, self-evaluating acceptance criteria.

**Core principle:** Every bead must answer "how do I know this is done?" with a test command.

## Steps

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

For each seam/component, create a task bead:

```bash
bd create "<specific task>" \
  --parent <epic-id> \
  --type task \
  --acceptance "Test: <path>:<FnName> | Cmd: <test_cmd> | Assert: <expected>" \
  --estimate <minutes>
```

**Acceptance criteria format:**

```
Test: <path>:<FnName> | Cmd: <test_cmd> | Assert: <expected>
```

| Field | Example |
|-------|---------|
| `Test:` | `internal/auth/auth_test.go:TestValidateToken` |
| `Cmd:` | `go test ./internal/auth/... -run TestValidateToken -v` |
| `Assert:` | `returns valid=true for unexpired JWT` |

### Step 4: Apply Size Test (Recursive)

Check every task bead against size heuristics. If too large, decompose into children.

**Too large (recurse):**
- Estimate >7 minutes
- Needs >1 test file or >4 source files
- Title contains "and" (doing two things)
- Acceptance needs multiple unrelated assertions

**Small enough (base case):**
- One test function in acceptance criteria
- <=7 min estimate
- 1-3 source files
- Single-purpose title

**To recurse:**
1. Promote the bead: `bd update <id> --type epic`
2. Create child tasks with `--parent <id>`
3. Re-apply size test to children

### Step 5: Wire Dependencies

Add ordering constraints where one bead must complete before another can start:

```bash
bd dep add <later-bead> <earlier-bead>
```

Common patterns:
- Types/interfaces before implementations
- Core logic before integration
- Setup/config before features
- Lower layers before higher layers

### Step 6: Present Tree for Confirmation

```bash
bd show <epic-id>
```

Present the full tree to the user:
- Epic with all children
- Dependencies visualized
- Estimates totaled
- Acceptance criteria listed

Ask: "Does this decomposition look right? Any beads to add, remove, or re-scope?"

Wait for user confirmation before proceeding to execution.

## Example Decomposition

Spec: "Add JWT authentication to the API"

```
Epic: Implement JWT authentication (bd-001)
├── Task: Define auth types and interfaces (bd-002, 5min)
│   Acceptance: Test: internal/auth/types_test.go:TestTokenClaims | Cmd: go test ./internal/auth/... | Assert: Claims struct has required fields
├── Task: Implement token generation (bd-003, 7min, depends: bd-002)
│   Acceptance: Test: internal/auth/token_test.go:TestGenerateToken | Cmd: go test ./internal/auth/... -run TestGenerateToken | Assert: returns signed JWT with correct claims
├── Task: Implement token validation (bd-004, 7min, depends: bd-002)
│   Acceptance: Test: internal/auth/token_test.go:TestValidateToken | Cmd: go test ./internal/auth/... -run TestValidateToken | Assert: validates signature, expiry, issuer
├── Task: Add auth middleware (bd-005, 7min, depends: bd-003, bd-004)
│   Acceptance: Test: internal/middleware/auth_test.go:TestAuthMiddleware | Cmd: go test ./internal/middleware/... | Assert: rejects invalid tokens with 401, passes valid tokens
└── Task: Wire middleware to routes (bd-006, 5min, depends: bd-005)
    Acceptance: Test: internal/api/routes_test.go:TestProtectedRoutes | Cmd: go test ./internal/api/... | Assert: protected endpoints require valid JWT
```

## Red Flags

- Creating beads without acceptance criteria
- Acceptance criteria that can't be verified by a command
- Beads with estimates >7min that haven't been decomposed
- Missing dependencies (later bead assumes earlier bead's output)
- Proceeding to execution without user confirmation of the tree
- Vague titles like "implement feature" or "handle edge cases"
