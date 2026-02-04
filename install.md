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

### go-arch-lint

```bash
go install github.com/fe3dback/go-arch-lint@latest
```

Verify:
```bash
go-arch-lint version
```

### markdownlint

```bash
brew install markdownlint-cli
```

Verify:
```bash
markdownlint --version
```

### yamllint

```bash
brew install yamllint
```

Verify:
```bash
yamllint --version
```

### shellcheck

```bash
brew install shellcheck
```

Verify:
```bash
shellcheck --version
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

Run code linter:
```bash
golangci-lint run
```

Run architecture linter:
```bash
go-arch-lint check
```

Run markdown linter:
```bash
markdownlint '**/*.md'
```

Run YAML linter:
```bash
yamllint .
```

Run shell script linter:
```bash
shellcheck **/*.sh
```

Configuration files: `.golangci.yml`, `.go-arch-lint.yml`, `.markdownlint.yaml`, `.yamllint.yaml`
