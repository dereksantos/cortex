package main

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// TestSuggestModelsRAMTiers is the table-driven test for the RAM→
// suggestion heuristic: each tier boundary picks the expected tier name
// and code/study models, and the arch + note fields are always populated.
func TestSuggestModelsRAMTiers(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	cases := []struct {
		name      string
		ramBytes  uint64
		wantTier  string
		wantCode  string
		wantStudy string
	}{
		{"zero RAM (undetectable) falls back to minimal", 0, "minimal", "qwen2.5-coder:3b", "qwen2.5-coder:1.5b"},
		{"4 GiB is minimal", 4 * gib, "minimal", "qwen2.5-coder:3b", "qwen2.5-coder:1.5b"},
		{"exactly 8 GiB is small", 8 * gib, "small", "qwen2.5-coder:7b", "qwen2.5-coder:1.5b"},
		{"12 GiB is small", 12 * gib, "small", "qwen2.5-coder:7b", "qwen2.5-coder:1.5b"},
		{"exactly 16 GiB is medium", 16 * gib, "medium", "qwen2.5-coder:14b", "qwen2.5-coder:7b"},
		{"24 GiB is medium", 24 * gib, "medium", "qwen2.5-coder:14b", "qwen2.5-coder:7b"},
		{"exactly 32 GiB is large", 32 * gib, "large", "qwen2.5-coder:32b", "qwen2.5-coder:7b"},
		{"48 GiB is large", 48 * gib, "large", "qwen2.5-coder:32b", "qwen2.5-coder:7b"},
		{"exactly 64 GiB is xlarge", 64 * gib, "xlarge", "qwen2.5-coder:32b", "qwen2.5-coder:14b"},
		{"128 GiB is xlarge", 128 * gib, "xlarge", "qwen2.5-coder:32b", "qwen2.5-coder:14b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := suggestModels(tc.ramBytes, "arm64")
			if got.Tier != tc.wantTier {
				t.Errorf("Tier = %q, want %q", got.Tier, tc.wantTier)
			}
			if got.Code.Model != tc.wantCode {
				t.Errorf("Code.Model = %q, want %q", got.Code.Model, tc.wantCode)
			}
			if got.Study.Model != tc.wantStudy {
				t.Errorf("Study.Model = %q, want %q", got.Study.Model, tc.wantStudy)
			}
			if got.Code.Window <= 0 || got.Study.Window <= 0 {
				t.Errorf("expected positive windows, got code=%d study=%d", got.Code.Window, got.Study.Window)
			}
			if got.Arch != "arm64" {
				t.Errorf("Arch = %q, want arm64", got.Arch)
			}
			if got.Note == "" {
				t.Error("expected a non-empty Note (heuristic disclaimer)")
			}
		})
	}
}

// TestSuggestModelsUndetectableRAMNotesFallback covers the ramBytes==0 path
// specifically: the note must explain the fallback rather than silently
// presenting the minimal tier as if it were a real measurement.
func TestSuggestModelsUndetectableRAMNotesFallback(t *testing.T) {
	got := suggestModels(0, "amd64")
	if !strings.Contains(got.Note, "undetectable") {
		t.Errorf("Note = %q, want it to mention RAM being undetectable", got.Note)
	}
}

// TestSuggestModelsAppleSiliconNote covers the darwin/arm64 unified-memory
// caveat — informational only, must not change the tier/model picks.
func TestSuggestModelsAppleSiliconNote(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple Silicon note only applies on darwin")
	}
	got := suggestModels(16*1024*1024*1024, "arm64")
	if !strings.Contains(got.Note, "unified memory") {
		t.Errorf("Note = %q, want it to mention unified memory on darwin/arm64", got.Note)
	}
}

// TestParseMemTotalKB covers /proc/meminfo parsing in isolation (no live
// filesystem read) so the linux RAM-detection path is exercised on any
// platform running the test suite.
func TestParseMemTotalKB(t *testing.T) {
	cases := []struct {
		name    string
		meminfo string
		want    uint64
		wantErr bool
	}{
		{
			name:    "typical meminfo",
			meminfo: "MemTotal:       16384000 kB\nMemFree:         1000000 kB\n",
			want:    16384000 * 1024,
		},
		{
			name:    "MemTotal not first line",
			meminfo: "SomeOther:   1 kB\nMemTotal:   8192000 kB\n",
			want:    8192000 * 1024,
		},
		{
			name:    "missing MemTotal",
			meminfo: "MemFree: 1000 kB\n",
			wantErr: true,
		},
		{
			name:    "malformed MemTotal line",
			meminfo: "MemTotal:\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMemTotalKB(tc.meminfo)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d want %d", got, tc.want)
			}
		})
	}
}

// TestBuildModelCatalogLiteLLM exercises the catalog path against a canned
// /model/info response (fleetServer + fleetInfoJSON, defined in
// main_test.go) — no live network. Confirms role bindings resolve from
// config and the served-models list reflects the fake backend.
func TestBuildModelCatalogLiteLLM(t *testing.T) {
	srv := fleetServer(t, 200, fleetInfoJSON)
	cfg := &Config{
		Backend: Backend{Endpoint: srv.URL},
		Models: map[string]ModelSpec{
			roleCode:  {Model: "coder", Window: 131072},
			roleStudy: {Model: "reasoner-npu"},
		},
	}

	report := buildModelCatalog(context.Background(), cfg)

	if report.BackendType != "litellm" {
		t.Errorf("BackendType = %q, want litellm (default label)", report.BackendType)
	}
	if !report.BackendReachable {
		t.Fatalf("expected BackendReachable, got Note=%q", report.Note)
	}
	if got := report.Roles[roleCode]; got.Model != "coder" || got.Window != 131072 {
		t.Errorf("code role = %+v", got)
	}
	if got := report.Roles[roleStudy]; got.Model != "reasoner-npu" {
		t.Errorf("study role = %+v", got)
	}
	found := false
	for _, m := range report.ServedModels {
		if m.ID == "coder" && m.ContextLength == 131072 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected served model %q with context 131072, got %+v", "coder", report.ServedModels)
	}

	// Rendering shouldn't panic and should surface both the role bindings
	// and the served models.
	text := renderModelCatalog(report)
	if !strings.Contains(text, "coder") || !strings.Contains(text, "reasoner-npu") {
		t.Errorf("rendered catalog missing expected model ids:\n%s", text)
	}
}

// TestBuildModelCatalogUnreachable covers the "backend unreachable" path:
// exit-0-worthy degradation, not an error. Uses a closed port so the probe
// fails fast without touching the network.
func TestBuildModelCatalogUnreachable(t *testing.T) {
	cfg := &Config{Backend: Backend{Endpoint: "http://127.0.0.1:1"}}

	report := buildModelCatalog(context.Background(), cfg)

	if report.BackendReachable {
		t.Fatal("expected BackendReachable=false for an unreachable backend")
	}
	if report.Note == "" {
		t.Error("expected a Note explaining the unreachable backend")
	}
	// Role bindings must still be present — the config-side picture is
	// shown regardless of backend reachability.
	if _, ok := report.Roles[roleCode]; !ok {
		t.Error("expected code role binding to still be present")
	}

	text := renderModelCatalog(report)
	if !strings.Contains(text, "unreachable") {
		t.Errorf("rendered catalog should mention unreachable:\n%s", text)
	}
}

// TestBuildModelCatalogNon200IsUnreachable covers the LiteLLM-reachable-
// but-erroring case (e.g. a 500) — discoverFleet already treats this as
// nil, so the catalog must degrade the same way as a connection failure.
func TestBuildModelCatalogNon200IsUnreachable(t *testing.T) {
	srv := fleetServer(t, 500, "{}")
	cfg := &Config{Backend: Backend{Endpoint: srv.URL}}

	report := buildModelCatalog(context.Background(), cfg)

	if report.BackendReachable {
		t.Fatal("expected BackendReachable=false on a 500 response")
	}
}

// TestModelInfoThinkingMode covers the fleet's optional thinking_mode
// descriptor (docs/thinking-models.md §2): parsed verbatim when the fleet
// sends one, derived from the legacy bool (true→hybrid, false→none) when it
// doesn't.
func TestModelInfoThinkingMode(t *testing.T) {
	tests := []struct {
		name string
		info ModelInfo
		want string
	}{
		{"explicit levels", ModelInfo{ThinkingMode: "levels"}, "levels"},
		{"legacy true derives hybrid", ModelInfo{Thinking: true}, "hybrid"},
		{"legacy false derives none", ModelInfo{Thinking: false}, "none"},
		{"explicit wins over legacy bool", ModelInfo{Thinking: true, ThinkingMode: "always"}, "always"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.thinkingMode(); got != tt.want {
				t.Errorf("thinkingMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// windowSize falls back to the default when Window is unset, so the gauge never
// divides by zero or shows /0.
func TestWindowSizeFallback(t *testing.T) {
	def := &CortexSession{}
	if got := def.windowSize(); got != fallbackWindow {
		t.Errorf("windowSize() = %d, want fallback %d", got, fallbackWindow)
	}
	sized := &CortexSession{Window: 8192}
	if got := sized.windowSize(); got != 8192 {
		t.Errorf("windowSize() = %d, want 8192", got)
	}
}

func TestParseCtxSize(t *testing.T) {
	msg := "litellm.BadRequestError: request (41193 tokens) exceeds the available context size (32768 tokens)"
	if got := parseCtxSize(msg); got != 32768 {
		t.Errorf("parseCtxSize = %d, want 32768", got)
	}
	if got := parseCtxSize("no numbers here"); got != 0 {
		t.Errorf("parseCtxSize(no match) = %d, want 0", got)
	}
}

func TestStudyWindowResolution(t *testing.T) {
	defer func() { delete(learnedWindows, "m") }()
	cs := &CortexSession{Study: ModelSpec{Model: "m", Window: 32768}}
	if got := cs.studyWindow(); got != 32768 {
		t.Errorf("configured window = %d, want 32768", got)
	}
	learnedWindows["m"] = 16000 // learned beats configured
	if got := cs.studyWindow(); got != 16000 {
		t.Errorf("learned window = %d, want 16000", got)
	}
	empty := &CortexSession{Study: ModelSpec{Model: "x"}}
	if got := empty.studyWindow(); got != studyFallbackWindow {
		t.Errorf("fallback window = %d, want %d", got, studyFallbackWindow)
	}
}

// TestCodeWindowLearnedFromOverflow covers C2: the code role self-calibrates
// on a context-overflow error the same way study already did via
// studyWindow(). The first fixture is pkg/llm/context_overflow_test.go's
// "lemonade wrapped llama-server" case verbatim (the wire shape pkg/llm
// parses into a typed ContextOverflowError; cmd/cortex's parseCtxSize
// regex-matches the same text out of err.Error() for the REPL/discord
// recovery paths, per TestParseCtxSize above).
func TestCodeWindowLearnedFromOverflow(t *testing.T) {
	cases := []struct {
		name       string
		model      string
		configured int
		overflow   string // server message carrying the real limit
		wantReal   int
	}{
		{
			name:       "lemonade wrapped llama-server",
			model:      "coder-a",
			configured: 131072,
			overflow:   "litellm.BadRequestError: request (41193 tokens) exceeds the available context size (16384 tokens)",
			wantReal:   16384,
		},
		{
			name:       "openrouter-shaped message",
			model:      "coder-b",
			configured: 8192,
			overflow:   "local-gw (400): server error: llama-server request failed: request (5012 tokens) exceeds the available context size (4096 tokens)",
			wantReal:   4096,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer delete(learnedWindows, tc.model)
			cs := &CortexSession{Request: &AgentRequest{Model: tc.model}, Window: tc.configured}

			// (b, baseline) configured wins before anything overflowed.
			if got := cs.windowSize(); got != tc.configured {
				t.Fatalf("windowSize() before overflow = %d, want configured %d", got, tc.configured)
			}

			real := parseCtxSize(tc.overflow)
			if real != tc.wantReal {
				t.Fatalf("parseCtxSize(%q) = %d, want %d", tc.overflow, real, tc.wantReal)
			}
			cs.learnWindow(real)

			// (a) learnedWindows records it, keyed by the code model.
			if got, ok := learnedWindows[tc.model]; !ok || got != tc.wantReal {
				t.Errorf("learnedWindows[%q] = %d,%v, want %d,true", tc.model, got, ok, tc.wantReal)
			}

			// (b) subsequent window resolution for sizing prefers the learned
			// value over the originally configured one.
			if got := cs.windowSize(); got != tc.wantReal {
				t.Errorf("windowSize() after overflow = %d, want learned %d", got, tc.wantReal)
			}
		})
	}
}

// TestStudyWindowUnaffectedByCodeOverflow covers (c): learning the code
// model's window from an overflow must not perturb study's own resolution,
// and vice versa — learnedWindows is a single map shared by both roles, but
// keyed per-model, so the two precedence chains (windowSize() for code,
// studyWindow() for study) only interact when the roles are bound to the
// literal same model name.
func TestStudyWindowUnaffectedByCodeOverflow(t *testing.T) {
	defer func() {
		delete(learnedWindows, "coder-model")
		delete(learnedWindows, "study-model")
	}()
	cs := &CortexSession{
		Request: &AgentRequest{Model: "coder-model"},
		Window:  131072,
		Study:   ModelSpec{Model: "study-model", Window: 32768},
	}
	if got := cs.studyWindow(); got != 32768 {
		t.Fatalf("studyWindow() before = %d, want configured 32768", got)
	}
	cs.learnWindow(16384) // simulate a coder-path overflow learn
	if got := cs.studyWindow(); got != 32768 {
		t.Errorf("studyWindow() after code-model learn = %d, want unaffected 32768", got)
	}
	// And the reverse: learning study's own window doesn't perturb the code
	// model's already-learned resolution.
	learnedWindows["study-model"] = 4096
	if got := cs.windowSize(); got != 16384 {
		t.Errorf("windowSize() after study-model learn = %d, want unaffected 16384 (code's own learned value)", got)
	}
}

// CORTEX_LOOP_STUDY_WINDOW overrides every other window source — the
// recursion-experiment knob (force study mode on small digest corpora).
func TestStudyWindowEnvOverride(t *testing.T) {
	t.Setenv("CORTEX_LOOP_STUDY_WINDOW", "8192")
	cs := &CortexSession{Study: ModelSpec{Model: "reasoner", Window: 32768}}
	if got := cs.studyWindow(); got != 8192 {
		t.Errorf("studyWindow() = %d, want 8192 (env override)", got)
	}
	t.Setenv("CORTEX_LOOP_STUDY_WINDOW", "")
	if got := cs.studyWindow(); got != 32768 {
		t.Errorf("studyWindow() = %d, want 32768 (configured)", got)
	}
}
