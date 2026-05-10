# OpenRouter — what the eval harness needs to know

Findings from a one-shot probe (`cmd/cortex-or-probe`) on 2026-05-10 using
the user's OpenRouter account (`$OPEN_ROUTER_API_KEY`, $20 credit balance).
Raw response saved to `docs/openrouter-probe.json`.

This doc unblocks loop step 2 (`pkg/llm/openrouter.go`) and informs model
selection for the grid runner (steps 8–9).

---

## Cost field — use `usage.cost` from the chat response

```json
"usage": {
  "prompt_tokens": 72,
  "completion_tokens": 18,
  "total_tokens": 90,
  "cost": 0,
  "is_byok": false,
  "prompt_tokens_details": {
    "cached_tokens": 64,
    "cache_write_tokens": 0
  },
  "cost_details": {
    "upstream_inference_cost": 0,
    "upstream_inference_prompt_cost": 0,
    "upstream_inference_completions_cost": 0
  },
  "completion_tokens_details": {
    "reasoning_tokens": 8
  }
}
```

- **`usage.cost`** — flat USD numeric, **present even when 0** (free models).
  This is the field `pkg/llm/openrouter.go` should read for `CostUSD`.
- **Request must include** `"usage": {"include": true}` to surface cost.
  Without that flag, `usage.cost` is omitted on some response shapes.
- **`usage.prompt_tokens_details.cached_tokens`** — OpenRouter exposes prompt
  cache hits. Worth surfacing as a separate column in CellResult if we
  want to study cache efficiency later (deferred — not in v1 schema).
- **`usage.completion_tokens_details.reasoning_tokens`** — for thinking
  models (gpt-oss, claude-3.7-sonnet:thinking, etc.). The schema's
  `tokens_out` should be the *total* completion tokens; reasoning is a
  subset.

### `/api/v1/generation` — don't depend on it

The generation-lookup endpoint returned **404 within 5s** of a successful
chat completion, even with a valid `id`. May settle later, but we can't
build the harness around an eventually-consistent lookup.

**Decision:** rely on inline `usage.cost`. Skip the generation endpoint.

---

## Rate limits — provider-side throttling is the dominant signal

### Empirical observations

- **No proactive rate-limit headers.** Successful responses do not include
  `X-Ratelimit-*` headers. We learn limits only from 429 responses.
- **Free models are throttled at the upstream provider, not at OR.**
  Hit 429 immediately on `qwen/qwen3-coder:free` and
  `meta-llama/llama-3.3-70b-instruct:free` — both routed through the
  Venice provider, which was saturated. Same minute, `openai/gpt-oss-20b:free`
  (routed via OpenInference) returned 200.
- **429 metadata shape:**
  ```json
  "error": {
    "code": 429,
    "metadata": {
      "raw": "<model> is temporarily rate-limited upstream...",
      "provider_name": "Venice",
      "is_byok": false,
      "retry_after_seconds": 22
    }
  }
  ```
  The harness should parse `retry_after_seconds` from `error.metadata`
  and back off accordingly. The HTTP `Retry-After` header is also present.
- **BYOK escapes pooled throttling.** Error message hints at attaching
  your own provider keys to OR for accumulated limits. Out of scope until
  the user opts in.

### What this means for the grid

1. **Free-tier sweep is unreliable.** Expect ~10–30% of calls to 429
   under cross-harness load. The runner needs retry-with-backoff on free
   models (TODO 8).
2. **Paid models (any tier above free) are not provider-throttled the
   same way** — we're paying our way out of the shared free pool. The
   $20 credit is what makes the medium/large tier sweeps actually viable.
3. **Probing OR's daily-cap (free tier, 50 req/day or 1000 with $10
   credit) didn't trigger** — none of our requests came back with
   "OpenRouter daily cap exceeded" style errors, only provider-side
   429s. Daily-cap presumed in effect at the documented levels but
   not verified in this probe.

---

## Model selection by tier (current as of 2026-05-10)

Tiers are bucketed by **prompt $/M tokens**:

| Tier | Range (prompt) | Use case |
|---|---|---|
| Free | $0 | Smoke / development / small-tier baseline |
| Small-paid | < $0.20/M | Small-tier paid fallback when free 429s |
| Medium | $0.20–$0.79/M | Medium-tier in the amplifier comparison |
| Large | $0.80–$2.99/M | Large-tier ceiling under the $20 budget |
| Frontier | ≥ $3/M | Gated by `CORTEX_EVAL_ALLOW_FRONTIER=1`, budget #2 |

### Recommended IDs

**Free (small tier; expect occasional 429):**
- `openai/gpt-oss-20b:free` — verified working, OpenInference provider
- `openai/gpt-oss-120b:free` — bigger sibling, same provider family
- `google/gemma-4-26b-a4b-it:free` — Google-hosted, fewer collisions
- `nvidia/nemotron-nano-9b-v2:free` — NVIDIA-hosted
- `meta-llama/llama-3.2-3b-instruct:free` — tiny fallback
- `qwen/qwen3-coder:free` — coder-tuned but Venice-throttled; use only
  when Venice is healthy

**Small-paid (small-tier reliability fallback):**
- `meta-llama/llama-3.1-8b-instruct` — $0.02/$0.05 per M
- `mistralai/mistral-nemo` — $0.02/$0.03 per M
- `qwen/qwen-2.5-7b-instruct` — $0.04/$0.10 per M

**Medium (the workhorse tier — coder-tuned where possible):**
- `qwen/qwen3-coder` — $0.22/$1.80 per M, **coder-tuned, primary pick**
- `anthropic/claude-3-haiku` — $0.25/$1.25 per M, deterministic & fast
- `openai/gpt-5-mini` — $0.25/$2.00 per M
- `deepseek/deepseek-v3.2` — $0.252/$0.378 per M, very cheap completion
- `openai/gpt-5.1-codex-mini` — $0.25/$2.00 per M, codex-tuned

**Large (the ceiling we can afford under $20):**
- `anthropic/claude-haiku-4.5` — $1/$5 per M, **primary pick** (Haiku is
  the largest model the budget supports for ~10 cells per sweep)
- `nousresearch/hermes-3-llama-3.1-405b` — $1/$1 per M, very cheap
  completion side; worth a try as a "405B at Haiku price" alternative
- `google/gemini-2.5-pro` — $1.25/$10 per M, cheaper input than Sonnet
- `openai/gpt-5.1` — $1.25/$10 per M

**Frontier (DO NOT enable without `CORTEX_EVAL_ALLOW_FRONTIER=1`):**
- `anthropic/claude-sonnet-4.6` — $3/$15 per M
- `anthropic/claude-opus-4.7` — $5/$25 per M
- `openai/gpt-5.5` — $5/$30 per M

---

## Headers our HTTP client must send

```
Authorization: Bearer ${OPEN_ROUTER_API_KEY}
Content-Type: application/json
HTTP-Referer: https://github.com/dereksantos/cortex   (attribution)
X-Title: cortex-eval-harness                          (attribution)
```

`HTTP-Referer` and `X-Title` are optional but OpenRouter-recommended for
attribution and may affect routing/quality reporting. Worth setting.

---

## Open questions deferred to later steps

- **Provider pinning.** Some `:free` models can be routed to specific
  providers via `provider: { only: ["OpenInference"] }` in the request
  body. Worth wiring into the OpenRouter Provider in step 2 so the eval
  runner can pin around saturated providers.
- **Retry-with-backoff policy.** Free-tier 429s need bounded retries.
  Belongs in TODO 8 (grid runner), not the provider itself.
- **BYOK toggle.** If the user attaches an Anthropic key to OR, our paid
  Claude calls bypass OR's markup. Not relevant until budget pressure.
- **Daily-cap verification.** Probe didn't hit OR's user-tier daily cap.
  Defer empirical verification to the smoke run (TODO 11).
