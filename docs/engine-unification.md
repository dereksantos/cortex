# Engine unification — one agent loop, two seams

> **Status:** design + tracker (this doc). This is a **behavior-preserving
> refactor**, mutually exclusive from the study feature work in
> [`study-subagent.md`](study-subagent.md) — it lands **first** and the study
> redesign builds on top of it. Nothing here changes what the agent *does*; it
> changes how many copies of the loop exist (today: two).

## Why

There are two hand-rolled tool-iteration loops that have already drifted:

- `Resolve` — `cmd/loop/main.go:2853`. The main coder turn loop. 100 iterations,
  streaming, breadcrumbs, no-progress guard + nudge, XML tool-call recovery,
  usage accounting, ctx-cancel.
- `runNavigator` — `cmd/loop/navigator.go:81`. The study subagent loop. 10
  iterations, blocking sends, the **same** no-progress guard, the **same** XML
  recovery, the **same** usage accounting, the **same** ctx-cancel — plus a
  finalize-with-tools-withheld step.

Two copies of one algorithm means every fix lands twice or rots in one place.
`runNavigator` is a stale fork of `Resolve`; `navFinalize`, the repeat guard, and
the usage triple are copy-paste. This is the mess that makes the study redesign
hard to build cleanly, so it goes first.

**Important — `runNavigator` is deleted, not refactored onto the engine.** It is
the *internals of the old navigator* (`navigator.go`), which the study work
removes wholesale (study-subagent.md phase 4) and replaces with a `Study` profile
built directly on `runLoop`. So this doc only refactors the **surviving** loop
(`Resolve`) onto the engine and proves the subagent/blocking path with a
fake-`Sender` test — no work is spent refactoring a loop that's about to be
deleted. The end state ("one loop") is reached when the study work lands; in the
interim, `runNavigator` lingers as a doomed second loop. That's the honest
sequence — no refactor-then-delete.

## The shape: the loop is a function, the variation is two seams

There is exactly **one** engine, `runLoop` — a **function**, not an interface
(nobody injects a loop; you inject what it consumes). Everything that varies
between the coder and a subagent is injected through two small interfaces:

- **`Sender`** — how to perform one model round-trip. Streaming + breadcrumb in
  the REPL, plain blocking in a subagent, a fake in tests.
- **`AgentDispatcher`** — how to execute one tool call → observation. The impl
  bakes in the allowlist and any per-agent transforms (e.g. study's targeted
  read). The loop never branches on "am I a subagent."

```
Turn()  (main coder)                    RunSubagent(sa, seed)  (study, …)
  inject memory index                     req  = requestFor(spec, sa.System, seed, sa.Tools)
  send = streamingSender                   send = blockingSender (+ Progress sink)
  capture turn after                       (nothing after)
        │                                       │
        └──────────────► runLoop(ctx, send, req, toolset, bounds) ◄────────────┘
                              │  loop ≤ bounds.MaxIter:
                              │    resp ← send.Send(req)                 ← Sender seam
                              │    recover XML calls; append; account usage
                              │    no tool calls → return content
                              │    no-progress guard (shared, was duplicated)
                              │    each call: obs ← dispatch.Dispatch(call) ← Dispatch seam
                              │      readBytes += len(obs); append; Progress(line)
                              │    stop if readBytes ≥ budget / repeats / ctx done
                              └  finalize (tools withheld) → content

tests: send = SenderFunc(fake) → the real loop runs with zero network
```

## Interfaces (engine, `internal/agent` after phase 2)

```go
// Sender performs one model round-trip — the seam that makes runLoop testable.
type Sender interface {
	Send(ctx context.Context, req *Request) (*Response, error)
}
type SenderFunc func(context.Context, *Request) (*Response, error)
func (f SenderFunc) Send(c context.Context, r *Request) (*Response, error) { return f(c, r) }

// AgentDispatcher executes one tool call → observation (result or brief error);
// the impl bakes in the allowlist + per-agent transforms.
type AgentDispatcher interface {
	Dispatch(ctx context.Context, call ToolCall) string
}
type DispatchFunc func(context.Context, ToolCall) string
func (f DispatchFunc) Dispatch(c context.Context, t ToolCall) string { return f(c, t) }

type Toolset struct {
	Tools    []Tool          // advertised to the model
	Dispatch AgentDispatcher // executes them
}

// Bounds are the independent ceilings; whichever trips first forces finalize.
// Wall-clock time is bounded WITHOUT a hard whole-run timer: the Sender applies
// a per-request deadline (one hung model call can't run forever) and MaxIter
// caps the rounds, so worst-case time ≈ MaxIter × per-request deadline. A hard
// MaxWall was rejected (see the decisions log): cancelling the run's context
// guillotines a nearly-done study mid-call and leaves no live context to run
// finalize on. Revisit a SOFT between-iterations wall only if rounds×deadline
// proves too loose in practice.
type Bounds struct {
	MaxIter         int
	ReadBudgetBytes int
}

// Progress is an optional per-tool-call breadcrumb sink. The REPL wires it to
// stderr so a blocking subagent (study) still shows what it's doing; headless
// and tests pass nil. This is the seam that fixes "study stares back silently."
type Progress func(line string)

// runLoop is THE engine — one impl; the variation is Sender + Toolset (+ Progress).
func runLoop(ctx context.Context, send Sender, req *Request, ts Toolset, b Bounds, p Progress) (content string, err error)

// requestFor assembles a model request from a spec — a plain function (building a
// struct is not a behavior). The single place model/base/key/template-kwargs are
// set, replacing the duplication across runNavigator + the main request build.
func requestFor(spec ModelSpec, system, seed string, tools []Tool) *Request
```

**Split rule for the model seam:** *build = a function (`requestFor`), send = an
interface (`Sender`)*. Only sending is worth abstracting (I/O, faked in tests);
building a struct isn't.

## What lives where

- **The loop core** (iterate, XML recovery, no-progress guard, usage accounting,
  ctx-cancel, finalize) lives in `runLoop` — identical for every caller.
- **Main-loop extras stay outside the core.** Streaming + breadcrumb live in the
  `Sender`; memory-index injection and turn capture live in `Turn()` around the
  call.
- **The no-progress nudge becomes engine behavior**, not a main-loop special
  case. Today `Resolve` injects a "harness note" message one short of the repeat
  cap (main.go:2923). It's generically useful — a stuck study benefits too — so
  it moves into `runLoop` for every caller, gated by the repeat counter the
  engine already tracks. (Earlier this doc waffled on Sender vs dispatcher;
  resolved: engine-level.)
- **`Progress` is the only new capability**, and it's additive — a `nil` sink is
  today's behavior.

## The seam contract — `CortexSession` is three things, not one

`CortexSession` (in `package main`) is the **composition root / dependency sink**:
it sits at the top of the arrow graph, imports everything below, and nothing below
imports it. *Why* every interface it satisfies must be defined in the **consumer**:
`internal/agent` and `internal/tools` cannot import `cmd/loop`, so they declare the
narrow interface they need and `cs` (at the top) supplies it. This is the standard
Go "interfaces live on the consumer side" idiom — and it's the antidote to the
God object: nothing below `main` ever sees more than a 1–4 method slice of `cs`,
even though the struct itself stays fat.

`cs` relates to the layers below it in **three distinct ways** — conflating them is
what makes the seam feel murky:

| Relationship | Toward | Mechanism | Examples |
|---|---|---|---|
| **implements** (structural) | `internal/tools` | `cs` *is* the interface | the `ToolDeps` role-interfaces |
| **builds** (factory) | `internal/agent` | `cs` *constructs* an adapter value | `agent.Sender`, `agent.AgentDispatcher` |
| **owns** (composition) | `internal/{memory,journal,outline}` | `cs` *holds* a concrete | `*memory.Store`, `*capture.Capture` |

The engine never sees `cs`: `cs.streamingSender()` / `cs.blockingSender()` return
`agent.Sender`s, and `cs.coderDispatcher()` / `cs.dispatcherFor(sa)` return
`agent.AgentDispatcher`s. `internal/agent` stays pure (`imports only pkg/llm`)
because the variation is injected as *values*, not as a dependency on `main`. The
one place the two seam worlds meet is the dispatcher: an `agent.AgentDispatcher`
whose body calls `tc.Execute(ctx, cs)` — the engine seam wrapping the tool seam.

### Segment the fat `ToolDeps` (phase-1 sub-task)

Today `tools.ToolDeps` is **one 10-method interface** that every tool receives via
`Execute(ctx, deps)`, and `cs` satisfies it structurally with **no compile-time
assertion**. It violates Interface Segregation: a memory tool depends on the same
fat type as `bash`. The study work already starts pulling out `Outliner` /
`SubAgentRunner`; finish the job — **define narrow consumer-owned role-interfaces
and compose `ToolDeps` (the union `Execute`'s switch still needs) by embedding
them:**

```go
package tools // (internal/tools after phase 3)

type MemoryStore interface {
	MemoryWrite(name, content string) (string, error)
	MemoryRead(name string) (string, error)
	MemorySearch(query string) (string, error)
	MemoryForget(name string) (string, error)
}
type Outliner       interface { Outline(path string, budget int) (string, error) }
type SubAgentRunner interface { RunSubagent(ctx context.Context, sa Subagent, seed string) (string, error) }
type ShellGate      interface { GateShell(ctx context.Context, command string) (msg string, ok bool) }
type DeleteGate     interface { AllowDelete() (root string, allowed bool) }

// The union Execute's big switch consumes — assembled from the parts, not hand-listed.
type ToolDeps interface {
	MemoryStore
	Outliner
	SubAgentRunner
	ShellGate
	DeleteGate
	Quiet() bool
}
```

Payoffs: **pure tools take no deps at all** (`grep`, `read_file` body, `edit_file`
are functions over the filesystem); **the subagent path and tests depend narrowly**
(`RunSubagent` needs only `SubAgentRunner`; a memory test fakes 4 methods, not 10);
and **the God object's blast radius is the interface, not the struct** — `cs` need
not be broken up, only kept from leaking.

Make the implicit satisfaction explicit at the composition root, so a missing
method fails *there* with a clear location instead of at a distant dispatch call:

```go
// cmd/loop/main.go — the seam, asserted.
var (
	_ tools.ToolDeps       = (*CortexSession)(nil)
	_ tools.SubAgentRunner = (*CortexSession)(nil)
	_ tools.Outliner       = (*CortexSession)(nil)
)
```

This segmentation is **independent of the file moves** — it's pure interface
refactoring inside today's `cmd/loop/tools`, so it lands in **phase 1** (alongside
the `Resolve`→`Turn` fold, since phase 1 already touches the dispatch site). The
`Subagent`/`SubAgentRunner` shapes themselves are *built* by the study work
(study-subagent.md §1); phase 1 only needs to split the surface that exists today
(`MemoryStore`/`ShellGate`/`DeleteGate`/`Quiet` + the `Outliner` seam the new
outline tool will use), so the union is already segmented when study wires in.

## Status — phase tracker

Invariant for every phase: `go build ./...`, `go vet`, `./scripts/check.sh`, and
`go test ./...` all green. **No phase leaves the tree broken.** Flip ☐→☑ on land.

`navigator.go` is **not in this table** — it is deleted (not refactored) by
study-subagent.md phase 4, which builds the first real subagent caller
(`RunSubagent` + `Study` profile) on the engine and wires the `Progress`
consumer. Here, the engine is proven by the coder turn (`Turn`, after `Resolve`
folds in) + a fake-`Sender` test, so no phase touches the doomed loop.

| # | Phase | Scope | Files | Exit criteria | State |
|---|---|---|---|---|---|
| 0 | Extract `runLoop` + all seams **inside package `main`** | the engine function + `Sender`/`AgentDispatcher`/`Toolset`/`Bounds`/`Progress` types + `requestFor` + blocking/streaming `Sender`s (the streaming `Sender` carries the per-request deadline); no behavior change | `cmd/loop/` (new `loop.go`) | engine compiles, unused by callers yet, suite green | ☐ |
| 1 | Fold `Resolve` into `Turn` (delete `Resolve`) **+ segment `ToolDeps`** | the coder loop body becomes `runLoop`, driven via a streaming `Sender` + full dispatcher; the thin wrapper that remained folds into `Turn` (which already holds memory-index injection + turn capture) and **`Resolve` is deleted** — the engine is `runLoop`, the coder entry is `Turn`, no redundant indirection and the vestigial cognition-mode name is gone; nudge moves into the engine; `Progress` nil (REPL streams its own breadcrumb); per-request Sender deadline set; the two `cs.Resolve` test callers (`main_test.go:1930,1960`) retarget to `Turn` or a fake-`Sender` `runLoop`. **Sub-task:** split the fat `ToolDeps` into the role-interfaces (`MemoryStore`/`ShellGate`/`DeleteGate`/`Outliner`/`Quiet`, union by embedding) and add `var _` assertions at the composition root (see "The seam contract") | `cmd/loop/main.go`, `cmd/loop/tools/tools.go`, `cmd/loop/main_test.go` | REPL behaves identically; **`loop study-eval` numbers unchanged** (navigator untouched); **no `Resolve` symbol remains**; `var _ tools.ToolDeps = (*CortexSession)(nil)` compiles; manual smoke turn; suite green | ☐ |
| 2 | Model-free loop test | `SenderFunc` fake drives `runLoop`; assert `MaxIter`, byte-budget, per-request deadline, no-progress, finalize, and the blocking path all trip — proves the subagent path with no second real caller | `cmd/loop/*_test.go` | runs with zero network; each bound covered | ☐ |
| 3 | Move engine + shared data types → `internal/agent` **and regularize the package topology** | (a) Move the shared data types (`Tool`, `ToolCall`, `Request`, `Response`, `Message`) → `internal/agent`, renaming `AgentRequest`→`Request` and `AgentResponse`→`Response`. **This consolidates a split ownership, not an alias-rename:** today `AgentRequest`/`AgentResponse`/`Message` are **structs in `main`** while `Tool`/`ToolCall` are **aliases in `main` over `cmd/loop/tools`** — so the move pulls the wire types out of `main` *and* the tool types out of `tools` into one home. (b) **Rename `cmd/loop/tools` → `internal/tools`** (library code does not belong under `cmd/`; it already doesn't import `main`, so the move is just import-path churn in the `main` files). (c) **Fold `cmd/loop/ui`** (44 LOC, one file) into `internal/tools` or `pkg/cliout` — no 44-line package as its own node. End state: `cmd/loop` is `package main` only. | mechanical move once the boundary is proven; `internal/tools` flips from *defining* `Tool`/`ToolCall` to *importing* them from `internal/agent`; `internal/agent` never imports `internal/tools` (this is the one arrow that inverts — vet it first with a spike) | new `internal/agent/`, new `internal/tools/`, `cmd/loop/` (now `main` only) | no import cycles; `internal/agent` imports only `pkg/llm`; the `internal/tools`→`internal/agent` arrow exists; **no `internal/*` package imports `internal/tools`** (only `main` + the inverted type arrow touch it); `cmd/loop` has no sub-packages; suite green | ☐ |

Phase 3 is the only file-move and comes last, after the seam is proven in place.
It is also the package-topology phase: the type move, the `cmd/loop/tools`→`internal/tools`
rename, and the `cmd/loop/ui` fold all ride together so the import-path churn is paid once.

## Verification

Run `scripts/verify-study.sh` after each phase — it's the executable form of the
contract below (sections 2, 5, 6 cover this doc). It FAILs pre-implementation and
flips to PASS as phases land; wire into CI when both docs' work is complete.

### Expected physical deltas (bands, not point targets)

| Phase | Add (src) | Delete (src) | Net character |
|---|---|---|---|
| 0 extract engine + seams | +150…220 | 0 | additive (new `loop.go`) |
| 1 fold `Resolve` into `Turn` | +20 | −80 | `Resolve` (80) deleted; ~20 lines of seam-wiring absorbed into `Turn`; net-negative |
| 2 model-free loop test | +150…220 *(test)* | 0 | test-only |
| 3 move → `internal/agent` | ~0 net | ~0 net | **relocation only** |

This doc's source delta is **near-flat** — the engine absorbs `Resolve`'s loop
guts; nothing is added net beyond the seam types. **All navigator deletion lands
in study-subagent.md phase 4, not here.**

### Assertions (machine-checkable)

- **Behavior-preserving (the strongest check):** write characterization tests
  *first* (a `SenderFunc` fake recording the coder loop's message sequence,
  dispatch order, and stop conditions — captured against today's `Resolve`, then
  re-run against `Turn`/`runLoop` after the fold); they must pass unchanged after
  every phase. Plus:
  `loop study-eval` numbers must be **unchanged** through this work (the navigator
  is untouched here — if those numbers move, something leaked).
- **Existence:** `runLoop`, `Sender`, `AgentDispatcher`, `Toolset`,
  `Bounds`, `Progress`, `requestFor` exist (script §2).
- **Import graph:** `internal/agent` imports only `pkg/llm` — no
  cortex-session/tools-by-name (`go list -deps`; script §5).
- **Topology (post-phase-3):** `cmd/loop` has **no sub-packages** (it is `package
  main` only — `cmd/loop/tools` and `cmd/loop/ui` are gone); `internal/tools`
  exists and imports `internal/agent`; **no `internal/*` package imports
  `internal/tools`** (only `main` and the inverted type arrow touch the tool
  surface — guards against a future `internal` package taking a dep on it and
  inverting the graph). `go list -deps`; script §5.
- **Scoped diff:** the engine-unification diff **must not touch `navigator.go`**
  (it's deleted by the study work). `verify-study.sh --diff-base <ref>` flags it.
- **Green every phase:** `go build`/`go vet`/`check.sh`/`go test` all pass
  (script §6).

## Decisions log

_Append-only. Every choice gets a dated line so the rationale doesn't evaporate._

- **2026-06-27** Loop is a function; `Sender` + `AgentDispatcher` are the
  interfaces. The func-type-satisfies-1-method idiom lives on the injected seams,
  not on the orchestrator — making the loop an interface would invert the
  dependency for no benefit.
- **2026-06-27** **`Resolve` is folded into `Turn` and deleted** (not kept as a
  thin wrapper). `Turn` is already the per-turn entry that injects the memory
  index and captures the turn; it called `Resolve` as its one substantive step, so
  once `Resolve`'s loop body moves into `runLoop` the ~20-line remainder belongs in
  `Turn`. Result: the engine is `runLoop`, the coder entry is `Turn`, the subagent
  entry is `RunSubagent` — a clean three-name trio with no redundant indirection.
  Also retires the vestigial `Resolve` name (a leftover from the deleted
  Reflex/Reflect/Resolve/Think/Dream cognition modes — see
  [`archive.md`](archive.md)). The two `cs.Resolve` test callers retarget to `Turn`
  or a fake-`Sender` `runLoop`. Exit criterion: **no `Resolve` symbol remains.**
- **2026-06-27** This refactor is decoupled from the study feature and lands
  first. It is behavior-preserving; the study redesign assumes the unified engine
  exists.
- **2026-06-27** `Progress` sink: the param is introduced here (engine concern —
  the dispatcher sees every call), wired `nil` by `Resolve` (the REPL streams its
  own breadcrumb). The real consumer (`studyProgress`) is wired by the study work.
- **2026-06-27** `runNavigator` is **deleted, not refactored**. It is the old
  navigator's internals, removed wholesale by study-subagent.md phase 4 and
  replaced by a `Study` profile on `runLoop`. Refactoring it onto the engine first
  would be refactor-then-delete. The engine is proven via `Resolve` + the
  fake-`Sender` test instead; "one loop" is reached when the study work lands.
- **2026-06-27** Phase 3 (move to `internal/agent`) is last and mechanical — do
  it only after the seam is proven inside `package main`, to avoid one
  unreviewable big-bang diff. It moves the shared data types (`Tool`, `ToolCall`,
  `Request`, `Response`, `Message`) into `internal/agent` (renaming the `Agent*`
  request/response pair); `cmd/loop/tools` then imports `internal/agent`, and
  `internal/agent` must never import `cmd/loop/tools` (cycle).
- **2026-06-27** Phase 3 is a **consolidation, not an alias-rename** (corrected
  after reading the code): today the type ownership is split — `AgentRequest`,
  `AgentResponse`, and `Message` are **structs defined in `main`** (`main.go:369`,
  `:726`, `:753`), while `Tool`/`ToolCall`/`ToolFunction`/`FunctionCall` are
  **aliases in `main` over `cmd/loop/tools`** (`main.go:483-484`, `:817-818`). The
  move puts all of them in `internal/agent`, so `tools` flips from defining
  `Tool`/`ToolCall` to importing them. That one arrow inversion is the riskiest
  step in this doc — prove it with a throwaway spike before treating phase 3 as
  mechanical.
- **2026-06-27** **Seam contract recorded** ("The seam contract" section): `cs` is
  the composition root that relates to lower layers three ways — *implements* the
  `internal/tools` role-interfaces (structural), *builds* the `internal/agent`
  seams (factory; the engine never sees `cs`, so it stays `imports only pkg/llm`),
  and *owns* the `internal/{memory,journal,outline}` substrate (concrete). The fat
  `ToolDeps` is segmented into narrow consumer-owned role-interfaces (union by
  embedding) with `var _` assertions at the root; this is a **phase-1 sub-task**
  (pure interface refactor, independent of the phase-3 file moves).
- **2026-06-27** Phase 3 also **regularizes the package topology**:
  `cmd/loop/tools` → `internal/tools`, and `cmd/loop/ui` (44 LOC) folds into
  `internal/tools`/`pkg/cliout`, leaving `cmd/loop` as `package main` only.
  Rationale: library code doesn't belong under `cmd/` (which should hold thin
  entry points); the tool surface was the only substrate package not already in
  `internal/*`. **Rejected: merging `tools` into `internal/agent`** — `tools`
  imports `shellrisk`/`projectscan`/`config`/`events`/`secret`, so merging would
  destroy the engine's `imports only pkg/llm` invariant and couple the pure loop
  to concrete tool impls. The `Dispatcher` seam exists precisely to keep
  mechanism (`agent`) and capabilities (`tools`) apart; `agent` owns the type
  vocabulary, `tools` imports it. Rides phase 3 so the import-path churn is paid
  once; new invariant: **no `internal/*` imports `internal/tools`**.
- **2026-06-27** `Bounds.MaxWall` (a hard whole-run wall-clock cancel)
  **rejected / deferred.** Cancelling the run's context to enforce it kills a
  nearly-done study mid-call with no digest, and leaves no live context to run
  the finalize round-trip on. Time is bounded instead by the `Sender`'s
  per-request deadline × `MaxIter` (worst-case rounds). Revisit a *soft*
  between-iterations wall only if that bound proves too loose in practice.
- **2026-06-27** The no-progress nudge is engine-level (all callers), not a
  main-loop special case.
