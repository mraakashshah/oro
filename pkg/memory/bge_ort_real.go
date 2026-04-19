//go:build integration && cgo && darwin

package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// ortEnvOnce ensures the ORT environment is initialized exactly once per process.
var (
	ortEnvOnce sync.Once
	ortEnvErr  error
)

func initORTEnv() error {
	ortEnvOnce.Do(func() {
		// Allow override of ORT library path via env var; default to ~/.oro/lib.
		libPath := os.Getenv("ORT_LIB_PATH")
		if libPath == "" {
			home, _ := os.UserHomeDir()
			libPath = filepath.Join(home, ".oro", "lib", "onnxruntime.dylib")
		}
		if _, err := os.Stat(libPath); err != nil {
			ortEnvErr = fmt.Errorf("ORT library not found at %s (run oro models prefetch): %w", libPath, err)
			return
		}
		ort.SetSharedLibraryPath(libPath)
		if err := ort.InitializeEnvironment(); err != nil {
			ortEnvErr = fmt.Errorf("ORT InitializeEnvironment: %w", err)
		}
	})
	return ortEnvErr
}

// ortRealSession wraps a DynamicAdvancedSession and adapts it to the ortSession interface.
// needsTokenTypes is true when the underlying model declares token_type_ids as an
// input (BERT-family); we then synthesize an all-zeros tensor of the same shape.
// RoBERTa-family models (e.g. bge-reranker-base) do not declare it.
type ortRealSession struct {
	session         *ort.DynamicAdvancedSession
	needsTokenTypes bool
	outputSize      int
}

// Run tokenizes the sequence into a forward pass. For embedder models the
// output is last_hidden_state[0,0,:] (CLS-token, bgeDim floats). For reranker
// models the output is typically a single logit.
// Input tensors have shape [1, seqLen]. If needsTokenTypes, an all-zeros
// token_type_ids tensor of the same shape is added.
func (s *ortRealSession) Run(tokenIDs, attentionMask []int64) ([]float32, error) {
	seqLen := int64(len(tokenIDs))
	shape := ort.NewShape(1, seqLen)

	idTensor, err := ort.NewTensor(shape, tokenIDs)
	if err != nil {
		return nil, fmt.Errorf("create input_ids tensor: %w", err)
	}
	defer idTensor.Destroy()

	maskTensor, err := ort.NewTensor(shape, attentionMask)
	if err != nil {
		return nil, fmt.Errorf("create attention_mask tensor: %w", err)
	}
	defer maskTensor.Destroy()

	inputs := []ort.Value{idTensor, maskTensor}

	if s.needsTokenTypes {
		tokenTypes := make([]int64, seqLen)
		ttTensor, err := ort.NewTensor(shape, tokenTypes)
		if err != nil {
			return nil, fmt.Errorf("create token_type_ids tensor: %w", err)
		}
		defer ttTensor.Destroy()
		inputs = append(inputs, ttTensor)
	}

	outputs := make([]ort.Value, 1)
	if err := s.session.Run(inputs, outputs); err != nil {
		return nil, fmt.Errorf("ORT run: %w", err)
	}
	if outputs[0] != nil {
		defer outputs[0].Destroy()
	}

	outTensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("unexpected output type from ORT session")
	}
	data := outTensor.GetData()
	wantLen := s.outputSize
	if wantLen == 0 {
		wantLen = bgeDim
	}
	if len(data) < wantLen {
		return nil, fmt.Errorf("ORT output too short: got %d floats, want at least %d", len(data), wantLen)
	}
	result := make([]float32, wantLen)
	copy(result, data[:wantLen])
	return result, nil
}

// Close destroys the underlying DynamicAdvancedSession, releasing its
// native allocator, file handles, and thread pool. Must be called by
// BGEEmbedder.Close to avoid leaking ORT resources.
func (s *ortRealSession) Close() error {
	if s == nil || s.session == nil {
		return nil
	}
	if err := s.session.Destroy(); err != nil {
		return fmt.Errorf("destroy ORT session: %w", err)
	}
	s.session = nil
	return nil
}

// newORTSession creates a DynamicAdvancedSession for a BGE ONNX model.
// Introspects the model to discover which inputs it declares (so we support
// both BERT-family models that require token_type_ids and RoBERTa-family
// models that do not) and which output to bind. needsTokenTypes and
// outputSize are propagated into ortRealSession so Run() can allocate the
// correct tensors and return the correct-sized slice.
func newORTSession(modelPath string) (ortSession, error) {
	if err := initORTEnv(); err != nil {
		return nil, err
	}
	inputInfos, outputInfos, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		return nil, fmt.Errorf("introspect ONNX inputs: %w", err)
	}
	inputNames := make([]string, 0, len(inputInfos))
	needsTokenTypes := false
	for _, info := range inputInfos {
		inputNames = append(inputNames, info.Name)
		if info.Name == "token_type_ids" {
			needsTokenTypes = true
		}
	}
	if len(outputInfos) == 0 {
		return nil, fmt.Errorf("model has no outputs: %s", modelPath)
	}
	// Prefer last_hidden_state (embedder), else fall back to the first output
	// (reranker logits).
	outputName := outputInfos[0].Name
	outputSize := bgeDim
	for _, info := range outputInfos {
		if info.Name == "last_hidden_state" {
			outputName = info.Name
			outputSize = bgeDim
			break
		}
	}
	if outputName != "last_hidden_state" {
		// Reranker: single logit scalar.
		outputSize = 1
	}
	sess, err := ort.NewDynamicAdvancedSession(
		modelPath,
		inputNames,
		[]string{outputName},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("create ORT session: %w", err)
	}
	return &ortRealSession{
		session:         sess,
		needsTokenTypes: needsTokenTypes,
		outputSize:      outputSize,
	}, nil
}
