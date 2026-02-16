# Toggle Auto-Priming

Toggle whether SessionStart auto-injects project context (handoff, bd ready, git status).

Run this command, then report the new state to the user.

```bash
if [ -f .no-reprime ]; then
  rm .no-reprime
  echo "Auto-priming ENABLED — /clear will inject full project context"
else
  touch .no-reprime
  echo "Auto-priming DISABLED — /clear will start clean (superpowers only)"
fi
```
