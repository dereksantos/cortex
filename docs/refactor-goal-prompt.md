> **Historical artifact (2026-07).** The goal prompt that drove the
> autonomous refactor session; the refactor shipped (gate record:
> [`refactor-status.md`](refactor-status.md)). Kept as a worked example of a
> harness-executable goal prompt.

Implement the engine-unification + study-subagent refactor end-to-end, in one autonomous session, until BOTH layers of the verification gate are fully green. The design docs are authoritative — follow them exactly; do not redesign.

AUTHORITATIVE DOCS (read all four before starting; re-read the relevant phase + the eval design before each phase):
- docs/engine-unification.md   — lands FIRST (phases 0→3). Behavior-preserving: collapse the two tool loops into one runLoop engine + Sender/AgentDispatcher seams.
- docs/study-subagent.md        — lands SECOND (phases 1→6) on the unified engine. Replaces the navigator with a bounded Study subagent over outline/grep/read_file.
- docs/eval-design-example.md   — THE VERIFICATION DESIGN. Two layers (Δ deterministic, ø agentic/live), one gate. This defines "done."
- CLAUDE.md                     — Go patterns; stdlib `testing` only (no testify), table-driven + t.Run, errors wrapped with %w.

EXECUTION ORDER (strict): engine-unification 0 → 1 → 2 → 3, THEN study-subagent 1 → 6. Study phases 1–3 (internal/outline, grep, targeted/confined read_file) are independent and may be done in any order before phase 4. Each doc's phase tracker is the checklist; its Decisions log resolves every choice — when in doubt, the Decisions log wins.

══════════════════════════════════════════════════════════════════════
THE GATE — two layers, both HARD. Completion ⟺ Δ green AND ø green.
══════════════════════════════════════════════════════════════════════
Per docs/eval-design-example.md, the gate is EXPECTED TO FAIL on every intermediate commit — it is a contract, not a smoke test. Parts come online as phases land. You earn completion ONLY when both layers are fully green at the same time. No partial credit, no "deferred to follow-up," no lowering thresholds.

Δ — DETERMINISTIC (the shape is correct; machine-decided, no model):
   `scripts/verify-study.sh` (all sections green; use --diff-base <branch-point> for the LOC band) + `go test ./...` green + the behavior-preservation characterization test (TestCoderLoopCharacterization) exists. Grows monotonically from ~7/45 toward all-green as phases land.

ø — AGENTIC (the behavior is correct; LIVE model, model-decided):
   `cortex study-eval` drives the real Study subagent over the frozen probes on the live fleet and self-decides via exit code. The END-STATE bar (phase 5 onward): ø is GREEN ⟺ `cortex study-eval` EXITS 0 = every probe passes at n=1 on the configured study model, all completed & bounded. THE MODEL IS AVAILABLE — backend + role bindings are already in ~/.cortex/config.json (study draws from the reasoner/study model, deliberating with thinking ON; do not touch config). Run it for real; do not stub, skip, or treat it as optional.

THE GATE RULE: GREEN-AT-THE-END is the bar that matters, not green-at-the-start. After each phase, run BOTH layers to track progress. Only when Δ all-green AND ø exit 0 simultaneously (at the end) is the work complete. Before the new Study exists, ø is not yet a pass/fail gate (see below) — do not block on it.

ø ACROSS THE PHASES (this is the tricky part — internalize it). ø does NOT start at exit 0 and does NOT need to: today the OLD navigator scores a 5/6 BASELINE on the 6-probe set (it over-reads the `tools.go` smoke-floor probe to a ~102s, empty-digest fail — the exact brute-read failure the new grep-based Study is built to fix). So:
- Engine phases 0–3 AND study phases 1–3: the navigator is untouched, so ø is a REGRESSION DETECTOR, not a pass/fail gate — the 5/6 baseline must HOLD (the same probes keep passing). A DROP below baseline means the refactor leaked behavior — stop and investigate. Do NOT require exit 0 here; the one failing smoke-floor probe is expected until the new Study lands.
- Study phase 4: ø goes ABSENT (the navigator + the old eval driver are deleted). This is EXPECTED, not a failure — Δ keeps advancing (deletions → 0 refs) while ø is temporarily unavailable.
- Study phase 5: rebuild the driver on RunSubagent; ø NOW becomes the real HARD gate and must reach exit 0 — every probe passes at n=1, all completed & bounded. This is reachable BECAUSE grep removes the over-read that fails probe 6 on the old navigator (the flight check hit 3/3 on the earlier probe set, confirming a working impl exits 0). If a probe fails or a run isn't completed/bounded, your Study implementation or prompt is not good enough yet — ITERATE. Do NOT lower T, weaken probes, or defer. (Re-pointing a probe's moved path — see PROBE-PATH RE-POINT below — is allowed; that is fixing a moved path, not weakening.)
- End state: Δ all-green AND ø exit 0 ⇒ done.

PROBE-PATH RE-POINT (sanctioned; do this at phase 5 — it is NOT "weakening probes"): engine phase 3 renames `cmd/cortex/tools` → `internal/tools`, which MOVES two frozen-probe targets out from under their current paths. When you retarget the driver onto RunSubagent, re-point the probes whose targets the refactor moved: studyProbes path `"cmd/cortex/tools/tools.go"` → `"internal/tools/tools.go"`; and re-scope the multi-hop probe (path `"cmd/cortex"`, gold `{parseXMLToolCalls, Execute}`) so `Execute` stays reachable now that it lives in `internal/tools` (widen its study root, or split the hop across the two packages). Goals and gold facts stay frozen — only the moved path strings change.

PER-PHASE LOOP (every phase, no exceptions):
1. Implement exactly that phase's tracker scope.
2. Keep the tree green: `go build ./...`, `go vet ./...`, `./scripts/check.sh`, `go test ./...` all pass. No phase leaves the tree broken.
3. Run the gate: `scripts/verify-study.sh` (confirm the checks that phase should flip are green) and, whenever ø is present (i.e. not the phase-4 absent window), `cortex study-eval` — before phase 5, confirm the 5/6 baseline HOLDS (regression check, not exit 0); at phase 5 and after, confirm exit 0.
4. Commit, message ending with:
   Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>

BUILD-FIRST (precede the code, in the first commits): the Δ contract (verify-study.sh already exists, red) and the characterization test (a SenderFunc-fake recording the coder loop's message/dispatch/stop sequence captured against today's Resolve) — it must be written BEFORE the Resolve→Turn fold and stay green through it. The ø scorer (countGoalHits/StudyProbe/pass) already exists and is unit-tested; phase 5 is wiring the driver onto RunSubagent, not new scoring logic.

KNOWN-RISKY STEP: engine phase 3 inverts one import arrow (internal/tools imports internal/agent; internal/agent imports only pkg/llm). Prove it with a throwaway spike before treating phase 3 as mechanical. Phase 3 is last and is the only file-move; do not start it until the seam is proven in package main (phases 0–2).

INVARIANTS (verify-study.sh enforces these):
- Every model request carries a finite max_tokens (Bounds.MaxTokens, threaded through requestFor; subsumes/DELETES applyOutputCap — never two cap sites). Every Subagent profile + the coder Turn set a nonzero MaxTokens.
- Deletions reach 0 refs: navigator.go, internal/projectindex/, runNavigator, nav* symbols, Navigate, project_index tool (study phase 4). Migrate memory_tools_test.go off projectindex onto internal/outline.
- Study.Tools == {outline, grep, read_file} (no recursion, no bash); no `search` tool; memory_* unchanged.
- internal/agent imports only pkg/llm; no internal/* package imports internal/tools; cmd/cortex has no sub-packages after phase 3.

GIT: create a dedicated branch `loop/engine-study-refactor` off the current HEAD; do all work there; commit after each phase. DO NOT git push and DO NOT open a PR (no push without explicit consent). Leave untracked files (e.g. ideas.md) alone.

DONE ⟺ Δ fully green AND ø green: `scripts/verify-study.sh` passes every section (run with --diff-base <branch-point> for the LOC band), `go test ./...` is green, and `cortex study-eval` EXITS 0 (every probe passing at n=1, all completed & bounded). Print a final summary: phases landed, verify-study.sh pass count, net source/test LOC delta, and the per-probe ø results (goal-hit, stop-reason, per-tool counts, latency).

IF GENUINELY BLOCKED (a doc instruction is impossible against the real code, or a contradiction the Decisions log doesn't resolve — NOT "a probe failed," which means iterate): stop, leave the tree green and committed at the last good phase, and write a concise blocker note (what, where, what you tried). The docs were reviewed to have no such blockers.
