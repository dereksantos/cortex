package llm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

// TestHugotEmbedLatency_Local times the pure-Go Hugot embedder end-to-end for a
// chosen ONNX variant. Gated behind CORTEX_HUGOT_BENCH (downloads a model and
// runs real inference). Pick the variant via CORTEX_HUGOT_ONNX, default the
// quantized arm64 build:
//
//	CORTEX_HUGOT_BENCH=1 CORTEX_HUGOT_ONNX=onnx/model.onnx go test ./pkg/llm/ -run HugotEmbedLatency_Local -v -timeout 600s
func TestHugotEmbedLatency_Local(t *testing.T) {
	if os.Getenv("CORTEX_HUGOT_BENCH") == "" {
		t.Skip("set CORTEX_HUGOT_BENCH=1 to benchmark the local Hugot embedder")
	}
	onnxFile := os.Getenv("CORTEX_HUGOT_ONNX")
	if onnxFile == "" {
		onnxFile = "onnx/model.onnx"
	}
	ctx := context.Background()

	cache, err := os.MkdirTemp("", "hugot-bench-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cache)

	dl := hugot.NewDownloadOptions()
	dl.OnnxFilePath = onnxFile
	t0 := time.Now()
	path, err := hugot.DownloadModel(ctx, DefaultHugotModel, cache, dl)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	t.Logf("variant=%s download: %v", onnxFile, time.Since(t0))

	session, err := hugot.NewGoSession(ctx)
	if err != nil {
		t.Fatalf("NewGoSession: %v", err)
	}
	defer session.Destroy()

	t1 := time.Now()
	pipe, err := hugot.NewPipeline(session, hugot.FeatureExtractionConfig{
		ModelPath:    path,
		OnnxFilename: onnxFile,
		Name:         "bench",
		Options:      []hugot.FeatureExtractionOption{pipelines.WithNormalization()},
	})
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	t.Logf("pipeline load (one-time): %v", time.Since(t1))

	warm := func(texts []string) time.Duration {
		s := time.Now()
		if _, err := pipe.RunPipeline(ctx, texts); err != nil {
			t.Fatalf("RunPipeline: %v", err)
		}
		return time.Since(s)
	}
	_ = warm([]string{"warmup"}) // first inference pays JIT/alloc

	const n = 10
	var total time.Duration
	for i := 0; i < n; i++ {
		total += warm([]string{"how does the backend verify the client's identity"})
	}
	t.Logf("warm single embed avg over %d: %v", n, total/n)

	batch := make([]string, 8)
	for i := range batch {
		batch[i] = "session decisions and auth notes captured for the turn"
	}
	bt := warm(batch)
	t.Logf("batch-8: %v total (%v/item)", bt, bt/8)
}

// TestHugotEmbedder_WarmAndEmbed_Local exercises the production HugotEmbedder
// object: Embed is non-blocking before warm (returns errEmbedderWarming), a
// background Warm loads the arch-default variant, then Embed returns a 384-d
// vector. Gated behind CORTEX_HUGOT_BENCH.
func TestHugotEmbedder_WarmAndEmbed_Local(t *testing.T) {
	if os.Getenv("CORTEX_HUGOT_BENCH") == "" {
		t.Skip("set CORTEX_HUGOT_BENCH=1 to run the local Hugot warm path")
	}
	e := NewHugotEmbedder()

	// Before warm, Embed must return immediately without blocking on download.
	t0 := time.Now()
	if _, err := e.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("expected not-ready error before warm")
	}
	if d := time.Since(t0); d > 100*time.Millisecond {
		t.Errorf("pre-warm Embed blocked for %v; should be immediate", d)
	}

	// Warm was kicked by the Embed call; poll readiness.
	deadline := time.Now().Add(60 * time.Second)
	for !e.IsEmbeddingAvailable() {
		if time.Now().After(deadline) {
			t.Fatal("model did not warm within 60s")
		}
		time.Sleep(200 * time.Millisecond)
	}

	vec, err := e.Embed(context.Background(), "the client signs each request with a bearer token")
	if err != nil {
		t.Fatalf("Embed after warm: %v", err)
	}
	if len(vec) != 384 {
		t.Errorf("expected 384-d vector, got %d", len(vec))
	}
	t.Logf("warm path OK: variant=%s dims=%d", e.onnxFile, len(vec))
}
