.PHONY: build build-dash build-search-hook install test lint fmt vet gate clean stage-assets clean-assets

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X oro/internal/appversion.version=$(VERSION)"

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
