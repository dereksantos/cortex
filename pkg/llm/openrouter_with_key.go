package llm

import (
	"net/http"
	"os"
	"time"

	"github.com/dereksantos/cortex/pkg/config"
)

// NewOpenRouterClientWithKey is the additive constructor for callers
// that resolve the API key out-of-band (e.g. via macOS Keychain in
// pkg/secret). The standard NewOpenRouterClient reads from the
// OPEN_ROUTER_API_KEY env var; this variant accepts the key directly
// so the harness can keep it out of the process environment.
//
// cfg may be nil — only the default model knob is read from it today
// (and that's via the env-var fallback path, which still applies).
// All other fields default the same as NewOpenRouterClient.
func NewOpenRouterClientWithKey(cfg *config.Config, apiKey string) *OpenRouterClient {
	model := os.Getenv("OPEN_ROUTER_MODEL")
	if model == "" {
		model = openrouterDefaultModel
	}
	_ = cfg

	return &OpenRouterClient{
		apiKey:    apiKey,
		model:     model,
		maxTokens: defaultMaxTokens,
		apiURL:    openrouterAPIURL,
		httpClient: &http.Client{
			Timeout: openrouterTimeoutSec * time.Second,
		},
	}
}
