# Cortex Roadmap

**Last Updated:** May 2026
**Status:** Experimental. Core pipeline working; ABR optimization ongoing.
**North Stars:** ABR ≥ 0.9 | Token Cost Reduction over time

---

## What's Working

- Capture → store → retrieve → inject pipeline (used daily in development of Cortex itself)
- All five cognitive modes implemented (Reflex, Reflect, Resolve, Think, Dream)
- Multi-project support via global daemon and shared `~/.cortex/`
- Eval framework with SQLite persistence: 40 v2 scenarios plus the `library-service/` multi-session eval (sessions, scorer, injection, end-to-end probe)
- Claude Code integration (hooks, slash commands, status line)
- MCP server skeleton (untested at scale)

## What's Early or Aspirational

- **Cursor integration:** design-only; no shipping extension yet
- **MCP server:** wired up but not validated against real external clients
- **Slash-command UX:** functional but rough; some output is stubby
- **Eval LLM mix:** Haiku runs landed but the 3-way comparison (Cortex / native memory / no-context) is still settling — see `docs/archive/`
- **ABR:** 0.77 against a target of 0.9

## Current Eval Results

| Metric | Current | Target |
|--------|---------|--------|
| Semantic Lift | +35% | >0% |
| Win Rate | 44% (8/18) | >50% |
| ABR | 0.77 | ≥0.9 |

Most recent runs use Claude Haiku 4.5 with hooks-active; archived under `docs/archive/`. qwen 1.5B was retired (below task floor — see commit `bb309ce`).

---

## Core Metric: ABR (Agentic Benefit Ratio)

ABR measures how well background processing (Think) makes Fast mode perform like Full mode:

```
ABR = quality(Fast + Think) / quality(Full)
Goal: ABR → 1.0 as session progresses
```

| ABR | Meaning |
|-----|---------|
| ≥ 0.95 | Excellent |
| 0.85-0.95 | Good |
| 0.70-0.85 | Needs work |
| < 0.70 | Failing |

**Current: 0.77 (Needs work)**

ABR validates Cortex's core innovation: bounded intelligence through background processing. Alongside ABR, **token cost reduction** measures the practical payoff -- fewer tokens spent re-discovering context across sessions.

---

## Roadmap

### Phase 1: Consolidate Eval System ✅ DONE

**Goal:** Reduce complexity, single source of truth

- [x] Merge 23 eval files → 5 files (commit befe426)
- [x] Remove all mocks, use CLICortex everywhere
- [x] SQLite-only persistence (drop JSONL)

**Achieved structure:**
```
internal/eval/v2/
├── eval.go       # 286 lines (runner + types)
├── scenario.go   # 104 lines (YAML loading)
├── persist.go    # 155 lines (SQLite only)
├── measure.go    # 218 lines (scoring)
└── report.go     # 163 lines (output formatting)

Total: 926 lines (down from ~11,000)
```

---

### Phase 2: Simplify Scenarios ✅ DONE

**Goal:** Adding an eval = adding a YAML file only

- [x] Single YAML format: `context` + `tests`
- [x] Remove legacy scenario types
- [x] Every scenario just establishes context and runs tests
- [x] Unified runner for all scenarios

**Active scenarios:** 40 in `test/evals/v2/`, organized as:
- baseline patterns: auth, db, error, logging, naming, testing
- abstention, adversarial, extraction, reasoning, temporal, updates families
- locomo benchmark scenarios (commonsense, multihop, event-causality)

**Format:**
```yaml
id: auth-middleware
context:
  - type: decision
    content: "We use JWT, not sessions"
tests:
  - id: auth-approach
    query: "How do we handle authentication?"
    expect:
      includes: ["JWT"]
      excludes: ["session"]
```

---

### Phase 3: ABR Dashboard 🔄 IN PROGRESS

**Goal:** Track ABR over time, make progress visible

- [x] SQLite persistence for eval results (`evals_v2.db`)
- [ ] CLI: `cortex eval --summary` shows ABR trend
- [ ] Historical comparison: "ABR improved from 0.77 to 0.85"
- [ ] Pass criteria enforcement: ABR ≥ 0.9

---

### Phase 4: ABR Optimization 📋 NEXT

**Goal:** Improve ABR from 0.77 → 0.9

Landscape-informed priorities (Mar 2026 review):
- [ ] Replace brute-force vector search with sqlite-vec (indexed search)
- [ ] Retrieval tuning (top-k, similarity threshold)
- [ ] Context formatting for LLM consumption
- [~] Eval LLM upgrade landed (Haiku 4.5); 3-way comparison harness in flight
- [ ] Re-embedding migration after future model upgrades
- [ ] Local model optimization: prefer small models (Ollama) for Think/Dream background tasks

---

### Phase 5: Cross-Tool & Ecosystem 📋 FUTURE

**Goal:** Make Cortex tool-agnostic and ecosystem-ready

- [ ] MCP server (`cortex_search`, `cortex_recall`, `cortex_record` tools)
- [ ] MEMORY.md as DreamSource (complement Claude Code Auto-Memory)
- [ ] Expanded hook coverage (PreToolUse, Notification, SubagentComplete)
- [ ] HTTP hook handler for direct daemon delivery
- [ ] Plugin manifest conforming to current Claude Code spec
- [ ] Multi-agent / factory pattern support: shared context pool via MCP for parallel agents
- [ ] Token cost analytics: track and report token savings over time

---

## Blocked Until ABR ≥ 0.9

| Feature | Reason Blocked |
|---------|----------------|
| Git Dream source | Core evals must pass first |
| Entity relationships | Polish feature, not priority |
| Cross-session learning | Need single-session working first |
| MCP server expansion | Skeleton exists; broader tool surface blocked on retrieval quality |
| Team-shared context | Requires stable single-user first |

---

## Design Documents

- `docs/prompts/cli-focus.md` - CLICortex design and rationale
- `docs/prompts/eval-abr-focus.md` - ABR-focused eval philosophy

---

## Recently Completed

- [x] **Library-service multi-session eval** — scaffold, session runner, scorer, Cortex injection, end-to-end probe (Plans 01–05)
- [x] **First Haiku eval runs archived** with hooks-active correction; 3-way comparison harness landed
- [x] **Multi-project support** via single global daemon and shared `~/.cortex/`
- [x] **Composable status line** with compact format
- [x] **Per-mode cognitive tuning** via config
- [x] **Dream improvements**: fractal region sampling, novelty cache, follow-up queue
- [x] **Semantic search with embeddings** - nomic-embed-text, +35% lift
- [x] **Eval consolidation** - 23 files → 5 files (926 lines)
- [x] **Unified scenario format** - single YAML pattern
- [x] Cognition evals: 44% → 90% (19/21 passing)
- [x] E2E evals: 50% → 70%
- [x] CLICortex for true E2E testing via CLI commands
- [x] Idiom extraction and evals
- [x] Nuance extraction through pipeline
- [x] Core cognitive architecture (Reflex, Reflect, Resolve, Think, Dream)
- [x] Activity-based budget models
- [x] ABR metric baseline (0.77)
- [x] Claude Code integration

---

## Success Criteria

| Phase | Status | Key Metric |
|-------|--------|------------|
| Phase 1: Consolidate | ✅ Done | 926 lines (was 11k) |
| Phase 2: Scenarios | ✅ Done | 7 active scenarios |
| Phase 3: Dashboard | 🔄 In Progress | `--summary` flag |
| Phase 4: Optimize | 📋 Next | ABR ≥ 0.9 |

**Mandate complete when:** ABR ≥ 0.9 sustained over 3 consecutive runs.

---

*This roadmap is a living document. North stars: ABR ≥ 0.9, token cost reduction over time.*
