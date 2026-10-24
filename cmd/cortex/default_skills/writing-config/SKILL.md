---
name: writing-config
description: Write or update .cortex/config.json — every config field, its type, and the layered merge rules.
---

# Writing Cortex config

Read the existing config first — `read_file` on `.cortex/config.json` and/or
`~/.cortex/config.json` — and merge onto it. Never overwrite blindly.

## Where config lives (precedence, lowest → highest)

1. `~/.cortex/config.json` (or `$CORTEX_HOME/config.json`) — user defaults.
2. The nearest `.cortex/config.json` walking up from the CWD — project overrides.
3. `CORTEX_BACKEND` env — supplies `backend.endpoint` only when neither file set one.

Merge is field-by-field (`mergeConfig` in `cmd/cortex/config.go`): a key
present in the project file overrides the same key from the user file;
everything else falls through. Neither file is required.

## Top-level fields

| Key | Purpose |
|---|---|
| `backend` | `type` (`"openrouter"` is the only special value), `endpoint`, `key_env` (name of an env var, never the key itself), `key_service`, `provider` (OpenRouter routing) |
| `models` | map of role → `model`, `window`, `thinking`, `effort` (e.g. `models.code`, `models.study`) |
| `tools` | per-tool gates (`allow_delete`, `enable_web`, `enable_agent`, `enable_scan`, …) and numeric caps |
| `subagents` | `study`/`agent`/`learn` profile overrides (model, window, thinking) |
| `limits` | byte/count ceilings (e.g. memory-index cap chars) |
| `network` | probe timeouts, `self_heal` |
| `serve` | `cortex serve` tunables |
| `repl` | interactive REPL tunables |
| `discord` | Discord adapter tunables |
| `context` | two-zone working-set fractions |
| `skills` | `enabled`, `index_max`, `dirs` (discovery roots override) |
| `attribution` | commit-trailer / PR-footer attribution markers |
| `project` | instructions file, `commands`, post-edit hook |
| `decide` | the parallel shadow decider |
| `commands` | map of named shell commands |
| `prompt` | `file` (replaces the base system prompt), `append` (rides after it) |

## Rules

- Every field is optional; a config that omits a section behaves
  byte-identically to the hardcoded default.
- `backend.type: "openrouter"` (case-insensitive) turns on the curated
  free-model default when no model is pinned, OpenRouter usage accounting,
  the reasoning-effort wire dialect, and the startup preflight substitution
  check. Any other value is a generic OpenAI-compatible endpoint.
- `key_env` is the NAME of an environment variable holding the API key.
  Never write a key value into the file.
- For the full field-by-field spec, read `docs/configuration.md` in the
  workspace when it exists — it is the verified source of truth for every
  field above.
