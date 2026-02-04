# Oro Installation

## Prerequisites

### Go

```bash
brew install go
```

Verify:
```bash
go version
# go1.25.6 or later
```

### golangci-lint

```bash
brew install golangci-lint
```

Verify:
```bash
golangci-lint version
```

## Project Setup

Clone the repository and the Go module is already initialized:

```bash
git clone <repo-url>
cd oro
```

Download dependencies (once you have code):
```bash
go mod tidy
```

## Linting

Run the linter:
```bash
golangci-lint run
```

Configuration is in `.golangci.yml`.
