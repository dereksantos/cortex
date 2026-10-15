package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/decide"
	"github.com/dereksantos/cortex/pkg/llm"
)

// DecideConfig configures the parallel shadow decider (docs/decide.md,
// docs/configuration.md's `decide` section). Off unless backend names a
// working backend. The decider sends excerpts of the turn — the task, recent
// tool calls and results, the final reply — to its endpoint, which may differ
// from the coding model's, so it is a new outbound path: an explicit opt-in
// honored ONLY from the user config (decideFromUserConfig), refused under
// CORTEX_LOCAL_ONLY, and announced once on stderr when it starts.
type DecideConfig struct {
	// Backend: "" or "null" (off, the default), "chat" (an OpenAI-compatible
	// endpoint asked for strict JSON), "logprob" (not implemented; refused).
	Backend    string `json:"backend"`
	Endpoint   string `json:"endpoint"`
	KeyEnv     string `json:"key_env"`
	KeyService string `json:"key_service"`
	Model      string `json:"model"`
	// Questions limits which questions are asked; empty = next_move, looping,
	// claim_check (recall_target is opt-in: it is asked only when listed and
	// the session outline has citations to offer).
	Questions []string `json:"questions"`
	// MaxCalls caps decider calls per session; 0 = 200.
	MaxCalls int `json:"max_calls"`
	// TimeoutSec bounds one decision; 0 = 30.
	TimeoutSec int `json:"timeout_sec"`
	// DrainSec is how long session close waits for decisions still in flight
	// (the finalize claim_check, mostly) after the answer is out; 0 = 20.
	// The coder never waits on a decision — this only delays process exit.
	DrainSec int `json:"drain_sec"`
}

const (
	decideBackendChat    = "chat"
	decideBackendLogprob = "logprob"
	decideBackendNull    = "null"

	decideMaxCallsDefault = 200
	decideTimeoutDefault  = 30
	decideDrainDefault    = 20
)

var decideDefaultQuestions = []decide.Question{decide.NextMove, decide.Looping, decide.ClaimCheck}

// validateDecideConfig checks the decide block's shape. It does not judge
// whether the block is honored — only the user layer's copy ever is.
func validateDecideConfig(c DecideConfig) error {
	switch c.Backend {
	case "", decideBackendNull:
		return nil
	case decideBackendLogprob:
		return fmt.Errorf("decide.backend %q is not implemented yet; use %q or %q", decideBackendLogprob, decideBackendChat, decideBackendNull)
	case decideBackendChat:
	default:
		return fmt.Errorf("decide.backend must be %q, %q or %q, got %q", decideBackendNull, decideBackendChat, decideBackendLogprob, c.Backend)
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return fmt.Errorf("decide.endpoint is required when decide.backend is %q", decideBackendChat)
	}
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("decide.model is required when decide.backend is %q", decideBackendChat)
	}
	known := []string{string(decide.NextMove), string(decide.Looping), string(decide.ClaimCheck), string(decide.RecallTarget)}
	for _, q := range c.Questions {
		if !slices.Contains(known, q) {
			return fmt.Errorf("decide.questions: unknown question %q (known: %s)", q, strings.Join(known, ", "))
		}
	}
	for name, v := range map[string]int{"decide.max_calls": c.MaxCalls, "decide.timeout_sec": c.TimeoutSec, "decide.drain_sec": c.DrainSec} {
		if err := nonNegative(name, v); err != nil {
			return err
		}
	}
	return nil
}

// decideFromUserConfig reads the decide block straight from the USER config
// file — the layer a repository can never write. Like trustFromUserConfig it
// bypasses loadMergedConfig, whose fallbacks would hand a repo's own
// .cortex/config.json the power to switch on an outbound path and choose
// where it sends the session's code. Missing, unreadable, malformed, or
// invalid → the zero value (off).
func decideFromUserConfig(userPath string) DecideConfig {
	if userPath == "" {
		return DecideConfig{}
	}
	data, err := os.ReadFile(userPath)
	if err != nil {
		return DecideConfig{}
	}
	var cfg struct {
		Decide DecideConfig `json:"decide"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DecideConfig{}
	}
	if err := validateDecideConfig(cfg.Decide); err != nil {
		fmt.Fprintf(os.Stderr, "cortex: decider off: %v\n", err)
		return DecideConfig{}
	}
	return cfg.Decide
}

// enabled reports whether the block switches a decider on.
func (c DecideConfig) enabled() bool { return c.Backend == decideBackendChat }

func (c DecideConfig) questions() []decide.Question {
	if len(c.Questions) == 0 {
		return decideDefaultQuestions
	}
	out := make([]decide.Question, 0, len(c.Questions))
	for _, q := range c.Questions {
		out = append(out, decide.Question(q))
	}
	return out
}

func (c DecideConfig) maxCalls() int { return orDefault(c.MaxCalls, decideMaxCallsDefault) }
func (c DecideConfig) timeout() time.Duration {
	return time.Duration(orDefault(c.TimeoutSec, decideTimeoutDefault)) * time.Second
}
func (c DecideConfig) drain() time.Duration {
	return time.Duration(orDefault(c.DrainSec, decideDrainDefault)) * time.Second
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// newDeciderFromConfig builds the decider a session runs, or nil (off) with a
// one-line reason for stderr when the block asks for one that cannot run.
func newDeciderFromConfig(c DecideConfig) (decide.Decider, string) {
	if !c.enabled() {
		return nil, ""
	}
	if llm.LocalOnly() {
		return nil, fmt.Sprintf("decider off: %s is set and the decider would send turn excerpts to %s", llm.LocalOnlyEnv, c.Endpoint)
	}
	key := resolveKey(ModelSpec{KeyEnv: c.KeyEnv, KeyService: c.KeyService})
	return decide.NewChat(decide.ChatConfig{
		Endpoint: c.Endpoint,
		APIKey:   key,
		Model:    c.Model,
		Timeout:  c.timeout(),
		MaxCalls: c.maxCalls(),
	}), fmt.Sprintf("decider on (shadow): turn excerpts go to %s (model %s); answers are journaled, never shown to the agent", c.Endpoint, c.Model)
}
