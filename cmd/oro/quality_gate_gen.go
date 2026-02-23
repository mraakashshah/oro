package main

import "oro/pkg/langprofile"

// golangciLintTemplate is the .golangci.yml content for Go projects (v2 format).
// This matches oro's own .golangci.yml (192 lines, version 2).
const golangciLintTemplate = `# golangci-lint configuration (v2 format)
version: "2"

run:
  timeout: 5m
  modules-download-mode: readonly

linters:
  default: none
  enable:
    # Correctness
    - staticcheck
    - govet
    - ineffassign
    - unused
    - errcheck

    # Error handling
    - errorlint
    - wrapcheck
    - nilerr
    - errname

    # Complexity
    - gocyclo
    - gocognit
    - funlen
    - nestif

    # Structure
    - gochecknoglobals
    - gochecknoinits
    - testpackage

    # Resource safety
    - bodyclose
    - noctx
    - durationcheck

    # Cleanup
    - unconvert
    - unparam
    - nakedret
    - whitespace

    # Duplication
    - dupl

    # Testing
    - thelper

    # Security
    - gosec

    # Style
    - gocritic
    - revive
    - misspell

    # Performance
    - prealloc

  settings:
    gocyclo:
      min-complexity: 15

    gocognit:
      min-complexity: 20

    funlen:
      lines: 60
      statements: 40
      ignore-comments: true

    nestif:
      min-complexity: 4

    errcheck:
      check-type-assertions: true
      exclude-functions:
        - (io.Closer).Close
        - (*os.File).Close

    wrapcheck:
      ignore-sigs:
        - .Errorf(
        - errors.New(
        - errors.Join(

    nakedret:
      max-func-lines: 10

    dupl:
      threshold: 200

    gocritic:
      enabled-tags:
        - diagnostic
        - style
        - performance
      disabled-checks:
        - hugeParam
        - rangeValCopy
        - commentedOutCode

    revive:
      severity: warning
      rules:
        - name: blank-imports
        - name: context-as-argument
        - name: context-keys-type
        - name: dot-imports
        - name: error-return
        - name: error-strings
        - name: error-naming
        - name: exported
          arguments:
            - checkPrivateReceivers
            - sayRepetitiveInsteadOfStutters
        - name: if-return
        - name: increment-decrement
        - name: var-naming
        - name: var-declaration
        - name: package-comments
        - name: range
        - name: receiver-naming
        - name: time-naming
        - name: unexported-return
        - name: indent-error-flow
        - name: errorf
        - name: empty-block
        - name: superfluous-else
        - name: unreachable-code
        - name: redefines-builtin-id
        - name: get-return
        - name: string-of-int
        - name: early-return
        - name: unnecessary-stmt

    prealloc:
      simple: true
      range-loops: true
      for-loops: true

  exclusions:
    generated: lax
    presets:
      - std-error-handling
    rules:
      - path: _test\.go
        linters:
          - funlen
          - gocyclo
          - gocognit
          - gochecknoglobals
          - wrapcheck
          - noctx
          - unparam
          - dupl
          - prealloc
          - gocritic

      - path: main\.go
        linters:
          - gochecknoinits
          - gochecknoglobals

formatters:
  enable:
    - goimports
  settings:
    gofumpt:
      extra-rules: true
    goimports:
      local-prefixes:
        - oro
`

// pyprojectToolSectionsTemplate contains [tool.*] sections for Python projects.
// These are appended to an existing pyproject.toml or used standalone.
const pyprojectToolSectionsTemplate = `[tool.ruff]
target-version = "py311"
line-length = 120

[tool.ruff.lint]
select = ["E", "F", "W", "I", "N", "UP", "B", "A", "SIM", "RUF"]

[tool.pyright]
pythonVersion = "3.11"
venvPath = "."
venv = ".venv"

[tool.pylint.main]

[tool.pytest.ini_options]
testpaths = ["tests"]
`

// generatePyprojectToolSections returns pyproject.toml tool sections for Python projects.
// Returns empty string if cfg is nil or does not include a "python" language entry.
// The error return is reserved for future template-based generation.
func generatePyprojectToolSections(cfg *langprofile.Config) (string, error) { //nolint:unparam // error reserved for future use
	if cfg == nil {
		return "", nil
	}
	if _, ok := cfg.Languages["python"]; !ok {
		return "", nil
	}
	return pyprojectToolSectionsTemplate, nil
}

// generateGolangciLint returns a .golangci.yml configuration string for Go projects.
// Returns empty string if cfg is nil or does not include a "go" language entry.
// The error return is reserved for future template-based generation.
func generateGolangciLint(cfg *langprofile.Config) (string, error) { //nolint:unparam // error reserved for future use
	if cfg == nil {
		return "", nil
	}
	if _, ok := cfg.Languages["go"]; !ok {
		return "", nil
	}
	return golangciLintTemplate, nil
}
