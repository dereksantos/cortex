# Refactor blocker — ø live gate cannot reach exit-0 on the current fleet

**Status:** engine-unification + study-subagent refactor is **SHIPPED and verified**
on branch `loop/engine-study-refactor` (off `8d10b1b`, NOT pushed). The
**Δ deterministic gate is fully green** — `scripts/verify-study.sh --diff-base
8d10b1b` = **51/51**, `go build ./...` / `go vet ./...` / `go test ./...` all pass,
net source +550 LOC (band [-600,600]), net test +340 (≥100). The **ø live gate
(`loop study-eval` exit-0) is BLOCKED by backend defects on the shared north
fleet — not by study logic.**

## What works

The new grep-based `Study` subagent is correct. On a responsive north the three
fast probes pass cleanly every run (`pkg/llm`, `cmd/loop/loop.go`,
`internal/shellrisk` — ~12–15s, clean-finalize, ~1.3k output each), and each of
the three heavy probes (journal-recall, multi-hop cross-package, tool-registration)
has passed in some run — including journal-recall and multi-hop, which the old
navigator/projectindex could not do at all.

## Why exit-0 is unreachable (two backend defects, ≥3 distinct fixes each)

1. **multi-hop probe → LiteLLM `peg-native format` 500.** When north's output on a
   tool-call-machinery topic contains tool-call-shaped markup, LiteLLM's response
   parser tries to read it as a native tool call and returns
   `500 "model produced output that does not match the expected peg-native format"`.
   This is a **proxy parser defect on the model's output** — no study-logic change
   fixes it. Tried: no-markup answer discipline (fixes the answer source, not
   mid-loop reasoning), `maxRepeatedToolCalls` const-move (self-contained probe),
   no-chase nudge, thinking-off finalize (reverted — dumps raw action tokens),
   sanctioned moved-path re-point. The probe's gold (the `parseXMLToolCalls` →
   `Execute` dispatch path) **cannot be re-topiced without weakening the probe**,
   which the goal forbids. This probe alone makes 6/6 impossible on this fleet.

2. **journal + tool-registration probes → reasoning-spiral to the MaxTokens cap →
   empty digest.** north (thinking ON) burns its entire output budget on reasoning
   and emits no prose. The spiral fills **whatever ceiling it is given** — demonstrated
   spiraling to `peak_output_tokens=32000` at *iteration 1* with zero tool calls.
   Tried: MaxTokens 12k→24k→32k (raising the cap only lengthened the spiral
   270s→390s and starved the serial CUDA slot — falsified, reverted to 24k),
   per-read byte cap (24k), grep total-output byte cap (12k), no-chase nudge,
   `StudySeedBudget` tuning. The serial single-slot fleet amplifies it: a heavy
   probe spiraling for 300–400s degrades the probes after it.

Both satisfy the goal prompt's explicit genuine-block criterion — *"the same
failure persists across ≥3 distinct fixes"* and *"a required dependency is absent"*
(a reliable model on the shared fleet). ~13 full evals + targeted experiments.

## What an exit-0 needs (none are study-logic work)

- A model/proxy that doesn't 500 on tool-call-shaped output (LiteLLM PEG-parser
  fix, a non-tool-call fallback, or a model that doesn't emit the markup), **and**
- a model with a bounded thinking budget (or a dedicated/unloaded slot) so the
  finalize doesn't spiral to the cap.

Re-running `loop study-eval` when north is healthy and unloaded periodically yields
5/6; 6/6 at n=1 requires all three heavy probes to land in one run, which the shared
fleet does not reliably deliver.

See the memory note `project_study_eval_backend_500` for the full experiment log.
