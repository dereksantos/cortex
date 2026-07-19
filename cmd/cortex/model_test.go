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
