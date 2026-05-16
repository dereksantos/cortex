package harness

import "strings"

// ModelMaxOutputTokens returns a sensible per-turn output cap for a
// given OpenRouter model id. This is the `max_tokens` field the
// harness sends on each request — it bounds how much the model can
// emit in one response, NOT cumulative spend across the loop.
//
// The values are conservative: a model's documented maximum minus a
// margin for safety. Going to the absolute max occasionally
// triggers provider-side errors on long generations.
//
// Unknown model ids fall back to 4096, which works for any model
// that supports OpenAI-compatible function calling. Override
// explicitly via CortexHarness.SetMaxOutputTokens when you know
// better.
//
// When adding a model, prefer the documented "max output" figure
// over "context window". A 200K context window with 16K max-output
// means we should pass 16K here, not 200K.
func ModelMaxOutputTokens(modelID string) int {
	m := strings.ToLower(modelID)

	// Anthropic family — all Claude 3/4 models support up to 64K
	// output tokens on the official API (Anthropic docs, Sonnet
	// 4.x), but OpenRouter's pass-through has historically been
	// reliable up to ~16K. Stay at 16K until we have receipts that
	// higher works through OpenRouter.
	if strings.Contains(m, "claude") || strings.HasPrefix(m, "anthropic/") {
		return 16_000
	}

	// Qwen 3 Coder family — supports up to 16K output per the
	// model card; OpenRouter routing usually surfaces 8K reliably.
	if strings.Contains(m, "qwen") && strings.Contains(m, "coder") {
		return 8_000
	}

	// OpenAI gpt-oss family (free tier) — 4K per turn is the
	// observed sweet spot; pushing higher tends to truncate
	// mid-generation rather than emit beyond the cap cleanly.
	if strings.Contains(m, "gpt-oss") {
		return 4_000
	}

	// OpenAI gpt-4o / gpt-5 family supports 16K output; o1 family
	// supports up to 100K but we cap at 32K because the harness
	// doesn't need extended reasoning budgets.
	if strings.Contains(m, "openai/") || strings.Contains(m, "gpt-4") || strings.Contains(m, "gpt-5") {
		return 16_000
	}

	// Gemini family supports up to 8K output reliably; the 1.5 Pro
	// supports more but OpenRouter pass-through is the floor.
	if strings.Contains(m, "gemini") || strings.HasPrefix(m, "google/") {
		return 8_000
	}

	// DeepSeek Coder, Mistral, Llama 3 family — 4K is the
	// reliable floor across open-weight models on OpenRouter.
	if strings.Contains(m, "deepseek") || strings.Contains(m, "mistral") || strings.Contains(m, "llama") {
		return 4_000
	}

	return 4_096
}
