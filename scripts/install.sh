#!/usr/bin/env bash
# install.sh — curl-pipe-bash installer for oro (macOS only)
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/mraakashshah/oro/main/scripts/install.sh | bash
#   bash scripts/install.sh --dry-run
#   bash scripts/install.sh --version v0.1.0
set -euo pipefail

# ── Configuration ────────────────────────────────────────────────────────────
GITHUB_REPO="mraakashshah/oro"
INSTALL_BIN_PREFERRED="/usr/local/bin"
INSTALL_BIN_FALLBACK="${HOME}/.local/bin"
ORO_HOME="${HOME}/.oro"

# ── Colors ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${BLUE}==>${NC} $1"; }
log_success() { echo -e "${GREEN}==>${NC} $1"; }
log_warning() { echo -e "${YELLOW}==>${NC} $1"; }
log_error() { echo -e "${RED}Error:${NC} $1" >&2; }

# ── Flags ────────────────────────────────────────────────────────────────────
DRY_RUN=false
REQUESTED_VERSION=""

parse_args() {
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--dry-run)
			DRY_RUN=true
			shift
			;;
		--version)
			if [[ $# -lt 2 ]]; then
				log_error "--version requires a value (e.g. --version v0.1.0)"
				exit 1
			fi
			REQUESTED_VERSION="$2"
			shift 2
			;;
		--version=*)
			REQUESTED_VERSION="${1#--version=}"
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			log_error "Unknown option: $1"
			usage
			exit 1
			;;
		esac
	done
}

usage() {
	cat <<'USAGE'
Usage: install.sh [OPTIONS]

Options:
  --dry-run          Print actions without executing them
  --version VERSION  Install a specific version (e.g. v0.1.0). Default: latest
  -h, --help         Show this help message
USAGE
}

# ── Platform Detection ───────────────────────────────────────────────────────
detect_platform() {
	local os arch
	case "$(uname -s)" in
	Darwin) os="darwin" ;;
	*)
		log_error "oro currently supports macOS only. Detected: $(uname -s)"
		exit 1
		;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch="amd64" ;;
	aarch64 | arm64) arch="arm64" ;;
	*)
		log_error "Unsupported architecture: $(uname -m)"
		exit 1
		;;
	esac
	echo "${os}_${arch}"
}

# ── Version Resolution ───────────────────────────────────────────────────────
resolve_version() {
	if [[ -n "${REQUESTED_VERSION}" ]]; then
		# Strip leading 'v' if present for the archive name
		echo "${REQUESTED_VERSION#v}"
		return
	fi

	log_info "Fetching latest release version..."
	# Use redirect URL instead of API to avoid 60/hr unauthenticated rate limit.
	local redirect_url
	redirect_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
		"https://github.com/${GITHUB_REPO}/releases/latest")

	local tag
	tag="${redirect_url##*/}"  # extract tag from redirect URL (e.g. "v0.1.0")

	if [[ -z "${tag}" || "${tag}" == "latest" || "${tag}" == "releases" || ! "${tag}" =~ ^v[0-9] ]]; then
		log_error "Could not determine latest release version."
		log_error "Check https://github.com/${GITHUB_REPO}/releases"
		exit 1
	fi

	# Strip leading 'v' for archive name
	echo "${tag#v}"
}

# ── Install Directory Selection ──────────────────────────────────────────────
select_bin_dir() {
	if [[ -d "${INSTALL_BIN_PREFERRED}" && -w "${INSTALL_BIN_PREFERRED}" ]]; then
		echo "${INSTALL_BIN_PREFERRED}"
	else
		echo "${INSTALL_BIN_FALLBACK}"
	fi
}

# ── Execution Helper (respects --dry-run) ────────────────────────────────────
run() {
	if [[ "${DRY_RUN}" == "true" ]]; then
		log_info "[dry-run] $*"
	else
		"$@"
	fi
}

# ── Codesign ─────────────────────────────────────────────────────────────────
try_codesign() {
	local binary="$1"
	if command -v codesign &>/dev/null; then
		run codesign --force --sign - "${binary}" 2>/dev/null || true
	fi
}

# ── PATH Check ───────────────────────────────────────────────────────────────
check_path() {
	local dir="$1"
	local name="$2"
	if ! echo "${PATH}" | tr ':' '\n' | grep -qx "${dir}"; then
		log_warning "${dir} is not in your PATH."
		echo ""
		echo "  Add to your shell profile (e.g. ~/.zshrc):"
		echo ""
		echo "    export PATH=\"${dir}:\$PATH\"   # ${name}"
		echo ""
		echo "  Then reload:"
		echo ""
		echo "    source ~/.zshrc"
		echo ""
	fi
}

# ── Main ─────────────────────────────────────────────────────────────────────
main() {
	parse_args "$@"

	echo ""
	log_info "oro installer"
	echo ""

	# 1. Detect platform
	local platform
	platform=$(detect_platform)
	log_info "Platform: ${platform}"

	# 2. Resolve version
	local version
	version=$(resolve_version)
	log_info "Version:  ${version}"

	# 3. Select install directories
	local bin_dir
	bin_dir=$(select_bin_dir)
	local hooks_dir="${ORO_HOME}/hooks"

	log_info "oro binary:         ${bin_dir}/oro"
	log_info "oro-search-hook:    ${hooks_dir}/oro-search-hook"
	echo ""

	# 4. Build download URLs
	local archive_name="oro_${version}_${platform}.tar.gz"
	local base_url="https://github.com/${GITHUB_REPO}/releases/download/v${version}"
	local download_url="${base_url}/${archive_name}"
	local checksums_url="${base_url}/checksums.txt"
	log_info "Downloading ${download_url}"

	# 5. Create temp directory
	local tmpdir
	if [[ "${DRY_RUN}" == "true" ]]; then
		tmpdir="/tmp/oro-install-dry-run"
		log_info "[dry-run] mkdir -p ${tmpdir}"
	else
		tmpdir=$(mktemp -d)
		trap 'rm -rf "${tmpdir}"' EXIT
	fi

	# 6. Download archive and checksums
	if [[ "${DRY_RUN}" == "true" ]]; then
		log_info "[dry-run] curl -fsSL ${download_url} -o ${tmpdir}/${archive_name}"
		log_info "[dry-run] curl -fsSL ${checksums_url} -o ${tmpdir}/checksums.txt"
		log_info "[dry-run] shasum -a 256 -c checksums.txt (verify ${archive_name})"
		log_info "[dry-run] tar -xzf ${tmpdir}/${archive_name} -C ${tmpdir}"
	else
		curl -fsSL "${download_url}" -o "${tmpdir}/${archive_name}"
		curl -fsSL "${checksums_url}" -o "${tmpdir}/checksums.txt"

		# 7. Verify checksum
		log_info "Verifying checksum..."
		(cd "${tmpdir}" && grep "${archive_name}" checksums.txt | shasum -a 256 -c --quiet)
		log_success "Checksum verified."

		# 8. Extract
		tar -xzf "${tmpdir}/${archive_name}" -C "${tmpdir}"
	fi

	# 9. Create target directories
	run mkdir -p "${bin_dir}"
	run mkdir -p "${hooks_dir}"

	# 10. Install binaries
	log_info "Installing oro to ${bin_dir}/"
	run install -m 0755 "${tmpdir}/oro" "${bin_dir}/oro"

	log_info "Installing oro-search-hook to ${hooks_dir}/"
	run install -m 0755 "${tmpdir}/oro-search-hook" "${hooks_dir}/oro-search-hook"

	# 11. Codesign (macOS ad-hoc re-signing, skip gracefully if unavailable)
	log_info "Re-signing binaries (ad-hoc codesign)..."
	if [[ "${DRY_RUN}" == "true" ]]; then
		log_info "[dry-run] codesign --force --sign - ${bin_dir}/oro"
		log_info "[dry-run] codesign --force --sign - ${hooks_dir}/oro-search-hook"
	else
		try_codesign "${bin_dir}/oro"
		try_codesign "${hooks_dir}/oro-search-hook"
	fi

	# 12. PATH check
	echo ""
	check_path "${bin_dir}" "oro"

	# 13. Done
	log_success "oro ${version} installed successfully!"
	echo ""
	echo "  Try it out:"
	echo ""
	echo "    oro --version"
	echo ""
}

# Allow sourcing for tests: only run main when executed, not sourced.
# Use ${BASH_SOURCE[0]:-} default to handle curl | bash where BASH_SOURCE is unset.
if [[ "${BASH_SOURCE[0]:-$0}" == "${0}" ]]; then
	main "$@"
fi
