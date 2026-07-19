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
		function executable_code(line,    quote, i, c, out) {
			quote = ""
			out = ""
			for (i = 1; i <= length(line); i++) {
				c = substr(line, i, 1)
				if (quote == "\047") {
					if (c == "\047") { quote = ""; out = out " "; continue }
					if (c == "$" || c == "`") { out = out " "; continue }
					if (c == ";" || c == "&" || c == "|") { out = out ":"; continue }
					out = out c
					continue
				}
				if (quote == "\"") {
					if (c == "\"") { quote = ""; out = out " "; continue }
					if (c == "\\") {
						out = out "  "
						i++
						continue
					}
					if (c == ";" || c == "&" || c == "|") { out = out ":"; continue }
					out = out c
					continue
				}
				if (c == "\"" || c == "\047") { quote = c; out = out " "; continue }
				if (c == "\\") { out = out "  "; i++; continue }
				if (c == "#") return out
				out = out c
			}
			return out
		}
		function normalize_command(segment) {
			sub(/^[[:space:]]*/, "", segment)
			sub(/^[-+@]+[[:space:]]*/, "", segment)
			while (segment ~ /^(if|then|elif|while|until|do|!|sudo|command)[[:space:]]+/) {
				sub(/^[^[:space:]]+[[:space:]]+/, "", segment)
			}
			return segment
		}
		function has_ls_capture(segment) {
			return segment ~ /\$\([[:space:]]*(ls|lsd)([[:space:]]|\)|[|;&:]|$)/ ||
				segment ~ /`[[:space:]]*(ls|lsd)([[:space:]]|`|[|;&:]|$)/
		}
		{
			line = executable_code($0)
			if (line ~ /^[[:space:]]*$/) next
			segment_count = split(line, segments, /[;&|]+/)
			for (segment_index = 1; segment_index <= segment_count; segment_index++) {
				command = normalize_command(segments[segment_index])
				# A bare directory listing must not become an implicit filesystem path.
				if (command ~ /^(mkdir|cp|mv)([[:space:]]|$)/ && has_ls_capture(command)) {
					print FILENAME ":" FNR ": bare ls output passed to filesystem command"
					error = 1
				}
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
mkdir -p '$(ls)'                       # single-quoted text is not executed
mkdir -p "\$(ls)"                      # escaped substitution is not executed
EOF
	check_file "$fixture" || fail "allowed fixture was rejected"

	# These fixtures must preserve command substitutions as literal source text.
	# shellcheck disable=SC2016
	for command in \
		'mkdir -p "$(ls -d .tmp)"' \
		'cp "$(ls source)" destination' \
		'mv `ls source` destination' \
		'mkdir -p "$(lsd -d source)"' \
		'@mkdir -p "$(ls source)"' \
		'-@cp "$(ls source)" destination' \
		'if mkdir -p "$(ls source)"; then :; fi' \
		'sudo mv "$(ls source)" destination'; do
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
