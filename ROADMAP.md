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
- **Working memory** — bounded two-zone context: old complete turns demote to
  a citation-grounded outline, recent turns remain verbatim, outline layers fold
  under pressure, and append-only state restores the same frontier on resume.
  `recall` retrieves exact demoted transcript messages.
- **`study` tool** — size-adaptive, goal-curated reading of files/dirs;
  `loop study-eval` measures its latency / coverage / groundedness.
- **Tool surface** — project read/write/search tools, risk-gated shell,
  workspace-confined deletion, durable memory/recall, and coder-only public
  `web_search` + SSRF-safe `fetch_url`.
- **Layered config + multi-backend** — `pkg/llm` providers (Anthropic,
  Ollama, OpenRouter, OpenAI-compatible); per-role `code` / `study` models.
- **Headless + adapters** — `loop turn` (one-shot), `loop change` (git
  lifecycle), `loop discord`.

## Near-term

Detailed plan and dependency order: [`docs/roadmap-2026-06-23.md`](docs/roadmap-2026-06-23.md).

**New recommended sequence (2026-07-08 update):**

1. **Working memory: incremental/layered context** — two-zone demotion,
   citation recall, outline folding, persistent state, and cache-stable memory
   injection. *(landed)*
2. **Public web access** — coder-only `web_search` + SSRF-safe `fetch_url`,
   bounded read-only HTTP and text extraction. *(landed)*
3. **System prompt: small batches + tidy-first** — cheapest, highest signal. *(landed)*
4. **Documentation audit and digestion** — review and consolidate docs, update README.
5. **Cognition/DAG revival** — bring back from git history, rethink and simplify.

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

- Diffs in `edit_file` + edit UX — display-layer only, doesn't enable autonomy.
- A real browser / JS rendering for web search — read-only HTTP first.
- Merging in the streaming/distillation/retrieval depth from the removed harness — tidy first, reconsider later.

---

*Living document. Grounded in the codebase as of 2026-07.*
