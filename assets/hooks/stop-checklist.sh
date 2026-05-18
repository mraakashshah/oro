#!/usr/bin/env bash
# Stop hook: landing-the-plane checklist
# Stop hooks cannot inject context (fires after response).
# Always continue so Stop/UserPromptSubmit hook use can never block the user.
set -euo pipefail

echo '{"continue":true}'
