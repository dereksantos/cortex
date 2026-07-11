# The `agent` tool — design note (M3)

GOAL.md §3 slice 3a. Settles the three decisions ROADMAP.md item 6.3 flags
before the `agent` profile (M4) is registered. Each decision below has one
paragraph of rationale and a `DECISION:` line the M4 implementation must
match.

## 1. Toolset scope

The `agent` profile exists to hand off *implementation* work, not just
research — that is what distinguishes it from `study` (read-only:
`outline`/`grep`/`read_file`). A subagent that can only read can report what
to change but can't change it, which makes it a slower `study` with extra
ceremony. The coder still owns orchestration (deciding *when* to delegate)
and final review; the `agent` profile owns *doing* one bounded unit of work
end to end — including `bash` for running the tests/build that verify its
own edits, per CLAUDE.md's testing discipline ("tests are the spec"). This
mirrors the existing `Study` shape (own system prompt, own allowlist,
mandatory `Bounds`) rather than granting blanket tool access: it is
`outline`, `grep`, `read_file`, `write_file`, `edit_file`, and `bash` — the
coder's file-and-shell surface minus everything §3 excludes below.

DECISION: the `agent` profile's toolset is `outline`, `grep`, `read_file`,
`write_file`, `edit_file`, `bash` — read/search plus write/edit and bash,
scoped by depth cap (§3 slice 2) rather than by withholding `bash` outright.

## 2. `shellrisk` Risky inside a subagent

`internal/shellrisk.Verdict` documents its own contract: Risky means "gate
per the caller's policy (prompt interactively, block when headless)". A
subagent has no interactive operator in its loop — `RunSubagent` drives it
against the shared `runLoop` engine with no human in the round-trip, the same
shape headless `cortex turn` runs in. Treating Risky as anything other than
Blocked inside a subagent would mean either silently running a
reversible-but-consequential command with no one to ask, or inventing a new
approval channel that doesn't exist on this seam. Headless-blocked is also
the ROADMAP.md default this doc was asked to confirm or override, and
nothing about the `agent` profile's use case (bounded, single-goal,
unsupervised) argues for opening that hole.

DECISION: `shellrisk` Risky verdicts inside any subagent (including `agent`)
are treated as Blocked — the same policy headless `cortex turn` already
applies, enforced by passing the subagent's headless-ness through the same
`ShellGate` path the coder's `bash` tool uses. Safe still runs; Blocked
(deny-floor) already never runs for anyone.

## 3. Excluded coder-only tools

`recall`, the memory tools (`memory_write`/`memory_read`/`memory_search`/
`memory_forget`), and the context tools (`context_evict`/`context_merge`/
`context_adjust_watermarks`) all operate on session-scoped state that only
exists for the coder's own turn loop: `recall` resolves *this session's*
outline citations, the memory tools curate *this session's* durable notes,
and the context tools mutate *this session's* demotion watermarks and
outline. A subagent is seeded fresh per invocation (goal + a structural
outline of its target, per `runSubagent` in `internal/tools/tools.go`) and
never sees the coder's conversation — there is no session state on the
subagent side for these tools to act on, and offering them would either
no-op confusingly or (worse) let a subagent reach across the seam into the
coder's session. `Study` already excludes all of these by construction (its
allowlist is `outline`/`grep`/`read_file` only); `agent` extends the
allowlist for write/edit/bash but inherits the same exclusion for anything
session-scoped.

DECISION: `agent` excludes `recall`, `memory_write`, `memory_read`,
`memory_search`, `memory_forget`, `context_evict`, `context_merge`, and
`context_adjust_watermarks` — same as `Study`. It also excludes `study` and
`agent` itself as callable tools beyond what its depth cap (1, per §3 slice
2) allows, so nesting is bounded by the shared depth-cap mechanism rather
than by toolset omission alone.

## 4. Model binding (addendum, 2026-07-11)

The first cut ran every subagent on the study role's binding — wrong default
for an *implementation* subagent: the work delegated to `agent` is the same
kind of work the coder itself does, so it should default to the same
capability tier, not the (often smaller) reading model. The runner therefore
builds the `agent` profile's request off the coder's live request — model,
endpoint, key, template kwargs — so it tracks `/model` switches; Study keeps
the study role binding. The tool also takes an optional `model` argument so
the coder can deliberately route one bounded task to a different model
(e.g. a cheaper one for mechanical edits) without a config change.

DECISION: `agent` defaults to the model the coder is currently running as
(inherited from the live request, follows `/model`); an optional per-call
`model` argument pins just the model name on that binding. Study is
unchanged on the study role.
