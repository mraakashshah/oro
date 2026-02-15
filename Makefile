.PHONY: build build-dash build-search-hook install test lint fmt vet gate clean stage-assets clean-assets

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X oro/internal/appversion.version=$(VERSION)"

# stage-assets copies oro config assets into cmd/oro/_assets/ so that
# go:embed can bundle them into the binary. Assets live in ~/.oro/ after
# externalization (see docs/plans/2026-02-15-config-externalization-design.md).
ORO_HOME ?= $(HOME)/.oro
stage-assets:
	@mkdir -p cmd/oro/_assets/skills cmd/oro/_assets/hooks cmd/oro/_assets/beacons cmd/oro/_assets/commands
	@cp -r $(ORO_HOME)/.claude/skills/* cmd/oro/_assets/skills/ 2>/dev/null || true
	@cp $(ORO_HOME)/hooks/*.py $(ORO_HOME)/hooks/*.sh cmd/oro/_assets/hooks/ 2>/dev/null || true
	@cp -r $(ORO_HOME)/beacons/* cmd/oro/_assets/beacons/ 2>/dev/null || true
	@test -d $(ORO_HOME)/.claude/commands && cp -r $(ORO_HOME)/.claude/commands/* cmd/oro/_assets/commands/ 2>/dev/null || true
	@test -f $(ORO_HOME)/.claude/CLAUDE.md && cp $(ORO_HOME)/.claude/CLAUDE.md cmd/oro/_assets/ || true

clean-assets:
	@rm -rf cmd/oro/_assets

build: stage-assets
	go build $(LDFLAGS) ./cmd/oro
	@$(MAKE) clean-assets

install: stage-assets
	go install $(LDFLAGS) ./cmd/oro
	@$(MAKE) clean-assets

build-dash:
	go build $(LDFLAGS) -o oro-dash ./cmd/oro-dash

build-search-hook:
	@mkdir -p $(ORO_HOME)/hooks
	go build -o $(ORO_HOME)/hooks/oro-search-hook ./cmd/oro-search-hook

test: stage-assets
	go test -race -shuffle=on -p 2 ./...
	@$(MAKE) clean-assets

lint:
	golangci-lint run --timeout 5m

fmt:
	gofumpt -w .
	goimports -w .

vet: stage-assets
	go vet ./...
	@$(MAKE) clean-assets

gate: stage-assets
	./quality_gate.sh
	@$(MAKE) clean-assets

clean: clean-assets
	rm -f oro coverage.out
