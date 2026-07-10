# Roadmap

**Status:** Experimental. The Cortex coding harness works and is in daily
use; the work now is hardening it and proving the working-memory thesis
inside it.

**North star:** get strong coding work out of small/local models by
managing their context well — a *forever session* that curates its own
working memory instead of truncating or reaching for a bigger model.

The project was recently slimmed to center on `cmd/cortex`. The prior
`cortex` CLI, eval framework, and Claude-Code host integration were removed
(see [`docs/archive.md`](docs/archive.md)). This roadmap is Cortex-only.

---

## What's working

- **Cortex REPL** — agentic tool loop, persistent resumable sessions,
  per-turn journal capture, model-curated durable notes, and a turn-start note
  index.
- **Working memory** — bounded two-zone context: old complete turns demote to
  a citation-grounded outline, recent turns remain verbatim, outline layers fold
  under pressure, and append-only state restores the same frontier on resume.
  `recall` retrieves exact demoted transcript messages.
- **Model-driven context curation** — `context_evict` / `context_merge` /
  `context_adjust_watermarks` let the agent drop, group, and re-budget its
  own outline and demotion thresholds on top of the mechanical policy, and
  `recall(budget)` returns a compact digest of a demoted turn instead of the
  raw messages (session-local; per-tool config gates). See
  [`docs/context-window-modification-tools.md`](docs/context-window-modification-tools.md).
- **`study` tool** — size-adaptive, goal-curated reading of files/dirs;
  `cortex study-eval` measures its latency / coverage / groundedness.
- **Tool surface** — project read/write/search tools, risk-gated shell,
  workspace-confined deletion, durable memory/recall, and coder-only public
  `web_search` + SSRF-safe `fetch_url`.
- **Layered config + multi-backend** — `pkg/llm` providers (Anthropic,
  Ollama, OpenRouter, OpenAI-compatible); per-role `code` / `study` models.
- **Headless + adapters** — `cortex turn` (one-shot), `cortex change` (git
  lifecycle), `cortex discord`.

## Near-term

(The original planning doc, `docs/roadmap-2026-06-23.md`, is archived — see
[`docs/archive.md`](docs/archive.md); everything it planned shipped in a
different shape or is recorded below.)

**New recommended sequence (2026-07-08 update):**

1. **Working memory: incremental/layered context** — two-zone demotion,
   citation recall, outline folding, persistent state, and cache-stable memory
   injection. *(landed)*
2. **Public web access** — coder-only `web_search` + SSRF-safe `fetch_url`,
   bounded read-only HTTP and text extraction. *(landed)*
3. **System prompt: small batches + tidy-first** — cheapest, highest signal. *(landed)*
4. **Documentation audit and digestion** — align the public product surface,
   establish canonical documentation entry points, then classify and consolidate
   historical design records. *(in progress)*
5. **Cognition/DAG revival** — bring back from git history, rethink and simplify.

The loop-extraction work specified by
[`docs/engine-unification.md`](docs/engine-unification.md) and
[`docs/study-subagent.md`](docs/study-subagent.md) has shipped: coder and Study
share one bounded agent-loop engine, with the tool vocabulary in
`internal/agent`, implementations in `internal/tools`, and structural mapping in
`internal/outline`.

## Working-memory thesis (the bet)

The live design has two complementary layers:

- **Session context** is a bounded two-zone cache over the append-only
  transcript. Recent turns remain verbatim, older turns demote to a
  citation-grounded outline, and `recall` retrieves exact historical messages.
  See [`docs/context-architecture.md`](docs/context-architecture.md).
- **Durable memory** is curated by the model through named free-form notes and
  the `memory_write/read/search/forget` tools. A cache-stable note index exposes
  what can be recalled without mechanically injecting retrieved content. See
  [`docs/memory-tools.md`](docs/memory-tools.md).

The next memory bet, after documentation digestion, is to evaluate whether a
simplified Think/Dream or cognition layer can improve long-horizon curation
without restoring the removed automatic retrieve/rerank/distill pipeline.

## Deliberately deferred

- Diffs in `edit_file` + edit UX — display-layer only, doesn't enable autonomy.
- A real browser / JS rendering for web search — read-only HTTP first.
- Merging in the streaming/distillation/retrieval depth from the removed harness — tidy first, reconsider later.

---

*Living document. Grounded in the codebase as of 2026-07.*
