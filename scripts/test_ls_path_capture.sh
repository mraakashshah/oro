#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

is_source_file() {
	case "$1" in
	*.sh | Makefile | Makefile.*) return 0 ;;
	*) return 1 ;;
	esac
}

check_file() {
	local file=$1
	awk '
		function trim_comment(line,    quote, i, c) {
			quote = ""
			for (i = 1; i <= length(line); i++) {
				c = substr(line, i, 1)
				if (c == "\\" && quote != "") { i++; continue }
				if ((c == "\"" || c == "\047") && quote == "") { quote = c; continue }
				if (c == quote) { quote = ""; continue }
				if (c == "#" && quote == "") return substr(line, 1, i - 1)
			}
			return line
		}
		{
			line = trim_comment($0)
			if (line ~ /^[[:space:]]*$/) next
			# A bare directory listing must not become an implicit filesystem path.
			if (line ~ /(^[[:space:]]*|[;&|][[:space:]]*)(mkdir|cp|mv)[[:space:]][^;&|]*\$\([[:space:]]*(ls|lsd)([[:space:]]|\)|$)/) {
				print FILENAME ":" FNR ": bare ls output passed to filesystem command"
				error = 1
			}
			if (line ~ /(^[[:space:]]*|[;&|][[:space:]]*)(mkdir|cp|mv)[[:space:]][^;&|]*`[[:space:]]*(ls|lsd)([[:space:]]|`|$)/) {
				print FILENAME ":" FNR ": bare ls output passed to filesystem command"
				error = 1
			}
		}
		END { exit error }
	' "$file"
}

test_no_ls_derived_filesystem_paths() {
	local tmp fixture
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN

	fixture=$tmp/fixture.sh
	cat >"$fixture" <<'EOF'
# mkdir "$(ls)" is only a comment.
ls -la "$HOME"                         # display-only listing
echo "$(ls)"; mkdir -p destination      # display-only substitution is not a path
mkdir -p "$(git ls-files | head -1)"   # tracked source is an allowed provider
EOF
	check_file "$fixture" || fail "allowed fixture was rejected"

	# These fixtures must preserve command substitutions as literal source text.
	# shellcheck disable=SC2016
	for command in 'mkdir -p "$(ls -d .tmp)"' 'cp "$(ls source)" destination' 'mv `ls source` destination' 'mkdir -p "$(lsd -d source)"'; do
		printf '%s\n' "$command" >"$fixture"
		if check_file "$fixture"; then
			fail "unsafe fixture was accepted: $command"
		fi
	done

	while IFS= read -r -d '' file; do
		is_source_file "$file" || continue
		check_file "$REPO_ROOT/$file" || fail "unsafe ls-derived path in $file"
	done < <(git -C "$REPO_ROOT" ls-files -z -- '*.sh' 'Makefile' 'Makefile.*')
}

test_no_ls_derived_filesystem_paths
echo "PASS: no bare ls/lsd output is used as a filesystem path"
