package codesearch_test

import (
	"testing"

	"oro/pkg/codesearch"
)

func TestShouldBypass(t *testing.T) {
	tests := []struct {
		name  string
		input codesearch.ToolInput
		want  bool
	}{
		// --- Should bypass (return true) ---
		{
			name: "small file under 3KB",
			input: codesearch.ToolInput{
				FilePath: "/project/src/small.go",
				FileSize: 2048,
			},
			want: true,
		},
		{
			name: "file exactly at 3KB threshold",
			input: codesearch.ToolInput{
				FilePath: "/project/src/edge.go",
				FileSize: 3072, // exactly 3KB
			},
			want: true,
		},
		{
			name: "JSON config file",
			input: codesearch.ToolInput{
				FilePath: "/project/config.json",
				FileSize: 10000,
			},
			want: true,
		},
		{
			name: "YAML config file",
			input: codesearch.ToolInput{
				FilePath: "/project/deploy.yaml",
				FileSize: 10000,
			},
			want: true,
		},
		{
			name: "YML config file",
			input: codesearch.ToolInput{
				FilePath: "/project/deploy.yml",
				FileSize: 10000,
			},
			want: true,
		},
		{
			name: "markdown file",
			input: codesearch.ToolInput{
				FilePath: "/project/README.md",
				FileSize: 50000,
			},
			want: true,
		},
		{
			name: "TOML config file",
			input: codesearch.ToolInput{
				FilePath: "/project/pyproject.toml",
				FileSize: 8000,
			},
			want: true,
		},
		{
			name: "Go test file",
			input: codesearch.ToolInput{
				FilePath: "/project/pkg/handler/handler_test.go",
				FileSize: 15000,
			},
			want: true,
		},
		{
			name: "test file in nested directory",
			input: codesearch.ToolInput{
				FilePath: "/project/internal/parser/ast_test.go",
				FileSize: 20000,
			},
			want: true,
		},
		{
			name: "explicit offset in Read call",
			input: codesearch.ToolInput{
				FilePath: "/project/src/big.go",
				FileSize: 50000,
				Offset:   100,
			},
			want: true,
		},
		{
			name: "explicit limit in Read call",
			input: codesearch.ToolInput{
				FilePath: "/project/src/big.go",
				FileSize: 50000,
				Limit:    50,
			},
			want: true,
		},
		{
			name: "both offset and limit",
			input: codesearch.ToolInput{
				FilePath: "/project/src/big.go",
				FileSize: 50000,
				Offset:   10,
				Limit:    20,
			},
			want: true,
		},
		{
			name: "file in .claude directory",
			input: codesearch.ToolInput{
				FilePath: "/project/.claude/settings.json",
				FileSize: 5000,
			},
			want: true,
		},
		{
			name: "file in nested .claude directory",
			input: codesearch.ToolInput{
				FilePath: "/project/.claude/hooks/pre-tool-use.sh",
				FileSize: 10000,
			},
			want: true,
		},
		{
			name: "txt file (non-code)",
			input: codesearch.ToolInput{
				FilePath: "/project/notes.txt",
				FileSize: 10000,
			},
			want: true,
		},
		{
			name: "lock file",
			input: codesearch.ToolInput{
				FilePath: "/project/go.sum",
				FileSize: 100000,
			},
			want: true,
		},
		{
			name: "env file",
			input: codesearch.ToolInput{
				FilePath: "/project/.env",
				FileSize: 500,
			},
			want: true,
		},
		{
			name: "gitignore file",
			input: codesearch.ToolInput{
				FilePath: "/project/.gitignore",
				FileSize: 200,
			},
			want: true,
		},

		// --- Multi-language test file bypass ---
		{
			name: "Python test file (test_ prefix)",
			input: codesearch.ToolInput{
				FilePath: "/project/tests/test_handler.py",
				FileSize: 15000,
			},
			want: true,
		},
		{
			name: "TypeScript test file (.test.ts)",
			input: codesearch.ToolInput{
				FilePath: "/project/src/handler.test.ts",
				FileSize: 15000,
			},
			want: true,
		},
		{
			name: "JavaScript spec file (.spec.js)",
			input: codesearch.ToolInput{
				FilePath: "/project/src/handler.spec.js",
				FileSize: 15000,
			},
			want: true,
		},

		// --- Should NOT bypass (return false) — intercept for summarization ---
		{
			name: "large Go source file",
			input: codesearch.ToolInput{
				FilePath: "/project/pkg/server/handler.go",
				FileSize: 15000,
			},
			want: false,
		},
		{
			name: "large Go source file just above threshold",
			input: codesearch.ToolInput{
				FilePath: "/project/pkg/server/handler.go",
				FileSize: 3073,
			},
			want: false,
		},
		{
			name: "large Go file at root",
			input: codesearch.ToolInput{
				FilePath: "/project/main.go",
				FileSize: 5000,
			},
			want: false,
		},
		{
			name: "large Python source file",
			input: codesearch.ToolInput{
				FilePath: "/project/src/server.py",
				FileSize: 10000,
			},
			want: false,
		},
		{
			name: "large TypeScript source file",
			input: codesearch.ToolInput{
				FilePath: "/project/src/server.ts",
				FileSize: 10000,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codesearch.ShouldBypass(tt.input)
			if got != tt.want {
				t.Errorf("codesearch.ShouldBypass(%+v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
