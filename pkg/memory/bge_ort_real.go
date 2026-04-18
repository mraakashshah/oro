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
type ortRealSession struct {
	session *ort.DynamicAdvancedSession
}

// Run tokenizes the sequence into a forward pass through BGE-small.
// Input tensors have shape [1, seqLen]; output is the CLS-token vector [384].
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

	outputs := make([]ort.Value, 1)
	if err := s.session.Run([]ort.Value{idTensor, maskTensor}, outputs); err != nil {
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
	// last_hidden_state shape: [1, seqLen, 384]; CLS token is at position [0,0,:].
	if len(data) < bgeDim {
		return nil, fmt.Errorf("ORT output too short: got %d floats, want at least %d", len(data), bgeDim)
	}
	result := make([]float32, bgeDim)
	copy(result, data[:bgeDim])
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

// newORTSession creates a DynamicAdvancedSession for the BGE-small-en-v1.5 model.
func newORTSession(modelPath string) (ortSession, error) {
	if err := initORTEnv(); err != nil {
		return nil, err
	}
	sess, err := ort.NewDynamicAdvancedSession(
		modelPath,
		[]string{"input_ids", "attention_mask"},
		[]string{"last_hidden_state"},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("create ORT session: %w", err)
	}
	return &ortRealSession{session: sess}, nil
}
