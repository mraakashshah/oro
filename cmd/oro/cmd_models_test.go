package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/modelartifacts"
)

// testModelSpec builds a ModelSpec whose SHA256 matches the given content bytes.
func testModelSpec(name, filename string, content []byte) modelartifacts.ModelSpec {
	h := sha256.Sum256(content)
	return modelartifacts.ModelSpec{
		Name:     name,
		URL:      "http://fake.example.com/" + name + "/" + filename,
		SHA256:   hex.EncodeToString(h[:]),
		Filename: filename,
	}
}

func TestCmdModelsList(t *testing.T) {
	t.Run("prints_header_and_one_row_per_spec", func(t *testing.T) {
		modelDir := t.TempDir()
		specs := []modelartifacts.ModelSpec{
			{Name: "bge-small", URL: "http://example.com/bge-small/model.onnx", SHA256: "abc123def456", Filename: "model.onnx"},
		}

		cmd := newModelsListCmdWithSpecs(specs, modelDir)
		var out strings.Builder
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}

		output := out.String()
		for _, col := range []string{"NAME", "FILENAME", "SHA256", "PRESENT", "PATH"} {
			if !strings.Contains(output, col) {
				t.Errorf("expected column %q in output:\n%s", col, output)
			}
		}
		if !strings.Contains(output, "bge-small") {
			t.Errorf("expected model name in output:\n%s", output)
		}
		if !strings.Contains(output, "model.onnx") {
			t.Errorf("expected filename in output:\n%s", output)
		}
		if !strings.Contains(output, "abc123def456") {
			t.Errorf("expected SHA256 in output:\n%s", output)
		}
		if !strings.Contains(output, "false") {
			t.Errorf("expected Present=false for missing file:\n%s", output)
		}
		expectedPath := filepath.Join(modelDir, "bge-small", "model.onnx")
		if !strings.Contains(output, expectedPath) {
			t.Errorf("expected path %q in output:\n%s", expectedPath, output)
		}
	})

	t.Run("empty_specs_exits_0", func(t *testing.T) {
		cmd := newModelsListCmdWithSpecs([]modelartifacts.ModelSpec{}, t.TempDir())
		if err := cmd.Execute(); err != nil {
			t.Fatalf("expected exit 0 with no specs: %v", err)
		}
	})

	t.Run("present_true_when_file_exists", func(t *testing.T) {
		modelDir := t.TempDir()
		spec := modelartifacts.ModelSpec{Name: "tok", URL: "http://x/tok.json", SHA256: "aaa", Filename: "tokenizer.json"}

		dir := filepath.Join(modelDir, spec.Name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, spec.Filename), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}

		cmd := newModelsListCmdWithSpecs([]modelartifacts.ModelSpec{spec}, modelDir)
		var out strings.Builder
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}
		if !strings.Contains(out.String(), "true") {
			t.Errorf("expected Present=true when file exists:\n%s", out.String())
		}
	})

	t.Run("multiple_specs_each_get_a_row", func(t *testing.T) {
		modelDir := t.TempDir()
		specs := []modelartifacts.ModelSpec{
			{Name: "model-a", URL: "http://x/a.onnx", SHA256: "sha_a", Filename: "model.onnx"},
			{Name: "model-b", URL: "http://x/b.onnx", SHA256: "sha_b", Filename: "model.onnx"},
		}

		cmd := newModelsListCmdWithSpecs(specs, modelDir)
		var out strings.Builder
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}
		for _, name := range []string{"model-a", "model-b"} {
			if !strings.Contains(out.String(), name) {
				t.Errorf("expected %q in list output:\n%s", name, out.String())
			}
		}
	})
}

func TestCmdModelsVerify(t *testing.T) {
	t.Run("exit_0_when_no_specs", func(t *testing.T) {
		cmd := newModelsVerifyCmdWithSpecs([]modelartifacts.ModelSpec{}, t.TempDir())
		cmd.SilenceErrors = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("expected exit 0 with no specs: %v", err)
		}
	})

	t.Run("exit_0_when_all_present_and_matching", func(t *testing.T) {
		modelDir := t.TempDir()
		content := []byte("fake onnx model data")
		spec := testModelSpec("good-model", "model.onnx", content)

		dir := filepath.Join(modelDir, spec.Name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, spec.Filename), content, 0o600); err != nil {
			t.Fatal(err)
		}

		cmd := newModelsVerifyCmdWithSpecs([]modelartifacts.ModelSpec{spec}, modelDir)
		cmd.SilenceErrors = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("expected exit 0 for matching model: %v", err)
		}
	})

	t.Run("exit_1_on_sha256_mismatch_with_stderr_line", func(t *testing.T) {
		modelDir := t.TempDir()
		spec := modelartifacts.ModelSpec{Name: "bad-model", URL: "http://x/bad.onnx", SHA256: "wrongdigest", Filename: "model.onnx"}

		dir := filepath.Join(modelDir, spec.Name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, spec.Filename), []byte("wrong content"), 0o600); err != nil {
			t.Fatal(err)
		}

		cmd := newModelsVerifyCmdWithSpecs([]modelartifacts.ModelSpec{spec}, modelDir)
		cmd.SilenceErrors = true
		var errBuf strings.Builder
		cmd.SetErr(&errBuf)
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error (exit 1) on SHA256 mismatch")
		}
		if !strings.Contains(errBuf.String(), "bad-model") {
			t.Errorf("expected model name in stderr:\n%s", errBuf.String())
		}
	})

	t.Run("exit_1_on_missing_file_with_stderr_line", func(t *testing.T) {
		modelDir := t.TempDir()
		spec := modelartifacts.ModelSpec{Name: "missing-model", URL: "http://x/m.onnx", SHA256: "abc", Filename: "model.onnx"}

		cmd := newModelsVerifyCmdWithSpecs([]modelartifacts.ModelSpec{spec}, modelDir)
		cmd.SilenceErrors = true
		var errBuf strings.Builder
		cmd.SetErr(&errBuf)
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error (exit 1) for missing file")
		}
		if !strings.Contains(errBuf.String(), "missing-model") {
			t.Errorf("expected model name in stderr:\n%s", errBuf.String())
		}
	})

	t.Run("multiple_failures_each_get_a_stderr_line", func(t *testing.T) {
		modelDir := t.TempDir()
		specs := []modelartifacts.ModelSpec{
			{Name: "fail-a", URL: "http://x/a.onnx", SHA256: "wrong_a", Filename: "model.onnx"},
			{Name: "fail-b", URL: "http://x/b.onnx", SHA256: "wrong_b", Filename: "model.onnx"},
		}
		for _, s := range specs {
			dir := filepath.Join(modelDir, s.Name)
			_ = os.MkdirAll(dir, 0o750)
			_ = os.WriteFile(filepath.Join(dir, s.Filename), []byte("bad"), 0o600)
		}

		cmd := newModelsVerifyCmdWithSpecs(specs, modelDir)
		cmd.SilenceErrors = true
		var errBuf strings.Builder
		cmd.SetErr(&errBuf)
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for multiple failures")
		}
		for _, name := range []string{"fail-a", "fail-b"} {
			if !strings.Contains(errBuf.String(), name) {
				t.Errorf("expected %q in stderr:\n%s", name, errBuf.String())
			}
		}
	})
}

func TestCmdModelsPrefetchDryRun(t *testing.T) {
	t.Run("prints_url_and_target_path_without_writing", func(t *testing.T) {
		modelDir := t.TempDir()
		spec := modelartifacts.ModelSpec{
			Name:     "test-model",
			URL:      "http://fake.example.com/test-model/model.onnx",
			SHA256:   "abc123",
			Filename: "model.onnx",
		}

		cmd := newModelsPrefetchCmdWithSpecs([]modelartifacts.ModelSpec{spec}, modelDir)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--dry-run"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, spec.URL) {
			t.Errorf("expected URL %q in dry-run output:\n%s", spec.URL, output)
		}
		expectedPath := filepath.Join(modelDir, spec.Name, spec.Filename)
		if !strings.Contains(output, expectedPath) {
			t.Errorf("expected path %q in dry-run output:\n%s", expectedPath, output)
		}

		// File must NOT be written.
		if _, err := os.Stat(expectedPath); !os.IsNotExist(err) {
			t.Error("dry-run must not write any files")
		}
	})

	t.Run("empty_specs_dry_run_exits_0", func(t *testing.T) {
		cmd := newModelsPrefetchCmdWithSpecs([]modelartifacts.ModelSpec{}, t.TempDir())
		cmd.SetArgs([]string{"--dry-run"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("expected exit 0 with no specs: %v", err)
		}
	})

	t.Run("multiple_specs_each_printed", func(t *testing.T) {
		modelDir := t.TempDir()
		specs := []modelartifacts.ModelSpec{
			{Name: "alpha", URL: "http://x/alpha.onnx", SHA256: "s1", Filename: "model.onnx"},
			{Name: "beta", URL: "http://x/beta.json", SHA256: "s2", Filename: "tokenizer.json"},
		}

		cmd := newModelsPrefetchCmdWithSpecs(specs, modelDir)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--dry-run"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}

		output := out.String()
		for _, s := range specs {
			if !strings.Contains(output, s.URL) {
				t.Errorf("expected URL %q in dry-run output:\n%s", s.URL, output)
			}
		}
	})
}
