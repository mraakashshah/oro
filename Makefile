.PHONY: build test lint fmt vet gate clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X oro/internal/version.version=$(VERSION)"

build:
	go build $(LDFLAGS) ./cmd/oro

test:
	go test -race -shuffle=on ./...

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
