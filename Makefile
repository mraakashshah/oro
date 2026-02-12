.PHONY: build build-search-hook install test lint fmt vet gate clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X oro/internal/appversion.version=$(VERSION)"

build:
	go build $(LDFLAGS) ./cmd/oro

install:
	go install $(LDFLAGS) ./cmd/oro

build-search-hook:
	@mkdir -p .claude/hooks
	go build -o .claude/hooks/oro-search-hook ./cmd/oro-search-hook

test:
	go test -race -shuffle=on -p 2 ./...

lint:
	golangci-lint run --timeout 5m

fmt:
	gofumpt -w .
	goimports -w .

vet:
	go vet ./...

gate:
	./quality_gate.sh

clean:
	rm -f oro coverage.out
