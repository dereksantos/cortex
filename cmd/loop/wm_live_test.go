package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/study"
	"github.com/dereksantos/cortex/pkg/llm"
)

// TestWMEvalLive is the P1 working-memory eval against a real local model,
// bypassing the loop's config/fleet resolution so it runs even when the
// configured backend is down. It drives StudyLoop directly with an
// OpenAI-compatible provider (Ollama by default) and reuses the study-eval
// scorer, comparing findings OFF (baseline) vs ON over the same fixture.
//
// Gated behind CORTEX_WM_LIVE so it never runs in normal CI. Run with:
//
//	CORTEX_WM_LIVE=1 go test ./cmd/loop/ -run TestWMEvalLive -v -timeout 30m
//
// Overrides: CORTEX_WM_ENDPOINT (default http://localhost:11434/v1),
// CORTEX_WM_MODEL (default qwen2.5-coder:1.5b), CORTEX_WM_WINDOW (8192),
// CORTEX_WM_PASSES (3).
func TestWMEvalLive(t *testing.T) {
	if os.Getenv("CORTEX_WM_LIVE") == "" {
		t.Skip("set CORTEX_WM_LIVE=1 to run the live working-memory eval")
	}
	endpoint := envOr("CORTEX_WM_ENDPOINT", "http://localhost:11434/v1")
	model := envOr("CORTEX_WM_MODEL", "qwen2.5-coder:1.5b")
	window := envInt("CORTEX_WM_WINDOW", 8192)
	passes := envInt("CORTEX_WM_PASSES", 3)

	provider := llm.NewOpenAICompatClient(llm.EndpointConfig{
		Name:    "wm-live",
		BaseURL: endpoint,
		Timeout: 10 * time.Minute,
	})
	provider.SetModel(model)
	provider.SetTemperature(0)
	provider.SetMaxTokens(study.CompletionTokenBudget(window, 0))

	jsonl, err := ensureStudyEvalJSONL()
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	data, _ := os.ReadFile(jsonl)
	goal := "which services report errors and what kinds of errors appear"

	type result struct {
		cov, gp     float64
		relays      int
		g, f, u     int
		digestChars int
		err         string
	}
	out := map[bool]result{}

	curate := os.Getenv("CORTEX_WM_CURATE") != ""
	evicted := 0
	for _, wm := range []bool{false, true} {
		req := study.StudyRequest{
			Path:            jsonl,
			RelPath:         "events.jsonl",
			Goal:            goal,
			Window:          study.SampleTokenBudget(window, 0), // mirror runStudy's budget plumbing
			NoWorkingMemory: !wm,
			Infer:           study.ProviderInfer(provider),
		}
		if wm && curate {
			req.CurateFindings = true
			req.OnEvict = func(study.Finding) { evicted++ }
		}
		start := time.Now()
		res, rerr := study.StudyLoop(context.Background(), req, study.ModelCurator{Provider: provider}, passes)
		dur := time.Since(start)
		r := result{}
		if rerr != nil {
			r.err = rerr.Error()
			out[wm] = r
			t.Logf("findings=%v: ERROR after %s: %v", wm, dur.Round(time.Second), rerr)
			continue
		}
		r.cov = 100 * res.CoveragePct
		r.relays = res.FindingRelays
		r.g, r.f, r.u = scoreGroundedness(string(data), "json", res)
		if r.g+r.f > 0 {
			r.gp = 100 * float64(r.g) / float64(r.g+r.f)
		}
		r.digestChars = len(strings.Join(res.Digests, ""))
		out[wm] = r
		t.Logf("findings=%v: %s  cov=%.0f%% ground=%.0f%% relays=%d digests=%dB stopped=%s",
			wm, dur.Round(time.Second), r.cov, r.gp, r.relays, r.digestChars, res.Stopped)
	}

	// Emit a compact comparison table to stdout for the eval journal.
	fmt.Printf("\n--- WM live eval (model=%s, window=%d, passes=%d) ---\n", model, window, passes)
	fmt.Printf("%-9s %6s %8s %7s %9s\n", "findings", "cov%", "ground%", "relays", "digestB")
	for _, wm := range []bool{false, true} {
		r := out[wm]
		label := "off"
		if wm {
			label = "on"
		}
		if r.err != "" {
			fmt.Printf("%-9s ERROR: %s\n", label, firstLine(r.err))
			continue
		}
		fmt.Printf("%-9s %5.0f%% %7.0f%% %7d %9d\n", label, r.cov, r.gp, r.relays, r.digestChars)
	}

	if curate {
		fmt.Printf("curation: evicted %d findings (demoted)\n", evicted)
	}

	// The one mechanical P1 assertion that doesn't depend on model quality:
	// findings OFF can never relay (no prior findings in the prompt).
	if out[false].err == "" && out[false].relays != 0 {
		t.Errorf("findings-off must have 0 relays, got %d", out[false].relays)
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
