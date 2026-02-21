.PHONY: build build-dash build-search-hook install install-git-hooks setup test lint fmt vet gate clean stage-assets clean-assets dev-sync mutate-go mutate-go-diff mutate-py mutate-py-full

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X oro/internal/appversion.version=$(VERSION)"
ORO_HOME ?= $(HOME)/.oro
GOLANGCI_LINT_VERSION ?= v2.10.1

# stage-assets copies oro config assets from the repo's assets/ directory into
# cmd/oro/_assets/ so that go:embed can bundle them into the binary.
# The assets/ directory is the canonical source for embedded resources.
stage-assets:
	@if [ ! -d assets ]; then \
		echo "Error: assets/ directory not found. Cannot stage assets for embedding."; \
		exit 1; \
	fi
	@mkdir -p cmd/oro/_assets/skills cmd/oro/_assets/hooks cmd/oro/_assets/beacons cmd/oro/_assets/commands
	@cp -r assets/skills/* cmd/oro/_assets/skills/ 2>/dev/null || true
	@cp assets/hooks/*.py assets/hooks/*.sh cmd/oro/_assets/hooks/ 2>/dev/null || true
	@cp -r assets/beacons/* cmd/oro/_assets/beacons/ 2>/dev/null || true
	@test -d assets/commands && cp -r assets/commands/* cmd/oro/_assets/commands/ 2>/dev/null || true
	@test -f assets/CLAUDE.md && cp assets/CLAUDE.md cmd/oro/_assets/ || true
	@test -f assets/.test-marker && cp assets/.test-marker cmd/oro/_assets/ || true
	@echo "$(VERSION)" > cmd/oro/_assets/.version

clean-assets:
	@rm -rf cmd/oro/_assets

# dev-sync copies assets/ to ~/.oro/ for local development.
# Path mapping (NOT 1:1):
#   assets/hooks/     -> ~/.oro/hooks/
#   assets/skills/    -> ~/.oro/.claude/skills/
#   assets/beacons/   -> ~/.oro/beacons/
#   assets/commands/  -> ~/.oro/.claude/commands/
#   assets/CLAUDE.md  -> ~/.oro/.claude/CLAUDE.md
dev-sync:
	@if [ ! -d assets ]; then \
		echo "Error: Run from oro repo root (assets/ not found)"; \
		exit 1; \
	fi
	@echo "Syncing assets/ to ~/.oro/..."
	@mkdir -p ~/.oro/hooks ~/.oro/.claude/skills ~/.oro/beacons ~/.oro/.claude/commands
	@cp -r assets/hooks/* ~/.oro/hooks/ && echo "  ✓ hooks"
	@cp -r assets/skills/* ~/.oro/.claude/skills/ && echo "  ✓ skills"
	@cp -r assets/beacons/* ~/.oro/beacons/ && echo "  ✓ beacons"
	@cp -r assets/commands/* ~/.oro/.claude/commands/ && echo "  ✓ commands"
	@cp assets/CLAUDE.md ~/.oro/.claude/CLAUDE.md && echo "  ✓ CLAUDE.md"
	@echo "Sanity check..."
	@test -f ~/.oro/hooks/enforce-skills.sh && echo "  ✓ ~/.oro/hooks/ ok" || (echo "  ✗ ~/.oro/hooks/ FAILED" && exit 1)
	@test -d ~/.oro/.claude/skills/test-driven-development && echo "  ✓ ~/.oro/.claude/skills/ ok" || (echo "  ✗ ~/.oro/.claude/skills/ FAILED" && exit 1)
	@test -d ~/.oro/beacons && echo "  ✓ ~/.oro/beacons/ ok" || (echo "  ✗ ~/.oro/beacons/ FAILED" && exit 1)
	@test -d ~/.oro/.claude/commands && echo "  ✓ ~/.oro/.claude/commands/ ok" || (echo "  ✗ ~/.oro/.claude/commands/ FAILED" && exit 1)
	@test -f ~/.oro/.claude/CLAUDE.md && echo "  ✓ ~/.oro/.claude/CLAUDE.md ok" || (echo "  ✗ ~/.oro/.claude/CLAUDE.md FAILED" && exit 1)
	@echo "✓ dev-sync complete"

build: stage-assets
	go build $(LDFLAGS) ./cmd/oro
	@if [ -d cmd/oro-search-hook ]; then \
		mkdir -p .claude/hooks $(ORO_HOME)/hooks && \
		go build -o .claude/hooks/oro-search-hook ./cmd/oro-search-hook && \
		cp .claude/hooks/oro-search-hook $(ORO_HOME)/hooks/oro-search-hook; \
	fi
	@if [ -d cmd/oro-dash ]; then \
		mkdir -p bin $(ORO_HOME)/bin && \
		go build $(LDFLAGS) -o bin/oro-dash ./cmd/oro-dash && \
		cp bin/oro-dash $(ORO_HOME)/bin/oro-dash; \
	fi
	@$(MAKE) clean-assets

install: stage-assets
	go install $(LDFLAGS) ./cmd/oro
	@if [ -d cmd/oro-search-hook ]; then \
		mkdir -p $(ORO_HOME)/hooks && \
		go build -o $(ORO_HOME)/hooks/oro-search-hook ./cmd/oro-search-hook; \
	else \
		echo "Warning: cmd/oro-search-hook/ not found, skipping oro-search-hook build"; \
	fi
	@if [ -d cmd/oro-dash ]; then \
		mkdir -p $(ORO_HOME)/bin && \
		go build $(LDFLAGS) -o $(ORO_HOME)/bin/oro-dash ./cmd/oro-dash; \
	else \
		echo "Warning: cmd/oro-dash/ not found, skipping oro-dash build"; \
	fi
	@$(MAKE) clean-assets

build-dash:
	@mkdir -p $(ORO_HOME)/bin
	go build $(LDFLAGS) -o $(ORO_HOME)/bin/oro-dash ./cmd/oro-dash

build-search-hook:
	@mkdir -p $(ORO_HOME)/hooks
	go build -o $(ORO_HOME)/hooks/oro-search-hook ./cmd/oro-search-hook

test: stage-assets
	go test -race -shuffle=on -p 2 ./...
	@$(MAKE) clean-assets

lint:
	golangci-lint run --timeout 5m

fmt:
	go tool gofumpt -w .
	go tool goimports -w .

vet: stage-assets
	go vet ./...
	@$(MAKE) clean-assets

gate: stage-assets
	./quality_gate.sh
	@$(MAKE) clean-assets

clean: clean-assets
	rm -f oro coverage.out

# mutate-go runs mutation testing on Go packages in pkg/.
# Uses go-mutesting. Fails if mutation score drops below 0.40.
mutate-go:
	@echo "Running Go mutation testing on pkg/..."
	@trap 'git checkout -- pkg/ 2>/dev/null || true' EXIT; \
	go tool go-mutesting --exec-timeout=30 pkg/... 2>&1 | tee /tmp/go-mutesting-output.txt; \
	git checkout -- pkg/ 2>/dev/null || true; \
	score=$$(grep "The mutation score is" /tmp/go-mutesting-output.txt | awk '{print $$5}'); \
	echo "Mutation score: $$score"; \
	if [ -n "$$score" ] && [ $$(echo "$$score < 0.40" | bc -l) -eq 1 ]; then \
		echo "FAIL: mutation score $$score is below 0.40 threshold"; \
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

# setup installs all dev tooling required by the quality gate:
#   - npm deps (biome, markdownlint-cli2) from package.json
#   - golangci-lint at the pinned version via the official install script
#   - git hooks via install-git-hooks
#   Go tool deps (gofumpt, goimports, go-mutesting, govulncheck) are pinned in
#   go.mod and auto-fetched on first use via `go tool <name>` — no manual install.
setup: install-git-hooks
	@echo "Installing npm dependencies..."
	npm install
	@echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(shell go env GOPATH)/bin $(GOLANGCI_LINT_VERSION)
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
