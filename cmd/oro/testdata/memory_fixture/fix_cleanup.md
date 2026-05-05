---
name: unconditionally clear tracking maps on cleanup
description: Clear maps even if underlying operations fail
type: feedback
---

Unconditionally clear worktree tracking maps on cleanup, even if the worktree
removal itself fails. Partial cleanup leaves tracking state inconsistent.
