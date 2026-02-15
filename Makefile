.PHONY: build build-search-hook install test lint fmt vet gate clean stage-assets clean-assets

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X oro/internal/appversion.version=$(VERSION)"

# stage-assets copies oro config assets into cmd/oro/_assets/ so that
# go:embed can bundle them into the binary. The directory is ephemeral
# and cleaned up after build/test.
stage-assets:
	@mkdir -p cmd/oro/_assets/skills cmd/oro/_assets/hooks cmd/oro/_assets/beacons cmd/oro/_assets/commands
	@cp -r .claude/skills/* cmd/oro/_assets/skills/ 2>/dev/null || true
	@cp .claude/hooks/*.py .claude/hooks/*.sh cmd/oro/_assets/hooks/ 2>/dev/null || true
	@cp -r .claude/hooks/beacons/* cmd/oro/_assets/beacons/ 2>/dev/null || true
	@test -d .claude/commands && cp -r .claude/commands/* cmd/oro/_assets/commands/ 2>/dev/null || true
	@test -f CLAUDE.md && cp CLAUDE.md cmd/oro/_assets/ || true

clean-assets:
	@rm -rf cmd/oro/_assets

build: stage-assets
	go build $(LDFLAGS) ./cmd/oro
	@$(MAKE) clean-assets

install: stage-assets
	go install $(LDFLAGS) ./cmd/oro
	@$(MAKE) clean-assets

build-search-hook:
	@mkdir -p .claude/hooks
	go build -o .claude/hooks/oro-search-hook ./cmd/oro-search-hook

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
