.PHONY: build build-search-hook install install-git-hooks setup test test-all test-integration lint nilaway fmt vet gate clean stage-assets clean-assets dev-sync release mutate-go mutate-go-diff mutate-py mutate-py-full verify-bundled-libs download-ort vendor-sqlite-vec vendor-sqlite-vec-release test-vendor-sqlite-vec

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO_BUILD_FLAGS ?= -buildvcs=false
LDFLAGS := -ldflags "-X oro/internal/appversion.version=$(VERSION)"
ORO_HOME ?= $(HOME)/.oro
GOLANGCI_LINT_VERSION ?= v2.10.1
NILAWAY_VERSION ?= v0.0.0-20260318203545-ad240b12fb4c
NILAWAY_PACKAGES ?= ./cmd/... ./internal/... ./pkg/...
NILAWAY_FLAGS ?= -pretty-print=false -exclude-test-files -include-pkgs=oro

SQLITE_VEC_VERSION         ?= 0.1.6
SQLITE_VEC_REPO            := asg017/sqlite-vec
# SHA256 pinned against the upstream release tarballs. Bump together with
# SQLITE_VEC_VERSION (recompute by running `make vendor-sqlite-vec` on an
# unpinned checkout and pasting the printed digest).
SQLITE_VEC_SHA256_DARWIN_ARM64 ?= 142e195b654092632fecfadbad2825f3140026257a70842778637597f6b8c827
SQLITE_VEC_SHA256_DARWIN_AMD64 ?= 35d014e5f7bcac52645a97f1f1ca34fdb51dcd61d81ac6e6ba1c712393fbf8fd

# stage-assets copies oro config assets from the repo's assets/ directory into
# cmd/oro/_assets/ so that go:embed can bundle them into the binary.
# The assets/ directory is the canonical source for embedded resources.
stage-assets:
	@if [ ! -d assets ]; then \
		echo "Error: assets/ directory not found. Cannot stage assets for embedding."; \
		exit 1; \
	fi
	@set -e; tmp="cmd/oro/.assets-stage-$$$$"; old="cmd/oro/.assets-old-$$$$"; \
	cleanup() { \
		rc="$$1"; \
		if [ $$rc -ne 0 ] && [ ! -d cmd/oro/_assets ] && [ -d "$$old" ]; then mv "$$old" cmd/oro/_assets; fi; \
		rm -rf "$$tmp" "$$old"; \
		exit $$rc; \
	}; \
	trap 'cleanup $$?' EXIT; \
	trap 'cleanup 130' INT; \
	trap 'cleanup 143' TERM; \
	rm -rf "$$tmp" "$$old"; \
	mkdir -p "$$tmp/skills" "$$tmp/hooks" "$$tmp/beacons" "$$tmp/commands" "$$tmp/rules"; \
	if [ -d assets/skills ] && [ "$$(find assets/skills -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')" -gt 0 ]; then cp -R assets/skills/. "$$tmp/skills/"; fi; \
	if [ -d assets/beacons ] && [ "$$(find assets/beacons -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')" -gt 0 ]; then cp -R assets/beacons/. "$$tmp/beacons/"; fi; \
	if [ -d assets/commands ] && [ "$$(find assets/commands -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')" -gt 0 ]; then cp -R assets/commands/. "$$tmp/commands/"; fi; \
	if [ -d assets/rules ] && [ "$$(find assets/rules -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')" -gt 0 ]; then cp -R assets/rules/. "$$tmp/rules/"; fi; \
	if [ -d assets/hooks ]; then find assets/hooks -maxdepth 1 -type f \( -name '*.py' -o -name '*.sh' \) >"$$tmp/.hooks"; while IFS= read -r hook; do cp "$$hook" "$$tmp/hooks/"; done <"$$tmp/.hooks"; fi; \
	if [ -f assets/ORO_AGENT.md ]; then cp assets/ORO_AGENT.md "$$tmp/"; fi; \
	if [ -f assets/CLAUDE.md ]; then cp assets/CLAUDE.md "$$tmp/"; fi; \
	if [ -f assets/thresholds.json ]; then cp assets/thresholds.json "$$tmp/"; fi; \
	if [ -f assets/leakscan-allowlist.yaml ]; then cp assets/leakscan-allowlist.yaml "$$tmp/"; fi; \
	if [ -f assets/.test-marker ]; then cp assets/.test-marker "$$tmp/"; fi; \
	rm -f "$$tmp/.hooks"; \
	echo "$(VERSION)" > "$$tmp/.version"; \
	if [ -d cmd/oro/_assets ]; then mv cmd/oro/_assets "$$old"; fi; \
	mv "$$tmp" cmd/oro/_assets; \
	rm -rf "$$old"; \
	trap - EXIT INT TERM

clean-assets:
	@rm -rf cmd/oro/_assets

# dev-sync copies assets/ to $(ORO_HOME)/ for local development.
# Path mapping (NOT 1:1):
#   assets/hooks/     -> $(ORO_HOME)/hooks/
#   assets/skills/    -> $(ORO_HOME)/.claude/skills/
#   assets/beacons/   -> $(ORO_HOME)/beacons/
#   assets/commands/  -> $(ORO_HOME)/.claude/commands/
#   assets/CLAUDE.md  -> $(ORO_HOME)/.claude/CLAUDE.md
dev-sync:
	@if [ ! -d assets ]; then \
		echo "Error: Run from oro repo root (assets/ not found)"; \
		exit 1; \
	fi
	@echo "Syncing assets/ to $(ORO_HOME)/..."
	@mkdir -p "$(ORO_HOME)/hooks" "$(ORO_HOME)/.claude/skills" "$(ORO_HOME)/beacons" "$(ORO_HOME)/.claude/commands"
	@rsync --archive --delete --exclude=oro-search-hook assets/hooks/ "$(ORO_HOME)/hooks/" && echo "  ✓ hooks"
	@rsync --archive --delete assets/skills/ "$(ORO_HOME)/.claude/skills/" && echo "  ✓ skills"
	@rsync --archive --delete assets/beacons/ "$(ORO_HOME)/beacons/" && echo "  ✓ beacons"
	@rsync --archive --delete assets/commands/ "$(ORO_HOME)/.claude/commands/" && echo "  ✓ commands"
	@cp assets/CLAUDE.md "$(ORO_HOME)/.claude/CLAUDE.md" && echo "  ✓ CLAUDE.md"
	@test -f assets/thresholds.json && cp assets/thresholds.json "$(ORO_HOME)/thresholds.json" && echo "  ✓ thresholds.json" || true
	@echo "Sanity check..."
	@test -f "$(ORO_HOME)/hooks/enforce_skills.py" && echo "  ✓ $(ORO_HOME)/hooks/ ok" || (echo "  ✗ $(ORO_HOME)/hooks/ FAILED" && exit 1)
	@test -d "$(ORO_HOME)/.claude/skills/test-driven-development" && echo "  ✓ $(ORO_HOME)/.claude/skills/ ok" || (echo "  ✗ $(ORO_HOME)/.claude/skills/ FAILED" && exit 1)
	@test -d "$(ORO_HOME)/beacons" && echo "  ✓ $(ORO_HOME)/beacons/ ok" || (echo "  ✗ $(ORO_HOME)/beacons/ FAILED" && exit 1)
	@test -d "$(ORO_HOME)/.claude/commands" && echo "  ✓ $(ORO_HOME)/.claude/commands/ ok" || (echo "  ✗ $(ORO_HOME)/.claude/commands/ FAILED" && exit 1)
	@test -f "$(ORO_HOME)/.claude/CLAUDE.md" && echo "  ✓ $(ORO_HOME)/.claude/CLAUDE.md ok" || (echo "  ✗ $(ORO_HOME)/.claude/CLAUDE.md FAILED" && exit 1)
	@echo "✓ dev-sync complete"

build:
	@$(MAKE) stage-assets
	go build $(GO_BUILD_FLAGS) $(LDFLAGS) ./cmd/oro
	@if [ -d cmd/oro-search-hook ]; then \
		mkdir -p .claude/hooks $(ORO_HOME)/hooks && \
		go build $(GO_BUILD_FLAGS) -o .claude/hooks/oro-search-hook ./cmd/oro-search-hook && \
		cp .claude/hooks/oro-search-hook $(ORO_HOME)/hooks/oro-search-hook; \
	fi

install:
	@$(MAKE) stage-assets
	go install $(GO_BUILD_FLAGS) $(LDFLAGS) ./cmd/oro
	@if [ -d cmd/oro-search-hook ]; then \
		mkdir -p .claude/hooks $(ORO_HOME)/hooks && \
		go build $(GO_BUILD_FLAGS) -o .claude/hooks/oro-search-hook ./cmd/oro-search-hook && \
		install -m 0755 .claude/hooks/oro-search-hook $(ORO_HOME)/hooks/oro-search-hook; \
	else \
		echo "Warning: cmd/oro-search-hook/ not found, skipping oro-search-hook build"; \
	fi
	@$(MAKE) dev-sync
	@$(MAKE) clean-assets

build-search-hook:
	@mkdir -p $(ORO_HOME)/hooks
	go build $(GO_BUILD_FLAGS) -o $(ORO_HOME)/hooks/oro-search-hook ./cmd/oro-search-hook

test:
	@$(MAKE) stage-assets
	go test -race -shuffle=on -p 2 ./...
	@$(MAKE) clean-assets

test-all: test

# test-integration runs tests tagged with //go:build integration.
# CI-only target. Runs integration tests in cmd/oro/ package.
# For local development, run the default 'test' target instead.
test-integration:
	@$(MAKE) stage-assets
	go test -tags integration ./cmd/oro/...
	@$(MAKE) clean-assets

lint:
	GOCACHE="$(CURDIR)/.cache/go-build" GOLANGCI_LINT_CACHE="$(CURDIR)/.cache/golangci-lint" golangci-lint run --timeout 5m
	$(MAKE) nilaway

nilaway:
	GOCACHE="$(CURDIR)/.cache/go-build" nilaway $(NILAWAY_FLAGS) $(NILAWAY_PACKAGES)

fmt:
	go tool gofumpt -w .
	go tool goimports -w .
	go tool shfmt -w .

vet: stage-assets
	go vet ./...
	@$(MAKE) clean-assets

gate: stage-assets
	./scripts/quality_gate.sh
	@$(MAKE) clean-assets

clean: clean-assets
	rm -f oro coverage.out

# release tags and pushes a version. GitHub Actions runs GoReleaser.
# Usage: make release V=0.1.0
release:
	@if [ -z "$(V)" ]; then echo "Usage: make release V=0.1.0"; exit 1; fi
	git tag -a "v$(V)" -m "Release v$(V)"
	git push origin "v$(V)"

# mutate-go runs mutation testing on Go packages in pkg/.
# Uses go-mutesting. Fails if mutation score drops below 0.40.
mutate-go:
	@echo "Running Go mutation testing on pkg/..."
	@trap 'git checkout -- pkg/ 2>/dev/null || true' EXIT; \
	go tool go-mutesting --exec-timeout=30 pkg/... 2>&1 | tee /tmp/go-mutesting-output.txt; \
	git checkout -- pkg/ 2>/dev/null || true; \
	score=$$(grep "The mutation score is" /tmp/go-mutesting-output.txt | awk '{print $$5}'); \
	echo "Mutation score: $$score"; \
	if [ -n "$$score" ] && [ $$(echo "$$score < 0.55" | bc -l) -eq 1 ]; then \
		echo "FAIL: mutation score $$score is below 0.55 threshold"; \
		exit 1; \
	fi

# mutate-go-diff runs mutation testing only on Go files changed vs main.
# Used by the quality gate for fast incremental checks. Threshold: 0.75.
mutate-go-diff:
	@trap 'git checkout -- pkg/ internal/ cmd/ 2>/dev/null || true' EXIT; \
	changed=$$(git diff --name-only main -- '*.go' 2>/dev/null | grep -v '_test\.go$$' | grep -v '_generated\.' | grep -v 'cmd/oro/_assets'); \
	if [ -z "$$changed" ]; then echo "No changed Go files to mutate"; exit 0; fi; \
	printf "Mutating: %s\n" "$$changed"; \
	printf '%s\n' "$$changed" | xargs go tool go-mutesting --exec-timeout=30 2>&1 | tee /tmp/go-mutesting-diff.txt; \
	git checkout -- pkg/ internal/ cmd/ 2>/dev/null || true; \
	score=$$(grep "The mutation score is" /tmp/go-mutesting-diff.txt | awk '{print $$5}'); \
	echo "Mutation score: $$score"; \
	if [ -n "$$score" ] && [ $$(echo "$$score < 0.75" | bc -l) -eq 1 ]; then \
		echo "FAIL: mutation score $$score is below 0.75 threshold"; \
		exit 1; \
	fi

# mutate-py runs mutation testing on prompt_injection_guard.py (fast, ~23 mutations).
# Uses cosmic-ray.toml. Fails if survival rate exceeds 50%.
mutate-py:
	@cr_db="/tmp/cr-session-$$$$.sqlite"; \
	uv run cosmic-ray init cosmic-ray.toml "$$cr_db" --force && \
	uv run cosmic-ray exec cosmic-ray.toml "$$cr_db" && \
	uv run cr-report "$$cr_db" && \
	uv run cr-rate "$$cr_db" --fail-over 50; \
	rc=$$?; rm -f "$$cr_db"; exit $$rc

# mutate-py-full runs mutation testing across all Python hooks (may take several minutes).
# Uses cosmic-ray-full.toml.
mutate-py-full:
	@cr_db="/tmp/cr-full-session-$$$$.sqlite"; \
	uv run cosmic-ray init cosmic-ray-full.toml "$$cr_db" --force && \
	uv run cosmic-ray exec cosmic-ray-full.toml "$$cr_db" && \
	uv run cr-report "$$cr_db" && \
	uv run cr-rate "$$cr_db" --fail-over 50; \
	rc=$$?; rm -f "$$cr_db"; exit $$rc

# verify-bundled-libs checks ~/.oro/lib/ for expected dylibs after `make install`.
# Warning (not failure) if libs absent — dylibs are optional; oro works without them.
verify-bundled-libs:
	@echo "Checking $(ORO_HOME)/lib/ for bundled dylibs..."
	@missing=0; \
	for lib in libonnxruntime.dylib libtokenizers.dylib sqlite-vec.dylib; do \
		if [ -f "$(ORO_HOME)/lib/$$lib" ]; then \
			echo "  ✓ $$lib"; \
		else \
			echo "  ⚠ $$lib not found (semantic memory features may be unavailable)"; \
			missing=$$((missing + 1)); \
		fi; \
	done; \
	if [ "$$missing" -gt 0 ]; then \
		echo ""; \
		echo "Warning: $$missing dylib(s) absent from $(ORO_HOME)/lib/"; \
		echo "         Run 'make vendor-sqlite-vec' to fetch sqlite-vec."; \
	else \
		echo "✓ All dylibs present in $(ORO_HOME)/lib/"; \
	fi

# download-ort: stub for ORT/tokenizer dylib acquisition.
# TODO: implement artifact download + checksum once upstream URL and checksum
# policy are decided. This target is a placeholder — see follow-up bead for
# the actual acquisition logic.
download-ort:
	@echo "TODO: ORT/tokenizer dylib acquisition not yet implemented."
	@echo "      Place libonnxruntime.dylib and libtokenizers.dylib into a release"
	@echo "      tarball so that 'bash scripts/install.sh' can bundle them to ~/.oro/lib/."
	@echo "      For sqlite-vec, run: make vendor-sqlite-vec"

# _fetch_sqlite_vec downloads + verifies the sqlite-vec dylib for one arch and
# writes it to $(DEST). Expects DEST (output path), OS_ARCH (macos-aarch64 |
# macos-x86_64), and SHA256 (pinned digest, optional) as make variables.
#
# Factored out so vendor-sqlite-vec (local dev, current arch) and
# vendor-sqlite-vec-release (CI, both arches) share one download path.
define _fetch_sqlite_vec
	set -euo pipefail; \
	TARBALL="sqlite-vec-$(SQLITE_VEC_VERSION)-loadable-$(1).tar.gz"; \
	URL="https://github.com/$(SQLITE_VEC_REPO)/releases/download/v$(SQLITE_VEC_VERSION)/$$TARBALL"; \
	VTMPDIR=$$(mktemp -d); \
	trap "rm -rf '$$VTMPDIR'" EXIT; \
	echo "Downloading $$URL..."; \
	curl -fsSL "$$URL" -o "$$VTMPDIR/$$TARBALL"; \
	if [ -n "$(2)" ]; then \
		echo "Verifying SHA256 ($(1))..."; \
		echo "$(2)  $$VTMPDIR/$$TARBALL" | shasum -a 256 -c --quiet; \
		echo "SHA256 verified."; \
	else \
		COMPUTED=$$(shasum -a 256 "$$VTMPDIR/$$TARBALL" | awk '{print $$1}'); \
		echo "Info: SHA256 not pinned for $(1). To pin, paste into Makefile:"; \
		echo "  $$COMPUTED"; \
	fi; \
	tar -xzf "$$VTMPDIR/$$TARBALL" -C "$$VTMPDIR"; \
	DYLIB=$$(find "$$VTMPDIR" -maxdepth 1 -name '*.dylib' | head -1); \
	[ -z "$$DYLIB" ] && { echo "ERROR: no .dylib found in tarball"; exit 1; }; \
	mkdir -p "$$(dirname $(3))"; \
	cp "$$DYLIB" "$(3)"; \
	echo "Saved $(3) ($$(wc -c <$(3) | tr -d ' ') bytes)"
endef

# vendor-sqlite-vec downloads the sqlite-vec loadable extension for the current macOS
# arch (arm64 or amd64), saves it to _assets/lib/sqlite-vec.dylib for release bundling,
# and installs it to $(ORO_HOME)/lib/sqlite-vec.dylib with ad-hoc codesign for local dev.
vendor-sqlite-vec:
	@ARCH=$$(uname -m); \
	case "$$ARCH" in \
		arm64)  OS_ARCH=macos-aarch64; SHA256="$(SQLITE_VEC_SHA256_DARWIN_ARM64)" ;; \
		x86_64) OS_ARCH=macos-x86_64;  SHA256="$(SQLITE_VEC_SHA256_DARWIN_AMD64)" ;; \
		*) echo "Unsupported arch: $$ARCH"; exit 1 ;; \
	esac; \
	$(call _fetch_sqlite_vec,$$OS_ARCH,$$SHA256,_assets/lib/sqlite-vec.dylib)
	@mkdir -p $(ORO_HOME)/lib
	@install -m 0644 _assets/lib/sqlite-vec.dylib $(ORO_HOME)/lib/sqlite-vec.dylib
	@if command -v codesign >/dev/null 2>&1; then \
		codesign --force --sign - $(ORO_HOME)/lib/sqlite-vec.dylib; \
		echo "✓ Ad-hoc codesigned $(ORO_HOME)/lib/sqlite-vec.dylib"; \
	else \
		echo "Info: codesign unavailable, skipping ad-hoc signing"; \
	fi
	@echo "✓ sqlite-vec $(SQLITE_VEC_VERSION) ready at $(ORO_HOME)/lib/sqlite-vec.dylib"

# vendor-sqlite-vec-release fetches BOTH darwin arches and stages per-arch
# copies at _assets/lib/darwin_{arm64,amd64}/sqlite-vec.dylib. Called from the
# release workflow before GoReleaser so archives.files can template the src
# by {{.Arch}} and each tarball ships its matching dylib.
vendor-sqlite-vec-release:
	@$(call _fetch_sqlite_vec,macos-aarch64,$(SQLITE_VEC_SHA256_DARWIN_ARM64),_assets/lib/darwin_arm64/sqlite-vec.dylib)
	@$(call _fetch_sqlite_vec,macos-x86_64,$(SQLITE_VEC_SHA256_DARWIN_AMD64),_assets/lib/darwin_amd64/sqlite-vec.dylib)
	@echo "✓ sqlite-vec $(SQLITE_VEC_VERSION) staged for both darwin arches"

# test-vendor-sqlite-vec verifies _assets/lib/sqlite-vec.dylib is present and non-empty.
# Requires 'make vendor-sqlite-vec' to have been run first.
test-vendor-sqlite-vec:
	@test -f _assets/lib/sqlite-vec.dylib || { echo "FAIL: run 'make vendor-sqlite-vec' first"; exit 1; }
	@test -s _assets/lib/sqlite-vec.dylib || { echo "FAIL: _assets/lib/sqlite-vec.dylib is empty"; exit 1; }
	@echo "PASS: _assets/lib/sqlite-vec.dylib present ($$(wc -c <_assets/lib/sqlite-vec.dylib | tr -d ' ') bytes)"

# setup installs all dev tooling required by the quality gate:
#   - npm deps (biome, markdownlint-cli2) from package.json
#   - golangci-lint at the pinned version via the official install script
#   - NilAway at the pinned version via go install
#   - git hooks via install-git-hooks
#   Go tool deps (gofumpt, goimports, go-mutesting, govulncheck, shfmt) are pinned in
#   go.mod and auto-fetched on first use via `go tool <name>`.
#   CI pins go-arch-lint explicitly because adding it as a Go tool pulls a
#   large unrelated transitive dependency set.
setup: install-git-hooks
	@echo "Installing npm dependencies..."
	npm install
	@echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(shell go env GOPATH)/bin $(GOLANGCI_LINT_VERSION)
	@echo "Installing NilAway $(NILAWAY_VERSION)..."
	go install go.uber.org/nilaway/cmd/nilaway@$(NILAWAY_VERSION)
	@echo "Installing Python dependencies..."
	uv sync
	@echo "✓ Setup complete."

# install-git-hooks symlinks the canonical git hooks from git/hooks/ into .git/hooks/.
# Run once after cloning: make install-git-hooks
install-git-hooks:
	@echo "Installing git hooks from git/hooks/ → .git/hooks/..."
	@for hook in git/hooks/*; do \
		name=$$(basename "$$hook"); \
		target=".git/hooks/$$name"; \
		src="$$(pwd)/$$hook"; \
		ln -sf "$$src" "$$target" && echo "  ✓ $$name"; \
	done
	@echo "Done. Hooks active in .git/hooks/"
