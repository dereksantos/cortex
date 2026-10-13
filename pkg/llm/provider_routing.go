package llm

// ProviderRouting is OpenRouter's request-body `provider` object
// (https://openrouter.ai/docs/features/provider-routing). OpenRouter serves
// most open-weight models from several upstream providers, which differ in
// quantization, context length and sampling behavior; by default it load-
// balances across them and silently falls back to another one on error. A
// reproducible measurement (a benchmark run, a regression comparison) must
// pin one upstream and forbid fallback, or the "system under test" changes
// from call to call.
//
// Every field is optional and omitted from the wire when empty, so a nil or
// zero ProviderRouting sends nothing and leaves OpenRouter's defaults alone.
// Only OpenRouter understands this object; callers attach it only on the
// OpenRouter path (see cmd/cortex's Config.providerRouting).
type ProviderRouting struct {
	// Order lists provider slugs ("novita") or endpoint tags
	// ("novita/fp8") to try, in order.
	Order []string `json:"order,omitempty"`
	// Only restricts routing to these providers (slugs or tags).
	Only []string `json:"only,omitempty"`
	// Ignore excludes these providers.
	Ignore []string `json:"ignore,omitempty"`
	// AllowFallbacks false forbids routing to any provider outside
	// Order/Only when they fail — a failed call surfaces as an error
	// instead of being served by a different upstream. nil leaves
	// OpenRouter's default (true).
	AllowFallbacks *bool `json:"allow_fallbacks,omitempty"`
	// RequireParameters true routes only to providers that support every
	// parameter in the request (e.g. tools).
	RequireParameters *bool `json:"require_parameters,omitempty"`
	// Quantizations restricts routing to endpoints serving these
	// quantizations ("fp8", "bf16", ...).
	Quantizations []string `json:"quantizations,omitempty"`
	// DataCollection "deny" excludes providers that may retain prompts.
	DataCollection string `json:"data_collection,omitempty"`
}

// IsZero reports whether r carries no routing preference at all, so callers
// can avoid sending an empty `provider: {}` object.
func (r *ProviderRouting) IsZero() bool {
	return r == nil || (len(r.Order) == 0 && len(r.Only) == 0 && len(r.Ignore) == 0 &&
		r.AllowFallbacks == nil && r.RequireParameters == nil &&
		len(r.Quantizations) == 0 && r.DataCollection == "")
}

// routingOrNil normalizes a zero routing object to nil so it is omitted from
// the wire entirely rather than sent as `provider: {}`.
func routingOrNil(r *ProviderRouting) *ProviderRouting {
	if r.IsZero() {
		return nil
	}
	return r
}
