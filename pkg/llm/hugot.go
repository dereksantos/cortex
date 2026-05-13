// Package llm provides LLM client implementations
package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

// DefaultHugotModel is the default model for local embeddings.
// all-MiniLM-L12-v2 is a higher-quality model that produces 384-dimensional embeddings.
const DefaultHugotModel = "sentence-transformers/all-MiniLM-L12-v2"

// HugotEmbedder implements the Embedder interface using Hugot's pure Go backend.
// It provides local embeddings without requiring Ollama or external services.
type HugotEmbedder struct {
	modelPath string
	modelName string

	// expectedSHA, when non-empty, is the SHA256 of the trusted
	// model.onnx blob. init() refuses to load a model whose ONNX
	// weights don't match. See verifyModelSHA for the contract.
	expectedSHA string

	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline

	mu      sync.Mutex
	initErr error
	inited  bool
}

// NewHugotEmbedder creates a new HugotEmbedder.
// The embedder lazy-loads the model on first use to avoid slow startup.
func NewHugotEmbedder() *HugotEmbedder {
	return &HugotEmbedder{
		modelName: DefaultHugotModel,
	}
}

// NewHugotEmbedderWithModel creates a HugotEmbedder with a custom model.
func NewHugotEmbedderWithModel(modelName string) *HugotEmbedder {
	return &HugotEmbedder{
		modelName: modelName,
	}
}

// NewHugotEmbedderWithPath creates a HugotEmbedder with a pre-downloaded model path.
func NewHugotEmbedderWithPath(modelPath string) *HugotEmbedder {
	return &HugotEmbedder{
		modelPath: modelPath,
	}
}

// SetExpectedSHA pins the SHA256 of the model.onnx blob to verify on
// init. Call with an empty string to opt out of verification. Must be
// called before the first Embed() call — init() reads the field once
// and ignores later changes.
func (h *HugotEmbedder) SetExpectedSHA(sha string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expectedSHA = sha
}

// init performs lazy initialization of the model.
// It downloads the model if needed and creates the pipeline.
func (h *HugotEmbedder) init() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.inited {
		return h.initErr
	}
	h.inited = true

	// Determine model path
	modelPath := h.modelPath
	if modelPath == "" {
		// Download model to cache directory
		cacheDir, err := h.getCacheDir()
		if err != nil {
			h.initErr = fmt.Errorf("failed to get cache directory: %w", err)
			return h.initErr
		}

		modelPath, err = hugot.DownloadModel(h.modelName, cacheDir, hugot.NewDownloadOptions())
		if err != nil {
			h.initErr = fmt.Errorf("failed to download model %s: %w", h.modelName, err)
			return h.initErr
		}
		h.modelPath = modelPath
	}

	// Pin check: refuse a tampered model before its weights ever
	// influence retrieval. No-op when expectedSHA is empty.
	if err := verifyModelSHA(modelPath, h.expectedSHA); err != nil {
		h.initErr = err
		return h.initErr
	}

	// Create a Go session (pure Go backend, no cgo)
	session, err := hugot.NewGoSession()
	if err != nil {
		h.initErr = fmt.Errorf("failed to create Go session: %w", err)
		return h.initErr
	}
	h.session = session

	// Create feature extraction pipeline with normalization
	config := hugot.FeatureExtractionConfig{
		ModelPath: modelPath,
		Name:      "cortex-embeddings",
		Options: []hugot.FeatureExtractionOption{
			pipelines.WithNormalization(),
		},
	}

	pipeline, err := hugot.NewPipeline(session, config)
	if err != nil {
		session.Destroy()
		h.session = nil
		h.initErr = fmt.Errorf("failed to create pipeline: %w", err)
		return h.initErr
	}
	h.pipeline = pipeline

	log.Printf("[hugot] Initialized embedding model: %s", h.modelName)
	return nil
}

// verifyModelSHA computes the SHA256 of `<modelDir>/model.onnx` and
// compares it to `expected`. Used to refuse a tampered embedding model
// before it can bias retrieval — a poisoned model is a near-perfect
// attack on a RAG-shaped system, because the bias is invisible to
// downstream consumers.
//
// Contract:
//   - `expected == ""` is a no-op (verification opt-out). Existing
//     setups without a pinned SHA continue working unchanged.
//   - Otherwise, model.onnx must exist and its SHA256 must match the
//     expected hex string (case-insensitive comparison via lowercase
//     normalization).
//   - Returns an error naming both digests on mismatch so the operator
//     can compare against the real hub value.
func verifyModelSHA(modelDir, expected string) error {
	if expected == "" {
		return nil
	}
	modelFile := filepath.Join(modelDir, "model.onnx")
	f, err := os.Open(modelFile)
	if err != nil {
		return fmt.Errorf("verify model SHA: open %s: %w", modelFile, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("verify model SHA: read %s: %w", modelFile, err)
	}
	got := hex.EncodeToString(h.Sum(nil))

	if !equalFoldHex(got, expected) {
		return fmt.Errorf("verify model SHA: tampered weights at %s: expected %s, got %s", modelFile, expected, got)
	}
	return nil
}

// equalFoldHex compares two hex strings case-insensitively. Used for
// SHA256 digests where operators sometimes paste uppercase from one
// tool and lowercase from another.
func equalFoldHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ai, bi := a[i], b[i]
		if ai >= 'A' && ai <= 'F' {
			ai += 'a' - 'A'
		}
		if bi >= 'A' && bi <= 'F' {
			bi += 'a' - 'A'
		}
		if ai != bi {
			return false
		}
	}
	return true
}

// getCacheDir returns the directory to cache downloaded models.
func (h *HugotEmbedder) getCacheDir() (string, error) {
	// Use ~/.cache/cortex/models or platform-appropriate cache dir
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	cacheDir := filepath.Join(homeDir, ".cache", "cortex", "models")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}

	return cacheDir, nil
}

// Embed converts text to a vector embedding.
// The model is lazy-loaded on first call.
func (h *HugotEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := h.init(); err != nil {
		return nil, err
	}

	// Check context for cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Run pipeline with single input
	output, err := h.pipeline.RunPipeline([]string{text})
	if err != nil {
		return nil, fmt.Errorf("failed to generate embedding: %w", err)
	}

	if len(output.Embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	return output.Embeddings[0], nil
}

// EmbedBatch converts multiple texts to vector embeddings in a single call.
// This is more efficient than calling Embed multiple times.
func (h *HugotEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if err := h.init(); err != nil {
		return nil, err
	}

	if len(texts) == 0 {
		return nil, nil
	}

	// Check context for cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Run pipeline with batch input
	output, err := h.pipeline.RunPipeline(texts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate embeddings: %w", err)
	}

	return output.Embeddings, nil
}

// IsEmbeddingAvailable checks if the embedder is ready.
// It attempts to initialize and returns true if successful.
func (h *HugotEmbedder) IsEmbeddingAvailable() bool {
	return h.init() == nil
}

// Dimensions returns the embedding dimension for the loaded model.
// Returns 0 if the model is not initialized.
func (h *HugotEmbedder) Dimensions() int {
	if h.pipeline == nil {
		return 0
	}
	// all-MiniLM-L12-v2 produces 384-dimensional embeddings
	return 384
}

// ModelName returns the name of the model being used.
func (h *HugotEmbedder) ModelName() string {
	if h.modelName != "" {
		return h.modelName
	}
	return h.modelPath
}

// Close releases resources used by the embedder.
func (h *HugotEmbedder) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.session != nil {
		h.session.Destroy()
		h.session = nil
	}
	h.pipeline = nil
	h.inited = false
	h.initErr = nil

	return nil
}
