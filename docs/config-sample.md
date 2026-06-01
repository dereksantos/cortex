# `.cortex/config.json` — operator config sample

Per-machine config that pins endpoints, declares per-model capability
tags, and routes specific DAG nodes to specific models. The file lives
at `<project>/.cortex/config.json` and is **gitignored** by default —
each operator (or fleet) configures their own.

This document shows the shape Cortex's developer uses against a local
chatterbox fleet (`coder` = Qwen3-Coder-30B-A3B-Instruct,
`reasoner` = gpt-oss-20b, `xlam-1b-fc-r` = the tool-call specialist,
plus `reranker` and `embedder`). Adapt the names + URLs to your own
endpoint roster.

## Full sample

```json
{
  "context_dir": "/path/to/project/.cortex",
  "project_root": "/path/to/project",
  "global_dir": "/Users/you/.cortex",
  "project_id": "myproject",
  "skip_patterns": [".git", "node_modules", ".cortex", "__pycache__"],

  "ollama_url": "http://localhost:11434",
  "ollama_model": "qwen2.5-coder:1.5b",
  "ollama_embedding_model": "nomic-embed-text",

  "endpoints": [
    {
      "name": "chatterbox",
      "base_url": "http://chatterbox:4000",
      "max_context_override": 65536,
      "models": [
        "coder",
        "reasoner",
        "reranker",
        "embedder",
        "xlam-1b-fc-r"
      ],
      "model_capabilities": {
        "coder":    ["coding", "coding:specialist", "tool-calling"],
        "reasoner": ["reasoning", "reasoning:specialist", "tool-calling"],
        "reranker": ["reranking"],
        "embedder": ["embeddings"]
      }
    }
  ],

  "routing": {
    "decide.next": "openai/gpt-5.4"
  },

  "default_model": "coder",
  "enable_graph": true,
  "enable_vector": true
}
```

## Section-by-section

### `endpoints[]`

One entry per OpenAI-compatible endpoint reachable on the machine /
LAN. `base_url` is the endpoint's root (no `/v1`); Cortex hits
`/v1/models` for capability discovery and `/v1/chat/completions` for
calls. `max_context_override` pins the runtime context window when
the endpoint's `/v1/models` response advertises the model's theoretical
max instead of the deployed size (lemonade does this).

`models[]` is the list of bare model ids served by this endpoint —
enables bare-name routing so `cortex --model coder` resolves to
`chatterbox/coder` instead of falling through to Ollama.

### `model_capabilities`

Per-model capability tag overrides for endpoints whose `/v1/models`
response doesn't advertise labels (chatterbox / LiteLLM proxies don't).
Precedence in the registry:

```
endpoint-supplied labels  >  model_capabilities map  >  id-pattern inference
```

The id-pattern detector in `pkg/llm/capabilities.go` recognizes
`o1/o3/o4`, `qwen3`, `claude`, `gpt-4/gpt-5`, family substrings like
`coder` / `codestral` / `rerank` / `embed`, etc. — but it can't guess
operator aliases like `reasoner` or `coder`. The map fills that gap.

Common capability tags (from `pkg/llm/capabilities.go`):

| Tag | Picked by |
|---|---|
| `coding`, `coding:specialist` | `decide.tool_call`, agent loop, code synth |
| `reasoning`, `reasoning:specialist` | `decide.next`, `sense.estimate_scope`, audit/review synth |
| `tool-calling`, `tool-calling:specialist` | `decide.tool_call` JSON emitter |
| `reranking` | `cortex_search` Full mode rerank pass (when wired) |
| `embeddings` | indexing / vector search |
| `vision` | image-bearing prompts (reserved) |

Tag a local mid-size reasoning model (gpt-oss-20b, qwen3-14b, etc.) as
`reasoning:specialist` ONLY if you've confirmed it can produce
multi-claim synthesis answers. If you tag it and it's not actually
strong enough at planning, your `decide.next` will silently route there
via the Requires chain and emit malformed plans — pin `decide.next` to
a known-good planner via the `routing` map (below) to keep that
guarantee separable from the specialist tag.

### `routing`

Per-DAG-node hard pin. Maps a qualified node name to a model id any
provider factory can resolve. Takes precedence over the node's
Requires chain but loses to per-spawn `attrs.model` overrides.

This is the operator escape hatch: when the auto-pick is wrong for
your fleet, pin it without touching code. Example use cases:

```json
"routing": {
  "decide.next":            "openai/gpt-5.4",
  "sense.classify_intent":  "qwen3-1.7b-FLM",
  "attend.compress":        "qwen3-4b-Instruct-2507-GGUF"
}
```

The Cortex-developer config pins `decide.next` to OpenRouter's
`openai/gpt-5.4` because the local `chatterbox/reasoner`
(gpt-oss-20b) emits step-by-step iterative plans instead of the full
multi-node DAG shape the executor expects — gpt-5.4's compositional
planning produces complete plans in one shot. The pin costs a few
cents per turn but unblocks the whole chain. Local-only fleets can
omit this if their reasoner is strong enough at compositional planning
(qwen3-30b-A3B-Instruct, llama-4-maverick, deepseek-v3, etc.).

Missing model ids in routing pins fall through to the Requires chain
rather than blocking — same graceful-degrade pattern as a stale
`attrs.model`.

### `default_model`

The session-default model when neither `--model` nor `CORTEX_REPL_MODEL`
is set. Bare names resolve through `model_capabilities` lookup against
the endpoint roster.

## Verification

After editing config:

```bash
cortex models
```

Should show every model with capability tags applied per the map:

```
chatterbox:
  coder    (ctx=65536, coding specialist)
  reasoner (ctx=65536, reasoning specialist)
  reranker (ctx=65536, reranker)
  embedder (ctx=65536, embedder)
  xlam-1b-fc-r (ctx=65536, tool-call specialist)
```

The recommender at the bottom should pick a model for each role:

```
  code   ollama/qwen2.5-coder:7b  — local · coding,coding:specialist,tool-calling
  reason chatterbox/reasoner       — cloud · reasoning,reasoning:specialist,tool-calling
  rerank chatterbox/reranker       — cloud · reranking
```

If `(no inferred capabilities)` shows next to a model name, the map
entry is missing or the operator-supplied labels aren't being merged.
Run `cortex tools --out tools.json` then `cortex models` again to pick
up config edits without restart.

## References

- Path B (per-node model swap): [`docs/prompts/loop-codebase-44.md`](prompts/loop-codebase-44.md)
- Capability inference: `pkg/llm/capabilities.go`
- Routing config schema: `pkg/config/config.go` — `EndpointDef`, `Config.Routing`
- Per-node routing design: [`docs/per-node-routing-plan.md`](per-node-routing-plan.md)
