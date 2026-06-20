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

	// CORTEX_WM_THINK toggles built-in reasoning on hybrid thinking models
	// (reasoner/coder/coder80): "on" → enable_thinking=true, "off" → false,
	// unset → the model's template default. (CORTEX_WM_NOTHINK stays as an
	// off alias.) Lets a single model be compared thinking-on vs -off.
	var kwargs map[string]any
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORTEX_WM_THINK"))) {
	case "on", "true", "1":
		kwargs = map[string]any{"enable_thinking": true}
	case "off", "false", "0":
		kwargs = map[string]any{"enable_thinking": false}
	default:
		if os.Getenv("CORTEX_WM_NOTHINK") != "" {
			kwargs = map[string]any{"enable_thinking": false}
		}
	}
	provider := llm.NewOpenAICompatClient(llm.EndpointConfig{
		Name:               "wm-live",
		BaseURL:            endpoint,
		APIKey:             os.Getenv("CORTEX_WM_KEY"), // empty for local backends; set for OpenRouter
		ChatTemplateKwargs: kwargs,
		Timeout:            10 * time.Minute,
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
		cov, gp      float64
		relays       int
		synth        int
		warm, breaks int
		g, f, u      int
		digestChars  int
		err          string
	}
	// Three regimes so P3 (directed sampling) is isolated against on-blind, not
	// just against off. on-directed is added only when CORTEX_WM_DIRECTED is set.
	type regime struct {
		name     string
		wm       bool
		directed bool
	}
	regimes := []regime{{"off", false, false}, {"on-blind", true, false}}
	if os.Getenv("CORTEX_WM_DIRECTED") != "" {
		regimes = append(regimes, regime{"on-directed", true, true})
	}

	out := map[string]result{}
	curate := os.Getenv("CORTEX_WM_CURATE") != ""
	evicted := 0
	for _, rg := range regimes {
		req := study.StudyRequest{
			Path:             jsonl,
			RelPath:          "events.jsonl",
			Goal:             goal,
			Window:           study.SampleTokenBudget(window, 0), // mirror runStudy's budget plumbing
			NoWorkingMemory:  !rg.wm,
			DirectedSampling: rg.directed,
			Infer:            study.ProviderInfer(provider),
		}
		if rg.wm && curate {
			req.CurateFindings = true
			req.OnEvict = func(study.Finding) { evicted++ }
		}
		start := time.Now()
		res, rerr := study.StudyLoop(context.Background(), req, study.ModelCurator{Provider: provider}, passes)
		dur := time.Since(start)
		r := result{}
		if rerr != nil {
			r.err = rerr.Error()
			out[rg.name] = r
			t.Logf("%-11s ERROR after %s: %v", rg.name, dur.Round(time.Second), rerr)
			continue
		}
		r.cov = 100 * res.CoveragePct
		r.relays = res.FindingRelays
		r.synth = res.SynthesisTerms
		r.warm, r.breaks = res.PrefixWarmPasses, res.PrefixBreaks
		r.g, r.f, r.u = scoreGroundedness(string(data), "json", res)
		if r.g+r.f > 0 {
			r.gp = 100 * float64(r.g) / float64(r.g+r.f)
		}
		r.digestChars = len(strings.Join(res.Digests, ""))
		out[rg.name] = r
		t.Logf("%-11s %s  cov=%.0f%% ground=%.0f%% relays=%d synth=%d digests=%dB stopped=%s",
			rg.name, dur.Round(time.Second), r.cov, r.gp, r.relays, r.synth, r.digestChars, res.Stopped)
	}

	// Emit a compact comparison table to stdout for the eval journal.
	fmt.Printf("\n--- WM live eval (model=%s, window=%d, passes=%d) ---\n", model, window, passes)
	fmt.Printf("%-12s %6s %8s %7s %6s %6s %7s\n", "regime", "cov%", "ground%", "relays", "synth", "warm", "breaks")
	for _, rg := range regimes {
		r := out[rg.name]
		if r.err != "" {
			fmt.Printf("%-12s ERROR: %s\n", rg.name, firstLine(r.err))
			continue
		}
		fmt.Printf("%-12s %5.0f%% %7.0f%% %7d %6d %6d %7d\n", rg.name, r.cov, r.gp, r.relays, r.synth, r.warm, r.breaks)
	}

	if curate {
		fmt.Printf("curation: evicted %d findings (demoted)\n", evicted)
	}

	// The one mechanical assertion independent of model quality: the findings-off
	// baseline can never relay (no prior findings in the prompt).
	if out["off"].err == "" && out["off"].relays != 0 {
		t.Errorf("findings-off must have 0 relays, got %d", out["off"].relays)
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
