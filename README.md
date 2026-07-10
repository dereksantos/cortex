# Cortex / `loop`

[![CI](https://github.com/dereksantos/cortex/actions/workflows/test.yml/badge.svg)](https://github.com/dereksantos/cortex/actions/workflows/test.yml)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**An interactive coding agent for small and local models, with working
memory built in.** `loop` is a single-binary REPL coding harness: it runs
an agentic tool loop, captures what each turn did, and curates its own
context window so a long session stays coherent without blowing the
context budget. It's designed to get good work out of small/local models
by managing context for them, not by reaching for a bigger model.

> **Status: Experimental.** Research-grade and in daily use on the author's
> machine, not a polished product. Expect rough edges and breaking changes.
> The project was recently slimmed to center on `cmd/loop`; the prior
> `cortex` CLI, eval framework, and Claude-Code host integration were
> removed (preserved in [`docs/archive.md`](docs/archive.md) and git
> history).

## What `loop` does

The core loop: **read input → run agentic tool calls → capture the turn →
curate context → reply.** Three things make it more than a thin wrapper:

- **Working memory.** Instead of truncating history when the context
  window fills, `loop` *studies* the conversation into a curated digest
  (salience-ranked, with cited `file:line` ranges) and continues from
  that. Compaction fires automatically at ~80% context, or on `/compact`.
- **Per-turn capture.** Every turn records what it touched — files edited,
  commands run, the final answer — to an append-only journal, mechanically
  and without blocking the turn. A background pass distills durable
  insights. Retrieval injects relevant prior context at turn start.
- **The `study` tool.** A size-adaptive reader: small targets are inlined
  whole; large files/directories are chunked, boundary-snapped, and
  curated against your goal so the model sees signal, not raw bytes.

## Quick start

**Prerequisites:** Go 1.25+ and a model backend — a local OpenAI-compatible
endpoint (Ollama at `:11434`, LiteLLM at `:4000`, LM Studio, vLLM), or an
OpenRouter / Anthropic key.

```bash
go build -o bin/loop ./cmd/loop      # build

# point at a backend (any one of):
export CORTEX_BACKEND=http://localhost:11434      # local OpenAI-compatible
export OPENROUTER_API_KEY=...                      # or a hosted backend

./bin/loop                            # interactive REPL (fresh session)
./bin/loop resume                     # resume the latest session
```

Sessions persist to `.cortex/sessions/<id>.jsonl`. The agent reads an
`AGENTS.md` from the repo root if present, and has access to the tools
below.

## Commands

```bash
loop                       # interactive REPL (default)
loop resume [id]           # resume a prior session (default: latest)
loop turn [--session id] [--json] <input...>
                           # headless single turn — for drivers/scripts; session id echoed to stderr
loop study <path> [goal...] [passes]
                           # one-off study of a file/dir; prints a curated digest
loop change <start|commit|status>
                           # git change lifecycle — one reviewable change at a time (local git only)
loop discord               # run as a Discord bot (token from DISCORD_BOT_TOKEN)
loop study-eval [code-grid|wm]
                           # measure the study tool's latency / coverage / groundedness
```

### REPL slash commands

| Command | Purpose |
|---|---|
| `/compact` | Distill the conversation into a curated digest now (also auto-fires at ~80% context) |
| `/clear` | Reset to a fresh session (system prompt + `AGENTS.md`) |
| `/remember <text>` | Store an explicit, high-precision memory |
| `/sessions` | List saved sessions and their ids |
| `/model [name]` | Show role bindings, or switch the coding model |
| `/quit`, `/exit` | Exit (also Ctrl-D); prints a resume command |

## Tools the agent has

Registered in [`internal/tools/tools.go`](internal/tools/tools.go):

| Tool | What it does |
|---|---|
| `read_file` | Read a whole file. Large Go files return a declaration skeleton; large non-Go files redirect to `study`. |
| `write_file` | Write/create a file (parent dirs implied). |
| `edit_file` | Exact-match replace, whitespace-tolerant retry; supports atomic multi-edit. Preferred over `write_file` for edits. |
| `study` | Goal-curated reader for large files/directories; returns a grounded digest. |
| `outline` | Structural map of a project/file without reading all contents. |
| `grep` | Search project contents and return bounded `file:line:text` matches. |
| `web_search` | Search the public web for ranked titles, URLs, and snippets. |
| `fetch_url` | Fetch bounded readable text from a public HTTP(S) URL; local/private addresses are refused. |
| `bash` | Run a shell command, gated by a risk classifier (see below). |
| `remove_path` | Delete a file/dir, confined to the workspace (`.git`/`.cortex`/root refused). |

**Shell-risk gate** (`internal/shellrisk`): commands are classified Safe
(run immediately), Risky (prompt for approval, judged in the context of
your current request), or Blocked (refused). In headless/piped sessions,
Risky is treated as Blocked.

## Configuration

Config is layered, lowest to highest precedence:
`~/.cortex/config.json` (user, set once) → `./.cortex/config.json`
(project, overrides field-by-field) → `CORTEX_BACKEND` env.

```json
{
  "backend": { "type": "openrouter", "endpoint": "https://openrouter.ai/api/v1", "key_env": "OPENROUTER_API_KEY" },
  "models": {
    "code":  { "model": "qwen/qwen3-coder", "window": 131072 },
    "study": { "model": "deepseek/deepseek-r1" }
  },
  "tools": { "allow_delete": true, "enable_web": true }
}
```

- **Roles**: `code` (the agent) and `study` (curation/compaction). Each may
  pin its own `endpoint`, `model`, context `window`, and `thinking`
  override.
- **Auth** is resolved at call time from `key_env` (an env-var *name* — the
  portable default) or `key_service` (a macOS keychain item). Secrets are
  never written to config.
- **Deletion**: set `tools.allow_delete: false` to drop the `remove_path`
  tool; `tools.delete_root` confines it (default: cwd).
- **Web access**: set `tools.enable_web: false` to disable execution of both
  `web_search` and `fetch_url`. They are coder-only and are not available to
  the read-only study subagent.

Useful env vars: `CORTEX_BACKEND`, `CORTEX_HOME` (override config home),
`DISCORD_{BOT_TOKEN,CHANNEL_ID,SESSION_ID}`, `NO_COLOR`,
`CORTEX_LOOP_RENDER=0` (disable markdown rendering). Study experiment
knobs: `CORTEX_STUDY_{CURATE,DIRECTED,AST}`, `CORTEX_LOOP_STUDY_WINDOW`.

## Project structure

```
cortex/
├── cmd/loop/            # the loop binary
│   ├── main.go          # REPL, session, turn loop, dispatch
│   ├── change.go        # git change lifecycle
│   ├── discord.go       # Discord adapter
│   ├── study_eval.go    # study-tool eval harness
│   ├── tool_deps.go      # session implementation of tool dependency seams
│   └── ui/              # rendering helpers
├── internal/
│   ├── capture/         # fast per-turn event capture
│   ├── journal/         # append-only event log (source of truth)
│   ├── storage/         # local store (dormant — kept for a future semantic Reflect)
│   ├── projectindex/    # structural project mapping
│   ├── shellrisk/       # command risk classifier
│   └── projectscan/ lineedit/
└── pkg/
    ├── config/          # layered config
    ├── llm/             # providers (Anthropic, Ollama, OpenRouter, OpenAI-compatible)
    ├── events/ secret/ cliout/
```

## Development

```bash
go build ./cmd/loop          # build
go test ./...                # tests (standard library only — no testify)
./scripts/check.sh           # gofmt + go vet + golangci-lint
```

## Documentation

- [CLAUDE.md](CLAUDE.md) — guide for AI assistants working in this repo
- [ROADMAP.md](ROADMAP.md) — direction and status
- [docs/working-memory.md](docs/working-memory.md) — the working-memory design
- [docs/working-memory-study.md](docs/working-memory-study.md) — study-as-working-memory
- [docs/loop-production-harness.md](docs/loop-production-harness.md) — the plan to harden `loop`
- [docs/archive.md](docs/archive.md) — what the system was before centering on `loop`

## License

MIT
