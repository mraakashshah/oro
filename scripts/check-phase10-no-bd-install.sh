#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/oro-phase10-no-bd.XXXXXX")"

cleanup() {
	rm -rf "$tmp_root"
}
trap cleanup EXIT

bin_dir="$tmp_root/bin"
gobin="$tmp_root/gobin"
oro_home="$tmp_root/orohome"
db_path="$tmp_root/state.db"
mkdir -p "$bin_dir" "$gobin" "$oro_home"

link_tool() {
	local name="$1"
	local resolved
	resolved="$(command -v "$name" || true)"
	if [ -z "$resolved" ]; then
		echo "missing required tool: $name" >&2
		exit 1
	fi
	ln -sf "$resolved" "$bin_dir/$name"
}

for tool in go git make rsync; do
	link_tool "$tool"
done

controlled_path="$bin_dir:/usr/bin:/bin:/usr/sbin:/sbin"

if PATH="$controlled_path" command -v bd >/dev/null 2>&1; then
	echo "bd unexpectedly resolves inside controlled PATH" >&2
	exit 1
fi

echo "bd absent from controlled PATH"

(
	cd "$repo_root"
	PATH="$controlled_path" ORO_HOME="$oro_home" make build
	PATH="$controlled_path" ORO_HOME="$oro_home" GOBIN="$gobin" make install
)

oro_bin="$gobin/oro"
if [ ! -x "$oro_bin" ]; then
	echo "installed oro not found at $oro_bin" >&2
	exit 1
fi

PATH="$gobin:$controlled_path" "$oro_bin" --version

smoke_id="$(
	PATH="$gobin:$controlled_path" \
		ORO_HOME="$oro_home" \
		ORO_DB_PATH="$db_path" \
		ORO_BEADSOURCE_MODE=sqlite \
		"$oro_bin" task create \
		--type task \
		--title phase10-no-bd-install-smoke \
		--description "Phase 10 no-bd install smoke" \
		--acceptance-criteria "created by scripts/check-phase10-no-bd-install.sh"
)"

show_open="$(
	PATH="$gobin:$controlled_path" \
		ORO_HOME="$oro_home" \
		ORO_DB_PATH="$db_path" \
		ORO_BEADSOURCE_MODE=sqlite \
		"$oro_bin" task show "$smoke_id" --json
)"
printf '%s\n' "$show_open" | grep -q "\"id\": \"$smoke_id\""
printf '%s\n' "$show_open" | grep -q '"status": "open"'

PATH="$gobin:$controlled_path" \
	ORO_HOME="$oro_home" \
	ORO_DB_PATH="$db_path" \
	ORO_BEADSOURCE_MODE=sqlite \
	"$oro_bin" task close "$smoke_id" --reason "Phase 10 no-bd install smoke passed" >/dev/null

show_closed="$(
	PATH="$gobin:$controlled_path" \
		ORO_HOME="$oro_home" \
		ORO_DB_PATH="$db_path" \
		ORO_BEADSOURCE_MODE=sqlite \
		"$oro_bin" task show "$smoke_id" --json
)"
printf '%s\n' "$show_closed" | grep -q '"status": "closed"'

echo "PASS: no-bd install smoke created and closed $smoke_id"
