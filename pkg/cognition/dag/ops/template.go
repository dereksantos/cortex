package ops

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/dereksantos/cortex/pkg/cognition/prompts"
)

// TemplateMeta is the YAML frontmatter parsed off a .tmpl file. Each
// field has a strict role; the loader validates them.
//
//   - Version: monotonic int. Bump when prompt body changes meaningfully
//     (rewording instructions, changing output schema). Cost calibration
//     is tied to version; new version forces recalibration.
//   - Op: qualified node name ("function.op"). Must match the op the
//     handler registers under (loader cross-checks).
//   - Description: 1-line summary of what the prompt asks the LLM to do.
//   - MaxOutputTokens: per-op output budget. Stage 2 invariant: ≤ 100
//     for every micro-LLM op (the small-model-amplifier thesis). The
//     loader rejects templates that exceed this.
//   - Vars: list of variable names referenced in the body. Used at
//     render time to validate the caller's substitution map is complete.
type TemplateMeta struct {
	Version         int      `yaml:"version"`
	Op              string   `yaml:"op"`
	Description     string   `yaml:"description"`
	MaxOutputTokens int      `yaml:"max_output_tokens"`
	Vars            []string `yaml:"vars"`
}

// PromptTemplate is a loaded, parsed prompt ready to render.
type PromptTemplate struct {
	Meta TemplateMeta
	tmpl *template.Template
}

// Render substitutes vars into the body. Returns an error if any
// declared var is missing from the input map.
func (p *PromptTemplate) Render(vars map[string]any) (string, error) {
	if vars == nil {
		vars = map[string]any{}
	}
	for _, v := range p.Meta.Vars {
		if _, ok := vars[v]; !ok {
			return "", fmt.Errorf("template %s v%d: missing required var %q", p.Meta.Op, p.Meta.Version, v)
		}
	}
	var buf bytes.Buffer
	if err := p.tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("template %s v%d: render: %w", p.Meta.Op, p.Meta.Version, err)
	}
	return buf.String(), nil
}

// MaxOutputBudget is the per-op output token cap honored by handlers.
// Stage 2 invariant: 100. The loader enforces ≤ this value.
const MaxOutputBudget = 100

// templateCache memoizes parsed templates so each op doesn't re-parse
// on every handler call.
var (
	templateCacheMu sync.RWMutex
	templateCache   = map[string]*PromptTemplate{}
)

// LoadTemplate reads <name>.tmpl from the embedded prompts FS, parses
// the YAML frontmatter, and prepares the body for rendering.
// `name` is the file basename without the .tmpl extension (e.g.
// "maintain_extract_insight"). Results are cached.
func LoadTemplate(name string) (*PromptTemplate, error) {
	templateCacheMu.RLock()
	if cached, ok := templateCache[name]; ok {
		templateCacheMu.RUnlock()
		return cached, nil
	}
	templateCacheMu.RUnlock()

	raw, err := prompts.FS.ReadFile(name + ".tmpl")
	if err != nil {
		return nil, fmt.Errorf("load template %s: %w", name, err)
	}

	meta, body, err := splitFrontmatter(raw)
	if err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}

	if err := validateMeta(name, meta); err != nil {
		return nil, err
	}

	parsed, perr := template.New(meta.Op).Option("missingkey=error").Parse(body)
	if perr != nil {
		return nil, fmt.Errorf("template %s: parse body: %w", name, perr)
	}

	pt := &PromptTemplate{Meta: meta, tmpl: parsed}
	templateCacheMu.Lock()
	templateCache[name] = pt
	templateCacheMu.Unlock()
	return pt, nil
}

// MustLoadTemplate panics on load failure. Use only in init() or
// other startup paths where a missing/malformed template is a build
// error, not a runtime condition. Handlers should call LoadTemplate
// and surface the error.
func MustLoadTemplate(name string) *PromptTemplate {
	t, err := LoadTemplate(name)
	if err != nil {
		panic(err)
	}
	return t
}

// splitFrontmatter parses `---\n<yaml>\n---\n<body>` and returns the
// parsed metadata plus body text. Both delimiters must be on their own
// line. Returns an error if the format is malformed.
func splitFrontmatter(raw []byte) (TemplateMeta, string, error) {
	text := string(raw)
	if !strings.HasPrefix(text, "---") {
		return TemplateMeta{}, "", fmt.Errorf("missing frontmatter (must start with ---)")
	}
	// Trim the opening --- (and trailing newline) before searching for the close.
	rest := strings.TrimPrefix(text, "---")
	rest = strings.TrimLeft(rest, "\r\n")
	closeIdx := strings.Index(rest, "\n---")
	if closeIdx < 0 {
		return TemplateMeta{}, "", fmt.Errorf("missing closing --- for frontmatter")
	}
	frontmatter := rest[:closeIdx]
	body := rest[closeIdx+len("\n---"):]
	body = strings.TrimLeft(body, "\r\n")

	var meta TemplateMeta
	if err := yaml.Unmarshal([]byte(frontmatter), &meta); err != nil {
		return TemplateMeta{}, "", fmt.Errorf("yaml frontmatter: %w", err)
	}
	return meta, body, nil
}

// validateMeta enforces ADR-004 invariants on a parsed template.
func validateMeta(name string, m TemplateMeta) error {
	if m.Version <= 0 {
		return fmt.Errorf("template %s: version must be >= 1, got %d", name, m.Version)
	}
	if m.Op == "" {
		return fmt.Errorf("template %s: op is required", name)
	}
	if m.MaxOutputTokens <= 0 {
		return fmt.Errorf("template %s: max_output_tokens must be >= 1, got %d", name, m.MaxOutputTokens)
	}
	if m.MaxOutputTokens > MaxOutputBudget {
		return fmt.Errorf("template %s: max_output_tokens=%d exceeds Stage-2 cap of %d (small-model amplifier invariant)", name, m.MaxOutputTokens, MaxOutputBudget)
	}
	// Cross-check filename ⇄ op: "function_op.tmpl" should match "function.op".
	expectedOp := strings.Replace(name, "_", ".", 1)
	if expectedOp != m.Op {
		return fmt.Errorf("template %s: op=%q in frontmatter does not match filename-derived %q", name, m.Op, expectedOp)
	}
	return nil
}

// resetTemplateCache is a test-only helper for forcing reload.
func resetTemplateCache() {
	templateCacheMu.Lock()
	templateCache = map[string]*PromptTemplate{}
	templateCacheMu.Unlock()
}
