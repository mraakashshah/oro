package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type astGrepMatch struct {
	Text   string       `json:"text"`
	Range  astGrepRange `json:"range"`
	RuleID string       `json:"ruleId"`
}

type astGrepRange struct {
	Start astGrepPos `json:"start"`
}

type astGrepPos struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

var langRules = map[Language]string{ //nolint:gochecknoglobals // static config
	LangPython:     pythonRules,
	LangTypeScript: typescriptRules,
	LangJavaScript: javascriptRules,
	LangRust:       rustRules,
	LangJava:       javaRules,
}

const pythonRules = `id: py-func
language: python
rule:
  kind: function_definition
---
id: py-class
language: python
rule:
  kind: class_definition
---
id: py-decorated
language: python
rule:
  kind: decorated_definition`

const typescriptRules = `id: ts-func
language: typescript
rule:
  kind: function_declaration
---
id: ts-class
language: typescript
rule:
  kind: class_declaration
---
id: ts-iface
language: typescript
rule:
  kind: interface_declaration
---
id: ts-type-alias
language: typescript
rule:
  kind: type_alias_declaration
---
id: ts-enum
language: typescript
rule:
  kind: enum_declaration
---
id: ts-export-stmt
language: typescript
rule:
  kind: export_statement
  not:
    any:
      - has:
          kind: function_declaration
      - has:
          kind: class_declaration
      - has:
          kind: interface_declaration
      - has:
          kind: type_alias_declaration
      - has:
          kind: enum_declaration`

const javascriptRules = `id: js-func
language: javascript
rule:
  kind: function_declaration
---
id: js-class
language: javascript
rule:
  kind: class_declaration
---
id: js-export-stmt
language: javascript
rule:
  kind: export_statement
  not:
    any:
      - has:
          kind: function_declaration
      - has:
          kind: class_declaration`

// rustRules extracts top-level Rust items: structs, enums, traits, type
// aliases, impl blocks, and free functions (including pub and private).
const rustRules = `id: rs-struct
language: rust
rule:
  kind: struct_item
---
id: rs-enum
language: rust
rule:
  kind: enum_item
---
id: rs-trait
language: rust
rule:
  kind: trait_item
---
id: rs-type
language: rust
rule:
  kind: type_item
---
id: rs-impl
language: rust
rule:
  kind: impl_item
---
id: rs-fn
language: rust
rule:
  kind: function_item
  not:
    inside:
      kind: impl_item`

// javaRules extracts top-level Java declarations: classes, interfaces, and enums.
const javaRules = `id: java-class
language: java
rule:
  kind: class_declaration
---
id: java-iface
language: java
rule:
  kind: interface_declaration
---
id: java-enum
language: java
rule:
  kind: enum_declaration`

const astGrepTimeout = 5 * time.Second

func summarizeWithAstGrep(filePath string, lang Language) (string, error) {
	rules, ok := langRules[lang]
	if !ok {
		return "", fmt.Errorf("codesearch: no ast-grep rules for language %s", lang)
	}
	matches, err := runAstGrep(filePath, rules)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("codesearch: no structural elements found in %s", filePath)
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Range.Start.Line < matches[j].Range.Start.Line
	})
	matches = deduplicateMatches(matches)
	return formatMatches(matches, lang), nil
}

func runAstGrep(filePath, rules string) ([]astGrepMatch, error) {
	astGrepBin, err := exec.LookPath("ast-grep")
	if err != nil {
		return nil, fmt.Errorf("codesearch: ast-grep not found in PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), astGrepTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, astGrepBin, "scan", "--json", "--inline-rules", rules, filePath) //nolint:gosec // filePath from trusted caller
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("codesearch: ast-grep failed: %w", err)
	}
	var matches []astGrepMatch
	if err := json.Unmarshal(out, &matches); err != nil {
		return nil, fmt.Errorf("codesearch: failed to parse ast-grep output: %w", err)
	}
	return matches, nil
}

func deduplicateMatches(matches []astGrepMatch) []astGrepMatch {
	seen := make(map[int]bool, len(matches))
	result := make([]astGrepMatch, 0, len(matches))
	for _, m := range matches {
		line := m.Range.Start.Line
		if seen[line] {
			continue
		}
		seen[line] = true
		result = append(result, m)
	}
	return result
}

func formatMatches(matches []astGrepMatch, lang Language) string {
	var b strings.Builder
	for _, m := range matches {
		line := m.Range.Start.Line + 1
		kind, sig := extractSignature(m, lang)
		fmt.Fprintf(&b, "L%d: %s %s\n", line, kind, sig)
	}
	return b.String()
}

func extractSignature(m astGrepMatch, lang Language) (string, string) { //nolint:gocritic // readability
	kind := ruleIDToKind(m.RuleID)
	firstLine := firstLineOf(m.Text)
	switch lang {
	case LangPython:
		return formatPythonSignature(kind, firstLine, m.RuleID)
	case LangTypeScript, LangJavaScript:
		return formatTSSignature(kind, firstLine, m.RuleID)
	case LangRust:
		return formatRustSignature(kind, firstLine, m.RuleID)
	case LangJava:
		return formatJavaSignature(kind, firstLine, m.RuleID)
	default:
		return kind, firstLine
	}
}

// ruleKinds maps ast-grep rule IDs to their human-readable kind labels.
var ruleKinds = map[string]string{ //nolint:gochecknoglobals // static config
	"py-func":        "func",
	"py-class":       "class",
	"py-decorated":   "decorated",
	"ts-func":        "func",
	"js-func":        "func",
	"ts-class":       "class",
	"js-class":       "class",
	"ts-iface":       "interface",
	"ts-type-alias":  "type",
	"ts-enum":        "enum",
	"ts-export-stmt": "export",
	"js-export-stmt": "export",
	"rs-struct":      "struct",
	"rs-enum":        "enum",
	"rs-trait":       "trait",
	"rs-type":        "type",
	"rs-impl":        "impl",
	"rs-fn":          "func",
	"java-class":     "class",
	"java-iface":     "interface",
	"java-enum":      "enum",
}

func ruleIDToKind(ruleID string) string {
	if kind, ok := ruleKinds[ruleID]; ok {
		return kind
	}
	return ruleID
}

func formatPythonSignature(kind, firstLine, ruleID string) (string, string) { //nolint:gocritic // readability
	switch ruleID {
	case "py-func":
		return "func", extractPythonFuncSig(firstLine)
	case "py-class":
		return "class", extractPythonClassSig(firstLine)
	case "py-decorated":
		return "decorated", firstLine
	default:
		return kind, firstLine
	}
}

func extractPythonFuncSig(line string) string {
	sig := strings.TrimSuffix(strings.TrimSpace(line), ":")
	if strings.HasPrefix(sig, "def ") {
		return strings.TrimPrefix(sig, "def ")
	}
	if strings.HasPrefix(sig, "async def ") {
		return "async " + strings.TrimPrefix(sig, "async def ")
	}
	return sig
}

func extractPythonClassSig(line string) string {
	sig := strings.TrimSuffix(strings.TrimSpace(line), ":")
	return strings.TrimPrefix(sig, "class ")
}

func formatTSSignature(kind, firstLine, ruleID string) (string, string) { //nolint:gocritic // readability
	switch ruleID {
	case "ts-func", "js-func":
		return "func", extractTSFuncSig(firstLine)
	case "ts-class", "js-class":
		return "class", extractTSClassSig(firstLine)
	case "ts-iface":
		return "interface", extractTSInterfaceSig(firstLine)
	case "ts-type-alias":
		return "type", extractTSTypeSig(firstLine)
	case "ts-enum":
		return "enum", extractTSEnumSig(firstLine)
	case "ts-export-stmt", "js-export-stmt":
		return "export", extractTSExportSig(firstLine)
	default:
		return kind, firstLine
	}
}

func extractTSFuncSig(line string) string {
	sig := strings.TrimSpace(line)
	sig = strings.TrimSuffix(sig, "{")
	sig = strings.TrimSpace(sig)
	sig = strings.TrimPrefix(sig, "export ")
	sig = strings.TrimPrefix(sig, "async ")
	sig = strings.TrimPrefix(sig, "function ")
	return strings.TrimSpace(sig)
}

func extractTSClassSig(line string) string {
	sig := strings.TrimSpace(line)
	sig = strings.TrimSuffix(sig, "{")
	sig = strings.TrimSpace(sig)
	sig = strings.TrimPrefix(sig, "export ")
	sig = strings.TrimPrefix(sig, "abstract ")
	sig = strings.TrimPrefix(sig, "class ")
	return strings.TrimSpace(sig)
}

func extractTSInterfaceSig(line string) string {
	sig := strings.TrimSpace(line)
	sig = strings.TrimSuffix(sig, "{")
	sig = strings.TrimSpace(sig)
	sig = strings.TrimPrefix(sig, "export ")
	sig = strings.TrimPrefix(sig, "interface ")
	return strings.TrimSpace(sig)
}

func extractTSTypeSig(line string) string {
	sig := strings.TrimSpace(line)
	sig = strings.TrimPrefix(sig, "export ")
	sig = strings.TrimPrefix(sig, "type ")
	if idx := strings.Index(sig, "= {"); idx >= 0 {
		return strings.TrimSpace(sig[:idx])
	}
	if idx := strings.Index(sig, "="); idx >= 0 {
		return strings.TrimSpace(sig[:idx+1]) + " ..."
	}
	return strings.TrimSpace(sig)
}

func extractTSEnumSig(line string) string {
	sig := strings.TrimSpace(line)
	sig = strings.TrimSuffix(sig, "{")
	sig = strings.TrimSpace(sig)
	sig = strings.TrimPrefix(sig, "export ")
	sig = strings.TrimPrefix(sig, "enum ")
	return strings.TrimSpace(sig)
}

func extractTSExportSig(line string) string {
	sig := strings.TrimSpace(line)
	sig = strings.TrimSuffix(sig, ";")
	sig = strings.TrimPrefix(sig, "export ")
	return strings.TrimSpace(sig)
}

// formatRustSignature formats Rust structural items.
// Output format matches Go style: `L<line>: kind name/sig`
func formatRustSignature(kind, firstLine, ruleID string) (string, string) { //nolint:gocritic // readability
	switch ruleID {
	case "rs-struct":
		return "struct", extractRustItemName(firstLine, "struct")
	case "rs-enum":
		return "enum", extractRustItemName(firstLine, "enum")
	case "rs-trait":
		return "trait", extractRustItemName(firstLine, "trait")
	case "rs-type":
		return "type", extractRustTypeSig(firstLine)
	case "rs-impl":
		return "impl", extractRustImplSig(firstLine)
	case "rs-fn":
		return "func", extractRustFnSig(firstLine)
	default:
		return kind, firstLine
	}
}

// extractRustItemName strips visibility modifiers, the keyword, and trailing
// brace/punctuation, returning just the name (and optional generic params).
// e.g., "pub struct Config {" -> "Config"
func extractRustItemName(line, keyword string) string {
	sig := strings.TrimSpace(line)
	// Strip visibility: pub, pub(crate), pub(super), etc.
	sig = stripRustVisibility(sig)
	// Strip derive/attribute lines if present (shouldn't appear in firstLine).
	sig = strings.TrimPrefix(sig, keyword+" ")
	// Strip trailing brace or semicolon.
	sig = strings.TrimSuffix(strings.TrimSpace(sig), "{")
	sig = strings.TrimSuffix(strings.TrimSpace(sig), ";")
	return strings.TrimSpace(sig)
}

// extractRustTypeSig handles type aliases: "pub type Result<T> = ...;" -> "Result<T>"
func extractRustTypeSig(line string) string {
	sig := strings.TrimSpace(line)
	sig = stripRustVisibility(sig)
	sig = strings.TrimPrefix(sig, "type ")
	// Keep only up to the "=" sign.
	if idx := strings.Index(sig, "="); idx >= 0 {
		sig = strings.TrimSpace(sig[:idx])
	}
	sig = strings.TrimSuffix(sig, ";")
	return strings.TrimSpace(sig)
}

// extractRustImplSig handles impl blocks: "impl Config {" -> "Config"
// and "impl Handler for Server {" -> "Handler for Server"
func extractRustImplSig(line string) string {
	sig := strings.TrimSpace(line)
	sig = stripRustVisibility(sig)
	sig = strings.TrimPrefix(sig, "impl ")
	sig = strings.TrimSuffix(strings.TrimSpace(sig), "{")
	return strings.TrimSpace(sig)
}

// extractRustFnSig extracts a Rust function signature (name + params + return).
// e.g., "pub fn create_server(host: &str) -> Server {" -> "create_server(host: &str) -> Server"
func extractRustFnSig(line string) string {
	sig := strings.TrimSpace(line)
	sig = stripRustVisibility(sig)
	sig = strings.TrimPrefix(sig, "async ")
	sig = strings.TrimPrefix(sig, "fn ")
	// Remove trailing open brace.
	sig = strings.TrimSuffix(strings.TrimSpace(sig), "{")
	return strings.TrimSpace(sig)
}

// stripRustVisibility removes pub, pub(crate), pub(super), pub(in ...) prefixes.
func stripRustVisibility(s string) string {
	if !strings.HasPrefix(s, "pub") {
		return s
	}
	if strings.HasPrefix(s, "pub(") {
		// Find closing paren.
		if end := strings.Index(s, ")"); end >= 0 {
			return strings.TrimSpace(s[end+1:])
		}
	}
	return strings.TrimPrefix(strings.TrimPrefix(s, "pub "), "pub")
}

// formatJavaSignature formats Java declarations (class, interface, enum).
func formatJavaSignature(kind, firstLine, ruleID string) (string, string) { //nolint:gocritic // readability
	switch ruleID {
	case "java-class":
		return "class", extractJavaClassSig(firstLine)
	case "java-iface":
		return "interface", extractJavaInterfaceSig(firstLine)
	case "java-enum":
		return "enum", extractJavaEnumSig(firstLine)
	default:
		return kind, firstLine
	}
}

// extractJavaClassSig extracts the class name from a declaration.
// e.g., "public class ServerConfig {" -> "ServerConfig"
func extractJavaClassSig(line string) string {
	sig := strings.TrimSpace(line)
	// Remove access modifiers: public, protected, private, abstract, final, static.
	sig = stripJavaModifiers(sig)
	sig = strings.TrimPrefix(sig, "class ")
	// Remove everything from "{" onwards (extends, implements, body).
	if idx := strings.Index(sig, "{"); idx >= 0 {
		sig = sig[:idx]
	}
	return strings.TrimSpace(sig)
}

// extractJavaInterfaceSig extracts the interface name.
// e.g., "public interface Handler {" -> "Handler"
func extractJavaInterfaceSig(line string) string {
	sig := strings.TrimSpace(line)
	sig = stripJavaModifiers(sig)
	sig = strings.TrimPrefix(sig, "interface ")
	if idx := strings.Index(sig, "{"); idx >= 0 {
		sig = sig[:idx]
	}
	return strings.TrimSpace(sig)
}

// extractJavaEnumSig extracts the enum name.
// e.g., "public enum HttpMethod {" -> "HttpMethod"
func extractJavaEnumSig(line string) string {
	sig := strings.TrimSpace(line)
	sig = stripJavaModifiers(sig)
	sig = strings.TrimPrefix(sig, "enum ")
	if idx := strings.Index(sig, "{"); idx >= 0 {
		sig = sig[:idx]
	}
	return strings.TrimSpace(sig)
}

// stripJavaModifiers removes Java access and non-access modifiers from the
// beginning of a declaration line.
func stripJavaModifiers(s string) string {
	modifiers := []string{
		"public ", "protected ", "private ",
		"abstract ", "final ", "static ", "strictfp ",
		"@Override ", "@SuppressWarnings ",
	}
	for {
		changed := false
		for _, mod := range modifiers {
			if strings.HasPrefix(s, mod) {
				s = strings.TrimPrefix(s, mod)
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return s
}

func firstLineOf(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}
