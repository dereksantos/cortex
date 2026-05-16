# Benchmark integrity principles

> The job of a benchmark is to report whether the shipped product is good
> enough — not to construct a variant of the product that is.

This document captures principles for adding new benchmarks to Cortex
(`internal/eval/benchmarks/*`). The principles were extracted from real
failure modes seen during the LongMemEval integration (PR #32) and are
intended as a checklist the next benchmark author can apply *before*
writing code.

Canonical principles: [`docs/prompts/eval-principles.md`](../prompts/eval-principles.md). Per-run lab notebook: [`docs/eval-journal.md`](../eval-journal.md).

## 1. The CLI is the system under test

The `./cortex` binary is the only Cortex a user has. A benchmark that
reaches past the CLI into `internal/` Go packages is measuring a
configuration nobody runs.

Concrete rule: every step a benchmark performs — hydration, retrieval,
formatting, scoring inputs — should be expressible as an invocation of
`./cortex <subcommand> ...`. If a step can't be expressed that way, the
CLI is missing a mode. **Fix the CLI, don't reach around it.**

Anti-pattern (what PR #32 did):

```go
h, err := evalv2.NewCortexHarness(model)   // in-process harness
h.SetSystemPrompt(longmemevalSystemPrompt) // bypasses default
h.RunSessionWithResult(ctx, question, workdir)
// model calls `cortex_search` (internal tool, different defaults
// than `cortex search` CLI)
```

Pattern (what to do):

```go
exec.Command("./cortex", "init").Run()
exec.Command("./cortex", "capture", "--bulk", "-").Stdin(haystack).Run()
exec.Command("./cortex", "ingest").Run()
ctx, _ := exec.Command("./cortex", "search", question, "--mode", "full").Output()
answer := judgeModel.AskWithContext(question, ctx, gold)
```

Slower? Yes. Honest? Also yes.

## 2. Don't customize the tool to fit the eval

Every per-benchmark tweak — a custom system prompt, a tighter retrieval
mode, a different tokenizer, a swapped-in embedder — launders the
score. The benchmark's job is to *expose* product gaps, not to *patch
around* them.

Examples that look helpful but corrupt the signal:

- Per-benchmark `SetSystemPrompt(...)` so the model "engages properly"
- Calling `cortex.Retrieve(..., Full)` when the CLI defaults to `Fast`
- Pre-injecting `cortex_search` results into the prompt because the
  model "should have known to call it"
- Adding a benchmark-only Reflect path because Fast mode doesn't rerank

If a benchmark needs any of those to produce a usable number, the
*finding* is the gap, not a number obtained by working around it. File
the gap. Block the benchmark. Wait until the CLI grows the right
default.

## 3. A bad number is a useful number

The most damaging instinct in eval work is to keep tuning until the
score looks defensible. A 0/10 against a published benchmark is a
*product result*: "the shipped CLI's defaults are wrong for paraphrase
recall." That sentence is actionable. It tells the product team what to
fix.

A 7/10 obtained by routing around the CLI's defaults is not
actionable. It tells you the *internal* package can do well under
hand-tuned settings, which any LLM-based retrieval system can. You
can't ship that number without lying.

When in doubt, prefer the embarrassing number that maps to a real
experience over the comfortable number that doesn't.

## 4. Naming divergence is a leak

Cortex currently has two retrieval surfaces:

- `cortex search` — the CLI command, with one set of defaults
- `cortex_search` — an in-process tool name registered with the agent
  loop in `internal/harness/tool_cortex_search.go`, with a *different*
  set of defaults (embedder=nil, Fast mode, no Reflect)

Same product, two configurations, one name with an underscore vs a
space. A user staring at a benchmark cannot tell which one is being
exercised. That's a foot-gun.

Two acceptable resolutions:

- Align the defaults — the in-process tool and the CLI invoke the same
  retrieval entry point with the same configuration
- Rename one explicitly — e.g. `cortex_search_harness` so the divergence
  is visible at the call site

The unacceptable resolution is leaving it ambiguous and writing more
benchmarks that exercise the harness version.

## 5. Hydration ≠ retrieval

Performance arguments about *hydration* do not justify in-processing
*retrieval and scoring*. They are different phases with different
budgets.

In PR #32 the brief said: *"Do NOT shell out to `cortex capture` per
turn; that's slow enough to make the Oracle split painful."* That's a
defensible point for hydration — 24 capture calls × per-process startup
adds up. But the response should be **"give `cortex capture` a bulk
mode"**, not **"skip the CLI for everything downstream too."**

When you see a speed argument creep across phase boundaries in a
benchmark design, push back. Profile. If the actual bottleneck is
hydration, fix hydration. Leave the rest on the public surface.

## 6. Briefs can encode the anti-pattern

The design doc that bootstrapped a benchmark is part of what needs
review. If a brief says "use the in-process harness directly," then
every benchmark that follows it inherits the same architectural
mistake. The longmemeval brief did exactly this; NIAH (already merged)
and the MTEB / SWE-bench briefs are at risk of the same trap.

Before writing benchmark code, audit the brief against the principles
above. If the brief permits CLI bypass, the brief needs to change
first.

## 7. Trust requires uniformity across benchmarks

A scorecard with multiple benchmarks is only comparable if every
benchmark exercises the same product. If LongMemEval secretly turns on
Reflect, NIAH secretly doesn't, and SWE-bench secretly uses a custom
system prompt, the columns don't mean the same thing — and the
aggregate doesn't mean anything at all.

The discipline that buys cross-benchmark trust is *uniform CLI usage*.
Every benchmark talks to the same `./cortex`, with the same defaults,
through the same surface. If one benchmark needs a different default,
that default becomes the new default (with a version bump and a
note) — it doesn't become a per-benchmark override.

## 8. What to do when the CLI is missing a needed feature

The right sequence is:

1. **Stop coding the benchmark.** Don't try to make it work with an
   internal workaround.
2. **File an issue** describing the missing CLI feature in user terms.
   "`cortex capture` needs a `--bulk` mode that reads NDJSON from stdin
   and skips the per-event startup cost." Not: "the benchmark needs to
   call `internal/capture.Capture.CaptureEvent` directly."
3. **Block the benchmark PR** on the CLI feature landing.
4. **Resume the benchmark** once the CLI feature is in main. Use it
   through the public surface.

This is slower. It is also the only way to build benchmarks whose
numbers are worth reporting.

## Appendix: PR #32 retrospective

The LongMemEval integration violated principles 1, 2, 4, and 5
simultaneously. The plumbing works end-to-end; the architecture is
wrong. Symptoms (all surfaced *during* the work, not after):

- Hydration writes via `internal/capture.Capture.CaptureEvent`, not
  `cortex capture` (principle 1, 5)
- Retrieval via `evalv2.CortexHarness.RunSessionWithResult`, not
  `cortex search` (principle 1)
- A per-benchmark `SetSystemPrompt(longmemevalSystemPrompt)` was added
  to "make the model engage" (principle 2)
- The `cortex_search` in-process tool uses `embedder=nil` and Fast mode
  by default, diverging from what `cortex search` does, and the
  benchmark exercises this divergent surface (principle 4)
- A speed argument about hydration was used to justify skipping the
  CLI for the whole eval loop (principle 5)

The post-fix smoke run scored 0/10 with a useful failure mode
characterization (retrieval surfaces assistant turns over user
evidence turns; lexical recall beats short specific facts). That
finding is *more* valuable than the score itself — it's the
actionable product input. It would have been visible just as clearly
through a CLI-only path, without the architectural drift.

The remediation is not to fix PR #32 in place. It's to:

1. Treat #32 as a working proof of the plumbing — don't merge as the
   benchmark, do mine it for the cache, parser, and judge pieces
2. Rewrite the brief (`docs/prompts/benchmarks/03-longmemeval.md`) to
   mandate CLI-only and reference this document
3. Audit NIAH (#31, already merged) against these principles and
   plan remediation
4. Apply the same audit to MTEB and SWE-bench briefs *before* their
   PRs land
