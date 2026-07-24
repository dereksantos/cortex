# Configuration

The one page for configuring `cortex`: where config lives, what a minimal
setup looks like, what the zero-config default does, every `tools.*` gate
and numeric cap, every `models.<role>`/`subagents`/`limits`/`network`/
`serve`/`repl`/`discord` field, and every environment variable that changes
behavior. Everything below is verified against `cmd/cortex/config.go` and
the code that reads each setting — no aspirational fields. Every field on
this page is optional; a config that never mentions a section behaves
byte-identically to today's hardcoded value.

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
`memory_search`). Five other role names that used to appear in older
configs or docs (`hard-code`, `reason`, `fast`, `rerank`, `tools`) are
inert, and so is any other unrecognized key under `models`: loading a
config with one just prints a one-line stderr warning and ignores the key.

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

### Interactive first-run setup

Running `cortex` (or `cortex resume`) from a real terminal with **no** user
config, **no** project config, and **no** `$CORTEX_BACKEND` set is a true
first run: before the REPL starts, `cortex` walks the `BackendResolver`
chain (`cmd/cortex/bootstrap.go`) and, finding nothing already configured,
runs `GuidedSetup` (`cmd/cortex/bootstrap_wire.go`):

1. It prints a pointer to https://openrouter.ai/keys and prompts for a key
   (`OpenRouter API key: `). Pressing Enter with no input skips setup —
   `cortex` falls through to the previous behavior (targets
   `localhost:4000`, reports a connection error) and nothing is written.
2. A pasted key is stored in the macOS Keychain (service
   `cortex-openrouter`, the same convention `pkg/secret` already uses) and
   exported into the current process's environment so the very first turn
   can use it immediately.
3. The result is persisted to `~/.cortex/config.json` (`PersistBackend`):
   `backend.type: "openrouter"`, `backend.key_service: "cortex-openrouter"`
   (so a later launch finds the key without prompting again), and
   `models.code`/`models.study` seeded from the curated table's top pick —
   exactly the shape shown above, written for you.

This entire flow is skipped — silently, unchanged from before — whenever
any of the three bypass conditions holds (a user config file, a project
`.cortex/config.json`, or `$CORTEX_BACKEND`), or when stdin isn't a
terminal (a piped/CI/driver invocation): a **non-interactive** first run
instead prints one hint line to stderr naming the config path and this
doc, then proceeds exactly as before (targets `localhost:4000`, fails to
connect after a few retries if nothing is listening there). Headless
subcommands (`turn`, `study`, `scan`, `serve`, `discord`, …) never run this
flow at all, whether or not stdin is a terminal — only the bare REPL entry
points do.

The `KeyProbe`/`OllamaProbe`/`SmokeProbe` stages of the chain are also live
in production: an existing `$OPENROUTER_API_KEY` or an existing
`cortex-openrouter` Keychain entry short-circuits the prompt (same
persisted shape, `key_env`/`key_service` set accordingly), and a reachable
local Ollama is tried before falling back to the guided prompt. The
one-shot tool-call `SmokeProbe` itself is not wired in this pass — a stale
curated pick still self-heals at every session start via the zero-config
preflight check described above, which covers the same failure mode.

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

## `tools.*` numeric caps

The gates above turn tools on/off; these size them. All optional, all
default to today's hardcoded value.

| Field | Default | Meaning |
|---|---|---|
| `curation_budget_tokens` | 16000 | `read_file` → `study` redirect threshold: a whole-file read estimated above this is refused and redirected to `study`. |
| `max_tool_output` | 10000 | Cap on tool output chars fed back into context (bash, oversized reads). |
| `outline_default_budget` | 4000 | `outline` tool's structure budget when the model omits one. |
| `read.default_range_lines` | 200 | `read_file`'s window when `start` is given without `end`. |
| `read.max_range_lines` | 800 | Cap on a single ranged `read_file`. |
| `read.max_read_bytes` | 24000 | Per-read byte ceiling (bounds very-long-line spans the line cap alone can't). |
| `grep.max_hits` | 100 | Cap on `grep` match count. |
| `grep.line_cap` | 1200 | Window width for a long matching line (centered on the match). |
| `grep.max_output_bytes` | 6000 | Total-output ceiling for one `grep` call. |
| `fetch_url.timeout_sec` | 20 | HTTP timeout for `fetch_url`. |
| `fetch_url.max_redirects` | 5 | Redirect cap for `fetch_url`. |
| `fetch_url.max_body_bytes` | 1048576 (1 MiB) | Download/parse ceiling for `fetch_url` and `web_search`. |
| `web_search.default_max_results` | 5 | `web_search`'s result count when the model omits `max_results`. |
| `web_search.maximum_max_results` | 10 | Cap on `web_search`'s `max_results` argument. |

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
| `request_timeout_sec` | Per-request HTTP timeout for this role's model calls. Precedence: this field → `CORTEX_COMPAT_TIMEOUT_SEC` (env) → the historical default (10 min for the coder/subagent transport path, `models.study`'s summarizer/shell-risk sub-calls included). |
| `max_send_attempts` | Retry ceiling for a transient failure (transport error, 429/5xx) on this role's calls. Default 3. |
| `retry_backoff_ms` | Base linear-backoff delay between retries (`attempt × retry_backoff_ms`). Default 500ms. |

`request_timeout_sec`/`max_send_attempts`/`retry_backoff_ms` only exist under `models.code` and `models.study` — the coder's own turn and the `agent` subagent (which runs on the coder's live model) use `models.code`'s values; the `study` subagent plus the summarizer and shell-risk classifier (both of which build their sub-LLM client from the `study` binding) use `models.study`'s.

## `subagents.*` — Study/Agent/Learn/LearnUser profile bounds

```json
{
  "subagents": {
    "seed_budget_tokens": 6000,
    "study": { "max_tokens": 8192, "max_iter": 12, "read_budget_bytes": 96000 },
    "agent": { "max_tokens": 8192, "max_iter": 20, "read_budget_bytes": 128000 },
    "learn": { "max_tokens": 8192, "max_iter": 12, "read_budget_bytes": 96000 },
    "learn_user": { "max_tokens": 4096, "max_iter": 8, "read_budget_bytes": 32000 }
  }
}
```

| Field | Default | Meaning |
|---|---|---|
| `seed_budget_tokens` | 6000 | Outline budget used to seed EITHER profile before its loop starts (shared — one constant, not per-profile, hence this lives at the top level rather than duplicated under `.study`/`.agent`/`.learn`/`.learn_user`). |
| `study.max_tokens` / `study.max_iter` / `study.read_budget_bytes` | 8192 / 12 / 96000 | The `study` subagent's output-token cap, tool-call iteration cap, and cumulative read-byte budget. |
| `agent.max_tokens` / `agent.max_iter` / `agent.read_budget_bytes` | 8192 / 20 / 128000 | The `agent` subagent's same three bounds. |
| `learn.max_tokens` / `learn.max_iter` / `learn.read_budget_bytes` | 8192 / 12 / 96000 | The background learning-loop subagent's (`docs/learning-loop.md`) same three bounds — a separate section from `.study` even though `learn` also runs on the study role's model binding (see below), since the two profiles' bounds are independently tunable. |
| `learn_user.max_tokens` / `learn_user.max_iter` / `learn_user.read_budget_bytes` | 4096 / 8 / 32000 | The cross-project promotion subagent's (`docs/cross-source-learning.md` piece 1's promotion half, `cmd/cortex/learn_user.go`) same three bounds — a separate section from `.learn` even though both run on the study role's model binding: `learn_user`'s per-call seed is one candidate group's notes (small), not a whole capture-window digest, so lower defaults than `.learn`. |

**Naming honesty**: this section configures PROFILES (the `study`, `agent`, `learn`, and `learn_user` subagents), not model roles — despite the name overlap with `models.study`, `subagents.agent` does **not** run on a "study" or "agent" role binding. The `agent` profile runs on the **coder's own live model** by default (an optional per-call `model` argument on the `agent` tool call can pin a different one); `learn` and `learn_user` both run on the **study role's binding**, exactly like `study` itself (neither has a coder session to inherit from — both can run from a scheduled loop firing with no coder turn live at all). The original audit sketched this as `models.study.subagent.{study,agent}`; it landed as a top-level `subagents` section instead, because nesting it under `models.study` would have implied the `agent` profile is bound to the study role, which it isn't.

## `limits.*` — assorted byte/count ceilings

| Field | Default | Meaning |
|---|---|---|
| `max_tool_iterations` | 100 | Bounds the coder turn's tool-call loop. |
| `max_instruction_bytes` | 16384 | `AGENTS.md` truncation cap. |
| `memory_index_cap_chars` | 4000 | Truncation cap on the injected PROJECT-tier memory-note index. |
| `user_memory_index_cap_chars` | 1500 | Truncation cap on the injected USER-tier memory-note index (`~/.cortex/memory`, shared across every project on the machine) — independent of `memory_index_cap_chars`; the user tier renders first, above it, in the turn-start injection. See `docs/cross-source-learning.md` piece 1. |
| `capture_excerpt_cap_chars` | 280 | Truncation cap on the final-answer excerpt the journal capture records. |
| `max_task_context_chars` | 800 | Truncation cap on the turn context folded into the shell-risk classifier's prompt. |
| `route_max_output_tokens` | 80 | Output-token cap on Discord's continue/new-change routing classifier. |
| `max_served_models_shown` | 40 | Cap on the served-model list `cortex model` prints (the full list is still in `--json`). |

### Memory tiers and the `scope` tool arg

`memory_write`/`memory_read`/`memory_search`/`memory_forget` all take an
optional `scope` argument: `"project"` (this codebase's `.cortex/memory` —
the default for write/forget) or `"user"` (`~/.cortex/memory`, shared across
every project on the machine). An unscoped `memory_read` shadows
project-over-user (a same-named project note wins silently); an unscoped
`memory_search` spans both tiers, tagging each hit `[project]`/`[user]`. No
config gate — every session with memory enabled gets both tiers. See
`docs/cross-source-learning.md` piece 1 and `docs/memory-tools.md`.

## `network.*` — bounded-probe timeouts and self-healing

| Field | Default | Meaning |
|---|---|---|
| `fleet_discovery_timeout_sec` | 4 | Timeout for the `/model/info` fleet-discovery probe. |
| `preflight_timeout_sec` | 4 | Timeout for the startup model preflight's `ListModels` call — also bounds the healing ladder's mid-session catalog fetch. |
| `self_heal` | `true` | Model self-healing (`docs/model-self-healing.md`). On: a model call failing with a healable class (model unknown/retired; rate-limited or 5xx after the transport's own retries) marks the model dead for the session, walks the curated `:free` ladder (then catalog discovery), and re-issues the same pending request on the replacement — one stderr notice + a `model.substitution` journal event per switch, `model.failure` when nothing recovers. The startup preflight also substitutes a **pinned** OpenRouter model missing from the live catalog. Off: the preflight reverts to curated-picks-only and a failing model simply surfaces its classified error. OpenRouter only — there is no free suite behind a local endpoint. The config file is never rewritten either way. |
| `ollama_probe_timeout_sec` | 2 | Timeout for the guided first-run setup's local-Ollama reachability check. **Not actually reachable in practice**: this probe only ever runs during `GuidedSetup` on a true first run (no user config, no project config, no `$CORTEX_BACKEND` — see "Interactive first-run setup" above), which by definition means no config file exists yet to set this field from. Kept in the schema for documentation parity with the rest of `network.*`. |
| `compat_timeout_sec` | 300 | Sets the OpenAI-compatible client's fallback per-request timeout default (`pkg/llm`'s `DefaultCompatTimeoutSec`). Unlike every other timeout field on this page, **`CORTEX_COMPAT_TIMEOUT_SEC` (env) wins over this field** when both are set — matching the env knob's original subprocess-boundary rationale. A `models.<role>.request_timeout_sec` set explicitly still wins over both. |

## `serve.*` — `cortex serve` tunables

| Field | Default | Meaning |
|---|---|---|
| `port` | 7433 | Bind port when `--port` isn't passed on the command line (`--port` always wins over this). |
| `session_idle_timeout_min` | 30 | How long an untouched live session stays in memory before `SessionManager` evicts it (a later request transparently re-hydrates from disk). |
| `loop_cadence_floor_min` | 5 | Minimum non-zero interval a loop spec may run on (`internal/loops.CadenceFloorMinutes`). |
| `loop_auto_disable_strikes` | 3 | Consecutive failed firings that auto-disable a loop (D11's self-pacing tuning). |
| `project_sessions_limit` | 50 | Cap on how many sessions the `/api/projects/{name}/sessions` listing endpoint returns per project. |

## `prompt.*` — system-prompt customization

| Field | Default | Meaning |
|---|---|---|
| `file` | (unset) | Path to a file that replaces the built-in base system prompt. `~` expands; a relative path resolves upward from CWD (the AGENTS.md rule, so `.cortex/prompt.md` works from any subdirectory); truncated at the instruction cap. An unreadable or whitespace-only file warns on stderr and keeps the built-in — a broken path degrades to a working agent, never a silent empty prompt. |
| `append` | (unset) | Text appended after the base prompt (and before any AGENTS.md section), whether the base is built-in or file-replaced. |

## `repl.*` — interactive REPL tunables

| Field | Default | Meaning |
|---|---|---|
| `ticker_interval_ms` | 1000 | Wall-clock refresh period of the "thinking… Ns" elapsed label during a streaming turn. |

The braille spinner set was removed (2026-07-19); there is deliberately no
spinner-cadence knob. The line editor's own 90ms live-redraw tick
(`internal/lineedit`) is a separate, lower-level rendering loop from the
thinking-ticker above and was evaluated but left hardcoded — wiring it would
add a second, easily-confused REPL timing knob for a redraw-smoothness
concern nobody has asked to tune.

## `discord.*` — Discord adapter tunables

| Field | Default | Meaning |
|---|---|---|
| `typing_refresh_sec` | 8 | How often the typing indicator is re-triggered during a long turn. |
| `risk_approval_timeout_sec` | 120 | How long a risky-command approval prompt stays open before lapsing to headless-Blocked. |
| `progress_edit_interval_ms` | 1500 | Throttle on live status-message edits during a turn. |
| `route_confidence_threshold` | 0.8 | Confidence bar the continue/new-change router must clear to reset the session; must be in `(0, 1]` when set. |

`typing_refresh_sec`/`risk_approval_timeout_sec`/`progress_edit_interval_ms`
are process-wide (set once at `cortex discord` startup);
`route_max_output_tokens` (`limits.*`) and `route_confidence_threshold` are
read per-session from the live `*CortexSession`'s config.

## `context.*` — two-zone working-set fractions

```json
{
  "context": {
    "tail_high_fraction": 0.5,
    "tail_drain_fraction": 0.333,
    "outline_fraction": 0.125
  }
}
```

| Field | Default | Meaning |
|---|---|---|
| `tail_high_fraction` | 0.5 (W/2) | Fraction of the window at which the hydrated tail (zone B) triggers demotion — `docs/context-architecture.md`'s high watermark. |
| `tail_drain_fraction` | 1/3 ≈ 0.333 (W/3) | Fraction of the window demotion drains the tail down to — the low watermark. Also gates `recall`'s output size (`tool_deps.go`). |
| `outline_fraction` | 0.125 (W/8) | Fraction of the window the demoted-turn outline (zone A) may grow to before it folds via the summarizer. |

**These are eval-verified defaults** — `cmd/cortex/context_eval_test.go`'s
deterministic Δ suite and the live fleet eval
(`context_eval_live_test.go`, `CORTEX_LIVE_FLEET=1`) both run at the
defaults above (W/2, W/3, W/8); changing them moves the working set off the
configuration those evals actually exercised. The loader enforces a safety
inequality (below) so a bad combination fails fast at config load rather
than surfacing as a live prompt-size regression.

Each field is an independent fraction of the resolved window `W`
(`windowSize()`) — a pointer, like `route_confidence_threshold`, so an
explicit-but-invalid `0.0` is distinguishable from "not set." **Bit-identical
subtlety**: when a field is unset, the resolver uses the ORIGINAL
integer-division expression (`W/2`, `W/3`, `W/8`) verbatim, not
`W * defaultFraction` — integer division and float multiplication are not
guaranteed to agree for every `W`, and "omitted config reproduces today's
value exactly" is the whole point of the defaults above. Float math (
`int(float64(W) * fraction)`) only runs once a field is explicitly
configured (`cmd/cortex/config.go`'s `tailHighWatermark`/
`tailDrainWatermark`/`outlineBudget`).

**The safety inequality**, enforced at load time whenever ANY field in this
section is explicitly set (an absent `context` section skips validation
entirely):

- Each fraction must be in `(0, 1)`.
- `tail_high_fraction` must be strictly greater than `tail_drain_fraction`
  — demotion needs hysteresis (the tail must grow past the drain target
  before demoting fires, or every turn re-triggers it).
- `tail_high_fraction + outline_fraction + 0.16 <= 0.8` — the dormancy
  inequality. `0.8` is the hardcoded compact-trigger threshold
  (`compactThreshold`, `main.go`; Derek's option-2 decision keeps it out of
  this config group). `0.16` (`contextPrefixHeadroom`, `config.go`) is a
  conservative constant standing in for the two zone-A pieces this section
  doesn't configure — the system prompt (+ AGENTS.md, capped by
  `limits.max_instruction_bytes`) and the memory index (capped by
  `limits.memory_index_cap_chars`) — sized against the smallest window these
  caps would plausibly still run against (`fallbackWindow`, 32768) so a
  smaller real window only makes the guarantee more conservative, never
  less. At the shipped defaults this leaves a deliberately thin (0.015)
  but real margin: `0.5 + 0.125 + 0.16 = 0.785 <= 0.8`.

Unset fields resolve to their documented default fraction for these two
cross-field checks — setting only `tail_high_fraction` still validates
against the *default* `tail_drain_fraction`/`outline_fraction`, not a
skipped check. A rejection names the inequality and the offending numbers,
e.g. `context: tail_high_fraction (0.7000) + outline_fraction (0.2000) +
prefix_headroom (0.1600, system prompt + memory index slack) = 1.0600
exceeds the compact trigger (0.8000) — …`.

## Validation

Every field above is optional; 0 (or, for `route_confidence_threshold` and
every `context.*` fraction, absent/`null`) means "not set — use the
default." An EXPLICIT nonsensical value — negative for any of the
count/byte/timeout fields, `route_confidence_threshold` outside `(0, 1]`, or
a `context.*` combination violating the range/hysteresis/dormancy invariants
above — is a fatal config-load error: `cortex` prints `cortex: <path>:
invalid config: <field> must be positive, got <value>` (or the
threshold-/context-specific message) to stderr and exits 1 rather than
silently falling back to the default. Unknown keys anywhere under a
recognized section (or an entirely unrecognized top-level section) are
ignored, not errors — the same forward-compatible behavior
`models.<role>`'s unknown-role warning already had.

## Environment variables

| Variable | Effect |
|---|---|
| `CORTEX_BACKEND` | Fallback `backend.endpoint` when config sets none. |
| `CORTEX_HOME` | Redirects the whole user-level state tree (`config.json`, journal, session registry) off `~/.cortex`. |
| `CORTEX_LOOP_STREAM` | `0`/`false`/`no`/`off` disables token streaming. |
| `CORTEX_LOOP_RENDER` | `0`/`false`/`no`/`off` disables the rendered/anchored turn UI, falling back to raw streaming. |
| `CORTEX_LOOP_STUDY_WINDOW` | Overrides the `study` subagent's context window. |
| `CORTEX_STUDY_REPS` | Rep count for `cortex study-eval` (dev/CI, not needed for normal use). `CORTEX_NAV_REPS` is a deprecated alias, still honored as a fallback. |
| `CORTEX_STUDY_PROBE_TIMEOUT` | Per-probe wall-clock cap, in seconds, for `cortex study-eval` — a thrashing probe fails fast instead of hanging the gate. Default `300`. |
| `CORTEX_LOCAL_EMBED` | Falsey disables the local Hugot embedder default. |
| `CORTEX_HUGOT_ONNX` | Picks a specific ONNX variant for the local embedder. |
| `CORTEX_TEMPERATURE` | Pins sampling temperature for every request (mainly for deterministic eval runs); unset preserves each backend's own default. |
| `CORTEX_LLM_DEBUG` | Non-empty dumps every outbound request/response body to stderr — debugging only, will print secrets in headers if you look; don't leave it on. |
| `CORTEX_COMPAT_TIMEOUT_SEC` | Overrides the per-request HTTP timeout for every model call `cmd/cortex` makes — the coder turn, every subagent, the summarizer, and the shell-risk classifier — UNLESS the relevant `models.<role>.request_timeout_sec` is set explicitly (that always wins). Previously only reached `pkg/llm`'s OpenAI-compatible client; unified across the whole transport path in the same audit that added `network.compat_timeout_sec` (see above). |
| `NO_COLOR` | Disables ANSI color output. |
| `DISCORD_BOT_TOKEN`, `DISCORD_CHANNEL_ID`, `DISCORD_SESSION_ID` | Discord adapter (`cortex discord`). |
| `DISCORD_PROJECT` | Registered project the Discord bot binds to (CWD-implicit when unset); an unrecognized name fails `cortex discord` at startup with a fatal error. |

Not listed: `CORTEX_LOCAL_ONLY` exists in `pkg/llm` but currently has no
caller reachable from `cmd/cortex` — it is orphaned from a deleted eval
runner, not a working knob.

## See also

- [`README.md`](../README.md) — quick start and command reference.
- [`docs/thinking-models.md`](thinking-models.md) — the `thinking` field's
  effort vocabulary and per-dialect translation.
- [`docs/completion-roadmap.md`](completion-roadmap.md) — Track E1/E2 for
  why the role surface and curated fleet look the way they do.
- [`docs/context-architecture.md`](context-architecture.md) — the two-zone
  working-set design `context.*` configures the fractions of.
- [`docs/memory-tools.md`](memory-tools.md) — the model-driven memory tools
  the `limits.*` memory-index caps and the `scope` arg govern.
- [`docs/cross-source-learning.md`](cross-source-learning.md) — the user
  memory tier, its shadowing rules, and the cross-project promotion design.
