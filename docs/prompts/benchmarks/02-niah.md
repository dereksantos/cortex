Goal: Ship `internal/eval/benchmarks/niah/` — a synthetic needle-in-a-haystack
benchmark that validates Cortex's Reflex layer (capture → ingest → search)
finds a known fact buried at a configurable depth inside a haystack of
configurable size. Cheap, deterministic, runs in CI.

PREREQUISITE: 01-skeleton.md must be merged. This loop registers a benchmark
against the `internal/eval/benchmarks` registry that landed there.

WHY THIS MATTERS: Two reasons.

First, NIAH is the simplest possible "real" benchmark — no HuggingFace
fetcher, no LLM judge, no Docker. Building it second (after the skeleton)
proves the abstraction works end-to-end before the heavier loops
(LongMemEval, SWE-bench) commit to it. If the skeleton's `Benchmark`
interface has a wrong shape, you find out here, cheap.

Second, NIAH is a permanent regression smoke test for embedding + rerank
quality. Cortex's small-model-amplifier thesis depends on retrieval being
reliable; a needle going missing at 32K context is a red flag worth catching
in CI before it shows up as a 4-point LongMemEval regression three days
later.

INVESTIGATE FIRST:
- `internal/eval/benchmarks/{benchmark,registry,cache}.go` from the
  skeleton PR — your benchmark plugs into this. The `Benchmark` interface
  and `Env` shape are non-negotiable; if you find them awkward, file an
  issue against the skeleton, do not redefine them locally.
- `internal/capture/capture.go` — in-process capture API. NIAH should
  call this directly to hydrate the haystack, NOT shell out to
  `cortex capture` (faster, fewer moving parts in CI).
- `cmd/cortex/commands/ingest.go` — the path that drains journal →
  storage. NIAH calls the same internal function (look for what `cortex
  ingest` exec's into). Doing this in-process avoids race conditions and
  process startup cost per instance.
- `internal/storage/storage.go` — search API. Look for the function the
  Reflex layer ultimately calls; that's what NIAH measures.
- `internal/harness/tool_cortex_search.go` — uses `cognition.Fast` mode
  (Reflex → Resolve, no Reflect). NIAH should match that mode by default
  so the smoke matches what the agent harness actually hits at runtime.
  An optional `--cognition full` flag for diagnostic runs is fine.

DEFINITION OF DONE:
1. Package `internal/eval/benchmarks/niah/` with:
   - `generator.go` — `func Generate(opts GenerateOpts) Haystack`. Takes
     `Length` (tokens, target — actual filler is byte-approximated as
     `Length*4` chars), `Depth` (0.0..1.0 fraction), `Needle` (string),
     `Seed` (int64, deterministic filler). Returns a struct with the
     full haystack text and the byte offset where the needle landed (for
     debugging). Uses a deterministic filler (e.g. repeating phrases from
     a small built-in lorem corpus seeded by `Seed`) so re-running the
     same flags reproduces byte-identical haystacks.
   - `runner.go` — implements `benchmarks.Benchmark`. `Load()` returns
     one `Instance` per (length, depth) combination requested via flags.
     `Run()` per instance:
       a) Fresh `<workdir>/.cortex/`.
       b) Split haystack into "session" chunks (e.g. 512-token windows);
          capture each chunk via `internal/capture` with a synthetic
          `category="observation"` or similar — pick whichever existing
          category fits least awkwardly and document the choice.
       c) Ingest in-process.
       d) Query for a probe phrase derived from the needle (e.g. needle
          minus the secret value). Use Reflex via `cognition.Fast`.
       e) Substring match on the needle in the top-K results.
       f) Emit a `*evalv2.CellResult`:
          - `Benchmark="niah"`
          - `ScenarioID="niah/<length>-<depth>"` (e.g. `niah/16k-0.5`)
          - `Harness=HarnessCortex` (storage path, but harness label OK)
          - `ContextStrategy=StrategyCortex`
          - `TaskSuccessCriterion=CriterionScenarioAssertion`
          - `TestsPassed=1, TestsFailed=0` on hit; flipped on miss
          - `TaskSuccess=<hit>`
          - `Notes` records depth percent, length, top-1 score, retrieved
            position of needle (or `"missing"`).
   - `runner_test.go` — table-driven:
     * deterministic generator (same seed + opts → byte-identical haystack)
     * needle placement at depth 0.0, 0.5, 1.0 (start / middle / end)
     * scoring math (hit, miss, multiple needles edge case)
     * uses a mock provider so no LLM is called in tests
2. CLI integration:
   - In `cmd/cortex/commands/eval.go`, when `--benchmark niah`, parse:
     * `--length 8k|16k|32k|64k` (repeatable; one Instance per value)
     * `--depth 0.0..1.0` (repeatable; default `0.0,0.5,1.0`)
     * `--needle <string>` (default: `"The secret recipe code is 4F-9X-2B."`)
     * `--seed <int64>` (default: 1)
     * `--limit N` from the skeleton applies AFTER the cross-product.
3. Docs: `docs/benchmarks/niah.md` — one page: what the benchmark measures,
   what "passing" means (substring hit), how to interpret a miss (embedder
   quality vs storage bug vs depth-bias), reproduction commands.
4. End-to-end smoke (in the session, reported back):
   - `./cortex eval --benchmark niah --length 8k --depth 0.5 --limit 1 -v`
     succeeds and emits a CellResult.
   - `./cortex eval --benchmark niah --length 16k --depth 0.0,0.5,1.0` runs
     3 instances; report the pass rate per depth in the session report.
   - Confirm CellResults show up in `.cortex/db/cell_results.jsonl`,
     SQLite, and the journal — all three projections.

CONSTRAINTS:
- Standard library testing only. The deterministic-generator property is
  the most valuable test surface; spend the test budget there.
- No external dataset, no network calls. The generator is the source of
  truth.
- Default to `cognition.Fast` to match the agent harness's runtime mode.
  Don't add fanciness to "make NIAH pass" — if Fast misses needles, that's
  a real finding about Reflex quality, not a benchmark bug.
- Do NOT vary across LLM models in this benchmark. NIAH measures the
  retrieval substrate, not the LLM. The `--model` flag is irrelevant here;
  reject it cleanly if passed alongside `--benchmark niah`.
- No backwards-compat or feature-flagging. This is a new file under a new
  name.

DELIVERABLE: a branch `feat/bench-niah` off main (or stacked on
`feat/benchmarks-skeleton` if it hasn't merged yet), commits for the
package + CLI wiring + docs + tests, end-to-end smoke confirmed, plus a
short report:
- Pass rate per (length, depth) combination from the smoke run.
- Honest read: "needle survives X% at 16K depth=0.5; fails consistently at
  64K — embedder limit or storage chunking issue?"
- Any wart in the skeleton interface you wished were different (feed back
  to whoever owns the skeleton if it already merged).
