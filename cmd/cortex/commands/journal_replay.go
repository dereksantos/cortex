package commands

import (
	"fmt"
	"strconv"
	"strings"
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
