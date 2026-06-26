package capture

import (
	"context"
	"os"
	"testing"
	"time"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/storage"
	pkgcognition "github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

// TestCapture_SemanticRoundTrip_Live exercises the full embed-on-capture →
// vector-search → live-rerank path against the real fleet. It is gated behind
// CORTEX_LIVE_FLEET because it needs chatterbox:4000 (embedder + qwen3-4b).
//
// The query shares NO content words with the captured turn, so a lexical text
// search cannot match it — only the stored embedding can. A hit therefore
// proves both halves of semantic retrieval are wired: the async embed write
// path (Capture.embedEvent → StoreEmbedding) and the read path (Reflex →
// SearchByVector), with Reflect reranking on a live chat model in Full mode.
//
//	CORTEX_LIVE_FLEET=1 go test ./internal/capture/ -run SemanticRoundTrip_Live -v
func TestCapture_SemanticRoundTrip_Live(t *testing.T) {
	if os.Getenv("CORTEX_LIVE_FLEET") == "" {
		t.Skip("set CORTEX_LIVE_FLEET=1 to run against chatterbox:4000")
	}
	const base = "http://chatterbox:4000/v1"

	tempDir, err := os.MkdirTemp("", "cortex-semantic-live-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{ContextDir: tempDir, ProjectRoot: tempDir}
	store, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	embedder := llm.NewOpenAICompatEmbedder(llm.EndpointConfig{Name: "embedder", BaseURL: base}, "embedder")

	rerank := llm.NewOpenAICompatClient(llm.EndpointConfig{
		Name: "rerank", BaseURL: base,
		ChatTemplateKwargs: map[string]any{"enable_thinking": false},
	})
	rerank.SetModel("qwen3-4b")

	cortex, err := intcognition.New(store, rerank, nil, embedder, cfg)
	if err != nil {
		t.Fatalf("cognition.New: %v", err)
	}

	cap := NewWithStorage(cfg, store)
	cap.SetEmbedder(embedder)

	event := &events.Event{
		ID:        "evt-sem-1",
		Source:    events.SourceGeneric,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "loop",
		ToolInput: map[string]interface{}{
			"type":        "turn",
			"user_prompt": "we sign every request with a JWT bearer token instead of server-side sessions",
		},
		ToolResult: "noted the token-based stateless auth decision",
		Context:    events.EventContext{SessionID: "sem-1", ProjectPath: tempDir},
	}
	if err := cap.CaptureEvent(event); err != nil {
		t.Fatalf("CaptureEvent: %v", err)
	}

	// embedEvent is async (fire-and-forget); poll until the vector lands.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if n, _ := store.GetEmbeddingCount(); n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no embedding stored within 20s — embed-on-capture write path is broken")
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Lexically-disjoint query: shares no words with the captured turn.
	res, err := cortex.Retrieve(context.Background(), pkgcognition.Query{
		Text:  "how does the client prove its identity to the backend",
		Limit: 5,
	}, pkgcognition.Full)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res == nil || len(res.Results) == 0 {
		t.Fatal("semantic retrieval returned empty — the embedding did not match a related-but-disjoint query")
	}
	found := false
	for _, r := range res.Results {
		if sm, _ := r.Metadata["semantic_match"].(bool); sm {
			found = true
		}
		t.Logf("hit: cat=%s score=%.3f semantic=%v content=%q", r.Category, r.Score, r.Metadata["semantic_match"], r.Content)
	}
	if !found {
		t.Error("got results but none were semantic_match — vector search did not fire (text fallback only)")
	}
}
