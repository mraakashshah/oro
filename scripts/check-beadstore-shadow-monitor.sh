#!/usr/bin/env bash
set -euo pipefail

mode=${ORO_BEADSOURCE_MODE:-}
if [[ "$mode" != "shadow" ]]; then
	echo "ORO_BEADSOURCE_MODE must be shadow for the shadow monitor gate; got '${mode}'" >&2
	exit 1
fi

state_db=${ORO_DB_PATH:-}
if [[ -z "$state_db" ]]; then
	echo "ORO_DB_PATH must point to the Phase 8 state.db recorded in the operator log" >&2
	exit 1
fi

shadow_started_at=$(sqlite3 "$state_db" "SELECT value FROM kv_store WHERE key = 'beadstore_shadow_started_at';")
if [[ -z "$shadow_started_at" ]]; then
	echo "missing beadstore_shadow_started_at in $state_db" >&2
	exit 1
fi

python3 - "$shadow_started_at" <<'PY'
from datetime import datetime, timedelta, timezone
import sys

raw = sys.argv[1].strip()
started = datetime.fromisoformat(raw.replace("Z", "+00:00"))
if started.tzinfo is None:
    started = started.replace(tzinfo=timezone.utc)
age = datetime.now(timezone.utc) - started.astimezone(timezone.utc)
required = timedelta(hours=24)
if age < required:
    raise SystemExit(f"shadow window too short: {age.total_seconds() / 3600:.2f}h < 24.00h")
print(f"shadow_window_hours={age.total_seconds() / 3600:.2f}")
PY

# Keep the CLI in the gate so unreadable event logs fail closed, but do not use
# its limited output for the blocking count.
./oro events --type=beadstore_divergence --since=24h --limit=1 >/dev/null
since_ts=$(
	python3 - <<'PY'
from datetime import datetime, timedelta, timezone

print((datetime.now(timezone.utc) - timedelta(hours=24)).strftime("%Y-%m-%d %H:%M:%S"))
PY
)
counts=$(
	sqlite3 "$state_db" <<SQL
SELECT
  COALESCE(SUM(CASE WHEN json_extract(payload, '$.kind') = 'real' THEN 1 ELSE 0 END), 0) || ' ' ||
  COALESCE(SUM(CASE WHEN json_extract(payload, '$.kind') = 'drift' THEN 1 ELSE 0 END), 0)
FROM events
WHERE type = 'beadstore_divergence'
  AND created_at > '$since_ts';
SQL
)
read -r real_count drift_count <<<"$counts"
printf 'real_count=%s drift_count=%s\n' "$real_count" "$drift_count"
test "$real_count" = 0
