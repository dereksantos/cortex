# Configuration

The one page for configuring `cortex`: where config lives, what a minimal
setup looks like, what the zero-config default does, every `tools.*` gate
and numeric cap, every `models.<role>`/`subagents`/`limits`/`network`/
`serve`/`repl`/`discord`/`skills`/`project` field, and every environment
variable that
changes behavior. Everything below is verified against `cmd/cortex/config.go` and
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
| `max_instruction_bytes` | 16384 | Truncation cap on the seeded project-instructions file (the first present, in priority order, of `AGENTS.md`, `CLAUDE.md`, `.github/copilot-instructions.md` — see "Project instructions" below). An over-cap file is cut at this size and marked `...[<file> truncated]`. |
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
| `file` | (unset) | Path to a file that replaces the built-in base system prompt. `~` expands; a relative path resolves upward from CWD (the AGENTS.md rule, so `.cortex/prompt.md` works from any subdirectory); truncated at the instruction cap. An unreadable or whitespace-only file warns on stderr and keeps the built-in — a broken path degrades to a working agent, never a silent empty prompt. A file-replaced base owns its own memory guidance: the built-in per-turn memory section (`memoryPromptSection`) is NOT injected on top of it — the memory INDEX note still injects either way (see `docs/memory-tools.md`). |
| `append` | (unset) | Text appended after the base prompt and the `attribution.*` line when one is on (and before any project-instructions section), whether the base is built-in or file-replaced. |

## Project instructions (seeded from the repo)

At session construction, Cortex seeds the system prompt with the repo's own
agent-instruction file. Resolution is **priority-ordered, first match wins —
no concatenation**: the first file present, in this order, is the one loaded
(`agentInstructionFiles`, `cmd/cortex/config.go`):

1. `AGENTS.md` — the cross-harness convention; stays first.
2. `CLAUDE.md`
3. `.github/copilot-instructions.md`

The list is deliberately short and documented in the code: every entry is a
file a real repo ships, and the order encodes intent (a repo with several
still loads exactly one).

Search rule: the walk starts at the working directory and goes up to the
filesystem root — the nearest directory from the CWD upward that contains any
candidate file wins, and within that directory the first file in the
priority order above is the one loaded (findUp semantics with the list
applied per level — a deeper directory's `CLAUDE.md` beats an ancestor's
`AGENTS.md`). The loaded file is trimmed, truncated at
`limits.max_instruction_bytes` (with a marker naming the file), and appended
to the system prompt as a
`# Project instructions (<file>)` section — the header names the file, and
`/context`'s system legend row shows it, so the seed always says where it came
from. When no candidate file exists anywhere up the chain, no section is
added (behavior identical to the old AGENTS.md-only rule).

The explicit-root leg (`--project`, serve) resolves the same list at the
project root via `Workspace.Instructions()`; the two legs are provably
identical for the same resolved file (`TestProjectInstructionsEquivalence`).

## `repl.*` — interactive REPL tunables

| Field | Default | Meaning |
|---|---|---|
| `ticker_interval_ms` | 1000 | Wall-clock refresh period of the "thinking… Ns" elapsed label during a streaming turn. |
| `gauge` | `"zones"` | Style of the two-zone context gauge shown in the prompt row: `"zones"` (default — two humanized numbers separated by a divider character, e.g. `10k` + divider + `100k`; zone A/head and the divider render gray, zone B/tail carries the green→yellow→red pressure color — near-limit is the only state that shouts), `"blocks"` (Unicode Block Elements eighth-block sparkline ramp bar, 9 levels), `"braille"` (braille density-ramp fill bar, 9 levels), `"ascii"` (structure-safe fallback ramp bar, 5 levels, no unicode), or `"numeric"` (the pre-bar "used/window" text). This setting is prompt-row-only — `/context` renders its own fixed 8×16 glyph grid over the whole model window (`cmd/cortex/context_grid.go`) regardless of `gauge`. |

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
  doesn't configure — the system prompt (+ the seeded project-instructions
  file, capped by
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

## `skills.*` — Agent Skills discovery

Cortex discovers [Agent Skills](https://agentskills.io/specification) (the
open, Linux-Foundation-governed standard): a directory `<name>/` containing a
`SKILL.md` whose YAML frontmatter declares `name` (must equal the directory
name) and `description`. Only `name`+`description` are ever injected into
context — the progressive-disclosure design the spec calls for; the body,
and any `scripts/`/`references/`/`assets/` it points at, load on demand via
`read_file`, exactly like any other file. See `internal/skills`.

```json
{
  "skills": {
    "enabled": true,
    "index_max": 20,
    "dirs": ["/absolute/path/to/skills"]
  }
}
```

| Field | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Availability kill-switch for discovery + index injection (nil/absent means enabled, like `tools.enable_web`). |
| `index_max` | 20 | Caps how many discovered skills the turn-start index lists; over-cap discovery appends one line noting how many were omitted rather than truncating silently. |
| `dirs` | (unset) | Overrides the four default discovery roots below **entirely** (not merged) when non-empty. |

Absent `dirs`, discovery walks these four roots, in precedence order —
project wins over user, cortex-native wins over compat:

1. `./.cortex/skills`
2. `./.claude/skills` (Claude Code compat)
3. `./.agents/skills` (Codex/generic compat)
4. `~/.cortex/skills` (`$CORTEX_HOME/skills` when set)

A name collision across roots resolves to the FIRST root that defines it —
later roots' same-named skill is silently shadowed. An invalid `SKILL.md`
(bad name charset, name/directory mismatch, description out of the spec's
1-1024 char range, missing frontmatter, or a description folded across
multiple lines — unsupported in v1, see `internal/skills`' package comment)
is skipped with one warning line on stderr; it never blocks discovery of the
rest.

The rendered index is injected at turn start alongside the memory index
(`cmd/cortex/turn.go`), in the same fixed wire slot — coder-only: the
Study/Learn/Agent subagent profiles are seeded from their own static system
prompt and never see it. `/context` surfaces it as a `skills` row (░ glyph)
in the grid legend when non-empty.

## `attribution.*` — commit and PR attribution markers

Marks work Cortex authors: a trailer on commits, and a footer the agent is
asked to put on pull request bodies. On by default.

```json
{
  "attribution": {
    "enabled": true,
    "commit": "Co-Authored-By: Cortex (<model>)",
    "pr": "Generated with Cortex",
    "include_model": true
  }
}
```

| Field | Default | Meaning |
|---|---|---|
| `enabled` | `true` | `false` turns attribution off entirely: no trailer is added anywhere and the system prompt carries no attribution line. |
| `commit` | `Co-Authored-By: Cortex (<model>)` | The commit trailer line. `<model>` is replaced with the code model's name. An explicit `""` turns off commit attribution only (and survives the user→project merge). |
| `pr` | `Generated with Cortex` | The line the system prompt asks the agent to end pull request bodies with. No `<model>` substitution. An explicit `""` turns off PR attribution only. |
| `include_model` | `true` | `false` removes the model from the trailer: ` (<model>)` is stripped (and any other `<model>` token). The same happens when no code model is known. |

The default trailer has no email address. GitHub only credits a
`Co-Authored-By` trailer in its contributor UI when it has the form
`Name <email>`, so set `commit` to include one if you want that.

### Where it applies

- **System prompt (every coder session).** When attribution is on, one line
  is added to the coder's system prompt, after the base prompt and before
  `prompt.append` and the project-instructions section (AGENTS.md, or the
  fallback files — see "Project instructions" above). It names the trailer (with the code model
  resolved at session start) and the PR footer verbatim, e.g. *Attribution:
  end every git commit message you author with the trailer line
  "Co-Authored-By: Cortex (qwen3-coder)", and end every pull request body you
  write with the line "Generated with Cortex".* A surface set to `""` is left
  out of the line. This covers the REPL, `cortex turn`, `cortex serve`,
  `cortex discord` and loop firings, which all build their session the same
  way. It is fixed for the session's lifetime (it is part of the cached
  prefix), and a resumed session keeps the system prompt stored in its
  transcript.
- **PR footer: prompt only.** Cortex never creates a pull request itself, so
  the footer has no mechanical backstop; whether a PR body ends with it is up
  to the model following the line above.
- **`cortex change commit`, loop-firing commits, Discord WIP checkpoints.**
  These commit mechanically through `git interpret-trailers --if-exists
  addIfDifferent --trailer <trailer>`: a message that already carries the
  identical trailer is not given a second copy, and a different trailer (a
  human `Co-Authored-By`, say) is kept alongside it. The model comes from
  `models.code` in config (no fleet discovery); without one, the model part
  is dropped. `cortex change commit` and the Discord checkpoint load config
  from the current directory; a loop firing uses its session's config.
- **`git commit` run by the agent through the `bash` tool.** Before the
  shell-risk gate classifies the command, the tool splices
  `--trailer='<trailer>'` in directly after `commit` (so it lands before any
  `--` and pathspecs), single-quoted so `$`, backticks and quotes in the
  template are never expanded. The gate and any confirmation prompt see the
  rewritten command. The trailer names the session's current code model. It
  is only applied when the whole command is one simple `git commit …`
  invocation that doesn't already contain the trailer text. Pipelines,
  `&&`/`||`/`;` chains, redirections or heredocs, subshells and command
  substitution, `git` options before `commit` (`git -C dir commit`),
  `--amend`, and `-F -`/`--file=-` are left exactly as written. When a
  command left alone this way still contains `git commit` and couldn't be
  parsed as one simple command, or reads its message from stdin (and isn't
  an `--amend`), the tool result gets a note naming the trailer the message
  should end with.

### What is recorded

Loop firings record whether the commit they made carried the trailer, as
`attributed` on the `loop.run` journal event (omitted when false or when the
firing made no commit). Commits from `cortex change commit`, Discord
checkpoints and the `bash` tool are not journaled with an attribution flag.

## Project commands, post-edit hook and workspace trust

Cortex discovers each project's own `format`/`lint`/`test`/`build` commands
from its manifests, and runs them after edits — but only in a workspace you
have trusted. This section is the single reference for that whole feature:
how the commands are found, when each runs, the trust gate, the mode switch,
and the budgets. The config keys involved live under `project`:

```json
{
  "project": {
    "commands": {
      "format": "custom-fmt -w {file}",
      "test": "go test -race ./..."
    },
    "trusted": ["/home/u/real-repo"]
  }
}
```

`commands`: keys are the four command roles `format`, `lint`, `test`,
`build`; each value is the shell command line for that role. Unknown
keys and blank values are ignored.

`trusted`: the workspace trust list (documented with the post-edit hook
below) — USER config only, managed with `cortex project trust`.

A command carrying the `{file}` placeholder is per-file: the post-edit hook
(below) substitutes the file just written. A command carrying `{dir}` is
per-package: the hook substitutes the file's `"./"`-prefixed package
directory, relative to the project root (`"./"` for a root-level file) —
the correct unit for cross-file tools (`go vet` type-checks a whole
package, so `go vet {dir}` never reports spurious `undefined:` for a symbol
defined in a sibling file). Commands without either placeholder apply to the
whole project and are reported but never auto-run per edit.

The same keys can be declared under a `## Commands` section of the
RESOLVED instruction file — the same file, resolved the same way, as the
system prompt's [Project instructions](#project-instructions-seeded-from-the-repo)
section: priority-ordered, first match wins (`AGENTS.md`, then
`CLAUDE.md`, then `.github/copilot-instructions.md`), capped at
`limits.max_instruction_bytes`.

```markdown
## Commands

- format: custom-fmt -w {file}
- lint: golangci-lint run
```

Only list items shaped `- <role>: <command>` in the LAST `## Commands`
section are read (roles case-insensitive, limited to the four roles);
the rest of the file is untouched. Precedence is field-by-field:
**config.json beats the instruction file, which beats discovery** — the
manifests
(`go.mod` → `gofmt -w {file}` / `go vet {dir}` / `go test ./...` /
`go build ./...`; `package.json` scripts — reported as their runnable npm
form, `npm run <script>` (or the bare `npm test` for the test script),
because a script body like `tsc -p tsconfig.json` only runs inside npm,
which puts node_modules/.bin on PATH and runs pre/post hooks — or
prettier/eslint deps; `pyproject.toml`'s `[tool.ruff]`/`[tool.black]` +
pytest; `Cargo.toml`; make targets named format/lint/test/build). Resolution happens once per
session in `resolveProjectCommands` (`cmd/cortex/session_core.go`), which
reads the resolved instruction file at the workspace root directly — the
single parsing path for that section — and labels the source with the
file's name (e.g. `CLAUDE.md`). A user-level `project.commands` entry
beats an instruction-file declaration for the same role, like every other
field-by-field merge.

| Field | Default | Meaning |
|---|---|---|
| `command_timeout_sec` | 10 | Per-command budget (seconds) for each hook run: the per-edit format and each turn-end lint run (capped at the time left in `turn_lint_budget_sec`). A slow formatter or linter is cut off here and the note/receipt reports the elapsed time. |
| `turn_lint_budget_sec` | 60 | TOTAL wall-clock budget (seconds) for the turn-end lint pass. It caps the whole pass at once: a lint run that has not started when the budget is spent is not run, and a run in progress is cut off at the budget (the deadline travels into the run). A slow linter is reported as a budget hit in the receipt, not as a per-file timeout. |

**The post-edit hook.** After `write_file`/`edit_file` lands, the session
runs the project's format command on the file just touched, so an
unformatted file never reaches review. It is FORMAT-ONLY: lint is slow and
noisy per edit (clippy, eslint), so it is not run per edit. Instead, on a
TRUSTED workspace in `"all"` mode, lint runs ONCE at the turn end, over the
distinct files the turn touched (the `write_file`/`edit_file` paths,
including the `agent` subagent's, minus any the turn deleted since) —
one run per file for a `{file}` lint, one run per distinct package dir for
a `{dir}` lint. Findings reach the model in one more tools-withheld
(finalize) round, and appear in the REPL, in `cortex turn`'s stderr and its
`lint` JSON field, and in the turn's journal capture.

**The mode switch.** `tools.post_edit_hook` controls how much the hook
does: `"off"` (nothing runs), `"format"` (only the per-file format command
per edit — lint is skipped), or `"all"` (default: the per-file format
command per edit, plus the turn-end lint pass over the turn's touched
files). Precedence: the
`CORTEX_POST_EDIT_HOOK` env var (same values), then the project config's
`tools.post_edit_hook`, then the user config's, then the default `"all"`.
An operator can lower it (the REPL's `/hook` command, the per-call
`hook: "skip"` argument on `write_file`/`edit_file`) but nothing RAISES it
above the configured ceiling — an agent can skip one call, never enable a
mode. Trust is never affected by the mode.

**Workspace trust is the ONLY gate.** Trust is a persisted,
per-workspace, USER-level decision: the user config's `project.trusted`
list (below), set with `cortex project trust`. It is read ONLY from the
user-level config — the repository's own `.cortex/config.json` is never
on the read path, so a repo can never mark itself trusted (the merge
drops the project-level `project.trusted` copy; `mergeProject`).
Default: no entry, untrusted. On an UNTRUSTED workspace the hook runs
NOTHING: a trusted repo may use repo-local binaries
(`./node_modules/.bin/eslint`, `./bin/fmt`) of any language, so there is
no per-tool allowlist to run — the trust decision authorizes running
what the repo configures. The first edit of a session on an untrusted
workspace gets a one-line "post-edit hook inactive" note (later edits
stay silent); a trusted workspace gets no note.

On a TRUSTED workspace, the per-edit format command runs when it applies:
the command must be per-file (a whole-project format has no argument to
substitute per edit) and applicable to the file's extension (a manifest-set
like gofmt's `.go`; a declaration or script with no recognized toolchain
applies to all files). It runs as a plain argv — the template is split once
and `{file}`/`{dir}` are each substituted as a single argument AFTER the
split, exec'd directly with no shell, so a path is always one inert
argument. A template containing shell syntax (pipe, chain, redirect,
command substitution, subshell, or newline) cannot run without a shell and
is skipped with a note — its intent is unexpressible, not dangerous. The
format command gets the per-command budget (default 10s); a timeout or a
non-zero exit is folded into the tool result as a note (capped at 2000 bytes
of output), appended after the tool's own observations about the change
(`edit_file`'s line delta and removal warning, and the large-deletion note
either tool adds). The hook never fails the edit — the result of the write
stands regardless.

**`project.trusted` — the workspace trust list.** A list of workspace
root directories the operator has decided are trusted — the only gate
for the post-edit hook (above). Lives in the USER config only; managed
with:

```
cortex project trust add <root>     # trust a workspace root (idempotent)
cortex project trust remove <root>  # untrust it (errors if not listed)
cortex project trust list           # show the trusted roots
```

Editing round-trips the whole user config, so a trust edit never
clobbers other settings (unknown top-level keys are preserved). Absent
or empty means no workspace is trusted — the safe default.

**Inspecting the resolved set.** `cortex project commands` prints the
resolved format/lint/test/build commands, one per line (or a JSON document
with `--json`), each carrying `role`, `command`, `source` — the manifest
name when discovered, `config.json` when declared in `project.commands`, or
the instruction file's name (`AGENTS.md`, `CLAUDE.md`,
`.github/copilot-instructions.md`) when declared in its `## Commands`
section — and `when`: when the post-edit hook runs this command NOW, for
this workspace and session. `when` is one of: `per-edit` (a per-file
format command, trusted workspace, hook mode format or all), `turn-end`
(a lint with `{file}`/`{dir}`, trusted workspace, hook mode all), `never`
(the test and build roles, or a whole-project format/lint — the hook never
auto-runs those), `inactive: workspace untrusted` (nothing runs on an
untrusted workspace), or `inactive: hook mode <mode>` (a trusted workspace
whose hook mode would not run this role now). `when` is computed from the
user-config trust list and the effective hook mode (the `CORTEX_POST_EDIT_HOOK`
env var, then the config's `tools.post_edit_hook`, then the default `all`).
`--project <name>` targets a registered project (from the registry) instead
of the CWD-derived workspace root — the same pair the hook uses in a
session.

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
