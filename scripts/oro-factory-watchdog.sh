#!/usr/bin/env bash
set -u

ORO_BIN="${ORO_BIN:-/Users/as21/go/bin/oro}"
PROJECT_DIR="${PROJECT_DIR:-/Users/as21/codehouse/oro}"
WORKERS="${WORKERS:-2}"
MAX_WORKERS="${MAX_WORKERS:-2}"
INTERVAL_SECS="${INTERVAL_SECS:-60}"
STUCK_CHECKS_BEFORE_RESTART="${STUCK_CHECKS_BEFORE_RESTART:-2}"
STALE_WORKER_GRACE_SECS="${STALE_WORKER_GRACE_SECS:-300}"

stuck_idle_checks=0

log() {
	printf '[%s] %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*"
}

start_swarm() {
	(cd "$PROJECT_DIR" && "$ORO_BIN" start --workers "$WORKERS" --max-workers "$MAX_WORKERS" --detach)
}

status_json() {
	(cd "$PROJECT_DIR" && "$ORO_BIN" directive status)
}

while true; do
	status="$(status_json 2>&1)"
	rc=$?

	if [[ $rc -ne 0 ]] || ! jq -e . >/dev/null 2>&1 <<<"$status"; then
		log "dispatcher status unavailable; starting swarm"
		start_swarm || log "start failed"
		sleep "$INTERVAL_SECS"
		continue
	fi

	state="$(jq -r '.state' <<<"$status")"
	active="$(jq -r '.active_count' <<<"$status")"
	idle="$(jq -r '.idle_count' <<<"$status")"
	queue="$(jq -r '.queue_depth' <<<"$status")"
	qg_open="$(jq -r '.qg_failure_incidents_open' <<<"$status")"
	progress_timeout="$(jq -r '.progress_timeout_secs // 600' <<<"$status")"
	stale_progress_threshold=$((progress_timeout + STALE_WORKER_GRACE_SECS))
	log "state=$state active=$active idle=$idle queue=$queue qg_open=$qg_open"

	if [[ "$state" != "running" ]]; then
		log "dispatcher state is $state; resuming and starting assignment"
		(cd "$PROJECT_DIR" && "$ORO_BIN" directive resume) || log "resume failed"
		(cd "$PROJECT_DIR" && "$ORO_BIN" directive start) || log "start directive failed"
	fi

	if [[ "$idle" -gt 0 && "$queue" -gt 0 ]]; then
		log "idle workers with queued work; kicking scheduler"
		(cd "$PROJECT_DIR" && "$ORO_BIN" directive start) || log "start directive failed"
		if [[ "$active" -eq 0 ]]; then
			stuck_idle_checks=$((stuck_idle_checks + 1))
		else
			stuck_idle_checks=0
		fi
	else
		stuck_idle_checks=0
	fi

	if [[ "$stuck_idle_checks" -ge "$STUCK_CHECKS_BEFORE_RESTART" ]]; then
		log "assignment stuck across checks; restarting dispatcher"
		(cd "$PROJECT_DIR" && ORO_HUMAN_CONFIRMED=1 "$ORO_BIN" stop --force) || log "stop failed"
		sleep 5
		start_swarm || log "restart failed"
		stuck_idle_checks=0
	fi

	while read -r worker bead progress context; do
		[[ -z "$worker" ]] && continue
		log "worker $worker on $bead is stale with context=$context progress=${progress}s; restarting worker"
		(cd "$PROJECT_DIR" && "$ORO_BIN" directive restart-worker "$worker") || log "restart-worker $worker failed"
	done < <(jq -r --argjson threshold "$stale_progress_threshold" '.workers[]? | select(.state == "busy" and (.last_progress_secs > $threshold)) | [.id, .bead_id, (.last_progress_secs|tostring), (.context_pct|tostring)] | @tsv' <<<"$status")

	sleep "$INTERVAL_SECS"
done
