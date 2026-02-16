#!/usr/bin/env bash
# SessionStart hook: inject skill enforcement context
set -euo pipefail

cat <<'EOF'
<EXTREMELY_IMPORTANT>
YOU MUST invoke the `using-skills` skill (via the Skill tool) BEFORE your first response or action. This is not negotiable, not optional. YOU DO NOT HAVE A CHOICE.

If you think there is even a 1% chance a skill might apply, you ABSOLUTELY MUST invoke it.

Common rationalizations that mean STOP:
- "This is just a simple question" — Questions are tasks.
- "Let me explore first" — Skills tell you HOW to explore.
- "I'll just do this one thing first" — Check BEFORE doing anything.
- "I know what that means" — Knowing the concept ≠ using the skill. Invoke it.

Invoke `using-skills` NOW.
</EXTREMELY_IMPORTANT>
EOF
