package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
)

// TestOpenAICompatProbeRespectsModelCapabilities validates the
// operator-config path for capability tagging. A bare model id that
// id-pattern inference doesn't recognize (here: "reasoner", which
// happens to serve gpt-oss-20b on the chatterbox fleet) becomes
// CapReasoningSpecialist when declared via ModelCapabilities.
//
// This is the substrate for Path B in
// docs/prompts/loop-codebase-44.md — without it, decide.coding_turn's
// Requires chain can never pick the reasoner for audit/refactor turns.
func TestOpenAICompatProbeRespectsModelCapabilities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": [
				{"id":"reasoner","max_context_window":65536},
				{"id":"coder","max_context_window":65536}
			]
		}`))
	}))
	defer srv.Close()

	probe := NewOpenAICompatProbe(OpenAICompatProbeConfig{
		Endpoint: EndpointConfig{Name: "chatterbox", BaseURL: srv.URL + "/v1"},
		IsLocal:  true,
		ModelCapabilities: map[string][]string{
			"reasoner": {CapReasoning, CapReasoningSpecialist, CapToolCalling},
		},
	})
	models, err := probe.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}

	byID := map[string]ModelInfo{}
	for _, m := range models {
		byID[m.ID] = m
	}

	reasoner, ok := byID["chatterbox/reasoner"]
	if !ok {
		t.Fatalf("reasoner not registered with endpoint prefix; got %v", models)
	}
	if !reasoner.HasCapability(CapReasoningSpecialist) {
		t.Errorf("reasoner missing CapReasoningSpecialist: caps=%v", reasoner.Capabilities)
	}
	if !reasoner.HasCapability(CapReasoning) {
		t.Errorf("reasoner missing CapReasoning: caps=%v", reasoner.Capabilities)
	}

	// coder isn't in ModelCapabilities and the id-pattern inference
	// catches it via the "coder" substring rule — so it should still
	// have CapCoding even without a config override.
	coder := byID["chatterbox/coder"]
	if !coder.HasCapability(CapCoding) {
		t.Errorf("coder lost CapCoding inference: caps=%v", coder.Capabilities)
	}
}

// TestOpenAICompatProbeEndpointLabelsWinOverConfig asserts that when
// the endpoint's /v1/models response advertises labels[] (Lemonade
// does this), those wire-supplied labels take precedence over the
// operator's ModelCapabilities override. Rationale: the wire is the
// ground truth for the running deployment; the config map is a
// fallback for label-less endpoints.
func TestOpenAICompatProbeEndpointLabelsWinOverConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": [
				{"id":"reasoner","labels":["coding"],"max_context_window":65536}
			]
		}`))
	}))
	defer srv.Close()

	probe := NewOpenAICompatProbe(OpenAICompatProbeConfig{
		Endpoint: EndpointConfig{Name: "ep", BaseURL: srv.URL + "/v1"},
		ModelCapabilities: map[string][]string{
			"reasoner": {CapReasoningSpecialist},
		},
	})
	models, err := probe.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("want 1 model, got %d", len(models))
	}
	got := models[0].Capabilities
	sort.Strings(got)
	if got[0] != CapCoding {
		t.Errorf("wire-supplied labels should win; got %v", got)
	}
	if models[0].HasCapability(CapReasoningSpecialist) {
		t.Errorf("config override should NOT apply when wire labels exist; got %v", got)
	}
}

func TestOpenAICompatProbeNoOverrideFallsThroughToInference(t *testing.T) {
	// Missing config + no wire labels = id-pattern inference path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": [
				{"id":"qwen3-coder-30b","max_context_window":65536}
			]
		}`))
	}))
	defer srv.Close()

	probe := NewOpenAICompatProbe(OpenAICompatProbeConfig{
		Endpoint: EndpointConfig{Name: "ep", BaseURL: srv.URL + "/v1"},
	})
	models, err := probe.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !models[0].HasCapability(CapCoding) {
		t.Errorf("inference should pick CapCoding from 'coder' substring; got %v", models[0].Capabilities)
	}
}
