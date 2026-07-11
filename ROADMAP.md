# Roadmap

**Status:** Experimental. The Cortex coding harness works and is in daily
use; the work now is hardening it and proving the working-memory thesis
inside it.

**North star:** get strong coding work out of small/local models by
managing their context well — a *forever session* that curates its own
working memory instead of truncating or reaching for a bigger model.

The project was recently slimmed to center on `cmd/cortex`. The prior
`cortex` CLI, eval framework, and Claude-Code host integration were removed
(see [`docs/archive.md`](docs/archive.md)). This roadmap covers the coding
harness; the web app is a parallel track with its own self-contained plan
(see below).

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
- **`study` tool** — size-adaptive, goal-curated reading of files/dirs.
  `cortex study-eval` is its acceptance gate: hard pass/fail per probe
  (goal-hit + bounded + clean finalize; non-zero exit on any failure), with
  latency reported alongside.
- **Tool surface** — project read/write/search tools, risk-gated shell,
  workspace-confined deletion, durable memory/recall, and coder-only public
  `web_search` + SSRF-safe `fetch_url`.
- **Layered config + multi-backend** — `pkg/llm` providers (Anthropic,
  Ollama, OpenRouter, OpenAI-compatible); per-role `code` / `study` models.
- **Headless + adapters** — `cortex turn` (one-shot), `cortex change` (git
  lifecycle), `cortex discord`.

## The implementable bar

Cortex implements its own roadmap through the headless harness (fresh
session per piece, small context). So before a near-term item enters the
sequence it must carry:

- **named target files** (with line refs where they matter),
- **a red/green acceptance test** — the failing test is written first,
- **scope that fits one fresh headless session** — if it doesn't, it gets
  sliced until it does (the per-tool `implementation-context-*.md` docs are
  the worked example of the right grain).

Anything that can't meet the bar yet is a **design gate** — a doc and a
decision, not a work item — and is labeled as such below.

## Near-term

(The original planning doc, `docs/roadmap-2026-06-23.md`, is archived — see
[`docs/archive.md`](docs/archive.md); everything it planned shipped in a
different shape or is recorded below.)

**Sequence (2026-07-10 update):**

1. **Working memory: incremental/layered context** — two-zone demotion,
   citation recall, outline folding, persistent state, and cache-stable memory
   injection. *(landed)*
2. **Public web access** — coder-only `web_search` + SSRF-safe `fetch_url`,
   bounded read-only HTTP and text extraction. *(landed)*
3. **System prompt: small batches + tidy-first** — cheapest, highest signal.
   *(landed; extended 2026-07-10 with the no-hoarding memory default)*
4. **Documentation audit and digestion** — see the disposition table below.
   *(largely done 2026-07-10; remaining: a README pass against the current
   product surface)*
5. **Single-writer session lock** — per-session-file flock so a second
   process gets a clear "session busy" error instead of silently
   interleaving appends. Fixes a latent corruption path that exists today
   (REPL and `cortex discord` can open the same transcript; the discord
   mutex is in-process only), and is the prerequisite the web track's
   Phase 4 names for any second surface. Pattern to copy: the journal's
   per-segment flock (`internal/journal/lock_unix.go`). Target: the
   transcript open in `cmd/cortex/session.go` (~`:85`, currently
   `O_CREATE|O_EXCL` only). Test: two openers, second gets the busy error;
   lock released on close.
6. **General `agent` tool** — a dispatchable subagent the coder can hand a
   goal to, built on the registered `Subagent` profile system
   (`internal/tools/study.go`) rather than beside it. The registry was built
   for inheritors (`tools.go:433` — "the same path any future inheritor …
   will use"); Study stays untouched as the read-only profile. This
   supersedes the don't-build list's "subagents/task tool" entry in
   [`docs/cortex-production-harness.md`](docs/cortex-production-harness.md) —
   the Cortex-native answer it deferred to now exists. Slices, in order:
   1. **Per-profile seeding** — the shared dispatch path hardwires
      `StudySeed(goal, path, outline)` for every profile
      (`internal/tools/tools.go:579`); move seed construction into the
      profile (a `Seed func` field or equivalent). Red/green: existing study
      tests stay green, byte-identical Study seed.
   2. **Explicit depth policy** — replace the blanket no-recursion rule with
      a per-profile depth cap threaded through `RunSubagent`
      (`cmd/cortex/study.go`), so a general agent can exist without
      reintroducing runaway spawn chains (cap 1 to start; Study stays 0).
   3. **The `agent` profile** — register a general profile with its own
      system prompt, tool allowlist, and mandatory `Bounds`; config-gated
      (`tools.enable_agent`) per the established pattern
      ([`docs/IMPLEMENTATION-PATTERN.md`](docs/IMPLEMENTATION-PATTERN.md)).
      Decisions to settle at this slice (small design note first): toolset
      scope (write/edit and bash, or read-only-plus-write?), how `shellrisk`
      Risky resolves inside a subagent (no human mid-loop → treat as
      headless/Blocked?), and which coder-only tools stay excluded
      (`recall`, memory writes, context tools).
   Tests: profile-registry + seed-seam unit tests in `internal/tools`; a
   scripted-`Sender` loop test driving a full agent-tool call end to end.
7. **Web track, Phases 1–3** — bootstrap, landscape scan, workspace
   threading + registry, per [`docs/cortex-web.md`](docs/cortex-web.md)
   (authoritative and self-contained; sliced to the bar above at
   implementation time, one slice per seam).
8. **Think/Dream evaluation — design gate.** `pkg/cognition` /
   `internal/cognition` were deleted outright (see
   [`docs/archive.md`](docs/archive.md)); there is nothing to switch back
   on — only git history to mine for ideas. Before any implementation: a
   design doc answering whether a simplified Think/Dream layer improves
   long-horizon curation beyond what the memory tools + context tools
   already deliver, and the eval that would prove it. Not implementable
   until that doc exists.

The loop-extraction work specified by
[`docs/engine-unification.md`](docs/engine-unification.md) and
[`docs/study-subagent.md`](docs/study-subagent.md) has shipped: coder and Study
share one bounded agent-loop engine, with the tool vocabulary in
`internal/agent`, implementations in `internal/tools`, and structural mapping in
`internal/outline`.

### Documentation disposition (the audit, made concrete)

| Doc | Disposition |
|---|---|
| [`docs/cortex-production-harness.md`](docs/cortex-production-harness.md) | Kept with a status note: parts 4/6 record the pre-pivot mechanical retrieval/distillation pipeline (since removed); the MECE framing and don't-build list remain live reference. |
| [`docs/IMPLEMENTATION-PATTERN.md`](docs/IMPLEMENTATION-PATTERN.md) | Kept as the config-gated-tool pattern guide; note updated for the `context_summarize` → `recall(budget)` fold. |
| `docs/implementation-context-{evict,merge,adjust-watermarks}.md` | Historical plans, marked shipped; `internal/tools/context*.go` is authoritative. |
| `docs/implementation-context-summarize.md` | Historical, marked folded into `recall(budget)`. |
| `docs/implementation-context-reorder.md` | Already marked cut (tool was unshippable by construction). |
| `docs/refactor-status.md`, `docs/refactor-goal-prompt.md` | Historical records of the shipped engine+study refactor, marked as such. |
| `CLAUDE.md` | Stale dispatch line refs corrected. |
| `ideas.md` | Folded into this roadmap ("Someday" below + web track pointer) and removed. |

## Parallel track: Cortex Web

[`docs/cortex-web.md`](docs/cortex-web.md) is the authoritative,
self-contained plan for the web track — first-run bootstrap, landscape scan,
project registry, `cortex serve`, web UI, cross-project loops, discord
parity. It is additive to this roadmap, and its phase boundaries and
decisions live there, not here.

One verification note for its Phase 3, from a 2026-07-10 code audit: the
implicit-CWD coupling is **five** sites, not the three the doc names —
`findUp` (`cmd/cortex/config.go:314`, backing both `contextDir()` and
config/AGENTS.md discovery), the two distinct tool confiners
(`internal/tools/confine.go:37` and the delete-path confiner at
`internal/tools/tools.go:1148`), the `deleteRoot` derivation
(`cmd/cortex/session_core.go:137`), and `config.Default()`
(`pkg/config/config.go:376`). The `Workspace` threading slices should cover
all five.

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

The next memory bet is the Think/Dream design gate above: evaluate whether a
simplified background-curation layer earns its place without restoring the
removed automatic retrieve/rerank/distill pipeline.

## Someday / needs a design doc

Folded from the former `ideas.md`; none of these meet the implementable bar
yet. (Its other entries already landed — session working memory, the
tidy-first harness personality prompt — or became the web track.)

- **ACP editor integration** — make Cortex speak Agent Client Protocol so it
  drops into existing Neovim front-ends (agentic.nvim, CodeCompanion) as a
  back-end: a Go ACP adapter over the existing `Session.Turn` / `cortex turn`
  seam, no bespoke plugin. The differentiator: every Neovim AI plugin today
  assumes a frontier model behind it; Cortex is a small/local-model back-end
  with persistent memory behind the same front-end. Open questions before
  committing: does ACP map cleanly onto `Session.Turn`, and is there a usable
  Go ACP library?
- **Hooks / extensibility / skills** — deliberate holes today (see the
  don't-build list in
  [`docs/cortex-production-harness.md`](docs/cortex-production-harness.md));
  revisit only when a concrete need survives that list's rationale.
- **Journal categories** — can a classifier reliably shape journal capture
  into notes / memories / directives, and does that enable an adaptive
  harness that continually learns?
- **Response preflight** — a template for replies that stays easy to read;
  don't make simple things sound complicated.
- **Agentic load** — classify the context difficulty and scope a turn is
  dealing with.
- **Integrated secrets manager.**
- **Proactivity** — proactive studying, thinking, and dreaming, visible in
  the tool. (Downstream of the Think/Dream design gate.)

## Deliberately deferred

- Diffs in `edit_file` + edit UX — display-layer only, doesn't enable autonomy.
- A real browser / JS rendering for web search — read-only HTTP first.
- Merging in the streaming/distillation/retrieval depth from the removed harness — tidy first, reconsider later.

---

*Living document. Grounded in the codebase as of 2026-07.*
