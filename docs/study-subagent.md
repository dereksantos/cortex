# Study: a bounded read-only subagent

> **Status:** design + tracker (this doc). Supersedes the recursive map-first
> navigator design (the old `study-navigator.md` + `refactor-loop-main.md` were
> removed 2026-06-27 — this doc and [`engine-unification.md`](engine-unification.md)
> replace them). **Builds on the unified engine** in
> [`engine-unification.md`](engine-unification.md), which lands first. Memory
> direction: [`memory-tools.md`](memory-tools.md).

## What study is

`study(path, goal)` spins up **one** read-only subagent off the main loop. It is
seeded with a structural **outline** of the target plus the goal, then works a
small, bounded tool loop — `outline`, `grep`, `read_file` — to gather exactly the
goal-relevant context and report a digest back to the coder. It does **not** see
the coder's conversation, and it **cannot spawn another subagent**.

```
main loop (coder)
   │  study(path, goal)
   ▼
┌─ STUDY ─ one subagent, off the main loop ─────────────────────────────────┐
│  prefill: [studySystem] + [goal + outline(path, seedBudget)]              │
│  loop (bounded by Bounds: MaxIter + ReadBudgetBytes + no-progress guard): │
│     outline(path,budget)   structure — where things are (names + spans)   │
│     grep(pattern,path)     content   — filesystem matches → file:line      │
│     read_file(path,a,b)    fetch     — a targeted span                     │
│  → digest (one line per tool call shown via the engine's Progress sink)    │
└──────────────────────────────────► returned to the coder as the result ───┘
```

## Why this shape

The lesson from opencode: **don't rely on the model being clever enough to read
narrowly.** Engineer it so narrow is the only option *and* the obvious one:

- **Only option** — `read_file` is targeted (a span; whole-file refused above a
  small floor), so even a greedy model can't pull a 3,000-line file.
- **Obvious option** — `outline` / `grep` hand back exact line numbers, so the
  model never *needs* to scan to find where something lives.

The three tools are **MECE** along one axis — *how you find or fetch context*:

| Tool | Axis | Gives you |
|---|---|---|
| `outline` | structure | what's there + where (names → child path or line span) |
| `grep` | content pattern | where a string/regex occurs (file:line) |
| `read_file` | fetch | the actual bytes of a span |

`outline` answers "what is here," `grep` "where is this token," `read_file` "show
me those lines." No overlap; together they cover finding and reading **any path**.

### Recall is two existing surfaces — there is no `search` tool

An earlier draft added a vector `search` tool. **Dropped.** Recall has two real
needs, each already served:

| Need | How | Corpus |
|---|---|---|
| "What do I durably know?" | `memory_*` tools (model-driven notes) | curated, named |
| "What did we actually do before?" | `study(.cortex/journal, goal)` | the canonical journal, via the three tools above |

The journal is simply **a path you study** — `outline`/`grep`/`read_file` work on
`.cortex/journal/**` like any other path. That needs no embeddings, no SQLite
projection, no separate corpus to populate. A vector `search` would have been a
third path overlapping both, shipping against an empty store (the capture
write-side and storage projection are disconnected from the loop — see "Dormant
substrate"). What's lost is fuzzy semantic recall; for a coding harness you recall
by file/symbol/command, which `grep` covers.

### Tools are flat capabilities, not members of an agent

A tool is a registered capability — it has no notion of who calls it. An **agent
loop** (the main loop *or* study) is defined by **which tools it is granted**, its
system prompt, its bounds, and its model. The same `outline`, `grep`, `read_file`
are granted to whichever loops need them — one implementation each,
caller-agnostic. Each tool depends on the **narrowest** interface it needs,
defined in the tools package and satisfied by `*CortexSession`: `Outliner`
(outline + the study seed), `SubAgentRunner` (study). Pure tools (`grep`,
`read_file` body) need no interface. Tools never branch on "am I in the main loop
or a subagent."

## What this changes (vs. today)

- `Navigate` / `runNavigator` / `navMap` / `navTools` / `navMaxDepth` — gone.
  Study is the `Study` profile run on the unified `runLoop`
  ([`engine-unification.md`](engine-unification.md)).
- `project_index` tool + the `navMap` seed — folded into the new
  `internal/outline` (one structural primitive; a clean reimplementation, §2).
- study-spawns-study recursion — removed. Breadth is `outline(child)` + `grep`.
- the `passes` parameter / multi-pass language — gone.
- **`grep` is NEW** — the content locator study never had.
- **No `search` tool, no `memory_search` change.** Recall = memory notes +
  `study(.cortex/journal)`.

---

## 1. The `Study` profile

A subagent is **inert data** (a `Subagent` profile: name, role, system prompt,
offered tools, bounds) plus **shared behavior** (`SubAgentRunner` → the one
generic `runLoop`). `Study` is the only profile today; adding another is a `var`.

```go
package tools

type Subagent struct {
	Name   string  // banner / telemetry label
	Role   string  // model-role binding the runner resolves (e.g. "study")
	System string  // system prompt
	Tools  []Tool  // offered == execution allowlist
	Bounds Bounds  // engine type — MaxTokens (mandatory), MaxIter, ReadBudgetBytes
}

type SubAgentRunner interface {
	RunSubagent(ctx context.Context, sa Subagent, seed string) (string, error)
}

// The one profile today. Read-only: no write/edit/bash/remove, no study (no
// recursion), no memory_write (writing notes is the coder's / a future Reflect's
// job, not a read-only researcher's).
var Study = Subagent{
	Name: "study", Role: "study", System: studySystem,
	Tools:  []Tool{OutlineTool, GrepTool, ReadFile},
	Bounds: Bounds{MaxTokens: 12_000, MaxIter: 10, ReadBudgetBytes: 96_000},
}
```

```go
package main

// RunSubagent: resolve the model, build the request + the profile's toolset, hand
// off to the shared runLoop. No per-profile loop, no recursion. The Progress sink
// shows the subagent's tool calls live (REPL → stderr; headless/tests nil).
func (cs *CortexSession) RunSubagent(ctx context.Context, sa tools.Subagent, seed string) (string, error) {
	spec := cs.specForRole(sa.Role)
	req  := requestFor(spec, sa.System, seed, sa.Tools, sa.Bounds.MaxTokens)
	ts   := tools.Toolset{Tools: sa.Tools, Dispatch: cs.dispatcherFor(sa)}
	return runLoop(ctx, cs.blockingSender(), req, ts, sa.Bounds, cs.studyProgress())
}

// dispatcherFor builds a profile's dispatcher: allowlist + transforms composed
// HERE, so ToolCall.Execute stays caller-agnostic (no "subagent mode" flags).
func (cs *CortexSession) dispatcherFor(sa tools.Subagent) agent.AgentDispatcher {
	allow := tools.AllowOf(sa.Tools)
	return agent.DispatchFunc(func(ctx context.Context, call ToolCall) string {
		if !allow[call.Function.Name] {
			return "Error: " + call.Function.Name + " is not available here."
		}
		// One door-guard check for the whole subagent: confine the path arg of
		// EVERY path-taking tool (outline, grep, read_file). The tools never
		// re-confine — confinement lives here and only here.
		call, err := tools.ConfinePath(call, cs.root)
		if err != nil {
			return "Error: " + err.Error()
		}
		call = tools.TargetedRead(call) // study's read clamp; no-op for other tools
		out, err := call.Execute(ctx, cs)
		if err != nil {
			return "Error: " + err.Error()
		}
		return out
	})
}

// The study tool IS the entry point: seed via Outliner, run via SubAgentRunner.
func (tc ToolCall) Study(ctx context.Context, deps ToolDeps) (string, error) {
	path, _ := tc.StringArg("path")
	goal, _ := tc.StringArg("goal")
	ol, err := deps.Outline(path, studySeedBudget)
	if err != nil {
		return "", err
	}
	return deps.RunSubagent(ctx, Study, studySeed(goal, path, ol))
}
```

### `studySystem`

The "works on any model" bet rides on this: it teaches **locate-then-read** and
**stop-and-answer** as principles (not "for X do Y" recipes), and "be concise" is
the entire output discipline — so no separate digest contract is needed.

```
You are a code researcher. You're given a GOAL and an OUTLINE of a path. Find the
parts of the codebase relevant to the goal, read them, and explain what you found.

Your tools are read-only:
- outline(path, budget): the structure of a file or directory — entries with line
  spans (a file lists its declarations; a directory lists its files). Outline a
  path you haven't seen yet to orient before reading it.
- grep(pattern, path): find where text or a regex (RE2 syntax — no lookahead or
  backreferences) occurs in the code — returns file:line. Use it to locate a
  symbol instead of scanning.
- read_file(path, start, end): read a specific line range.

Locate, then read. Use outline/grep to find exactly where the answer lives, then
read_file only those spans. Don't read whole files or wander — your reads are
limited; spend them on what the goal needs, then stop. If the outline already
answers the goal, just answer — you don't have to read.

When you've seen enough, stop calling tools and answer the goal: explain
concretely how the relevant code works and how the pieces fit, citing file:line
where it helps the reader. Base your answer only on what you read, and be concise.
```

### Output, failure & the bounds that must always hold

- **No digest contract.** "be concise / cite file:line" + the finalize call's
  max-tokens are the only bounds. Add a cap later only if an eval shows rambling.
- **Failure = a brief error the agent adapts to.** A failed tool returns its short
  error *as the observation* (the existing `bash`/`edit_file` pattern). If `study`
  itself errors, the coder gets the short error and adapts. No failure table.
- **Empty digest is handled explicitly.** If finalize returns "", the coder gets
  "study produced no digest for <path>" — never a silent empty string.
- **Bounds are the safety net that must always hold.** `MaxTokens` (the mandatory
  per-request completion cap — the primary runaway backstop; see
  [`engine-unification.md`](engine-unification.md)), `MaxIter` (rounds),
  `ReadBudgetBytes` (accumulated output), and the no-progress guard are
  independent; whichever trips first forces the engine's finalize. Wall-clock
  time is bounded *without* a hard whole-run timer: the `Sender`'s per-request
  deadline caps any single hung model call, so worst-case time ≈ `MaxIter` ×
  that deadline — which keeps a slow/greedy model from running for minutes (the
  north failure) without a `MaxWall` that would guillotine a nearly-done study
  mid-call (rejected — see the decisions log).
- **No recursion** — `Study.Tools` does not include `study`.

### Paths are workspace-confined — one door-guard check

Study runs model-supplied paths, and **all three tools take a path** (`outline`,
`grep`, `read_file`). Today `read_file` does `os.ReadFile(path)` with **no
confinement** (only `remove_path` confines, via `confinedPath`) — a model can read
`/etc/passwd` or `../../secrets`. For production, **one** check — `tools.ConfinePath(call, root)`
in the dispatcher (§1) — guards **every** path-taking call: reject absolute paths
and `..` escapes; require the resolved path stay within the workspace root. The
tools themselves never re-confine (`grep`/`outline` don't each re-implement it) —
the door guard checks everybody, exactly once, before execution.

This is a *separate* rule from `remove_path`'s `confinedPath`, which *protects*
`.git`/`.cortex` from deletion. Reads must **allow** `.cortex/journal` (that's how
journal recall works) — it lives inside the root, so "stays within root" admits it
while still blocking escapes. Don't reuse the delete-protection helper verbatim.

---

## 2. `outline` — the structural primitive (clean reimplementation)

`outline(path, budget)` is **`ls`, generalized to file interiors**, filled
**breadth-first** until a token budget is spent. A directory lists its
files/subdirs; a file lists its top-level units. Every entry carries a
**locator** — a child `path` to `outline` deeper, or a line **span** to
`read_file`. `budget` is the only knob. Deterministic, no model.

A **fresh `internal/outline` package**, not a wrapper over `projectindex` (three
callers — `navigator.go`, `tools.go`, and `memory_tools_test.go` (which builds a
`projectindex` over `.cortex/journal`) — all inside `cmd/loop`, so fully
replaceable; the test migrates onto `internal/outline` in phase 4). The
AST/regex *knowledge* in `projectindex/outline.go` is re-expressed as small,
named functions, not imported.

```go
package outline

type Entry struct {
	Name string // "navigator.go", "tools/", "func Resolve", "# Setup"
	Path string // a dir/file to outline deeper; "" for an in-file unit
	Span [2]int // 1-indexed [start,end] for an in-file unit; {0,0} otherwise
}

func Outline(path string, budget int) ([]Entry, error) // tree, breadth-first to budget
func Render(path string, budget int) (string, error)   // the text the model sees

// The extraction seam — one obviously-named function per "how do I list this
// thing's interior," dispatched by type. No tier soup.
//   listChildren(dir)  → directory entries (the ls case)
//   listUnits(file)    → in-file units, dispatching by file kind:
//        goUnits(src)    via go/ast       (funcs, types, EndLine spans)
//        regexUnits(src) decl/heading regex (other code, markdown)
//        wholeFile(src)  single L1-N entry  (unparseable floor)
//   fill(root, budget) → breadth-first walk
```

**Truncation rule (load-bearing).** Always list **every direct child's name**
(cheap); spend budget only on going *deeper*. A relevant file can never silently
vanish from a directory listing — only its expansion is deferred to a one-line
`… +N more — outline("child") to expand` note. This is what makes outline
reliable on a large package.

```
# example rendered output (file)
func Resolve(ctx context.Context) error      L2853-2932
type CortexSession struct                     L1099-1198
func (cs *CortexSession) RunSubagent          L2640-2660
… +37 more — outline("cmd/loop/main.go", 8000) to expand
```

- `budget` units = tokens (≈4 bytes/token). Seed default `studySeedBudget`; the
  `outline` tool defaults to `outlineDefaultBudget` when the model omits it.
- **Breadth-first** — never lose "what files exist" to show one file's detail.
- **Caching (deferred, noted):** outline re-parses every call; `go/ast` over a big
  seed directory is slow on large repos. A path+mtime cache is the production fix
  — build it when measured latency warrants, not before.

---

## 3. `grep` — content search across the filesystem

`grep(pattern, path)` is a **pure-Go**, dependency-free content search: a regex
over the working tree, reusing the existing ignore set
(`projectscan.LoadIgnoreSet` — `.gitignore` + dir-skips + binary/secret skip),
returning `file:line:text` matches (capped) — never file bodies. It's the
*content* locator: grep for a symbol, get exact lines, then `read_file` the span.
This is the tool the recursive navigator lacked, which forced weak models to scan
an outline instead of jumping. It is also how journal recall works:
`grep("<symbol>", ".cortex/journal")`.

```go
package tools

const FunctionGrep = "grep"

// grep is PURE (filesystem only) — a ToolCall method, like outline; no ToolDeps.
func (tc ToolCall) Grep(root string) (string, error) // root scopes the walk; ConfinePath already vetted the path

const (
	grepMaxHits     = 100      // cap so a broad pattern can't flood context
	grepMaxFileSize = 1 << 20  // skip files larger than 1 MiB
)

// grepFiles walks root (reusing projectscan.LoadIgnoreSet), matches re per line,
// returns up to cap "file:line:text" hits. Pure Go: filepath.WalkDir + regexp +
// bufio — no exec. CHECKS ctx.Err() in the walk so the per-request deadline /
// cancellation actually interrupts a scan of a huge tree (a bare WalkDir would
// ignore the deadline).
func grepFiles(ctx context.Context, root, pattern string, cap int) (string, error)
```

### Decisions for grep

- **RE2, not PCRE.** Go's `regexp` is RE2: linear-time, **no backreferences**
  (`\1`) and **no lookahead/lookbehind** (`(?=…)`). PCRE — what most models emit —
  has those, so a model may write a pattern that fails to compile. Mitigation, no
  translator: (1) the compile error returns as the observation so the model
  retries; (2) the tool description says "RE2 syntax (no lookahead or
  backreferences)" with one example.
- **ctx-aware walk.** `grepFiles` checks `ctx.Err()` periodically so the
  per-request deadline / cancellation interrupts a long scan.
- **Workspace-confined by the door guard, not itself.** `grep`'s `path` is vetted
  by `ConfinePath` in the dispatcher (the one check that covers all three tools);
  `grep` doesn't re-confine — it just walks the already-vetted root.
- **Pure Go, ~100–150 LOC.** We already own the ignore set; the rest is walk +
  regexp + line scan + cap. Skip ripgrep's mmap/parallelism — one project tree,
  regexp-per-line is sub-second.
- **Dedicated tool, not bash.** Study's toolset excludes `bash`; `grep` is the one
  allowlisted search path — read-only and capped, no arbitrary exec.
- **Deferred: `google/codesearch`** (trigram index). Decided **against adopting
  now** (2026-06-27): it's an index you build and maintain, and for an *editing*
  agent it goes stale the moment a file is written — a grep right after an edit
  would miss the change (a correctness bug, not a perf nit). It earns its keep
  only at monorepo scale, where the right design is *hybrid* (index the clean
  tree + scan dirty files), not raw codesearch. The `grep(pattern, path)→file:line`
  tool is the seam; slot an index behind it later on measured need, no interface
  change.

---

## 4. `read_file` — targeted fetch (one knob)

`read_file(path, start, end)` fetches **a span**, not a file. The two magic
constants in today's navigator (`readWholeFloor` bytes + `navReadLines` lines)
collapse into **one** number, expressed in lines — what the model reasons in:

```go
const maxReadLines = 200 // governs BOTH "what counts as small" and "max span"
```

- A **no-range** read of a file **≤ `maxReadLines`** returns it whole (small
  files — configs, short helpers — read in one shot; never frustrated).
- A file **above** `maxReadLines` with no range is **refused**: "too large —
  `outline(path)` first, then read a span."
- A **ranged** read is clamped to `maxReadLines`. The clamp is **visible** — the
  `@path:start-end` header shows the actual span returned, so a model that asked
  for more sees it got a window and can read on.

One constant, two behaviors, no byte floor. The targeted contract is enforced by
`tools.TargetedRead(call)` in the study dispatcher (§1) before execution, so the
main-loop `read_file` is unchanged. 200 lines ≈ one screenful.

```go
// In the study dispatcher, reads are constrained (after confinement) before exec:
func TargetedRead(call ToolCall) ToolCall {
	if call.Function.Name != FunctionReadFile {
		return call // no-op for every other tool
	}
	return clampOrRefuse(call, maxReadLines) // ≤max no-range → whole; over → refuse; range → clamp
}
```

---

## 5. Acceptance eval — replace the navigator driver, keep the scorer

The navigator is gone as a concept, so its **eval driver** goes with it —
`navEvalCases`, `CORTEX_NAV_REPS`, `runStudyEvalNav` are deleted in **phase 4**
(coupled to the `Navigate`/`runNavigator` deletion, else phase 4 can't be "suite
green" — `study_eval.go` would still reference dead symbols). What survives is the
**scorer**, `countGoalHits`, which is implementation-agnostic ("is the goal-fact
present in the digest"). Phase 5 builds the new driver on `RunSubagent` and reuses
that scorer — a repurpose, not an extend-in-place.

Build it with **good separation** — four distinct pieces, not one mega-loop:

```
fixture   StudyProbe{ Path, Goal, Gold []string }   — frozen probes + gold facts
runner    drive RunSubagent → StudyEvalResult        — collect, with instrumentation
scorers   mechanical: stop-reason / bounded / tokens / per-tool / latency  (free, deterministic)
          judge:      goal-hit = fraction of gold present
          derived:    goal-hit per 1k output tokens; grep-vs-read ratio; rep pass-rate / p95
reporter  model × probe table
```

```go
type StudyProbe struct {
	Path string
	Goal string
	Gold []string // key facts a correct digest must contain
}

type StudyEvalResult struct {
	Model string
	Probe string

	// quality
	GoalHit float64 // fraction of Gold present in the digest

	// termination — WHY it stopped, not just whether (the diagnostic axis)
	StopReason     string // clean-finalize|max-iter|read-budget|no-progress|deadline|error
	Iterations     int    // model rounds consumed
	FinalizeForced bool   // answered because a bound dragged finalize out, not voluntarily

	// tokens — cumulative across every round-trip (cost + runaway axis)
	InputTokens      int
	OutputTokens     int
	PeakOutputTokens int  // max output on any single request
	MaxTokensClamped bool // any request hit Bounds.MaxTokens (eval-time runaway tripwire)

	// tools — per-tool, the locate-then-read discriminator
	Outlines  int
	Greps     int
	Reads     int
	ToolErrs  int  // bad regex, refused/over-budget read, confinement reject, no-progress repeat
	ReadBytes int  // accumulated tool output
	Bounded   bool // peak ReadBytes ≤ ReadBudgetBytes

	LatencyMS int64
}
```

The fields beyond `GoalHit`/`Bounded`/`ReadBytes`/`Reads`/`LatencyMS` exist
because the 2026-06-28 flight check proved **goal-hit alone is fooled by a model
that got the right answer the wrong way** — `north` passed needle/bounded/grounding
by brute-reading (~800 lines / 4 reads on a single-region goal, 0 greps). Each
added field discriminates that failure:

- **Per-tool counts (`Outlines`/`Greps`/`Reads`)** measure the redesign's whole
  thesis directly: a run with 0 greps and 6 reads *is* the brute-read failure even
  at goal-hit 1.0. The grep-vs-read ratio is the locate-then-read signal.
- **`StopReason` + `Iterations` + `FinalizeForced`** say *which bound is binding*.
  If every probe stops at `max-iter`, the budget is too tight or the model greedy —
  a `Completed` bool can't tell you that, so it replaces it.
- **`PeakOutputTokens` + `MaxTokensClamped`** are the eval-time form of the north
  runaway tripwire: a config that *wants* to run away shows up as a request pinned
  at the `Bounds.MaxTokens` ceiling *before* it pins a GPU slot for 30 min. Ties the
  eval to the mandatory-`max_tokens` invariant (engine-unification.md).
- **`InputTokens`/`OutputTokens`** are cumulative cost; the headline efficiency
  metric is *derived* — **goal-hit per 1k output tokens** — because two models at
  goal-hit 1.0 are not equal if one spent 5× the tokens (the small-model-amplifier
  frontier).
- **`ToolErrs`** counts the thrash modes the design calls out (RE2 compile errors,
  refused over-budget reads, confinement rejects, no-progress repeats) — turns "it
  felt slow" into a number. The engine already tracks the repeat counter.

- **Ship first** on the existing keyword goal-hit + the mechanical instrumentation
  above — `stop-reason`/`bounded`/`latency`/per-tool counts/`tool-errs`/tokens all
  come *free* from the engine's usage + budget accounting (both loops already
  account usage; the dispatcher already sees every tool call + error). Accept when
  goal-hit ≥ T, `StopReason == clean-finalize` (not bound-forced), all `Bounded`,
  and no `MaxTokensClamped`.
- **Report reps, not means.** Under `CORTEX_STUDY_REPS` emit per-rep rows and a
  `k/n` pass-rate + **p50/p95 latency** (not the mean) — the north thrash was a
  ~15-min *tail* event a mean would launder, and a harness for local models lives
  or dies on determinism (2/3 ≠ 3/3).
- **Defer panel-gold.** Vendored snapshot + panel-consensus `Gold` +
  `loop study-eval gold` is its own mini-project — add only if keyword proves
  coarse. Likewise **read precision** (useful-bytes ÷ read-bytes) needs gold spans —
  defer with panel-gold.
- **Telemetry, always-on.** Emit each study run's `StudyEvalResult`-shaped stats
  (model/latency/stop-reason/per-tool/tokens/bytes) to the journal in normal
  operation too, not just under the eval — it feeds debugging and the eval reads the
  same record. **One shape, emitted every run** — never measure eval-only
  instrumentation that's absent in production.
  - **Shipped status (deferred):** the eval emits the full `StudyEvalResult`-shaped
    row — every mechanical field (`stop_reason`, `outlines`/`greps`/`reads`/`tool_errs`,
    `read_bytes`/`bounded`, tokens incl. `peak_output_tokens`/`max_tokens_clamped`) — as
    **stdout JSONL** (`studyEvalRow`), which is the real ø discrimination and what
    `verify-study.sh` asserts (`json:"read_bytes|bounded"`). The **journal-sink**
    alignment (a `study.result` entry sharing `EvalCellResultPayload`'s vocabulary) was
    **deferred** to keep the refactor's net source LOC inside the contracted band; the
    row shape is identical, so adding the `journal.NewWriter` path later is a thin wiring
    change, not a new format.

### Align with the canonical eval sink (don't invent a third format)

The codebase already has a structured eval sink: **`journal.EvalCellResultPayload`**
(`internal/journal/eval.go`), written as an `eval.cell_result` entry into
`.cortex/journal/eval/` — `emitSessionMetrics()` (`cmd/loop/main.go:2432`) already
emits one per REPL session. (Its comment still says it "mirrors
`internal/eval/v2.CellResult`"; that grid framework was **deleted** — the journal
struct is now the de-facto canonical one, and the comment is stale.
`pkg/cliout`'s `source`-discriminated `.cortex/db/cell_results.jsonl` union is a
**second, dormant** sink from the old `cortex` CLI — **not** wired into `loop`;
align to the journal one, the path `loop` actually writes.)

`StudyEvalResult` should **not** be its own bespoke stdout-only shape (today's
`studyEvalRow` `fmt.Println`s JSONL with off-vocabulary names —
`GoldPresent`/`DigestChars`/`Pass`). Align in two moves:

1. **Reuse the standard field vocabulary** for everything that maps, so one query
   schema reads study + session rows:

   | `StudyEvalResult` | `EvalCellResultPayload` json tag |
   |---|---|
   | `Model` | `model` |
   | `LatencyMS` | `latency_ms` |
   | `InputTokens` / `OutputTokens` | `tokens_in` / `tokens_out` |
   | `Iterations` | `agent_turns_total` |
   | `Probe` | `scenario_id` |
   | goal-hit pass | `task_success` + `task_success_criterion` |
   | — | `schema_version`/`run_id`/`timestamp`/`git_*`/`harness:"study"`/`cost_usd` (populate from run context) |

2. **Carry the study-specific discriminators as additional structured fields** —
   `goal_hit` (fractional), `stop_reason`, `finalize_forced`, `peak_output_tokens`,
   `max_tokens_clamped`, `outlines`/`greps`/`reads`/`tool_errs`, `read_bytes`,
   `bounded`. These have no home in the grid struct and **must not** be flattened
   into the `notes` string (the "structured outputs, not free-text" rule). Emit as a
   distinct journal entry type (`study.result`, a `StudyResultPayload`) through the
   same `journal.NewWriter` path `emitSessionMetrics` uses — the journal already
   discriminates by entry `Type`, exactly as `cliout` discriminates its union by
   `source`. Net: same vocabulary + same sink mechanism + a typed extension block,
   not a third format. **Phase-5 sub-task:** route the driver off `fmt.Println`
   onto this writer.

---

## Future: a `Reflect` profile (deferred — door left open)

There is currently **no agentic process that analyzes the journal** — `captureTurn`
writes it mechanically and only an explicit `study(.cortex/journal)` ever reads
it. The `internal/cognition` Dream/Think/Reflect subsystem used to do proactive
distillation; it was disconnected in the memory pivot and then **deleted** when
the cognition DAG it was built on was removed (2026-06-27).

Decision (2026-06-27): **stay pull-only** — memory notes + on-demand journal study
is enough. But the `Subagent` abstraction makes proactive analysis a `var`, not a
subsystem, if recall ever proves insufficient:

```go
// NOT built now — recorded so the shape is obvious when/if it's wanted.
var Reflect = Subagent{
	Name: "reflect", Role: "study",
	Tools:  []Tool{OutlineTool, GrepTool, ReadFile, MemoryWriteTool}, // read journal → write notes
	Bounds: Bounds{MaxTokens: 12_000, MaxIter: 6, ReadBudgetBytes: 64_000},
}
// Triggered at idle / a turn-count threshold / session end; reads recent journal
// entries on the same runLoop engine and writes memory notes. Resurrects the
// INTENT of Dream/Think as one clean profile over the model-driven memory store
// — not the deleted storage/embeddings pipeline.
```

## Dormant substrate (what remains after the cognition removal)

The loop does **not** use the vector/cognition stack, and this design keeps it
that way (no `search`, no storage projection). The cognition DAG
(`pkg/cognition`), its retrieve/Dream/Think/Reflect subsystem
(`internal/cognition`), and the blind-sampling study engine (`internal/study`)
were **deleted** on 2026-06-27 — `internal/cognition` couldn't be left dormant
because it was built on the DAG types and stopped compiling without them. What
genuinely remains compiling-but-unused, on purpose:

- `internal/storage` vector index + `capture`'s embed write-side — `cs.capturer`
  is built with `capture.New` (no storage), so events go to the journal JSONL
  only; nothing populates storage. Kept as the substrate a future semantic
  `Reflect` would project into.
- `CortexSession.resolveEmbedder` — kept, reserved for a future semantic `Reflect`.

A separate cleanup may later remove the unused projection; out of scope here.

## Package structure

```
pkg/llm/            wire protocol: providers, chat/stream, usage          (exists)
internal/agent/     THE engine + seams + shared data types               (NEW, phase 3)
                    (Tool, ToolCall, Request, Response, Message; see engine-unification.md)
internal/outline/   structural primitive: Entry, Outline, Render          (NEW; replaces projectindex's three cmd/loop callers)
internal/memory/    notes store (unchanged — memory_* tools)              (exists)
internal/journal/   append-only journal (study reads it as a path)        (exists)
internal/tools/     Tool decls, Execute, pure tools (grep, outline-tool,   (was cmd/loop/tools;
                    read_file); role-interfaces (ToolDeps split into       MOVES in phase 3)
                    MemoryStore/Outliner/SubAgentRunner/…); ConfinePath
cmd/loop/           package main ONLY: REPL + CortexSession (the composition (exists, SHRINKS;
                    root — implements the tool role-interfaces, BUILDS the   no sub-packages
                    agent seams, OWNS the substrate), Turn, config, Study    after phase 3)
                    wiring  ·  cmd/loop/ui folds away in phase 3

dependency arrows (no cycles):
  cmd/loop ──► internal/tools ──► internal/agent ──► pkg/llm
  internal/tools ──► internal/agent   (for the moved data types: Tool, ToolCall,
                                       Request, Response, Message — phase 3)
  cmd/loop ──► internal/{tools,outline,memory,journal}
  internal/agent ──► pkg/llm only   (never internal/tools — the types move INTO agent)
  (invariant) no internal/* package imports internal/tools — only main + the
              inverted type arrow touch the tool surface
```

## Status — phase tracker

Built **after** [`engine-unification.md`](engine-unification.md) lands. Invariant
every phase: `go build ./...`, `go vet`, `./scripts/check.sh`, `go test ./...`
green. Flip ☐→☑ on land.

**Unattended boundary.** A phase's executable acceptance is `go build/vet` +
`./scripts/check.sh` + `go test ./...` + its `scripts/verify-study.sh` checks —
all **zero-network**, so the full build-out runs unattended. Goal-hit thresholds
are **pinned in code** (`StudyProbe.need()` / `pass()`; `verify-study.sh` asserts
the `passes != total` hard gate), not left to judgment. The **only** model-gated
steps are the live `loop study-eval` fleet numbers (phase 5) and a manual smoke
`study` — an ops sign-off **after** the code lands, **not** a blocker for marking a
phase ☑.

| # | Phase | Scope | Exit criteria | State |
|---|---|---|---|---|
| 1 | `internal/outline` | `Entry`/`Outline`/`Render`; breadth-first to budget; truncate deep-only (names always listed); go/ast + regex + wholeFile listers | unit tests: budget breadth-first, child-name retention, tree collapse | ☑ |
| 2 | `grep` tool | pure-Go `grepFiles` (walk + `projectscan` ignore + RE2 + caps + ctx-checks); confinement; `GrepTool` decl | unit tests: cap, binary-skip, ignore-set, bad-regex error, escape rejected | ☑ |
| 3 | targeted + confined `read_file` | `maxReadLines`; `TargetedRead`; `ConfinePath` (allows `.cortex/journal`, blocks escapes) | unit tests: floor, refuse, clamp, abs/`..` rejected, journal allowed | ☑ |
| 4 | `Study` profile + wiring | `Subagent`/`SubAgentRunner`, `dispatcherFor`, `RunSubagent`, `Study` tool entry, empty-digest guard; **delete** `runNavigator`/`navMap`/`navTools`/`navMaxDepth`/`Navigate`/`project_index` tool **and `projectindex/`**, migrating `memory_tools_test.go` off `projectindex.Build` onto `internal/outline`; **retarget the eval driver** — `study_eval.go` already holds the discriminating `StudyProbe` set + `pass()` scorer + per-probe timeout (the old `navEvalCases` were replaced 2026-06-28); when `runNavigator` is deleted here, point `runStudyEvalNav` at `RunSubagent` (phase 5 finishes the scorer wiring). Keep `countGoalHits`/`StudyProbe` | `loop study` works on the new path; old nav code + `projectindex` gone (incl. the test caller); `study_eval.go` calls `RunSubagent`, not `runNavigator`; suite green | ☑ |
| 5 | eval + telemetry | retarget the driver from `runNavigator` to `RunSubagent` (the discriminating `StudyProbe` set, `pass()` scorer, and per-probe timeout already exist in `study_eval.go`); **re-point the two frozen probes whose targets engine phase 3 MOVED** — `Path: "cmd/loop/tools/tools.go"` → `"internal/tools/tools.go"`, and re-scope the multi-hop probe (`Path: "cmd/loop"`, gold `{parseXMLToolCalls, Execute}`) so `Execute` stays reachable now that it lives in `internal/tools` (widen its study root or split the hop). Goals + gold facts stay frozen — only the moved path strings change; this is fixing a moved path, NOT weakening; **wire the full mechanical scorer from the engine's usage + budget accounting — the real discriminator, not optional: `StopReason`/`Iterations`/`FinalizeForced`, per-tool `Outlines`/`Greps`/`Reads`/`ToolErrs`, `ReadBytes`/`Bounded`, and `InputTokens`/`OutputTokens`/`PeakOutputTokens`/`MaxTokensClamped`**; derived goal-hit-per-1k-output + grep-vs-read ratio; model × probe table with `k/n` pass-rate + p50/p95 latency; rename `CORTEX_NAV_REPS`→`CORTEX_STUDY_REPS`; always-on run stats → journal | `loop study-eval` reports goal-hit **AND** the full mechanical block per probe (stop-reason, per-tool counts, tokens incl. peak, bounded); accept when goal-hit ≥ T, `StopReason == clean-finalize`, all `Bounded`, no `MaxTokensClamped`, across the fleet. **Goal-hit alone is insufficient** — flight check 2026-06-28: the old navigator passed the needle/bounded probes by brute-reading (4 reads / ~800 lines / 0 greps on a single-region goal); ø only gains teeth when the stop-reason + per-tool + bounded scorers fire | ☑ |
| 6 | docs / CLAUDE.md | point `study` at this design (`study-navigator.md` already removed 2026-06-27); update tool list (no project_index, no memory_search change) | docs match shipped code | ☑ |

`internal/outline`, `grep`, and targeted/confined `read_file` (phases 1–3) are
independent; any order before phase 4 wires them into the profile.

## Verification

`scripts/verify-study.sh` is the executable form of this contract (sections 1, 3,
4, 5, 6 cover this doc). It FAILs pre-implementation and flips to PASS as phases
land; `--diff-base <ref>` adds the LOC-band + scoped-diff checks. Baseline today:
21,655 src / 14,431 test LOC (re-snapshotted 2026-06-27 after the cognition-stack
*and* `internal/measure` deletions — `pkg/cognition`, `internal/cognition`,
`internal/study`, `internal/measure` removed, ~−20k src / ~−12k test from the
pre-deletion 41,468 / 26,304).

### Expected physical deltas (bands, not point targets)

| Phase | Add (src) | Delete (src) | Key fact to verify |
|---|---|---|---|
| 1 `internal/outline` | +350…450 | 0 | new pkg; no `cmd/loop` import |
| 2 `grep` | +120…160 | 0 | ctx-aware, confined; reuses `projectscan` ignore set |
| 3 targeted+confined `read_file` | +60…90 | 0 | one `maxReadLines`; `ConfinePath` allows `.cortex/journal` |
| 4 `Study` profile + **deletions** | +100…130 | **−1150…−1300** | delete `navigator.go` (~283) + `projectindex/` (~895) + `project_index` tool; migrate the test caller |
| 5 eval + telemetry | +200…300 | 0 | `study_eval.go` ~210→~440 (the 2026-06-28 probe set + `pass()` + per-probe timeout are already in; this adds the mechanical scorer: stop-reason, per-tool, token incl. peak, derived ratios, rep p50/p95, journal telemetry) |

**Whole-effort headline (both docs):** net source **≈ −500 … +300** — the unified
engine + `grep` + confined/bounded study land for zero-to-negative net lines
because they're paid for by deleting `navigator.go` (~283), `projectindex/`
(~895), recursion, and the duplicated loop. **+2000 = scope creep or duplication
not removed; −1800 = deletions ran but new code missing.** Net test **≈ +200 …
+600** (tests going *down* is a red flag).

### Assertions (machine-checkable)

- **Deletions → 0 refs:** `navigator.go`, `projectindex/`, `runNavigator`,
  `nav*` symbols, `Navigate`, `project_index` tool all gone (script §1).
- **Existence:** `internal/outline` (`Outline`/`Render`/`Entry`), `grepFiles` +
  `GrepTool`, `maxReadLines`, `ConfinePath`, `TargetedRead`, `Subagent`,
  `RunSubagent`, `Study` (script §3, §4).
- **Tool surface:** `Study.Tools == {outline, grep, read_file}` (no `study`
  recursion, no `bash`); **no `search` tool**; `memory_*` unchanged (script §4).
- **Import graph:** `internal/outline` imports no `cmd/loop` (script §5).
- **Confinement test:** `read_file`/`grep` reject abs/`..` escapes but **allow
  `.cortex/journal`** (unit test, phase 3).
- **Eval gate:** `loop study-eval` reports goal-hit ≥ T with all probes
  completed & bounded across the fleet (phase 5).

## Decisions log

_Append-only._

- **2026-06-27** Engine unification decoupled into
  [`engine-unification.md`](engine-unification.md) and built first.
- **2026-06-27** **No `search` tool.** Recall = `memory_*` notes (durable facts) +
  `study(.cortex/journal)` (what we did). The journal is a path you study/grep; no
  embeddings, no storage projection, no empty-corpus problem. `memory_search`
  unchanged.
- **2026-06-27** **Stay pull-only**; no proactive journal analysis now. A future
  `Reflect` subagent profile (reads journal → writes notes on the same engine) is
  recorded but not built. The dormant cognition/storage stack is left
  compiling-but-unused and documented, not deleted.
- **2026-06-27** **All study paths are workspace-confined by one door-guard check**
  — `ConfinePath` in the dispatcher vets the path arg of every path-taking tool
  (`outline`/`grep`/`read_file`); the tools don't re-confine (reject
  abs/`..`/escape; *allow* `.cortex/journal`). Distinct from `remove_path`'s
  delete-protection of `.cortex`.
- **2026-06-27** **`Bounds.MaxWall` (hard wall-clock cancel) rejected / deferred.**
  It would guillotine a nearly-done study mid-call with no digest and leaves no
  live context to run finalize on. Time is bounded by the `Sender`'s per-request
  deadline × `MaxIter` instead. Revisit a *soft* between-iterations wall only on
  measured need. See engine doc.
- **2026-06-27** `outline` is a clean `internal/outline`, not a `projectindex`
  wrapper. Truncation rule: always list every direct child name; truncate deep
  expansion only. mtime cache deferred to measured need.
- **2026-06-27** `read_file` floor collapses to one `maxReadLines` (~200, lines):
  ≤max no-range → whole; over → refuse→outline; range → clamp, visible header.
- **2026-06-27** Study activity shown via the engine `Progress` sink; empty
  finalize returns an explicit "no digest," never "".
- **2026-06-27** `grep` is pure-Go RE2, ctx-aware, confined; compile errors go
  back as observations + the description names the dialect. **`codesearch`
  rejected for now** (index staleness for an editing agent; monorepo-only payoff;
  hybrid > raw if ever needed) — deferred behind the same tool.
- **2026-06-27** Eval extends `study_eval.go`; ship keyword goal-hit + mechanical
  instrumentation + always-on telemetry first; panel-gold + vendored fixture
  deferred.
- **2026-06-27** **Correction: the eval is a driver-replacement, not an
  extend-in-place.** The navigator concept is gone, so its driver
  (`navEvalCases`/`CORTEX_NAV_REPS`/`runStudyEvalNav`) is deleted in **phase 4**
  alongside `Navigate` (required to keep phase 4 "suite green"). The scorer
  `countGoalHits` is implementation-agnostic and survives; phase 5 builds the new
  `RunSubagent` driver on top of it and renames `CORTEX_NAV_REPS`→`CORTEX_STUDY_REPS`.
- **2026-06-28** **ø probe set hardened + flight-tested; goal-hit alone proven
  insufficient.** Replaced the 3 weak keyword cases with 6 discriminating probes
  (needle/bounded/grounding/recall/multi-hop/smoke) against cortex's own tree +
  journal (`study_eval.go`), each leaning on a capability the old navigator lacks.
  Live flight check on `north`: the navigator passed needle/bounded/grounding by
  **brute-reading** (e.g. bounded: 4 reads / ~800 lines for a single-region goal),
  and **thrashed ~15 min on journal-recall** (no JSONL search). Conclusions: (1)
  goal-hit alone under-discriminates — the `ReadBytes`/`Reads`/`Bounded` scorer is
  the real discriminator and is now a HARD phase-5 exit criterion, not optional;
  (2) the eval needs a per-probe wall-clock timeout (added,
  `CORTEX_STUDY_PROBE_TIMEOUT`, default 120s) so a thrash fails fast instead of
  hanging the gate; (3) journal-recall is the one probe that discriminates on
  goal-hit today (old path can't search the journal). `verify-study.sh` now
  asserts the bounded/reads scorer exists, so ø cannot be declared green on
  keywords alone.
- **2026-06-28** **`StudyEvalResult` extended to a full mechanical scorer** (§5),
  on the same "goal-hit alone is fooled by brute-reading" logic that hardened the
  probe set. Added, all free from the engine's usage + budget accounting:
  **termination** (`StopReason`/`Iterations`/`FinalizeForced` — replaces the
  `Completed` bool with *which bound is binding*); **per-tool counts**
  (`Outlines`/`Greps`/`Reads`/`ToolErrs` — the grep-vs-read ratio measures
  locate-then-read directly; 0 greps + N reads is the brute-read failure even at
  goal-hit 1.0); **tokens** (`InputTokens`/`OutputTokens` cumulative, plus
  `PeakOutputTokens`/`MaxTokensClamped` as the eval-time form of the north runaway
  tripwire); **derived** goal-hit-per-1k-output (the small-model efficiency
  frontier) + rep `k/n` pass-rate + p50/p95 (the north thrash was a tail event a
  mean would launder). Acceptance tightened: goal-hit ≥ T **and**
  `StopReason == clean-finalize` **and** all `Bounded` **and** no `MaxTokensClamped`.
  Same shape is the always-on journal telemetry record — one shape, emitted every
  run, read by the eval (no eval-only instrumentation). Engine-side peak-output /
  clamp metrics mirrored into engine-unification.md's runaway tripwire.
- **2026-06-28** **ø is green-at-the-END, not green-at-the-start; two probes get
  re-pointed at phase 5.** Re-running the 6-probe set on the OLD navigator (north)
  scores **5/6**: the `tools.go` smoke-floor probe over-reads to a ~102s,
  empty-digest fail — the exact brute-read failure the new grep-based `Study`
  fixes. So ø is NOT exit-0 today and is not required to be. Through engine phases
  0–3 + study 1–3 (navigator untouched) ø is a **regression detector — the 5/6
  baseline must hold, not exit 0**; it becomes the real hard gate only at phase 5,
  where the new `Study` drives it to 6/6 exit 0. Separately, engine phase 3
  renames `cmd/loop/tools` → `internal/tools`, which MOVES two frozen-probe
  targets (`tools.go` itself, and `Execute` out of the `cmd/loop` multi-hop
  scope). Phase 5 must **re-point those moved paths** (path strings only; goals +
  gold facts stay frozen) — re-pointing a moved target is not "weakening a probe".
  refactor-goal-prompt.md + eval-design-example.md updated to match.
- **2026-06-28** **Every `Subagent` profile sets a mandatory `Bounds.MaxTokens`**
  (`Study` and `Reflect` = 12K), per the engine's "no unbounded request" invariant
  ([`engine-unification.md`](engine-unification.md)). `RunSubagent` threads
  `sa.Bounds.MaxTokens` into `requestFor`; the profile literals carry it explicitly.
  A profile with `MaxTokens` unset would silently reintroduce the 2026-06-28 north
  runaway — so it is part of the `Bounds` "safety net" list, not optional.
```
