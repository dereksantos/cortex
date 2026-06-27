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

## Status — phase tracker

Invariant for every phase: `go build ./...`, `go vet`, `./scripts/check.sh`, and
`go test ./...` all green. **No phase leaves the tree broken.** Flip ☐→☑ on land.

`navigator.go` is **not in this table** — it is deleted (not refactored) by
study-subagent.md phase 4, which builds the first real subagent caller
(`RunSubagent` + `Study` profile) on the engine and wires the `Progress`
consumer. Here, the engine is proven by `Resolve` + a fake-`Sender` test, so no
phase touches the doomed loop.

| # | Phase | Scope | Files | Exit criteria | State |
|---|---|---|---|---|---|
| 0 | Extract `runLoop` + all seams **inside package `main`** | the engine function + `Sender`/`AgentDispatcher`/`Toolset`/`Bounds`/`Progress` types + `requestFor` + blocking/streaming `Sender`s (the streaming `Sender` carries the per-request deadline); no behavior change | `cmd/loop/` (new `loop.go`) | engine compiles, unused by callers yet, suite green | ☐ |
| 1 | Refactor `Resolve` onto `runLoop` | main coder loop uses the engine via a streaming `Sender` + full dispatcher; nudge moves into the engine; capture/memory-index stay in `Turn`; `Progress` nil (REPL streams its own breadcrumb); per-request Sender deadline set | `cmd/loop/main.go` | REPL behaves identically; **`loop study-eval` numbers unchanged** (navigator untouched); manual smoke turn; suite green | ☐ |
| 2 | Model-free loop test | `SenderFunc` fake drives `runLoop`; assert `MaxIter`, byte-budget, per-request deadline, no-progress, finalize, and the blocking path all trip — proves the subagent path with no second real caller | `cmd/loop/*_test.go` | runs with zero network; each bound covered | ☐ |
| 3 | Move engine + the shared data types (`Tool`, `ToolCall`, `Request`, `Response`, `Message`) → `internal/agent`, renaming `AgentRequest`→`Request` and `AgentResponse`→`Response` (today these are aliases in `main` over `cmd/loop/tools`) | mechanical move once the boundary is proven; `cmd/loop/tools` now imports `internal/agent` for the moved types; `internal/agent` never imports `cmd/loop/tools` | new `internal/agent/`, `cmd/loop/`, `cmd/loop/tools/` | no import cycles; `internal/agent` imports only `pkg/llm`; the `cmd/loop/tools`→`internal/agent` arrow exists; suite green | ☐ |

Phase 3 is the only file-move and comes last, after the seam is proven in place.

## Verification

Run `scripts/verify-study.sh` after each phase — it's the executable form of the
contract below (sections 2, 5, 6 cover this doc). It FAILs pre-implementation and
flips to PASS as phases land; wire into CI when both docs' work is complete.

### Expected physical deltas (bands, not point targets)

| Phase | Add (src) | Delete (src) | Net character |
|---|---|---|---|
| 0 extract engine + seams | +150…220 | 0 | additive (new `loop.go`) |
| 1 `Resolve`→engine | +30 | −50 | `Resolve` 80→~30; flat-to-negative |
| 2 model-free loop test | +150…220 *(test)* | 0 | test-only |
| 3 move → `internal/agent` | ~0 net | ~0 net | **relocation only** |

This doc's source delta is **near-flat** — the engine absorbs `Resolve`'s loop
guts; nothing is added net beyond the seam types. **All navigator deletion lands
in study-subagent.md phase 4, not here.**

### Assertions (machine-checkable)

- **Behavior-preserving (the strongest check):** write characterization tests
  *first* (a `SenderFunc` fake recording `Resolve`'s message sequence, dispatch
  order, and stop conditions); they must pass unchanged after every phase. Plus:
  `loop study-eval` numbers must be **unchanged** through this work (the navigator
  is untouched here — if those numbers move, something leaked).
- **Existence:** `runLoop`, `Sender`, `AgentDispatcher`, `Toolset`,
  `Bounds`, `Progress`, `requestFor` exist (script §2).
- **Import graph:** `internal/agent` imports only `pkg/llm` — no
  cortex-session/tools-by-name (`go list -deps`; script §5).
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
- **2026-06-27** `Bounds.MaxWall` (a hard whole-run wall-clock cancel)
  **rejected / deferred.** Cancelling the run's context to enforce it kills a
  nearly-done study mid-call with no digest, and leaves no live context to run
  the finalize round-trip on. Time is bounded instead by the `Sender`'s
  per-request deadline × `MaxIter` (worst-case rounds). Revisit a *soft*
  between-iterations wall only if that bound proves too loose in practice.
- **2026-06-27** The no-progress nudge is engine-level (all callers), not a
  main-loop special case.
