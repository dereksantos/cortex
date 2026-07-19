// model.go — `cortex model [--json]` (docs/completion-roadmap.md Track C1):
// catalog the current code/study role bindings + what the configured
// backend actually serves, and suggest a config `models` block sized to
// this machine's detected RAM. The role surface here is deliberately the
// two live roles (code, study) — see Track E's role collapse; this
// command does not resurrect the dormant reason/fast/embed/rerank roles.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// modelRoles is the canonical role list this command inspects, in display
// order. Kept local (not rolePolicies' full map) since `cortex model` only
// speaks to the two roles that are actually on a live path.
var modelRoles = []string{roleCode, roleStudy}

// RoleBinding is one role's resolved config-side picture: what `cortex`
// would actually send for this role today, per Config.resolveBinding.
type RoleBinding struct {
	Model    string `json:"model"`
	Window   int    `json:"window"`
	Endpoint string `json:"endpoint"`
}

// ServedModel is one model entry from the backend's own catalog (LiteLLM's
// /model/info or OpenRouter's /api/v1/models), normalized to the fields
// `cortex model` displays.
type ServedModel struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length,omitempty"`
	Free          bool   `json:"free,omitempty"`
}

// ModelCatalogReport is the catalog half of `cortex model`: the merged
// config's role bindings plus a best-effort read of what the backend
// actually serves. BackendReachable is false (with Note explaining why)
// when the backend couldn't be queried — this is not a fatal condition;
// the config-side picture is still shown.
type ModelCatalogReport struct {
	BackendType      string                 `json:"backend_type"`
	Endpoint         string                 `json:"endpoint"`
	Roles            map[string]RoleBinding `json:"roles"`
	BackendReachable bool                   `json:"backend_reachable"`
	ServedModels     []ServedModel          `json:"served_models,omitempty"`
	Note             string                 `json:"note,omitempty"`
}

// buildModelCatalog resolves the merged config's role bindings and probes
// the configured backend for its live catalog. OpenRouter backends are
// queried via ListModels (pkg/llm/openrouter.go — unauthenticated, no key
// required); anything else is treated as a LiteLLM-compatible endpoint and
// queried via discoverFleet's existing /model/info path. A probe failure
// (timeout, connection refused, non-200) degrades to BackendReachable:false
// with an explanatory Note — never an error return, so the caller always
// has the config-side picture to show.
func buildModelCatalog(ctx context.Context, cfg *Config) ModelCatalogReport {
	report := ModelCatalogReport{
		BackendType: cfg.Backend.Type,
		Endpoint:    cfg.backendEndpoint(),
		Roles:       make(map[string]RoleBinding, len(modelRoles)),
	}
	if report.BackendType == "" {
		report.BackendType = "litellm" // the neutral default per defaultEndpoint's doc comment
	}

	for _, role := range modelRoles {
		spec := cfg.resolveBinding(role, nil)
		report.Roles[role] = RoleBinding{Model: spec.Model, Window: spec.Window, Endpoint: spec.Endpoint}
	}

	pctx, cancel := context.WithTimeout(ctx, fleetDiscoveryTimeout)
	defer cancel()

	if cfg.isOpenRouter() {
		models, err := llm.NewOpenRouterClient(nil).ListModels(pctx)
		if err != nil {
			report.Note = fmt.Sprintf("backend unreachable: %v", err)
			return report
		}
		report.BackendReachable = true
		report.ServedModels = make([]ServedModel, 0, len(models))
		for _, m := range models {
			report.ServedModels = append(report.ServedModels, ServedModel{
				ID:            m.ID,
				ContextLength: m.ContextLength,
				Free:          strings.HasSuffix(m.ID, ":free"),
			})
		}
		return report
	}

	fleet := discoverFleet(pctx, report.Endpoint)
	if fleet == nil {
		report.Note = fmt.Sprintf("backend unreachable: no response from %s/model/info", strings.TrimRight(report.Endpoint, "/"))
		return report
	}
	report.BackendReachable = true
	report.ServedModels = make([]ServedModel, 0, len(fleet))
	for name, info := range fleet {
		report.ServedModels = append(report.ServedModels, ServedModel{ID: name, ContextLength: info.MaxInput})
	}
	sort.Slice(report.ServedModels, func(i, j int) bool { return report.ServedModels[i].ID < report.ServedModels[j].ID })
	return report
}

// maxServedModelsShown caps the text-rendered served-models list so a huge
// OpenRouter catalog (hundreds of entries) doesn't flood the terminal; the
// full list is still available via --json. Config-overridable via
// limits.max_served_models_shown (Config.maxServedModelsShown); passed as an
// explicit parameter to renderModelCatalog rather than a mutable package var
// — runModelCLI is a one-shot CLI command with cfg already in scope, so
// there's no reason to reach for process-wide state here.
const maxServedModelsShown = 40

// renderModelCatalog renders a ModelCatalogReport as the text `cortex
// model` prints by default. shownCap overrides maxServedModelsShown; <= 0
// falls back to it.
func renderModelCatalog(r ModelCatalogReport, shownCap int) string {
	if shownCap <= 0 {
		shownCap = maxServedModelsShown
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Cortex model catalog\n")
	fmt.Fprintf(&b, "Backend: %s (%s)\n\n", r.BackendType, r.Endpoint)

	fmt.Fprintf(&b, "Configured role bindings:\n")
	for _, role := range modelRoles {
		rb := r.Roles[role]
		model := rb.Model
		if model == "" {
			model = "(unset)"
		}
		fmt.Fprintf(&b, "  - %-6s model=%-32s window=%-8d endpoint=%s\n", role, model, rb.Window, rb.Endpoint)
	}

	if !r.BackendReachable {
		fmt.Fprintf(&b, "\n%s\n", r.Note)
		return b.String()
	}

	freeCount := 0
	for _, m := range r.ServedModels {
		if m.Free {
			freeCount++
		}
	}
	fmt.Fprintf(&b, "\nBackend-served models (%d", len(r.ServedModels))
	if freeCount > 0 {
		fmt.Fprintf(&b, ", %d marked :free", freeCount)
	}
	fmt.Fprintf(&b, "):\n")
	shown := r.ServedModels
	truncated := false
	if len(shown) > shownCap {
		shown = shown[:shownCap]
		truncated = true
	}
	for _, m := range shown {
		free := ""
		if m.Free {
			free = "  [free]"
		}
		if m.ContextLength > 0 {
			fmt.Fprintf(&b, "  - %s (context=%d)%s\n", m.ID, m.ContextLength, free)
		} else {
			fmt.Fprintf(&b, "  - %s%s\n", m.ID, free)
		}
	}
	if truncated {
		fmt.Fprintf(&b, "  ... and %d more (see --json for the full list)\n", len(r.ServedModels)-shownCap)
	}
	if len(r.ServedModels) == 0 {
		fmt.Fprintf(&b, "  (none reported)\n")
	}
	return b.String()
}

// SuggestedRole is one role's suggested model + window for the pasteable
// config block `cortex model` prints.
type SuggestedRole struct {
	Model  string `json:"model"`
	Window int    `json:"window"`
}

// ModelSuggestion is the suggest half of `cortex model`: a RAM-tier-based
// guess at a reasonable code/study split, plus the detected inputs so the
// user can judge the suggestion for themselves.
type ModelSuggestion struct {
	RAMBytes uint64        `json:"ram_bytes,omitempty"`
	RAMGiB   float64       `json:"ram_gib,omitempty"`
	Arch     string        `json:"arch"`
	Tier     string        `json:"tier"`
	Code     SuggestedRole `json:"code"`
	Study    SuggestedRole `json:"study"`
	Note     string        `json:"note"`
}

// ramTier is one row of the honest, simple RAM→suggestion heuristic below.
// MinGiB is inclusive; tiers are checked in ascending order and the last
// match wins (so open-ended "64 GiB and up" is just the tier with the
// highest MinGiB).
type ramTier struct {
	name   string
	minGiB float64
	code   SuggestedRole
	study  SuggestedRole
}

// ramTiers is the suggestion heuristic. It is deliberately coarse — a
// bucket by total RAM, not a benchmark or a live throughput measurement.
// Model IDs are illustrative (Ollama-tag style, matching README's quick
// start) and sized so the *code* role gets the strongest coder that tier
// can plausibly serve and the *study* role gets something smaller/faster,
// per the roadmap's ask. A user on a different backend should treat the
// id as "a model in this size class" and cross-check against the catalog
// printed above, not paste it blindly.
var ramTiers = []ramTier{
	{
		name:   "minimal",
		minGiB: 0,
		code:   SuggestedRole{Model: "qwen2.5-coder:3b", Window: 8192},
		study:  SuggestedRole{Model: "qwen2.5-coder:1.5b", Window: 8192},
	},
	{
		name:   "small",
		minGiB: 8,
		code:   SuggestedRole{Model: "qwen2.5-coder:7b", Window: 32768},
		study:  SuggestedRole{Model: "qwen2.5-coder:1.5b", Window: 16384},
	},
	{
		name:   "medium",
		minGiB: 16,
		code:   SuggestedRole{Model: "qwen2.5-coder:14b", Window: 32768},
		study:  SuggestedRole{Model: "qwen2.5-coder:7b", Window: 32768},
	},
	{
		name:   "large",
		minGiB: 32,
		code:   SuggestedRole{Model: "qwen2.5-coder:32b", Window: 65536},
		study:  SuggestedRole{Model: "qwen2.5-coder:7b", Window: 32768},
	},
	{
		name:   "xlarge",
		minGiB: 64,
		code:   SuggestedRole{Model: "qwen2.5-coder:32b", Window: 131072},
		study:  SuggestedRole{Model: "qwen2.5-coder:14b", Window: 65536},
	},
}

const bytesPerGiB = 1024 * 1024 * 1024

// suggestModels applies the RAM-tier heuristic. ramBytes of 0 (RAM
// undetectable on this platform) still returns the minimal tier with a
// note explaining the fallback, rather than failing — the suggestion is
// advisory either way.
func suggestModels(ramBytes uint64, arch string) ModelSuggestion {
	ramGiB := float64(ramBytes) / bytesPerGiB

	tier := ramTiers[0]
	for _, t := range ramTiers {
		if ramGiB >= t.minGiB {
			tier = t
		}
	}

	note := "Heuristic RAM-tier suggestion, not a benchmark — verify against " +
		"the backend catalog above and your own throughput needs before pasting."
	if ramBytes == 0 {
		note = "RAM undetectable on this platform (" + runtime.GOOS + ") — showing the " +
			"conservative minimal-tier suggestion. " + note
	} else if arch == "arm64" && runtime.GOOS == "darwin" {
		note += " Apple Silicon shares RAM with the GPU (unified memory), so the " +
			"figure above is the real ceiling, not a CPU-only budget."
	}

	return ModelSuggestion{
		RAMBytes: ramBytes,
		RAMGiB:   ramGiB,
		Arch:     arch,
		Tier:     tier.name,
		Code:     tier.code,
		Study:    tier.study,
		Note:     note,
	}
}

// renderModelSuggestion renders a ModelSuggestion as the pasteable text
// block `cortex model` prints by default.
func renderModelSuggestion(s ModelSuggestion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Suggested config (tier: %s", s.Tier)
	if s.RAMBytes > 0 {
		fmt.Fprintf(&b, ", ~%.1f GiB RAM, %s", s.RAMGiB, s.Arch)
	} else {
		fmt.Fprintf(&b, ", RAM unknown, %s", s.Arch)
	}
	fmt.Fprintf(&b, "):\n%s\n\n", s.Note)
	fmt.Fprintf(&b, "{\n")
	fmt.Fprintf(&b, "  \"models\": {\n")
	fmt.Fprintf(&b, "    \"code\":  { \"model\": %q, \"window\": %d },\n", s.Code.Model, s.Code.Window)
	fmt.Fprintf(&b, "    \"study\": { \"model\": %q, \"window\": %d }\n", s.Study.Model, s.Study.Window)
	fmt.Fprintf(&b, "  }\n")
	fmt.Fprintf(&b, "}\n")
	return b.String()
}

// totalSystemRAM returns total physical RAM in bytes, best-effort:
// `sysctl hw.memsize` on darwin, /proc/meminfo's MemTotal on linux.
// Any other platform (or a probe failure) returns (0, err) — callers
// treat 0 as "undetectable" and fall back to the conservative tier
// rather than failing the command.
func totalSystemRAM() (uint64, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0, fmt.Errorf("sysctl hw.memsize: %w", err)
		}
		v, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse hw.memsize output %q: %w", string(out), err)
		}
		return v, nil
	case "linux":
		data, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, fmt.Errorf("read /proc/meminfo: %w", err)
		}
		return parseMemTotalKB(string(data))
	default:
		return 0, fmt.Errorf("RAM detection not supported on %s", runtime.GOOS)
	}
}

// parseMemTotalKB extracts the MemTotal line from /proc/meminfo (given as
// kB, per the kernel's documented convention) and returns it in bytes.
func parseMemTotalKB(meminfo string) (uint64, error) {
	for _, line := range strings.Split(meminfo, "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed MemTotal line %q", line)
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse MemTotal value %q: %w", fields[1], err)
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("MemTotal not found in /proc/meminfo")
}

// ModelReport is the full `cortex model --json` payload: catalog + suggestion.
type ModelReport struct {
	Catalog    ModelCatalogReport `json:"catalog"`
	Suggestion ModelSuggestion    `json:"suggestion"`
}

// runModelCLI is the `cortex model [--json]` entry point (dispatched from
// main.go). Always exits 0 — a backend that can't be reached is reported,
// not fatal (Track C1's ask: "handle backend-unreachable gracefully").
func runModelCLI(args []string) {
	asJSON := false
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		}
	}

	cfg := LoadConfig()
	fleetDiscoveryTimeout = cfg.fleetDiscoveryTimeout()

	// The context's cancel (deferred below) must run before any exit, so the
	// body that needs it lives in a closure returning an exit code — os.Exit
	// does not run deferred calls.
	exitCode := func() int {
		ctx, cancel := context.WithTimeout(context.Background(), fleetDiscoveryTimeout+time.Second)
		defer cancel()
		catalog := buildModelCatalog(ctx, cfg)

		ram, ramErr := totalSystemRAM()
		if ramErr != nil {
			ram = 0
		}
		suggestion := suggestModels(ram, runtime.GOARCH)

		if asJSON {
			report := ModelReport{Catalog: catalog, Suggestion: suggestion}
			b, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				fmt.Fprintln(os.Stderr, "model:", err)
				return 1
			}
			fmt.Println(string(b))
			return 0
		}

		fmt.Print(renderModelCatalog(catalog, cfg.maxServedModelsShown()))
		fmt.Println()
		fmt.Print(renderModelSuggestion(suggestion))
		return 0
	}()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
