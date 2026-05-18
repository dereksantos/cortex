# ADR-004: Prompt template format and versioning for DAG ops

**Status:** Accepted (Stage 2)
**Date:** 2026-05-18
**Context:** `docs/dag-build-plan.md` Stage 2 — registry expansion to
~9 LLM-backed micro-ops; ADR placeholder listed in the build plan's
"ADRs likely to land here" table

## Decision

Every LLM-backed DAG op stores its prompt as a versioned `.tmpl` file
under `pkg/cognition/prompts/`, loaded into the binary via
`embed.FS`, parsed at first use by `pkg/cognition/dag/ops/template.go`.

### File layout

```
pkg/cognition/prompts/
  <function>_<op>.tmpl      ← one per op
  doc.go                    ← embed.FS declaration
```

Filename convention: `<function>_<op>.tmpl` (snake_case). The loader
cross-checks the filename against the `op:` field in frontmatter so
the two cannot drift.

### Template format

```
---
version: 1
op: maintain.extract_insight
description: extract 1-2 durable insights from content, or none
max_output_tokens: 100
vars: [content, source]
---
<Go text/template body referencing {{.content}}, {{.source}}, ...>
```

Frontmatter is YAML between two `---` lines (must be on their own
lines). Five required fields:

| Field | Purpose |
|---|---|
| `version` | Monotonic int. Bump when prompt body changes meaningfully. |
| `op` | Qualified node name `<function>.<op>`. Must match filename. |
| `description` | One-line summary; surfaces in `tools.json` and registry. |
| `max_output_tokens` | Per-op output budget. ≤ 100 (Stage-2 invariant). |
| `vars` | Names of template variables. Loader validates the substitution map at render time. |

Body is a Go `text/template` parsed with `Option("missingkey=error")`
so a typo'd variable name fails loud at render time, not silently
emits `<no value>`.

### The 100-token invariant

`max_output_tokens` ≤ 100 is enforced at template load. This is the
**small-model amplifier thesis** made mechanical: if a prompt needs
more than ~100 tokens of output, the op's scope is too broad and
should be split. The loader rejects templates that exceed this cap.

Three knock-on consequences:

1. **No "summarize this codebase" ops.** Anything that needs
   paragraph-length output is the wrong granularity for the DAG
   layer — push it into `decide.coding_turn` (the one BIG-LLM node).
2. **Models smaller than Haiku stay viable.** Qwen-3B can produce a
   100-token JSON envelope reliably. It can't produce a 1000-token
   one.
3. **Cost calibration stays predictable.** Output token cost has a
   hard ceiling per op call; budget exhaustion analysis at the
   trace layer becomes a simple sum.

### Versioning

`version: 1` for first land; bump to 2 when the body changes in a way
that would invalidate cost calibration or alter the parsed-output
shape. The number is a coarse indicator; cell_results telemetry
tracks per-template stats so regressions are observable across
versions.

Old versions are removed (not kept) — the journal records the
template body inline with each op call (slice X2 will add this), so
historical replay doesn't depend on retaining old template files in
the repo.

### Cost calibration tied to version

The op's `dag.NodeSpec.Cost` hint is calibrated against `version: 1`'s
output on a known model (Anthropic Haiku 4.5). When a template's
version increments, the hint is re-measured from `cell_results.jsonl`
and updated in the same commit. Drift between hint and observed cost
shows up in the executor's pre-spawn budget check; persistent drift
is the recalibration trigger.

### Mechanical fallback per op

Every LLM-backed op MUST have a deterministic fallback path that runs
when:

- The budget's `LatencyMS` is below `fallbackBelowLatencyMS` (200).
- No provider is configured or available.
- The LLM call returns an error.
- The model output fails to parse.
- The template fails to load (build-error class; falls back rather
  than crashing so the executor can keep walking).

Fallback exposure: every op's `Out` map includes `"fallback": bool`
so eval pipelines can distinguish LLM-path runs from fallback-path
runs.

Fallback quality target: the mechanical path should produce
**non-zero useful output** — it does not need to match the LLM
path's quality, but it must not return empty when the input
genuinely contains signal. This makes the eval-principles 4
(Reproducible) and 5 (Isolated) achievable without a live model.

## Rationale

### Why embed templates instead of reading from disk

- **`cortex install`-ed binaries work without copying a prompts
  directory** to the install location. One executable, all prompts
  bundled.
- **Tests don't need a working-directory dance** — `embed.FS` is the
  same path in `go test` and in the daemon.
- **Vendoring is implicit** — pinning a Cortex version pins prompts
  too. No drift between binary and template.

### Why YAML frontmatter instead of separate metadata files

Co-locates the contract (output budget, var list) with the prompt
body. Reading one file gives a full picture; the loader's filename
cross-check catches the one drift class that matters (the op the
prompt actually targets).

### Why declare vars explicitly

Go's `text/template` with `missingkey=error` already catches typos.
The redundant declaration in frontmatter:

1. Surfaces the contract without parsing the body — a tooling-friendly
   index.
2. Lets the loader fail-fast if a caller forgets a var, with a clear
   error naming which one is missing.
3. Will be the input to a future "swap prompt for A/B test" path
   (slice X2 counterfactual replay).

### Why not Jinja / Handlebars / Mustache

Go's stdlib `text/template` is already in every binary. No new
dependency. The vocabulary needed for a prompt template (variable
substitution, no logic) is a strict subset of what `text/template`
provides. The escape hatch (custom funcs) exists but is unused.

## Consequences

- **Templates can't reference each other.** No `{{include}}`. The
  20–30 line cap per template is the right granularity anyway; if
  two ops share boilerplate, lift it to a code-level helper that
  emits the rendered string.
- **No multi-shot prompts.** Each `.tmpl` is one prompt. Few-shot
  examples go inline in the body (and count against the prompt's
  *input* token budget, which is bounded only loosely — Stage 2's
  invariant is on the *output* side).
- **No prompt swapping at runtime.** Templates are baked at build
  time. A/B testing prompts requires a binary rebuild. This is
  acceptable for Stage 2; if it becomes a bottleneck, the slice X2
  counterfactual replay infrastructure can override prompts via the
  attrs path.
- **Stage 3 inherits the convention.** When `decide.coding_turn`
  spawns `act.*` child nodes per ADR-001, those nodes' prompts (if
  any) follow the same format.

## Alternatives considered

**Inline prompt strings in handler source (rejected).** What the
codebase started with (see `pkg/llm/provider.go AnalysisSystemPrompt`
and `internal/cognition/dream.go DreamAnalysisPrompt`). Pros: no
loader, no embed, no file lookup. Cons: every prompt edit recompiles
unrelated handler code; no separation of contract (frontmatter) from
body; reviewing a prompt change requires reviewing a `.go` diff.

**JSON-schema-validated prompts (rejected for Stage 2).** Would catch
shape mismatches at load time. Too heavy for the small-model output
contracts in scope here — a JSON envelope with 3–4 fields and a
1-line `parse` helper is sufficient. Revisit if the output schemas
grow.

**Separate `.tmpl` body and `.yaml` metadata files (rejected).** Two
files per op, double the surface area for drift, and the loader needs
to correlate them by name anyway. The single-file frontmatter form
keeps the contract co-located with what it constrains.
