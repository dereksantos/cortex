# `cortex study DURATION` — Plan

## Context

`cortex bootstrap` works but exposes three knobs (`--window-lines`, `--budget`, `--batch`) that all depend on the model + the user's time tolerance. A user who just wants "spend ~5 minutes learning this project" has to compute those themselves and re-tune when they swap models.

`cortex study DURATION` is the friendlier verb: one knob (time), everything else auto-derived from the model's context window and observed per-call latency. It maps directly onto the `cortex <verb> "optional prompt"` future-state sketched in the `main.go` TODO comment — `study` slots in alongside `cortex journal "learn this project"` from the same comment. `cortex bootstrap` stays as the explicit-knob power-user form; both share the same controller engine.

Conceptual fit with the DAG seed+grow+decay model:
- **Seed**: boundary scan (deterministic, fixed cost).
- **Grow**: each iteration spawns N chunks via the hierarchical sampler.
- **Decay**: wall-clock budget counter — every LLM call decrements remaining seconds; halt when ≤0 or coverage cap hit.

The "thin-and-long vs thick-and-short branches" trade-off the user described falls out naturally from the joint optimization:
- Big-ctx model (e.g., Qwen3-Coder-30B at 262K) → chunks become whole files → thick + short.
- Small-ctx model (e.g., Ollama 7B at 32K) → chunks ~75 lines → thin + long.
- Same time budget across both → different coverage profiles, both principled.

The deliverable that lets the user "compare numbers": every study invocation tags its insights with a `run_id` so a follow-up query can A/B `study 1m` vs `study 5m` vs `study 30m`.

## User-locked decisions

1. **Hard cap on D**: when wall-clock hits D, halt immediately. Last in-flight LLM call is context-canceled; partial insight discarded. State persisted up to last completed chunk.
2. **Probe + cache** for model ctx-window + per-call latency, **reusing** existing calibrate infrastructure (`.cortex/db/op_cost_hints.json` + `pkg/cognition/dag/calibrate.go`) and `pkg/llm/openai_compat.go`'s `/v1/models` parser. No parallel new surface.
3. **Tag with run_id** — e.g., `study-20260521T1442Z-abc` — included in every insight's `Tags` slice plus the duration shorthand (`study-5m`). Comparison query: `jq 'select(.tags | contains(["study-5m"]))'` or by run_id directly.

## Approach

### Existing infrastructure to reuse (no parallel surface)

| Concern | Existing piece | Path |
|---|---|---|
| `/v1/models` probe + `max_context_window` parse | `(*OpenAICompatClient).ListModels` and the `CompatModel.ContextLength` field | `pkg/llm/openai_compat.go:326-373` |
| Context-class classification (Large/Medium/Small) | `llm.InferContextClass(modelID, ctxWindow)` | `pkg/llm/capabilities.go:183` |
| Per-op latency cost hints | `pkg/cognition/dag/calibrate.go` + `.cortex/db/op_cost_hints.json` | constant `DefaultCalibrationSnapshotPath` |
| Bootstrap controller (loop, persistence, pidlock, extract routing) | `internal/bootstrap/controller.go` | use as engine |
| Model id → provider | The provider-resolution refactor (`docs/provider-resolution-refactor.md`) — **prereq for study landing cleanly**, but study can ship before the refactor by calling `buildBootstrapProvider` directly | both |
| Duration parsing | `time.ParseDuration` (stdlib) — handles `30s`, `5m`, `1h`, `2h30m` | stdlib |

### New types / new files

**`internal/bootstrap/study.go`** — the duration→config planner. Pure function, no I/O:

```go
// StudyPlan is what the planner derives from a duration + model
// capability + observed latency.
type StudyPlan struct {
    Duration       time.Duration
    WindowLines    int           // derived from model ctx_window
    WindowOverlap  int           // proportional to WindowLines
    BatchSize      int           // 1 in v1 (sequential); knob for future parallelism
    MaxCalls       int           // duration / latency_p50
    TargetCoverage float64       // 0.80 default; capped so we don't overshoot
    RunID          string        // study-<ts>-<short>
    Shorthand      string        // study-5m
    Reasoning      string        // human-readable derivation trace, written to meta insight
}

// PlanStudy derives a StudyPlan given the requested duration, the
// model's context window (tokens), and the calibrated per-call
// latency (ms). All inputs come from cheap probes; no LLM calls.
func PlanStudy(d time.Duration, ctxWindowTokens int, latencyMS int, projectEffLOC int) StudyPlan
```

Derivation algorithm (documented inline):
```
prompt_overhead_tokens = 800  // extract_overview prompt + system
output_cap_tokens      = 100  // MaxOutputBudget from template loader
target_fill            = 0.5  // half the context window

usable_tokens   = ctxWindowTokens * target_fill - prompt_overhead - output_cap
usable_chars    = usable_tokens * 4              // rough chars/token
window_lines    = clamp(usable_chars / 50, 50, 4000)  // 50-char avg line; clamp keeps it sane
window_overlap  = window_lines / 10              // 10% overlap (matches default ratio)

max_calls       = (d.Seconds() * 1000 - startup_ms) / latencyMS
// startup_ms covers analyzer walk + meta insight emission, ~1500ms

target_coverage = min(0.80, project_eff_loc / (max_calls * window_lines))
// don't aim above 0.80 (controller default); cap by what max_calls can realistically reach
```

For Qwen3-Coder-30B (262144 ctx, ~5000ms/call) + Cortex (94K eff_loc):
- usable_tokens = 130172, usable_chars = ~520K, window_lines = 4000 (clamped)
- For 5m: max_calls ≈ (300000 - 1500) / 5000 = 59
- target_coverage = min(0.80, 94000 / (59 * 4000)) = min(0.80, 0.40) = 0.40

For Ollama qwen2.5-coder:7b (32768 ctx, ~2000ms/call):
- usable_tokens = 15484, usable_chars = ~62K, window_lines ≈ 1240
- For 5m: max_calls ≈ 149
- target_coverage = min(0.80, 94000 / (149 * 1240)) = min(0.80, 0.51) = 0.51

**`internal/bootstrap/probe.go`** — model capability probe (writes to op_cost_hints.json via the existing calibrate snapshot path):

```go
// ProbeModelCapability runs a one-shot calibration call against the
// provider, measures latency, and reads ctx_window from /v1/models
// (if available). Result is persisted to .cortex/db/op_cost_hints.json
// under a new "study.probe" key (or extends the existing schema).
type ModelProbe struct {
    ModelID         string
    CtxWindowTokens int    // from /v1/models or InferContextClass fallback
    LatencyMS       int    // observed
    ProbedAt        time.Time
    Source          string // "openai_compat_models" | "inferred" | "cached"
}

func Probe(ctx context.Context, provider llm.Provider, contextDir string, ttl time.Duration) (ModelProbe, error)
```

Behavior:
1. Try cached entry in `.cortex/db/op_cost_hints.json` keyed by `(model_id, endpoint)`. If present and `now - probed_at < ttl` (default 7 days), return cached.
2. Else: probe `/v1/models` if `provider` is an `OpenAICompatClient`; else `llm.InferContextClass(modelID, 0)` for the fallback table.
3. Run a calibration call: render a short extract_overview prompt against a tiny synthetic chunk, time the round-trip. Record `latency_ms`.
4. Write to op_cost_hints.json. Return `ModelProbe`.

Reuses `pkg/cognition/dag/calibrate.go`'s snapshot schema — adds a `study_probes` map at the top level (or extends an existing map) so we don't conflict.

### New CLI file: `cmd/cortex/commands/study.go`

```go
package commands

func init() { Register(&StudyCommand{}) }

type StudyCommand struct{}

func (*StudyCommand) Name() string        { return "study" }
func (*StudyCommand) Description() string { return "Spend N seconds/minutes/hours learning the project via the bootstrap DAG" }

func (*StudyCommand) DescribeFlags(fs *flag.FlagSet) {
    fs.String("model", "",       "Model id (provider-specific; default = cfg.OllamaModel)")
    fs.String("endpoint", "",    "OpenAI-compat base URL override (one-off; persistent routes go in .cortex/config.json model_routes)")
    fs.Bool("force", false,      "Re-run even if previously completed")
    fs.String("extract-op", "auto", "auto | extract_insight | extract_overview")
    fs.Bool("dry-run", false,    "Plan only; don't run LLM calls or write to journal")
    fs.Bool("verbose", false,    "Print the derivation trace + per-iteration banners")
    fs.Float64("fill", 0.5,      "Target fraction of context window the chunk should consume (advanced)")
}

func (*StudyCommand) Execute(ctx *Context) error {
    // Positional arg: DURATION (5m, 30s, 1h, 2h30m, ...)
    // Parse + validate.
    // Resolve provider (via the shared helper from the provider-resolution refactor,
    // OR via the existing buildBootstrapProvider until then).
    // Probe model capability.
    // Plan = PlanStudy(d, ctxTokens, latencyMS, projectEffLOC).
    // Print the derivation (always, since study is the friendly form).
    // If --dry-run: stop here.
    // Build bootstrap.ControllerConfig from the plan + global Cortex defaults.
    // Wrap the controller's Run in a context.WithDeadline(now+D) so the hard cap is OS-enforced.
    // Run.
    // Emit a final dream.insight tagged with the run_id summarizing what was learned.
}
```

Help text:
```
Usage: cortex study DURATION [flags]

  DURATION: a Go time.Duration string — e.g. 30s, 5m, 1h, 2h30m.

Spends DURATION of wall-clock budget extracting overview insights about
the project. Chunk size and chunk count are derived from your model's
context window and observed per-call latency. Hard cap: when DURATION
elapses, study halts immediately (in-flight call is context-canceled).

Each run is tagged with a unique run_id so multiple studies are
comparable. To compare understanding gained:

  cortex study 1m  --model X
  cortex study 5m  --model X --force
  cortex study 30m --model X --force
  cortex search "the question" --filter-tag=study-1m
  cortex search "the question" --filter-tag=study-5m
  cortex search "the question" --filter-tag=study-30m

Flags:
  --model M         Model id (default cfg.OllamaModel)
  --endpoint URL    OpenAI-compat base URL (one-off; prefer model_routes config)
  --force           Re-run even if previously completed
  --extract-op X    auto | extract_insight | extract_overview (default auto)
  --dry-run         Plan only — print the derivation, don't call the LLM
  --verbose         Print each iteration's banner
  --fill F          Target context-window fill fraction (default 0.5)
```

### CLI routing in `cmd/cortex/main.go`

Add `"study"` to the same switch arm as `bootstrap`:

```go
case "init", "install", "uninstall", "projects", "bootstrap", "study":
    if cmd := commands.Get(command); cmd != nil {
        runCommand(command, cmd, &commands.Context{Args: os.Args[2:]})
    }
```

### Run tagging — controller integration

The `BootstrapController` already accepts a `Config` with extension points; add one optional field `RunID string` that, when non-empty, is included in every dream.insight emitted during the run:

```go
// internal/bootstrap/types.go — Config additions
type Config struct {
    // ... existing fields ...
    RunID       string   // optional; when set, included in every dream.insight tag list
    RunShorthand string  // optional; e.g. "study-5m" — also added to tags
}
```

In `controller.emitDreamInsight`:
```go
tags := append([]string{"bootstrap"}, ins.Tags...)
if c.cfg.RunID != "" {
    tags = append(tags, c.cfg.RunID)
}
if c.cfg.RunShorthand != "" {
    tags = append(tags, c.cfg.RunShorthand)
}
sort.Strings(tags)
```

The meta insight gets the same tags + the derivation trace inlined into its `Content`.

### Hard-cap implementation

```go
deadline := time.Now().Add(plan.Duration)
ctx, cancel := context.WithDeadline(context.Background(), deadline)
defer cancel()

// Pass ctx into controller.Run. The controller's per-iteration check
// already does `if err := ctx.Err(); err != nil { halt }`, so deadline
// expiry naturally halts the loop at the next iteration boundary.
// For mid-LLM-call cancellation, the OpenAI-compat client honors ctx
// through its http.Request — verified at pkg/llm/openai_compat.go.
```

The existing controller's `state.Halted = "canceled"` case fires when ctx is canceled (controller.go:236 area). Study sets a more informative halted reason post-hoc:

```go
if controller.State().Halted == "canceled" {
    controller.State().Halted = "study_duration_elapsed"
    _ = bootstrap.SaveState(...)
}
```

### Meta insight (per study run)

Before the loop starts, emit a dream.insight that captures the plan:

```json
{
  "insight_id": "study:meta:<run_id>",
  "category": "pattern",
  "content": "Study started: duration=5m, model=Qwen3-Coder-30B, ctx=262144, latency=5000ms, window=4000, max_calls=59, target_coverage=40%, project_eff_loc=94247",
  "importance": 3,
  "tags": ["bootstrap", "meta", "study", "<run_id>", "study-5m"],
  "source_item_id": "study:meta:<run_id>",
  "source_name": "study"
}
```

After the loop ends (any halt reason), emit a closing dream.insight:

```json
{
  "insight_id": "study:done:<run_id>",
  "category": "pattern",
  "content": "Study halted: reason=study_duration_elapsed, elapsed=4m58s, iterations=58, insights=58, eff_loc_covered=39.4%, file_coverage=22%",
  "importance": 4,
  "tags": ["bootstrap", "meta", "study", "<run_id>", "study-5m", "halt:study_duration_elapsed"],
  "source_item_id": "study:done:<run_id>",
  "source_name": "study"
}
```

### Comparison helper (out of scope for v1, sketched for context)

`cortex study-compare RUN_ID_A RUN_ID_B [--query Q]` — runs the same query against the journal entries scoped to each run_id, prints the top-K results side by side. Useful for the "compare 1m vs 5m vs 30m" workflow.

For v1, the user can do this manually:
```bash
jq -c 'select(.payload.tags | index("study-1m")) | .payload.content' .cortex/journal/dream/*.jsonl > /tmp/study-1m.txt
jq -c 'select(.payload.tags | index("study-5m")) | .payload.content' .cortex/journal/dream/*.jsonl > /tmp/study-5m.txt
diff <(sort /tmp/study-1m.txt) <(sort /tmp/study-5m.txt)
```

The closing meta insight per run also makes `cortex search "study:done"` a one-liner for finding the run summary post-hoc.

## Critical files

**Touch sites (new + modified):**
- `internal/bootstrap/study.go` *(new)* — `StudyPlan`, `PlanStudy`
- `internal/bootstrap/study_test.go` *(new)* — derivation table tests
- `internal/bootstrap/probe.go` *(new)* — `ModelProbe`, `Probe()`, cached in op_cost_hints.json
- `internal/bootstrap/probe_test.go` *(new)* — probe + cache hit/miss tests
- `internal/bootstrap/types.go` *(modify)* — add `RunID`, `RunShorthand` to `Config`
- `internal/bootstrap/controller.go` *(modify)* — apply RunID/Shorthand to all emitted dream.insight tags; honor ctx.Done for hard-cap halt
- `cmd/cortex/commands/study.go` *(new)* — the subcommand entry point
- `cmd/cortex/main.go` *(modify)* — add `"study"` to the switch arm
- `tools.json` *(regenerate)* — picks up the new subcommand

**Read-only references (reused):**
- `pkg/llm/openai_compat.go:326-373` — `/v1/models` parser + `CompatModel.ContextLength`
- `pkg/llm/capabilities.go:183` — `InferContextClass`
- `pkg/cognition/dag/calibrate.go:63` — `DefaultCalibrationSnapshotPath`
- `pkg/cognition/dag/ops/maintain_extract_overview.go` — the extract op the study calls
- `internal/bootstrap/controller.go` — the loop engine
- `cmd/cortex/commands/bootstrap.go` — adapter functions (`wrapOverviewFn`, etc.) we'll lift or share

**Schema additions to `.cortex/db/op_cost_hints.json`:**

```jsonc
{
  // ... existing fields ...
  "study_probes": {
    "Qwen3-Coder-30B-A3B-Instruct-GGUF@http://localhost:13305/v1": {
      "model_id": "Qwen3-Coder-30B-A3B-Instruct-GGUF",
      "endpoint": "http://localhost:13305/v1",
      "ctx_window_tokens": 262144,
      "latency_ms": 4800,
      "probed_at": "2026-05-21T13:34:41Z",
      "source": "openai_compat_models"
    }
  }
}
```

If the existing snapshot loader rejects unknown top-level keys, add `study_probes` to its allowlist or extend the struct.

## Implementation sequence

Each step independently reviewable.

1. **Lift the bootstrap adapter functions** out of `cmd/cortex/commands/bootstrap.go` into a small `cmd/cortex/commands/bootstrap_adapters.go` (or `bootstrap_helpers.go`) so `study.go` can call them without duplication. No behavior change; pure refactor. Tests still pass.

2. **`internal/bootstrap/study.go` + tests.** Pure derivation function `PlanStudy(d, ctx, latency, projectEffLOC) StudyPlan`. Unit tests over the three example cases above (Qwen30B at 5m, Ollama 7B at 5m, tiny model at 30s).

3. **`internal/bootstrap/probe.go` + tests.** Probe + cache. Cache layer reuses `op_cost_hints.json` via a small extension to the existing snapshot reader/writer (or stores `study_probes` separately if extending the existing schema is invasive). Tests mock the OpenAI-compat client's `/v1/models` response.

4. **Controller modification.** Add `RunID` + `RunShorthand` fields to `Config`. Thread them into `emitDreamInsight` and `emitMetaInsight`. Existing controller tests pass unchanged; add one new test that asserts the tags survive into a journal entry.

5. **`cmd/cortex/commands/study.go`.** Wire the duration parse, probe, plan, controller call, run_id generation, hard-cap context.WithDeadline, and the closing meta-insight. Dry-run mode prints the plan + skips LLM calls.

6. **`cmd/cortex/main.go`.** Add `"study"` to the switch.

7. **Regenerate `tools.json`.** `go run ./cmd/cortex tools --out tools.json`.

8. **Dogfood + comparison evidence.** From the same fresh state on Cortex:
   - `./cortex study 1m --model Qwen3-Coder-30B-A3B-Instruct-GGUF --endpoint http://localhost:13305/v1`
   - `./cortex study 5m --force --model ... --endpoint ...`
   - `./cortex study 30m --force --model ... --endpoint ...`
   Capture: insight counts, eff_loc coverage, file coverage, and a representative jq-diff of insights about a focal area (e.g., `cortex search "decide.next"` filtered to each run_id). Land the comparison results in `docs/eval-journal.md` as a study-scaling entry.

## Verification

**Unit tests:**
- `internal/bootstrap/study_test.go` — derivation table covers: Qwen3-Coder-30B@262K, Ollama 7B@32K, Anthropic Haiku@200K, tiny mock@4K. For each, assert chunk_lines, max_calls, target_coverage land in the expected bands. Edge cases: D=0 → 0 calls; D very large → target_coverage capped at 0.80; ctx=0 → falls back to default 400.
- `internal/bootstrap/probe_test.go` — fresh probe writes cache; subsequent call within TTL reads cache; stale entry triggers re-probe; `/v1/models` 404 falls back to `InferContextClass`.
- `internal/bootstrap/controller_test.go` — extend `TestController_Run_HitsTarget` to assert `RunID` and `Shorthand` end up in the emitted entries' tags.

**Integration test:**
- `internal/bootstrap/study_integration_test.go` — fixture project, mock LLM, run study with `Duration=2s` and a mocked latency of `200ms`. Assert: ~10 chunks processed, meta insight has the run_id, closing insight has `halt:study_duration_elapsed`, ctx.Deadline-driven halt fires within +/-200ms of D.

**Manual dogfood:**
```bash
./cortex study 30s --model Qwen3-Coder-30B-A3B-Instruct-GGUF \
  --endpoint http://localhost:13305/v1 --verbose --force
./cortex study 2m --model Qwen3-Coder-30B-A3B-Instruct-GGUF \
  --endpoint http://localhost:13305/v1 --verbose --force
./cortex study 5m --model Qwen3-Coder-30B-A3B-Instruct-GGUF \
  --endpoint http://localhost:13305/v1 --verbose --force

# Run-id-scoped queries (current `cortex search` may need a --filter-tag
# flag to honor the run_id; document the gap or add the flag in step 5).
RUN_30S=$(jq -r 'select(.payload.tags | index("study-30s")) | .payload.tags | map(select(startswith("study-2026"))) | .[0]' \
  .cortex/journal/dream/0001.jsonl | head -1)
RUN_5M=$( jq -r 'select(.payload.tags | index("study-5m"))  | .payload.tags | map(select(startswith("study-2026"))) | .[0]' \
  .cortex/journal/dream/0001.jsonl | head -1)
echo "30s run: $RUN_30S"
echo "5m  run: $RUN_5M"

# Compare insight counts + coverage per run
for tag in study-30s study-2m study-5m; do
  echo "=== $tag ==="
  jq -r --arg t "$tag" 'select(.payload.tags | index($t)) | .payload.insight_id' \
    .cortex/journal/dream/0001.jsonl | wc -l
done
```

## Risks / gotchas

1. **Latency probe is itself an LLM call.** A 5s probe inside a 30s study cuts effective budget by ~16%. **Mitigation**: cache probes in op_cost_hints.json with 7-day TTL so the second study run within a week pays zero startup cost. For the first run, document the probe overhead in the planner's trace.

2. **Context-window probe lies on some endpoints.** Ollama's `/api/tags` doesn't report a context window; LM Studio reports max but not effective; Lemonade (chatterbox) reports accurately. **Mitigation**: `Probe()` falls through `OpenAICompatClient.ListModels` → `InferContextClass` → conservative 8K default. Document that misreported ctx windows lead to thicker-than-optimal chunks (the LLM call will still complete, just under-fills the window).

3. **Hard-cap mid-LLM-call cancellation depends on transport.** `OpenAICompatClient.GenerateWithStats` honors `ctx.Done()` through `http.Request`, but if a model loads slowly on Lemonade, the cancel may take seconds to propagate. **Mitigation**: hard cap is target+ε in practice. The closing meta insight records actual elapsed, not requested D.

4. **`window_lines` clamp is heuristic.** 4000 lines is the upper bound but most code files are < 1000 lines, so big-ctx models will mostly chunk by file size, not by the window cap. That's intended — but document that "thick branches" effectively means "whole file per call" for any reasonable codebase.

5. **Comparison only works if `cortex search` honors tag filters.** Current search is full-text + vector, may not have a `--filter-tag` knob. If it doesn't, either (a) extend search with `--filter-tag` in step 5, or (b) document the jq workaround as the v1 comparison path.

6. **Multiple parallel `cortex study` runs in the same project** would both grab the bootstrap pidlock. Second invocation logs + skips. That's correct behavior; document it so users don't think it's broken.

7. **Provider-resolution refactor as soft prereq.** Study replicates bootstrap's `--endpoint` band-aid for now. Once `docs/provider-resolution-refactor.md` lands, both bootstrap and study migrate to the shared `internal/llm.BuildProvider` helper — `study.go` becomes a one-line provider construction.

## Out of scope (deferred)

- **Parallelism**: real concurrent LLM calls within a batch. The controller is sequential today; adding goroutines is a separate change with its own concurrency-correctness audit. Study's `BatchSize` knob is reserved for that day.
- **`cortex study-compare`** subcommand: nice-to-have for the "1m vs 5m vs 30m" analysis. v1 uses jq + manual diff.
- **Probe of generation-only models for embedding-relevance**: study's probe is generation-shaped; embedding probes belong to `BuildEmbedder` per the provider-resolution refactor.
- **Adaptive replanning**: re-probe latency partway through a long study and adjust max_calls if the model speeds up or slows down. Not v1.
- **`--budget-tokens`**: a token-budget twin to `--duration`. Same machinery, different stop condition. Easy to add once duration version is solid.

## Open follow-ups (after study lands)

- **Auto-tune `target_fill`** per model class — Large-context models may want 0.7 (deeper synthesis), Small-context want 0.4 (room for the prompt envelope). Calibrate empirically.
- **Surface study results in `cortex status`** — last study run + coverage stats one-liner.
- **A/B comparison eval** — each focal-area query (auth, storage, eval pipeline, etc.) scored across study durations, recorded in `docs/eval-journal.md` quarterly.
