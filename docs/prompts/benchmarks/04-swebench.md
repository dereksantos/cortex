Goal: Ship `internal/eval/benchmarks/swebench/` — fetches SWE-bench Verified
from HuggingFace, runs each instance through Cortex's in-process coding
harness (so Cortex IS the agent solving the bug), and scores the produced
patch by applying it inside a Docker container and running the canonical
`FAIL_TO_PASS` + `PASS_TO_PASS` test sets.

PREREQUISITE: 01-skeleton.md must be merged. This loop registers a benchmark
against the `internal/eval/benchmarks` registry and reuses the in-process
harness from PR #29.

INTERNAL PREP (first commit on the branch): generic-test refactor —
extract `internal/eval/v2/score_gol_frames.go`'s build+run+test contract
into `internal/eval/v2/score_repo_tests.go` (generic: build command, test
command, expected pass set, returns parsed results). Keep GoL frame-diff
as a thin specialization that calls into the generic scorer. This is a
small, mechanical refactor scoped to this PR's prep — the other three
benchmark loops do NOT need it. If the diff balloons (>200 LOC delta in
the existing GoL file) or feels riskier than expected, split into a
separate `refactor/score-repo-tests` PR landed first.

WHY THIS MATTERS: SWE-bench Verified (500 human-curated GitHub issues) is
the field's standard yardstick for coding-agent capability in 2026. With
the in-process harness now in main (PR #29), Cortex can compete on this
benchmark as an agent, not just as a context broker behind one. The
baseline-vs-cortex split (harness with cortex_search disabled vs enabled,
running on a Cortex store seeded with prior captures from the same repo)
is exactly the small-model-amplifier thesis stated in benchmark vocabulary
the rest of the field already understands. Even a 10-instance smoke
produces a number that's directly comparable across published agents.

INVESTIGATE FIRST:
- `internal/eval/benchmarks/{benchmark,registry,cache}.go` — substrate
  from 01-skeleton.
- `internal/eval/v2/score_gol_frames.go` — the file to refactor. The
  build+test loop here is what gets generalized.
- `internal/eval/v2/coding_runner.go` and `coding_scenario.go` — the
  pattern for per-attempt CellResult emission. SWE-bench is single-attempt
  (no retry loop) but the per-instance shape mirrors `RunCodingScenario`.
- `internal/eval/v2/library_service_cortex_harness.go` and
  `internal/harness/loop.go` — `evalv2.NewCortexHarness(model)` and
  `RunSessionWithResult(ctx, prompt, workdir)`. This is what produces the
  patch.
- `internal/harness/sandbox.go` and `internal/harness/tool_run_shell.go`
  — the shell allowlist for the in-process harness is restricted (go,
  gofmt, ls, cat, head, tail, wc, diff, grep). SWE-bench tasks are Python
  repos primarily. **Critical decision point:** either (a) extend the
  shell allowlist with `python`, `pip`, `pytest` for SWE-bench runs only,
  or (b) skip the in-loop test-execution affordance — agent writes the
  patch, scoring happens outside the loop in Docker. Recommendation: (b).
  The agent can read code, write code, and reason; it doesn't need to
  run Python to solve the issue. Document this choice in the per-package
  README.
- SWE-bench upstream:
  * HF dataset: `princeton-nlp/SWE-bench_Verified` (500 instances, human
    validated)
  * Parquet format; auto-converts to JSON via HF datasets-server.
  * Per-instance fields: `instance_id`, `repo`, `base_commit`,
    `problem_statement`, `patch` (gold; do not show to model), `test_patch`,
    `hints_text`, `created_at`, `version`, `environment_setup_commit`,
    `FAIL_TO_PASS` (JSON list of test names that must change failing→passing),
    `PASS_TO_PASS` (JSON list of tests that must stay passing).
  * Scoring harness: `swebench` Python package; published prebuilt Docker
    images (one per repo+version) at https://www.swebench.com.
  * License on the dataset card: not explicitly stated — **fetch at
    runtime only, do NOT vendor any sample into the repo**.

DEFINITION OF DONE:
0. (Prep commit, on this same branch unless split per the note above)
   `internal/eval/v2/score_repo_tests.go` — generic build+test contract:
   ```go
   type RepoTestSpec struct {
       BuildCmd       []string
       TestCmd        []string
       ExpectedPass   []string  // test names that must pass; len 0 → "all"
       Timeout        time.Duration
   }
   type RepoTestResult struct {
       BuildOK     bool
       BuildOut    string
       Passed      []string
       Failed      []string
       AllPassed   bool
   }
   func RunRepoTests(ctx, workdir string, spec RepoTestSpec) (RepoTestResult, error)
   ```
   `score_gol_frames.go` now calls `RunRepoTests` for its build+test step,
   then layers its frame-diff logic on top. Existing GoL tests still pass.
1. Package `internal/eval/benchmarks/swebench/` with:
   - `loader.go` — fetches Verified split via skeleton cache. HF URL
     pattern: use the datasets-server JSON endpoint (works without the
     parquet→arrow toolchain). Filter by `--subset verified` (only value
     accepted), `--repo <slug>` (optional, repeatable), `--limit N`.
     Each Instance carries the upstream fields needed for run + score.
   - `runner.go` — per instance:
     a) `<workdir>/repo/` — `git clone https://github.com/<repo>.git`,
        `git -C repo checkout <base_commit>`. If the repo is large
        (django, sympy), the clone is expensive; consider a one-time
        `--git-cache-dir` next to `~/.cortex/benchmarks/swebench/` and
        local `git clone --reference` semantics. Document the choice.
     b) Construct a programmatic coding-scenario-equivalent (you don't
        need to load a YAML file — directly call the harness):
        - `prompt = problem_statement` (verbatim from the dataset)
        - `workdir = <workdir>/repo`
     c) `h := evalv2.NewCortexHarness(model)`; result, err :=
        `h.RunSessionWithResult(ctx, prompt, workdir)`.
     d) Extract the patch: `git -C workdir/repo diff --no-color` (the
        in-loop edits ARE the patch). Persist to
        `<workdir>/cortex.patch`.
     e) Call `score.go`'s `RunSWEBenchTests(ctx, instance, patchPath)`
        which shells out to Docker.
     f) Emit `*evalv2.CellResult`:
        - `Benchmark="swebench"`
        - `ScenarioID="swebench/<instance_id>"`
        - `Harness=HarnessCortex`, `Provider=ProviderOpenRouter`,
          `Model=<flag>`, `ContextStrategy` per `--strategy`
        - `TaskSuccessCriterion=CriterionTestsPassAll`
        - `TestsPassed = len(F2P_passed) + len(P2P_passed)`
        - `TestsFailed = len(F2P_failed) + len(P2P_failed)`
        - `TaskSuccess = (all F2P pass) && (all P2P pass)`
        - `Notes` records F2P/P2P counts separately for analysis
   - `score.go` —
     ```go
     func RunSWEBenchTests(ctx, instance Instance, patchPath, dockerImage string) (Result, error)
     ```
     Shells out to Docker via the `docker` CLI (no Python in the build
     path). Mounts the patch into the prebuilt swebench image for that
     repo+version, runs `python -m pytest -v <F2P + P2P tests>`, parses
     the output, returns per-test results.
     * Hard-fail with a clean message if `docker` is not on PATH.
     * Honor a `--docker-image-prefix` flag (default:
       `swebench/sweb.eval.x86_64.`) so registry-mirror users can override.
     * Per-instance timeout: 30 min (configurable). Kill cleanly.
2. Baseline vs cortex split (same shape as 03-longmemeval.md):
   - `--strategy baseline,cortex`. `baseline`: harness built without
     `cortex_search` tool; empty `.cortex/`. `cortex`: harness built with
     `cortex_search`; store optionally pre-seeded if `--seed-from <dir>`
     points at a prior capture dump (Phase B; skip in this PR if
     scope-pressing — document the deferral).
   - Two CellResults per instance when both strategies selected.
3. CLI integration in `cmd/cortex/commands/eval.go`:
   - `--benchmark swebench`
   - `--subset verified` (required; only value accepted)
   - `--repo <slug>` (optional, repeatable)
   - `--limit N` (skeleton flag; defaults to 10 in this PR — full 500 is
     a CI nightly target later)
   - `--strategy baseline,cortex`
   - `--model <openrouter-id>` selects the model under test
   - `--docker-image-prefix <prefix>` (optional)
   - `--git-cache-dir <path>` (optional; documented in package README)
4. Docs: `docs/benchmarks/swebench.md` —
   - What the benchmark is, upstream URL, license caveat (not explicit on
     dataset card — fetch at runtime, never vendor)
   - The shell-allowlist decision (agent doesn't run tests in-loop;
     scoring is out-of-loop Docker)
   - Docker as a soft dependency; how to install + which images get pulled
   - Phase A scope (≤10 instances default), Phase B (full 500, Python
     shim for canonical scoring) deferred
   - Reproduction commands
5. End-to-end smoke (in-session report):
   - 3 instances minimum. Pick small, fast-cloning repos
     (`pylint-dev/astroid`, `psf/requests` if in Verified — check first;
     otherwise pick whatever's small from the actual dataset).
   - Run `--strategy baseline,cortex`. Report pass rate per strategy.
   - Confirm CellResults show in all three projections.

CONSTRAINTS:
- Standard library testing only for code that doesn't need the network.
  `loader.go` tests use a captured fixture from one instance. `score.go`
  tests use a mock Docker invocation that returns pre-recorded pytest
  output.
- Honor the spend ceiling. SWE-bench can burn $1+ per instance on
  high-end models; default to a small model + `--limit 10`. Hard-fail
  on projected over-budget.
- Do NOT vendor SWE-bench instances into the repo (license uncertainty).
- Do NOT extend the shell allowlist beyond Phase B unless explicitly
  required to make a model produce a working patch — and document the
  decision with the reasoning.
- Docker is a soft dependency. Print a one-line "install Docker:..."
  message and exit 1 if not present; do not try to install it or warn
  silently.
- Patch extraction is `git diff` on the workdir — do NOT trust the
  agent to "print the patch as final output." Models forget; the diff
  is authoritative.
- Generic-test refactor (step 0) MUST land green: `go test ./internal/eval/v2/...`
  passes including the existing GoL test suite. If the refactor breaks GoL
  scoring, fix the refactor, do not weaken the GoL test.

DELIVERABLE: a branch `feat/bench-swebench` off main (or stacked on
`feat/benchmarks-skeleton`), commits as:
1. `refactor(eval): extract score_repo_tests from score_gol_frames`
2. `feat(bench): add swebench package + loader`
3. `feat(bench): wire docker scoring`
4. `feat(cli): --benchmark swebench dispatch`
5. `docs(bench): swebench package docs`
6. (optional) `docs(bench): update README sequencing notes`

Plus a session report:
- Pass rate baseline vs cortex on the smoke set (be honest — first-run
  numbers may be ugly; that's data)
- Per-instance cost + wall time + Docker time breakdown
- Confirmation that the GoL test suite still passes (no regression
  from the refactor)
- Honest read: "On 3 instances, baseline N%, cortex M%; biggest failure
  mode was X." Plus what would be needed to scale beyond the smoke set
  (e.g. parallel Docker scoring, repo cache, larger model budget).
