package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

// ConfigOverrides holds whitelisted counterfactual overrides for
// `cortex journal replay --config-overrides=...`. The set of allowed
// keys is deliberately small and explicit so a malformed override string
// fails at parse time rather than silently flowing into cognition or an
// LLM provider.
type ConfigOverrides struct {
	Model       string
	Provider    string
	Temperature *float64
	MaxTokens   *int
}

// IsEmpty returns true when no overrides are configured. Used to skip
// the counterfactual path entirely.
func (o ConfigOverrides) IsEmpty() bool {
	return o.Model == "" && o.Provider == "" && o.Temperature == nil && o.MaxTokens == nil
}

// summary returns a comma-separated human-readable list of the
// configured overrides. Empty when IsEmpty.
func (o ConfigOverrides) summary() string {
	parts := make([]string, 0, 4)
	if o.Provider != "" {
		parts = append(parts, fmt.Sprintf("provider=%s", o.Provider))
	}
	if o.Model != "" {
		parts = append(parts, fmt.Sprintf("model=%s", o.Model))
	}
	if o.Temperature != nil {
		parts = append(parts, fmt.Sprintf("temperature=%v", *o.Temperature))
	}
	if o.MaxTokens != nil {
		parts = append(parts, fmt.Sprintf("max_tokens=%d", *o.MaxTokens))
	}
	return strings.Join(parts, ", ")
}

// allowedOverrideKeys is the explicit allow-list. Adding a key here
// requires also adding its validation branch in ParseConfigOverrides.
var allowedOverrideKeys = map[string]struct{}{
	"model":       {},
	"provider":    {},
	"temperature": {},
	"max_tokens":  {},
}

// ParseConfigOverrides parses a "k=v,k=v" string into ConfigOverrides.
// Returns a zero-value struct (IsEmpty()==true) when s is empty.
//
// Validation rules:
//   - Keys are matched against allowedOverrideKeys; unknown keys are
//     rejected with an error (security: explicit allow-list).
//   - Values may be optionally double-quoted; the quotes are stripped.
//   - Values must not contain shell-meta or newline characters; this
//     guards against accidental command injection if a value is ever
//     concatenated into a shell line (defense in depth — we don't do
//     that today, but the constraint is cheap to enforce).
//   - Temperature must parse as float in [0.0, 2.0].
//   - MaxTokens must parse as positive int.
//   - Duplicate keys: later values win.
func ParseConfigOverrides(s string) (*ConfigOverrides, error) {
	out := &ConfigOverrides{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out, nil
	}

	pairs := strings.Split(s, ",")
	for _, raw := range pairs {
		pair := strings.TrimSpace(raw)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			return nil, fmt.Errorf("missing '=' in override segment %q", pair)
		}
		key := strings.TrimSpace(pair[:eq])
		val := strings.TrimSpace(pair[eq+1:])

		if key == "" {
			return nil, fmt.Errorf("empty key in segment %q", pair)
		}
		if val == "" {
			return nil, fmt.Errorf("empty value for key %q", key)
		}
		if _, ok := allowedOverrideKeys[key]; !ok {
			return nil, fmt.Errorf("unknown override key %q (allowed: model, provider, temperature, max_tokens)", key)
		}
		val = stripQuotes(val)
		if err := validateOverrideValue(val); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}

		if err := out.applyKV(key, val); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	return out, nil
}

func (o *ConfigOverrides) applyKV(key, val string) error {
	switch key {
	case "model":
		o.Model = val
	case "provider":
		o.Provider = val
	case "temperature":
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("temperature must be a number, got %q", val)
		}
		if f < 0 || f > 2.0 {
			return fmt.Errorf("temperature %v out of range [0.0, 2.0]", f)
		}
		o.Temperature = &f
	case "max_tokens":
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("max_tokens must be an integer, got %q", val)
		}
		if n <= 0 {
			return fmt.Errorf("max_tokens %d must be positive", n)
		}
		o.MaxTokens = &n
	}
	return nil
}

// stripQuotes removes one leading and one trailing double-quote if both
// are present. Single quotes are deliberately not handled — POSIX shells
// already pass single-quoted values verbatim to the binary; double
// quotes are the ambiguous case worth normalizing.
func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// validateOverrideValue rejects shell-meta and control characters in
// override values. Cheap defense-in-depth: even though current consumers
// never concat values into a shell line, the journal replay command is
// the kind of surface where future tooling might.
func validateOverrideValue(s string) error {
	for _, r := range s {
		switch r {
		case ';', '|', '&', '`', '$', '\n', '\r', '\x00':
			return fmt.Errorf("invalid character %q in value", r)
		}
		if r < 0x20 && r != '\t' {
			return fmt.Errorf("invalid character %q in value", r)
		}
	}
	return nil
}

// asJournalOverrides converts the commands-side ConfigOverrides to the
// journal-side CounterfactualOverrides serialization shape. Mirrors
// field-for-field; the duplicate types exist only to break the package
// dependency (internal/journal cannot import cmd/cortex/commands).
func (o ConfigOverrides) asJournalOverrides() journal.CounterfactualOverrides {
	out := journal.CounterfactualOverrides{Model: o.Model, Provider: o.Provider}
	if o.Temperature != nil {
		t := *o.Temperature
		out.Temperature = &t
	}
	if o.MaxTokens != nil {
		m := *o.MaxTokens
		out.MaxTokens = &m
	}
	return out
}

// buildOverrideLLM returns an llm.Provider configured per overrides,
// falling back to the base config for unspecified fields. Used by the
// counterfactual --execute path so a replay run uses the chosen
// model/provider instead of the daemon-time defaults.
//
// Provider is inferred from the model when not specified: a model
// beginning with "claude" routes to Anthropic, everything else to
// Ollama. Returns an error for explicitly-unknown providers.
func buildOverrideLLM(base *config.Config, o ConfigOverrides) (llm.Provider, error) {
	cfg := *base
	provider := strings.ToLower(strings.TrimSpace(o.Provider))
	if provider == "" {
		if strings.HasPrefix(strings.ToLower(o.Model), "claude") {
			provider = "anthropic"
		} else if o.Model != "" {
			provider = "ollama"
		} else {
			provider = "anthropic"
		}
	}
	switch provider {
	case "anthropic":
		if o.Model != "" {
			cfg.AnthropicModel = o.Model
		}
		return llm.NewAnthropicClient(&cfg), nil
	case "ollama":
		if o.Model != "" {
			cfg.OllamaModel = o.Model
		}
		return llm.NewOllamaClient(&cfg), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q (allowed: anthropic, ollama)", provider)
	}
}

// jaccardTopK returns the Jaccard similarity of the first k elements of
// each ranking (or len if shorter). Returns 1.0 when both lists are
// empty. k must be > 0.
func jaccardTopK(a, b []string, k int) float64 {
	if k <= 0 {
		return 0
	}
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	pick := func(xs []string) map[string]struct{} {
		m := make(map[string]struct{}, k)
		for i := 0; i < len(xs) && i < k; i++ {
			m[xs[i]] = struct{}{}
		}
		return m
	}
	left, right := pick(a), pick(b)
	if len(left) == 0 && len(right) == 0 {
		return 1
	}
	var intersect int
	for id := range left {
		if _, ok := right[id]; ok {
			intersect++
		}
	}
	union := len(left) + len(right) - intersect
	if union == 0 {
		return 1
	}
	return float64(intersect) / float64(union)
}

// counterfactualReflectRerank replays one reflect.rerank entry against
// an LLM provider built from the overrides and returns the corresponding
// replay.counterfactual payload. Returns a "failed" payload (not an
// error) when the original entry lacks the InputContents needed to
// reconstruct candidates — caller logs that and continues.
func counterfactualReflectRerank(ctx context.Context, src *journal.Entry, payload *journal.ReflectRerankPayload, overrides ConfigOverrides, llmFactory func(ConfigOverrides) (llm.Provider, error)) journal.ReplayCounterfactualPayload {
	out := journal.ReplayCounterfactualPayload{
		SourceOffset:      int64(src.Offset),
		SourceClass:       "reflect",
		SourceType:        src.Type,
		Overrides:         overrides.asJournalOverrides(),
		OriginalRankedIDs: append([]string(nil), payload.RankedIDs...),
	}

	if len(payload.InputContents) == 0 {
		out.Status = journal.ReplayStatusFailed
		out.Error = "source entry has no input_contents; cannot reconstruct candidates"
		return out
	}

	candidates := make([]cognition.Result, 0, len(payload.InputIDs))
	for _, id := range payload.InputIDs {
		content, ok := payload.InputContents[id]
		if !ok {
			continue
		}
		candidates = append(candidates, cognition.Result{ID: id, Content: content})
	}
	if len(candidates) == 0 {
		out.Status = journal.ReplayStatusFailed
		out.Error = "all input ids missing from input_contents"
		return out
	}
	// Stable order for deterministic prompt construction even if the
	// map iteration order was non-stable in some path.
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })

	provider, err := llmFactory(overrides)
	if err != nil {
		out.Status = journal.ReplayStatusFailed
		out.Error = fmt.Sprintf("build provider: %v", err)
		return out
	}
	if provider == nil || !provider.IsAvailable() {
		out.Status = journal.ReplayStatusFailed
		out.Error = "configured provider is unavailable"
		return out
	}

	reflector := intcognition.NewReflect(provider)
	// Do NOT set JournalDir — the counterfactual must not emit its own
	// reflect.rerank entry into the live journal.
	reranked, err := reflector.Reflect(ctx, cognition.Query{Text: payload.QueryText}, candidates)
	if err != nil {
		out.Status = journal.ReplayStatusFailed
		out.Error = fmt.Sprintf("reflect: %v", err)
		return out
	}
	cfRanked := make([]string, len(reranked))
	for i, c := range reranked {
		cfRanked[i] = c.ID
	}
	out.CounterfactualRankedIDs = cfRanked
	k := 5
	if k > len(payload.RankedIDs) {
		k = len(payload.RankedIDs)
	}
	if k > len(cfRanked) {
		k = len(cfRanked)
	}
	if k > 0 {
		out.JaccardK = k
		out.JaccardTopK = jaccardTopK(payload.RankedIDs, cfRanked, k)
	}
	out.Status = journal.ReplayStatusExecuted
	return out
}

// openReplayWriter opens the writer-class journal at
// <contextDir>/journal/replay/. Used to emit replay.counterfactual
// entries from runReplay.
func openReplayWriter(contextDir string) (*journal.Writer, error) {
	return journal.NewWriter(journal.WriterOpts{
		ClassDir: filepath.Join(contextDir, "journal", "replay"),
		Fsync:    journal.FsyncPerBatch,
	})
}
