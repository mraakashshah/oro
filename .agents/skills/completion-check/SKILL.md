---
name: completion-check
description: Use when infrastructure or features are built but before declaring done -- verifies work is wired into the system and actively used
---

# Completion Check: Verify Infrastructure Is Wired

## Overview

Infrastructure is not done when the code is written — it's done when it's wired into the system and actively used. Dead code is wasted effort.

## The Checklist

Before declaring infrastructure complete:

- [ ] **Traced execution path** from entry point to new code
- [ ] **Verified registration** — handlers registered, interfaces satisfied, hooks configured
- [ ] **Confirmed correct backend** — using intended database/service/config
- [ ] **Ran end-to-end test** showing new code is actually invoked
- [ ] **Searched for dead code** — no orphaned or parallel implementations

## Verification Steps

### 1. Trace the Execution Path

Follow from user intent to actual code execution:
```bash
# Go: Verify handler is registered
rg "HandleFunc.*myEndpoint" cmd/ internal/
rg "mux.Handle" cmd/

# Python: Verify function is called
rg "my_function\(" src/
```

### 2. Check Registration

```go
// Go: Is the interface satisfied?
var _ MyInterface = (*MyStruct)(nil)

// Is the handler registered in the router?
router.HandleFunc("/api/resource", handler.Create).Methods("POST")
```

```python
# Python: Is the entry point wired?
# Check __init__.py exports, CLI registrations, etc.
```

### 3. Test End-to-End

Run the feature and verify new code is invoked:
```bash
# Go
go test ./... -run TestIntegration -v

# Python
uv run pytest tests/ -k "integration" -v
```

### 4. Search for Orphans

```bash
# Find functions defined but never called
rg "func \w+" --type go | # all function definitions
  # cross-reference with call sites
```

## Common Traps

| Trap | Reality |
|------|---------|
| "Code exists, so it works" | Existing ≠ wired. Trace the path. |
| "It compiles" | Compiling ≠ running. Test E2E. |
| "Tests pass" | Unit tests don't prove integration. |
| "Handler is written" | Written ≠ registered in router. |

## Red Flags

- Marking "complete" without running the feature
- Assuming code is wired because it exists
- Building parallel implementations (old + new both present)
- Skipping end-to-end verification
