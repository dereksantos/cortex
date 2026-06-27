# Roadmap

**Status:** Experimental. The `loop` coding harness works and is in daily
use; the work now is hardening it and proving the working-memory thesis
inside it.

**North star:** get strong coding work out of small/local models by
managing their context well — a *forever session* that curates its own
working memory instead of truncating or reaching for a bigger model.

The project was recently slimmed to center on `cmd/loop`. The prior
`cortex` CLI, eval framework, and Claude-Code host integration were removed
(see [`docs/archive.md`](docs/archive.md)). This roadmap is `loop`-only.

---

## What's working

- **`loop` REPL** — agentic tool loop, persistent resumable sessions,
  per-turn journal capture, async insight distillation, turn-start
  retrieval injection.
- **Working memory v1** — context compaction via the `study` engine at
  ~80% window (and on `/compact`); the session continues from a curated
  digest with cited `file:line` ranges.
- **`study` tool** — size-adaptive, goal-curated reading of files/dirs;
  `loop study-eval` measures its latency / coverage / groundedness.
- **Tool surface** — `read_file`, `write_file`, `edit_file`, `study`,
  `project_index`, `bash` (risk-gated), `remove_path` (workspace-confined).
- **Layered config + multi-backend** — `pkg/llm` providers (Anthropic,
  Ollama, OpenRouter, OpenAI-compatible); per-role `code` / `study` models.
- **Headless + adapters** — `loop turn` (one-shot), `loop change` (git
  lifecycle), `loop discord`.

## Near-term

Detailed plan and dependency order: [`docs/roadmap-2026-06-23.md`](docs/roadmap-2026-06-23.md).
Recommended sequence (each a green-build checkpoint):

1. **System prompt: small batches + tidy-first** — cheapest, highest signal. *(landed)*
2. **Extract `cmd/loop/tools`** — pure move, no behavior change. *(landed)*
3. **Diffs in `edit_file` + edit UX** — visible, small.
4. **Extract `cmd/loop/transport`** — wire types out of `main.go`.
5. **`web_search` tool** — read-only HTTP + text extraction, in the clean tools package.
6. **Extract `cmd/loop/fleet`** (config/models) then **`cmd/loop/session`** (the big move).
7. **Working memory: incremental study compaction** — `CompactRecent` +
   a clean session-state layer + manifest.

`cmd/loop/main.go` is ~3.6k lines; the loop-extraction work is now specified by
[`docs/engine-unification.md`](docs/engine-unification.md) (one `runLoop` engine
in `internal/agent`) and [`docs/study-subagent.md`](docs/study-subagent.md). The
older `docs/refactor-loop-main.md` breakup was superseded and removed 2026-06-27.

## Working-memory thesis (the bet)

The live direction. See [`docs/working-memory.md`](docs/working-memory.md)
and [`docs/working-memory-study.md`](docs/working-memory-study.md).

- **P1–P4 landed** (findings-prefix, curation, directed sampling, cacheable
  prefix) and are the validated core: a curated findings prefix + new-sample
  tail gives multi-pass study continuity with a cacheable prefix.
- **Phase 2–3** (working-memory triage, background curation via Think/Dream,
  retrieval recall of evicted context) is the next bet — gated on the
  incremental + layered compaction above proving out in `loop`.

## Deliberately deferred

- A `loop` distribution pipeline (Homebrew / release CI) — rebuild when
  `loop` is ready to ship.
- A real browser / JS rendering for web search — read-only HTTP first.
- Merging in the streaming/distillation/retrieval depth from the removed
  harness — tidy first, reconsider later.

---

*Living document. Grounded in the codebase as of 2026-06.*
