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
	default:
		return kind, firstLine
	}
}

func ruleIDToKind(ruleID string) string {
	switch ruleID {
	case "py-func":
		return "func"
	case "py-class":
		return "class"
	case "py-decorated":
		return "decorated"
	case "ts-func", "js-func":
		return "func"
	case "ts-class", "js-class":
		return "class"
	case "ts-iface":
		return "interface"
	case "ts-type-alias":
		return "type"
	case "ts-enum":
		return "enum"
	case "ts-export-stmt", "js-export-stmt":
		return "export"
	default:
		return ruleID
	}
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

func firstLineOf(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}
