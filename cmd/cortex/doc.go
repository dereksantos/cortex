// Command cortex is an interactive coding agent for small and local models,
// with working memory built in. This file maps the package for orientation;
// it is not a tutorial — see CLAUDE.md (repo root) for the authoritative
// architecture description and docs/configuration.md for the config surface.
//
// # The turn loop
//
// read input → run agentic tool calls → capture the turn → curate context → reply.
//
// Three capabilities distinguish this agent from a plain tool-calling loop:
//
//  1. Working memory: a two-zone cache over the immutable session transcript
//     (an append-stable prefix of system + outline + memory index, and a
//     watermarked hydrated tail of recent turns). See docs/context-architecture.md.
//  2. Model-driven memory + per-turn capture: the agent curates durable notes
//     through the memory_write/read/search/forget tools (internal/memory),
//     and every turn is mechanically recorded to the append-only journal for
//     later study(.cortex/journal). See docs/memory-tools.md.
//  3. study — a bounded, read-only subagent (outline/grep/read_file only,
//     no recursion) that digests a path against a goal without polluting the
//     coder's own context. See docs/study-subagent.md.
//
// # Composition root
//
// CortexSession (session_core.go) is the composition root: it owns the
// backend/config, the transcript, memory, and the tool dependencies every
// tool call needs. runLoop (loop.go) is the one shared tool-iteration engine
// — driven by a Sender + AgentDispatcher seam — that both the interactive
// REPL turn and the Study/Agent subagents run on.
//
// # File-naming conventions
//
// Within this single package, related files share a prefix so `ls` groups
// them:
//
//   - serve_*.go     — the `cortex serve` HTTP/SSE adapter (routes, handlers,
//     dashboard, landscape, models, loops, scheduler, session, stream,
//     transcript, turn).
//   - webui_*.go     — server-rendered fragments the serve_* handlers return.
//   - discord_*.go   — the Discord adapter (client, commands, progress, risk).
//   - bootstrap_*.go — first-run setup (persisted state, wiring).
//   - context_*.go   — the context self-curation tools (evict/merge/adjust
//     watermarks) — declarations live in internal/tools; wiring here.
//   - memory_*.go    — the model-driven memory tool wiring (declarations in
//     internal/tools, store in internal/memory).
//
// # Why one package
//
// cmd/cortex is deliberately a single composition root rather than a set of
// importable internal packages. A 2026-07-18 compiler-driven dependency
// audit (docs/completion-roadmap.md, Track D, item D2) found the serve*/
// webui* handlers alone reach six shared package-main subsystems (config,
// scan, workspace, session-listing, change status, loop firing) that are
// themselves not importable — so splitting today means either relocating
// those first or duplicating roughly 5 DTOs and 20 wrappers, the
// half-broken outcome. D2 records the prerequisite ladder for a future
// extraction pass (Session/SessionFactory interfaces first, then the
// config/scan/workspace data types, then the handlers). That work is
// optional post-launch architecture, not required for this package to
// function — see the D2 entry for the full ordering.
package main
