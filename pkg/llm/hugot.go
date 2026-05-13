// Package llm provides LLM client implementations
package llm

import (
	"context"
	"fmt"
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

		modelPath, err = hugot.DownloadModel(context.Background(), h.modelName, cacheDir, hugot.NewDownloadOptions())
		if err != nil {
			h.initErr = fmt.Errorf("failed to download model %s: %w", h.modelName, err)
			return h.initErr
		}
		h.modelPath = modelPath
	}

	// Create a Go session (pure Go backend, no cgo)
	session, err := hugot.NewGoSession(context.Background())
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
	output, err := h.pipeline.RunPipeline(ctx, []string{text})
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
	output, err := h.pipeline.RunPipeline(ctx, texts)
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
