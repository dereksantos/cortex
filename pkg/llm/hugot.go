// Package llm provides LLM client implementations
package llm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

// DefaultHugotModel is the default model for local embeddings.
// all-MiniLM-L12-v2 is a higher-quality model that produces 384-dimensional embeddings.
const DefaultHugotModel = "sentence-transformers/all-MiniLM-L12-v2"

// fallbackOnnxFile is the universal fp32 ONNX export — slower than the
// quantized variants but present and loadable on every platform. Used as the
// default for unknown arches and as the retry when an arch-specific variant
// fails to fetch or load.
const fallbackOnnxFile = "onnx/model.onnx"

// errEmbedderWarming is returned by Embed/EmbedBatch while the model is still
// loading in the background. Callers treat it like any embed failure and fall
// back to text search; once warm, subsequent calls succeed. It exists so a
// first-run model download never blocks the foreground retrieval path.
var errEmbedderWarming = errors.New("hugot: embedder warming up")

// HugotEmbedder implements the Embedder interface using Hugot's pure Go backend
// (NewGoSession — no cgo, no onnxruntime shared library), so it cross-compiles
// and ships with the binary. The model weights are fetched once on first use
// and cached under ~/.cache/cortex/models. Loading happens in a background
// goroutine (Warm); Embed is non-blocking and reports not-ready until the
// pipeline is live.
type HugotEmbedder struct {
	modelPath string
	modelName string
	onnxFile  string // ONNX variant within the model repo (e.g. onnx/model_qint8_arm64.onnx)

	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline

	mu      sync.Mutex   // guards load() (download + session + pipeline)
	ready   atomic.Bool  // pipeline is loaded and usable
	warming atomic.Bool  // a background load goroutine is in flight
	initErr atomic.Value // last load error (error), for IsEmbeddingAvailable diagnostics
}

// defaultHugotOnnxFile picks the ONNX variant: a quantized int8 build matched
// to the CPU arch (≈3× faster + ≈4× smaller than fp32 on the pure-Go backend),
// falling back to the universal fp32 export on other arches. Override with
// CORTEX_HUGOT_ONNX (e.g. "onnx/model.onnx" to force fp32).
func defaultHugotOnnxFile() string {
	if v := strings.TrimSpace(os.Getenv("CORTEX_HUGOT_ONNX")); v != "" {
		return v
	}
	switch runtime.GOARCH {
	case "arm64":
		return "onnx/model_qint8_arm64.onnx"
	case "amd64":
		return "onnx/model_quint8_avx2.onnx"
	default:
		return fallbackOnnxFile
	}
}

// NewHugotEmbedder creates a new HugotEmbedder.
// The embedder lazy-loads the model on first use to avoid slow startup.
func NewHugotEmbedder() *HugotEmbedder {
	return &HugotEmbedder{
		modelName: DefaultHugotModel,
		onnxFile:  defaultHugotOnnxFile(),
	}
}

// NewHugotEmbedderWithModel creates a HugotEmbedder with a custom model.
func NewHugotEmbedderWithModel(modelName string) *HugotEmbedder {
	return &HugotEmbedder{
		modelName: modelName,
		onnxFile:  defaultHugotOnnxFile(),
	}
}

// NewHugotEmbedderWithPath creates a HugotEmbedder with a pre-downloaded model path.
func NewHugotEmbedderWithPath(modelPath string) *HugotEmbedder {
	return &HugotEmbedder{
		modelPath: modelPath,
		onnxFile:  defaultHugotOnnxFile(),
	}
}

// Warm starts loading the model in the background if it isn't ready and no
// load is already in flight. Non-blocking. Call it at startup so the one-time
// download/load happens off the first query's critical path. Idempotent and
// safe to call from any goroutine.
func (h *HugotEmbedder) Warm() {
	if h.ready.Load() {
		return
	}
	if !h.warming.CompareAndSwap(false, true) {
		return // a load is already running
	}
	go func() {
		defer h.warming.Store(false)
		err := h.load(h.onnxFile)
		if err != nil && h.onnxFile != fallbackOnnxFile {
			// The arch-specific (likely quantized) variant didn't fetch/load;
			// retry with the universal fp32 export so the embedder still works.
			log.Printf("[hugot] variant %s failed (%v); falling back to %s", h.onnxFile, err, fallbackOnnxFile)
			h.mu.Lock()
			h.onnxFile = fallbackOnnxFile
			h.mu.Unlock()
			err = h.load(fallbackOnnxFile)
		}
		if err != nil {
			h.initErr.Store(err)
			return
		}
		h.ready.Store(true)
		log.Printf("[hugot] embedding model ready: %s (%s)", h.modelName, h.onnxFile)
	}()
}

// load downloads (if needed) and builds the pipeline for a specific ONNX
// variant. Blocking; only ever called from the Warm goroutine.
func (h *HugotEmbedder) load(onnxFile string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	modelPath := h.modelPath
	if modelPath == "" {
		cacheDir, err := h.getCacheDir()
		if err != nil {
			return fmt.Errorf("failed to get cache directory: %w", err)
		}
		opts := hugot.NewDownloadOptions()
		opts.OnnxFilePath = onnxFile
		modelPath, err = hugot.DownloadModel(context.Background(), h.modelName, cacheDir, opts)
		if err != nil {
			return fmt.Errorf("failed to download model %s (%s): %w", h.modelName, onnxFile, err)
		}
		h.modelPath = modelPath
	}

	// Create a Go session (pure Go backend, no cgo)
	session, err := hugot.NewGoSession(context.Background())
	if err != nil {
		return fmt.Errorf("failed to create Go session: %w", err)
	}

	// Create feature extraction pipeline with normalization, pinned to the
	// chosen ONNX variant (the model repo ships several).
	config := hugot.FeatureExtractionConfig{
		ModelPath:    modelPath,
		OnnxFilename: onnxFile,
		Name:         "cortex-embeddings",
		Options: []hugot.FeatureExtractionOption{
			pipelines.WithNormalization(),
		},
	}
	pipeline, err := hugot.NewPipeline(session, config)
	if err != nil {
		session.Destroy()
		return fmt.Errorf("failed to create pipeline (%s): %w", onnxFile, err)
	}
	h.session = session
	h.pipeline = pipeline
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

// Embed converts text to a vector embedding. Non-blocking with respect to model
// loading: if the model isn't ready it kicks off a background warm and returns
// errEmbedderWarming immediately, so a cold first-run download never stalls the
// caller. Once warm, subsequent calls run inference normally.
func (h *HugotEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if !h.ready.Load() {
		h.Warm()
		return nil, errEmbedderWarming
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
// This is more efficient than calling Embed multiple times. Like Embed, it is
// non-blocking on model load and returns errEmbedderWarming until ready.
func (h *HugotEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if !h.ready.Load() {
		h.Warm()
		return nil, errEmbedderWarming
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

// IsEmbeddingAvailable reports whether the model is loaded and usable. It is
// non-blocking — it kicks off a background warm if needed and returns the
// current readiness, never waiting on the download. Cheap to call on a hot path.
func (h *HugotEmbedder) IsEmbeddingAvailable() bool {
	if h.ready.Load() {
		return true
	}
	h.Warm()
	return false
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
	h.ready.Store(false)

	return nil
}
