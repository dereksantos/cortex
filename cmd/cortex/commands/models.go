// Package commands — `cortex models` command.
//
// Phase 4 Slice D. The onboarding-flow entry point: probes all
// configured OpenAI-compatible endpoints in parallel, lists discovered
// models with capability labels, and prints a recommended role map. The
// --save flag persists the recommendation to <workdir>/.cortex/config.json
// so subsequent REPL launches honor it.
//
// This is the visible expression of the multi-model leverage thesis
// claim described in docs/eval-strategy.md Tier 2c and ROADMAP.md
// "Onboarding as the thesis surface" — a working team of small + mid
// models orchestrated well, made legible to the user on first run.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	intllm "github.com/dereksantos/cortex/internal/llm"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

// ModelsCommand implements `cortex models`.
type ModelsCommand struct{}

func init() {
	Register(&ModelsCommand{})
}

// Name returns the command name.
func (c *ModelsCommand) Name() string { return "models" }

// Description returns a brief description.
func (c *ModelsCommand) Description() string {
	return "Detect available models across configured endpoints + Ollama; show recommended role map"
}

// Execute runs the detect-recommend-(optionally save) flow.
func (c *ModelsCommand) Execute(ctx *Context) error {
	save := false
	jsonOut := false
	workdir := ""
	for i := 0; i < len(ctx.Args); i++ {
		switch ctx.Args[i] {
		case "--save":
			save = true
		case "--json":
			jsonOut = true
		case "--workdir":
			if i+1 < len(ctx.Args) {
				workdir = ctx.Args[i+1]
				i++
			}
		case "-h", "--help":
			printModelsHelp()
			return nil
		}
	}
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	}
	cortexDir := filepath.Join(workdir, ".cortex")
	cfg := loadREPLConfig(cortexDir)

	// Probe all configured OpenAI-compatible endpoints in parallel.
	pctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	endpointResults := intllm.DetectOpenAICompatEndpoints(pctx, buildEndpointConfigs(cfg))

	// Add Ollama (if reachable) as another local source.
	ollamaCatalog := probeOllamaCatalog()

	// Build catalogs for the recommender.
	catalogs := make([]llm.EndpointCatalog, 0, len(endpointResults)+1)
	for _, r := range endpointResults {
		if !r.Reachable {
			continue
		}
		catalogs = append(catalogs, llm.EndpointCatalog{
			Name:    r.Name,
			BaseURL: r.BaseURL,
			IsLocal: isLocalURL(r.BaseURL),
			Models:  r.Models,
		})
	}
	if ollamaCatalog != nil {
		catalogs = append(catalogs, *ollamaCatalog)
	}

	rec := llm.Recommend(catalogs)

	if jsonOut {
		return emitModelsJSON(endpointResults, ollamaCatalog, rec)
	}

	printDetected(endpointResults, ollamaCatalog)
	fmt.Println()
	printRecommendation(rec)

	if save {
		if err := saveRoleMap(cortexDir, cfg, rec); err != nil {
			return fmt.Errorf("save role map: %w", err)
		}
		fmt.Printf("\nSaved role map to %s\n", filepath.Join(cortexDir, "config.json"))
	} else {
		fmt.Println("\nRe-run with --save to persist this role map to .cortex/config.json.")
	}
	return nil
}

// buildEndpointConfigs converts the persisted EndpointDef list into
// the llm.EndpointConfig form the detector expects (resolving the
// API key from APIKeyEnv if set).
func buildEndpointConfigs(cfg *config.Config) []llm.EndpointConfig {
	if cfg == nil {
		return nil
	}
	out := make([]llm.EndpointConfig, 0, len(cfg.Endpoints))
	for _, ep := range cfg.Endpoints {
		out = append(out, llm.EndpointConfig{
			Name:    ep.Name,
			BaseURL: ep.BaseURL,
			APIKey:  ep.ResolveAPIKey(),
		})
	}
	return out
}

// probeOllamaCatalog returns the local Ollama catalog if Ollama is
// reachable; nil otherwise. Models get capability tags via inference
// (Ollama's API doesn't expose labels).
func probeOllamaCatalog() *llm.EndpointCatalog {
	models, _, err := listOllamaModels(defaultOllamaAPIURL)
	if err != nil || len(models) == 0 {
		return nil
	}
	cat := &llm.EndpointCatalog{
		Name:    "ollama",
		BaseURL: defaultOllamaAPIURL,
		IsLocal: true,
		Models:  make([]llm.CompatModel, 0, len(models)),
	}
	for _, m := range models {
		cat.Models = append(cat.Models, llm.CompatModel{
			ID:     m,
			Labels: llm.InferCapabilities(m),
		})
	}
	return cat
}

// isLocalURL returns true when u points at localhost / 127.0.0.1 / a
// private network. Used by the recommender to bias toward local
// endpoints.
func isLocalURL(u string) bool {
	lower := strings.ToLower(u)
	return strings.Contains(lower, "localhost") ||
		strings.Contains(lower, "127.0.0.1") ||
		strings.Contains(lower, "://192.168.") ||
		strings.Contains(lower, "://10.") ||
		strings.Contains(lower, "://172.16.") ||
		strings.Contains(lower, "://172.17.") ||
		strings.Contains(lower, "://172.18.") ||
		strings.Contains(lower, "://172.19.") ||
		strings.Contains(lower, "://172.2") ||
		strings.Contains(lower, "://172.30.") ||
		strings.Contains(lower, "://172.31.")
}

// printDetected pretty-prints the discovered endpoints and their
// models with capability tags.
func printDetected(endpoints []intllm.EndpointResult, ollama *llm.EndpointCatalog) {
	fmt.Println("Detected endpoints:")
	if len(endpoints) == 0 && ollama == nil {
		fmt.Println("  (none — no OpenAI-compatible endpoints configured and Ollama not reachable)")
		fmt.Println("  Tip: add `endpoints` to .cortex/config.json. See docs/llm-endpoints.md.")
		return
	}
	for _, r := range endpoints {
		if !r.Reachable {
			fmt.Printf("  [DOWN] %s (%s) — %s\n", r.Name, r.BaseURL, truncateErr(r.Error, 80))
			continue
		}
		fmt.Printf("  [OK]   %s (%s) — %d models\n", r.Name, r.BaseURL, len(r.Models))
		for _, m := range r.Models {
			labels := llm.EffectiveLabels(m)
			fmt.Printf("           %-45s  %s\n", m.ID, fmtLabels(labels))
		}
	}
	if ollama != nil {
		fmt.Printf("  [OK]   ollama (%s) — %d models\n", ollama.BaseURL, len(ollama.Models))
		for _, m := range ollama.Models {
			fmt.Printf("           %-45s  %s\n", m.ID, fmtLabels(llm.EffectiveLabels(m)))
		}
	}
}

func fmtLabels(ls []string) string {
	if len(ls) == 0 {
		return "(no inferred capabilities)"
	}
	return "[" + strings.Join(ls, ", ") + "]"
}

// printRecommendation pretty-prints the role-map proposal.
func printRecommendation(rec llm.Recommendation) {
	fmt.Println("Recommended role map:")
	rows := make([]string, 0, len(llm.AllRoles))
	for _, role := range llm.AllRoles {
		choice, ok := rec.Choices[role]
		if !ok {
			rows = append(rows, fmt.Sprintf("  %-7s  (no candidate — manual config required)", role))
			continue
		}
		rows = append(rows, fmt.Sprintf("  %-7s  %s/%s  — %s", role, choice.Endpoint, choice.Model, choice.Reason))
	}
	sort.Strings(rows) // stable display order; role names are already short and sortable
	for _, r := range rows {
		fmt.Println(r)
	}
}

// saveRoleMap merges the recommendation into the existing config and
// writes it back. Auto-registers any endpoint the recommendation
// references that isn't already in Config.Endpoints — e.g. when the
// recommender picked an "ollama" model, the saved config gains an
// ollama EndpointDef so ResolveModelRoute can route it.
func saveRoleMap(cortexDir string, cfg *config.Config, rec llm.Recommendation) error {
	if cfg == nil {
		cfg = &config.Config{ContextDir: cortexDir, ProjectRoot: filepath.Dir(cortexDir)}
	}
	cfg.Models = recommendationToModelsMap(rec)

	// Ensure every endpoint the role-map references is registered.
	// Ollama is the common implicit case: detected via probeOllamaCatalog
	// but not part of the user's explicit `endpoints` list.
	for _, c := range rec.Choices {
		if cfg.FindEndpoint(c.Endpoint) != nil {
			continue
		}
		cfg.Endpoints = append(cfg.Endpoints, endpointDefFor(c.Endpoint))
	}

	configPath := filepath.Join(cortexDir, "config.json")
	if err := os.MkdirAll(cortexDir, 0o755); err != nil {
		return err
	}
	return cfg.Save(configPath)
}

// endpointDefFor returns a sensible default EndpointDef for built-in
// endpoint names. Currently just ollama; future expansion (anthropic,
// openai) can be added here.
func endpointDefFor(name string) config.EndpointDef {
	switch name {
	case "ollama":
		return config.EndpointDef{
			Name:    "ollama",
			BaseURL: "http://localhost:11434/v1",
		}
	}
	// Unknown name — write the entry with empty BaseURL so the user
	// sees what to fill in on next edit. Better than silently dropping it.
	return config.EndpointDef{Name: name}
}

func recommendationToModelsMap(rec llm.Recommendation) *config.ModelsMap {
	m := &config.ModelsMap{}
	get := func(role llm.Role) *config.RoleAssignment {
		c, ok := rec.Choices[role]
		if !ok {
			return nil
		}
		return &config.RoleAssignment{Endpoint: c.Endpoint, Model: c.Model}
	}
	m.Code = get(llm.RoleCode)
	m.Reason = get(llm.RoleReason)
	m.Fast = get(llm.RoleFast)
	m.Embed = get(llm.RoleEmbed)
	m.Rerank = get(llm.RoleRerank)
	return m
}

// emitModelsJSON prints the detection result + recommendation as a
// single JSON object on stdout.
func emitModelsJSON(endpoints []intllm.EndpointResult, ollama *llm.EndpointCatalog, rec llm.Recommendation) error {
	out := map[string]any{
		"endpoints":      endpoints,
		"ollama":         ollama,
		"recommendation": rec.Choices,
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func truncateErr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func printModelsHelp() {
	fmt.Println(`Usage: cortex models [flags]

Probes all configured OpenAI-compatible endpoints in parallel + Ollama
(when reachable), lists discovered models with capability tags, and
prints a recommended per-role assignment. Use --save to persist the
recommendation to .cortex/config.json.

Flags:
  --save           Persist the recommended role map to .cortex/config.json
  --json           Emit the detection + recommendation as a JSON object
  --workdir DIR    Use DIR instead of cwd (for the .cortex/ lookup)
  -h, --help       Show this help

Examples:
  cortex models                     # Detect + display + recommend
  cortex models --save              # Detect, recommend, persist to config
  cortex models --json | jq .       # Machine-readable output`)
}
