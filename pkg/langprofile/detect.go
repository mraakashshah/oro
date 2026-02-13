package langprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// DetectExistingTools scans the project for existing tool configurations
// and adapts the language profile to use detected tools instead of defaults.
// Priority: .oro/config.yaml overrides > detected tools > profile defaults.
//
//oro:testonly
func DetectExistingTools(projectRoot string, profile LangProfile) LangProfile {
	// 1. Check for .oro/config.yaml override first (highest priority)
	if adapted, ok := loadOroConfig(projectRoot, profile); ok {
		return adapted
	}

	// 2. Detect tools based on language
	switch profile.Language {
	case "typescript", "javascript":
		return detectJSTools(projectRoot, profile)
	case "python":
		return detectPythonTools(projectRoot, profile)
	default:
		return profile
	}
}

// loadOroConfig loads .oro/config.yaml if it exists and adapts the profile.
// Returns the adapted profile and true if config was loaded, otherwise original profile and false.
func loadOroConfig(projectRoot string, profile LangProfile) (LangProfile, bool) {
	configPath := filepath.Join(projectRoot, ".oro", "config.yaml")
	//nolint:gosec // configPath is constructed from projectRoot parameter
	data, err := os.ReadFile(configPath)
	if err != nil {
		return profile, false
	}

	var config struct {
		Language   string `yaml:"language"`
		Formatters []struct {
			Name string `yaml:"name"`
			Cmd  string `yaml:"cmd"`
		} `yaml:"formatters"`
		Linters []struct {
			Name string `yaml:"name"`
			Cmd  string `yaml:"cmd"`
		} `yaml:"linters"`
	}

	if err := yaml.Unmarshal(data, &config); err != nil {
		return profile, false
	}

	// Apply config overrides
	adapted := profile

	if len(config.Formatters) > 0 {
		adapted.Formatters = make([]Tool, len(config.Formatters))
		for i, f := range config.Formatters {
			adapted.Formatters[i] = Tool{
				Name: f.Name,
				Cmd:  f.Cmd,
			}
		}
	}

	if len(config.Linters) > 0 {
		adapted.Linters = make([]Tool, len(config.Linters))
		for i, l := range config.Linters {
			adapted.Linters[i] = Tool{
				Name: l.Name,
				Cmd:  l.Cmd,
			}
		}
	}

	return adapted, true
}

// detectJSTools detects JavaScript/TypeScript tools in package.json.
func detectJSTools(projectRoot string, profile LangProfile) LangProfile {
	packageJSONPath := filepath.Join(projectRoot, "package.json")
	//nolint:gosec // packageJSONPath is constructed from projectRoot parameter
	data, err := os.ReadFile(packageJSONPath)
	if err != nil {
		return profile
	}

	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}

	if err := json.Unmarshal(data, &pkg); err != nil {
		// Parse error: skip JS detection
		return profile
	}

	adapted := profile

	// Detect eslint in dependencies or devDependencies
	if hasPackage(pkg.Dependencies, "eslint") || hasPackage(pkg.DevDependencies, "eslint") {
		adapted.Linters = []Tool{
			{
				Name:        "eslint",
				Cmd:         "eslint .",
				DetectCmd:   "eslint --version",
				InstallHint: "npm install eslint",
			},
		}
	}

	return adapted
}

// detectPythonTools detects Python tools in pyproject.toml.
func detectPythonTools(projectRoot string, profile LangProfile) LangProfile {
	pyprojectPath := filepath.Join(projectRoot, "pyproject.toml")
	//nolint:gosec // pyprojectPath is constructed from projectRoot parameter
	data, err := os.ReadFile(pyprojectPath)
	if err != nil {
		return profile
	}

	var pyproject map[string]interface{}
	if err := toml.Unmarshal(data, &pyproject); err != nil {
		// Parse error: skip Python detection
		return profile
	}

	adapted := profile

	// Detect black in [tool.black]
	if tool, ok := pyproject["tool"].(map[string]interface{}); ok {
		if _, hasBlack := tool["black"]; hasBlack {
			adapted.Formatters = []Tool{
				{
					Name:        "black",
					Cmd:         "black .",
					DetectCmd:   "black --version",
					InstallHint: "pip install black",
				},
			}
		}
	}

	return adapted
}

// hasPackage checks if a package name exists in the dependencies map.
func hasPackage(deps map[string]string, name string) bool {
	if deps == nil {
		return false
	}
	// Check for exact match or scoped package (e.g., @eslint/...)
	for key := range deps {
		if key == name || strings.HasPrefix(key, "@") && strings.Contains(key, name) {
			return true
		}
	}
	return false
}
