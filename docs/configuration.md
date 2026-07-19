# Configuration

The one page for configuring `cortex`: where config lives, what a minimal
setup looks like, what the zero-config default does, every `tools.*` gate,
and every environment variable that changes behavior. Everything below is
verified against `cmd/cortex/config.go` and the code that reads each
setting — no aspirational fields.

This describes the config `cmd/cortex` itself reads (the layered
`~/.cortex/config.json` → project `.cortex/config.json` → `CORTEX_BACKEND`
chain). `pkg/config` is a separate, mostly-dormant tree used by a few other
importers (`internal/capture`, `internal/storage`); it is not what the REPL,
`turn`, `study`, or `serve` read.

## File locations and precedence

1. `~/.cortex/config.json` (or `$CORTEX_HOME/config.json` when `CORTEX_HOME`
   is set) — user defaults.
2. The nearest `.cortex/config.json` found by walking up from the current
   directory — project overrides.
3. `CORTEX_BACKEND` (env) — supplies `backend.endpoint` only when neither
   config file set one.

Both files merge field-by-field (`mergeConfig` in `config.go`): a field
present in the project file overrides the same field from the user file;
everything else falls through. Neither file is required — with no config at
all, `cortex` targets `http://localhost:4000` (the conventional local
LiteLLM/OpenAI-compatible port) with no model pinned, which will fail to
connect unless something is actually listening there.

## The `backend` block

```json
{
  "backend": {
    "type": "openrouter",
    "endpoint": "https://openrouter.ai/api/v1",
    "key_env": "OPENROUTER_API_KEY"
  }
}
```

`backend.type` has exactly **one** value the code special-cases:
**`"openrouter"`** (case-insensitive, `Config.isOpenRouter()`). Setting it
turns on:

- the curated free-model default (see below) when no `models.code` /
  `models.study` is pinned;
- OpenRouter usage accounting on responses (`Usage.Include`);
- the OpenRouter reasoning-effort wire dialect instead of
  `chat_template_kwargs`;
- the startup preflight substitution check (below).

Any other value — including empty/unset, `"ollama"`, `"litellm"`, or
anything else — is treated identically: a generic OpenAI-compatible
chat-completions endpoint. `cortex` optionally probes that endpoint's
`/model/info` (LiteLLM's shape) at startup for auto window/thinking-mode
detection; an endpoint that doesn't serve it (a bare Ollama or
`llama.cpp --server`) just prints one note that discovery is unavailable
and expects `models.<role>.window` to be set by hand. There is no
Anthropic-native or Ollama-native request path in `cmd/cortex` — every
backend receives the same OpenAI-style chat-completions request body.

## Auth

- `key_env` — the **name** of an environment variable holding the API key.
  Read at call time (`os.Getenv`); never written to disk.
- `key_service` — a macOS Keychain service name, read via
  `security find-generic-password -s <service> -w`. Used only if `key_env`
  is unset or its variable is empty.
- Per-role `models.<role>.key_env` / `key_service` override
  `backend.key_env` / `backend.key_service` for that role only.

There is **no automatic fallback** to a provider-named environment variable
(e.g. `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`) on this path — those names
only work because the examples in this doc set `key_env` to them explicitly.
(`OPEN_ROUTER_API_KEY`/`ANTHROPIC_API_KEY` auto-resolution exists in
`pkg/secret`/`pkg/llm.NewLLMClient`, but `cmd/cortex` does not call that
resolver.)

A backend requiring a key with none resolved is not rejected locally —
`cortex` sends the request and reports the provider's own auth error (e.g.
OpenRouter's `401 No cookie auth credentials found`).

## The two agent roles

`code` drives the interactive/headless agent. `study` drives the `study`
subagent, the summarizer, and the shell-risk classifier. They typically
point at the same model. A third role, `embed`, is parsed but **reserved**
— nothing on a live path resolves it yet (kept for a future semantic
`memory_search`). Six other role names that used to appear in older
configs or docs (`hard-code`, `reason`, `fast`, `rerank`, `tools`, and any
other unrecognized key under `models`) are inert: loading a config with one
just prints a one-line stderr warning and ignores the key.

Minimal pinned example:

```json
{
  "backend": {
    "type": "openrouter",
    "endpoint": "https://openrouter.ai/api/v1",
    "key_env": "OPENROUTER_API_KEY"
  },
  "models": {
    "code":  { "model": "qwen/qwen3-coder", "window": 131072 },
    "study": { "model": "deepseek/deepseek-r1" }
  }
}
```

### Zero-config story

With `backend.type` set to `"openrouter"` and **no** `models` block at all,
both roles default to the top entry of the curated free-model table
(`cmd/cortex/curated.go` — currently `qwen/qwen3-coder:free`, a
large-context coder model):

```json
{
  "backend": {
    "type": "openrouter",
    "endpoint": "https://openrouter.ai/api/v1",
    "key_env": "OPENROUTER_API_KEY"
  }
}
```

At every session start, `cortex` does one bounded (4s) live check against
OpenRouter's `/api/v1/models`. If the curated pick has been retired or rate
limited, it substitutes the next surviving entry from the curated table, or
failing that discovers a `:free` model by a context/name heuristic —
prints one line naming old → new and why, and journals the event. This is
per-process only; the config file is never rewritten. A failed/slow check
(network down) just leaves the configured model unchanged — it never blocks
startup.

**What is not (yet) live:** an interactive first-run prompt that asks for
and stores an OpenRouter key. `cmd/cortex/bootstrap.go` defines that chain
(`BackendResolver` + `GuidedSetup`) but nothing in `main.go` calls
`Resolve()` outside tests — running `cortex` with no config file at all
does not prompt; it silently targets `localhost:4000` and fails to connect
after three retries if nothing is listening there. Getting to a green turn
today requires hand-writing (or copy-pasting) a config file as shown above.

## `tools.*` gates

All fields live under a top-level `"tools"` object. `nil`/absent means the
tool ships enabled **except** `enable_effort_escalation`, which defaults
off (opt-in behavior change, not an availability kill-switch).

| Field | Gates | Default |
|---|---|---|
| `allow_delete` | `remove_path` (workspace-confined delete). `false` strips the tool from the wire entirely. | `true` |
| `delete_root` | The confinement root `remove_path` is restricted to (absolute path). | workspace root (`.`) |
| `enable_web` | Both `web_search` and `fetch_url`. | `true` |
| `enable_agent` | The `agent` general-implementation subagent tool. | `true` |
| `enable_scan` | The `scan_landscape` coder tool. | `true` |
| `enable_context_evict` | `context_evict`. | `true` |
| `enable_context_merge` | `context_merge`. | `true` |
| `enable_context_adjust_watermarks` | `context_adjust_watermarks`. | `true` |
| `enable_effort_escalation` | The stuck-guard's one-shot reasoning-effort escalation (`docs/thinking-models.md` §5c) — an engine behavior, not a tool. | `false` |

Every `false` above strips the corresponding tool declaration from the
wire request, not just the dispatch path — a disabled tool is invisible to
the model, not just refused if called (`session_core.go`'s
`filterEnabledTools`).

Example disabling deletion and web access:

```json
{
  "tools": {
    "allow_delete": false,
    "enable_web": false
  }
}
```

## Model fields (`models.<role>`)

| Field | Meaning |
|---|---|
| `endpoint` | Overrides `backend.endpoint` for this role only. |
| `model` | Model id sent to the backend. |
| `window` | Context window size (tokens) used for budget/gauge math; auto-filled from `/model/info` when the backend serves it. |
| `max_tokens` | Per-request output-token cap. Defaults: 16384 (code), 8192 (study). |
| `temperature` | Overrides `backend`/global temperature for this role. |
| `key_env` / `key_service` | Per-role auth override (see Auth above). |
| `thinking` | Reasoning-effort intent — `false`/`true` (legacy bool), a level string (`"off"`/`"on"`/`"low"`/`"medium"`/`"high"`), or `{"budget": N}`. See `docs/thinking-models.md`. Both live roles default to `"on"`. |

## Environment variables

| Variable | Effect |
|---|---|
| `CORTEX_BACKEND` | Fallback `backend.endpoint` when config sets none. |
| `CORTEX_HOME` | Redirects the whole user-level state tree (`config.json`, journal, session registry) off `~/.cortex`. |
| `CORTEX_LOOP_STREAM` | `0`/`false`/`no`/`off` disables token streaming. |
| `CORTEX_LOOP_RENDER` | `0`/`false`/`no`/`off` disables the rendered/anchored turn UI, falling back to raw streaming. |
| `CORTEX_LOOP_STUDY_WINDOW` | Overrides the `study` subagent's context window. |
| `CORTEX_STUDY_REPS` | Rep count for `cortex study-eval` (dev/CI, not needed for normal use). |
| `CORTEX_LOCAL_EMBED` | Falsey disables the local Hugot embedder default. |
| `CORTEX_HUGOT_ONNX` | Picks a specific ONNX variant for the local embedder. |
| `CORTEX_TEMPERATURE` | Pins sampling temperature for every request (mainly for deterministic eval runs); unset preserves each backend's own default. |
| `CORTEX_LLM_DEBUG` | Non-empty dumps every outbound request/response body to stderr — debugging only, will print secrets in headers if you look; don't leave it on. |
| `CORTEX_COMPAT_TIMEOUT_SEC` | Overrides the per-request HTTP timeout for OpenAI-compatible backends. |
| `NO_COLOR` | Disables ANSI color output. |
| `DISCORD_BOT_TOKEN`, `DISCORD_CHANNEL_ID`, `DISCORD_SESSION_ID` | Discord adapter (`cortex discord`). |

Not listed: `CORTEX_LOCAL_ONLY` exists in `pkg/llm` but currently has no
caller reachable from `cmd/cortex` — it is orphaned from a deleted eval
runner, not a working knob.

## See also

- [`README.md`](../README.md) — quick start and command reference.
- [`docs/thinking-models.md`](thinking-models.md) — the `thinking` field's
  effort vocabulary and per-dialect translation.
- [`docs/completion-roadmap.md`](completion-roadmap.md) — Track E1/E2 for
  why the role surface and curated fleet look the way they do.
