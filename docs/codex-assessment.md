# Codex Assessment

**Date:** 2026-05-19

This is a candid assessment of Cortex as a coding harness, especially against the user's actual goal: relying on small to medium models to accomplish work that normally requires frontier models. Cortex should not be evaluated primarily as "another Claude Code." Its stronger thesis is that frontier-model capability can emerge from a structured small/medium-model system: decomposition, narrow tools, verification, memory, and patient retries.

## Short assessment

Cortex is already pointed in a promising direction. It has a real harness spine: a tool-calling loop, read/write/shell tools, snapshots, verifier retry, transcripts, DAG traces, Cortex search, and a serious memory/evaluation substrate. The unusual strength is not the terminal UI or agent polish. It is the combination of memory, background cognition, and eval discipline.

The project currently feels more like a cognitive harness and evaluation lab than a finished coding product. That is not bad, but it matters. Compared with Claude Code, pi.dev, and OpenCode, the research/memory/eval layer is more developed than the daily driver coding-agent layer.

The opportunity is to stop trying to be a feature-for-feature Claude Code clone and instead build the harness that makes weaker models useful through structure.

## Comparison to mainstream coding agents

Claude Code, pi.dev, and OpenCode are primarily polished coding harnesses. Their center of gravity is the user-facing agent loop:

- Receive a coding request.
- Gather context.
- Edit files.
- Run commands.
- Verify results.
- Maintain session continuity.
- Expose permissions, extension points, and ergonomic steering.

Cortex has pieces of this, especially in `cortex code` and the REPL, but its center of gravity is different:

- Capture context from sessions.
- Reflect on durable decisions.
- Retrieve and inject relevant context.
- Run background Think/Dream loops.
- Measure whether memory improves behavior.
- Evaluate small-model performance across scenarios.

That distinction is important. Cortex's natural wedge is not "the most polished terminal agent." It is:

> A coding harness where memory, evals, and cross-session learning are native, not bolted on.

## What Cortex already has

Cortex already contains the bones of a real coding harness:

- A model/tool loop with turn, token, and cost limits.
- A registry of agent tools.
- Workdir-contained file reads and writes.
- Shell execution with timeout and output caps.
- Session transcripts and structured telemetry.
- REPL snapshots and `/undo`.
- Basic verifier retry behavior.
- Cortex search as an agent tool.
- DAG trace infrastructure for observability.
- A serious eval harness and ABR metric.

This is enough to test the core thesis. It is not yet enough to feel like a highly reliable daily driver.

## Missing product-grade harness pieces

The largest missing area is not more cognition. It is product-grade control of the coding loop.

### Permissions as a first-class UX

`run_shell` currently defaults to full `bash -c` with inherited environment and an optional substring policy. That is powerful, but it is not a polished permission model.

Cortex needs explicit modes:

- Plan/read-only.
- Auto-accept safe reads.
- Auto-accept edits.
- Ask before shell commands.
- Ask before network or destructive commands.
- YOLO/local-sandbox mode.

Small models especially need rails. A permission system is not only about safety; it also narrows the action space so the model has fewer ways to wander.

### Code intelligence

File search and grep-style tools are not enough if the goal is to make smaller models act like stronger ones. Smaller models need sharper tools:

- Symbol search.
- Jump to definition.
- Find references.
- LSP diagnostics after edits.
- Type errors and warnings as structured feedback.
- Possibly AST summaries for large files.

This is one of OpenCode's real advantages. LSP support gives the model compressed, high-signal context that would otherwise require long file reads and better reasoning.

### Context compaction for the active harness

Cortex has durable memory, but the live coding loop still needs active-session context management:

- Summarize old tool traces.
- Preserve decisions and current task state.
- Drop bulky command output.
- Keep failed attempts and verifier output in compact form.
- Continue long sessions without context thrash.

Memory is not a substitute for compaction. They solve adjacent problems.

### Patch/edit ergonomics

Full-file `write_file` is acceptable for small examples, but serious coding work needs more precise editing:

- Apply unified patches.
- Replace specific ranges.
- Preview diffs.
- Reject malformed patches.
- Recover cleanly from conflicts.
- Track exactly what the agent changed.

Small models benefit from patch tools because patches constrain the edit. They reduce the chance of accidental unrelated rewrites.

### Interactive steering

The REPL has a good start: snapshots, `/undo`, `/model`, verifier retry, session logs. The missing layer is live steering:

- Interrupt the agent while it is running.
- See tool calls as a readable stream.
- Approve or reject edits.
- Approve or reject shell commands.
- Switch between plan and build modes.
- Ask the model to shrink or split the task.

This is part of why Claude Code feels useful even when it makes mistakes: the user remains inside the loop.

### Extensibility and packaging

Cortex has docs around tool surface and MCP, but it does not yet have a compelling package ecosystem story.

Comparable systems expose some combination of:

- MCP servers and clients.
- Skills or reusable workflows.
- Hooks.
- Prompt templates.
- Custom commands.
- Plugins/packages.
- SDK, RPC, or JSON event stream modes.

Cortex does not need all of these at once. It does need one clean extension story that fits the small-model harness thesis.

### Multi-agent isolation

Cortex has DAG language and background cognition, but not yet the everyday multi-worker affordance:

- Spawn a narrow worker.
- Give it a restricted tool set.
- Give it isolated context.
- Optionally run it in a separate worktree.
- Return only a compact summary or patch.

This matters because small models should not all share one giant context. They should work in small, isolated rooms.

## The actual goal

The user's goal is better and more specific than building a generic coding agent:

> Build a personal harness where small to medium models can do work that normally requires frontier models. It may take longer, but together, with enough structure, they may achieve it.

This is a plausible thesis. But the harness must not ask small models to behave like frontier models. It must deny them the opportunity to be vague.

The key idea:

> Frontier-model capability may emerge from a small/medium-model system, not from a single small model pretending to be Sonnet or Opus.

## Architecture for small/medium-model success

The harness should turn each coding task into a sequence of small, verifiable moves.

```
user request
  -> task splitter
  -> context scout
  -> checkpointed plan
  -> small edit loop
       -> inspect
       -> patch
       -> verify
       -> diagnose failure
       -> retry or shrink task
  -> critic/reviewer
  -> capture durable lessons
```

The important property is not that the system has multiple models. It is that each model call has a narrow job, a small context, a clear output contract, and an external verifier.

## Core principles

### Decompose every task

Small models fail when the task requires holding too many moving parts. The harness should force decomposition:

- Understand the request.
- Identify relevant files.
- Propose one small next step.
- Edit only the necessary surface.
- Verify.
- Continue.

For larger tasks, the harness should maintain an explicit checklist and make each item independently verifiable.

### Use narrow specialist roles

Multi-agent work should be practical, not theatrical. Useful roles include:

- Planner: turns the user request into small milestones.
- Codebase scout: finds relevant files and symbols.
- Editor: makes one constrained patch.
- Test runner: runs verifier commands and extracts failures.
- Bug critic: inspects the patch and failure output.
- Summarizer: compacts the current state.
- Memory curator: captures durable lessons and project facts.

Each role should get a short prompt, a restricted tool set, and bounded output.

### Make verification the authority

Small models can be mediocre reasoners but useful search/edit engines if the verifier is always closing the loop.

The harness should treat these as ground truth when available:

- Tests.
- Typecheckers.
- Linters.
- Build output.
- Runtime smoke checks.
- Golden files.
- User-provided acceptance criteria.

The model's explanation is secondary. Passing verification is primary.

### Prefer tools over context

Small models should not be handed giant files and asked to infer everything. They need sharper retrieval:

- Exact file snippets.
- Symbol-level facts.
- Search results with line references.
- LSP diagnostics.
- Structured test failures.
- Prior decisions from Cortex memory.

The goal is to give the model the minimum context that makes the next action obvious.

### Shrink after failure

The killer feature should be automatic task shrinkage.

When a model fails, Cortex should not merely retry the same task with the same prompt. It should reduce scope:

- From "implement feature" to "find relevant files."
- From "fix tests" to "explain the first failing assertion."
- From "rewrite module" to "patch this function."
- From "make all tests pass" to "make this one test pass."

Retries should become smaller, more concrete, and more tool-assisted.

### Use frontier models as calibration, not dependency

Frontier models can still be valuable, but they should not sit on the normal critical path. Use them occasionally to:

- Judge failed small-model traces.
- Improve prompts.
- Generate better decomposition strategies.
- Validate eval scoring.
- Teach the memory extractor what durable insights look like.

The day-to-day harness should run on small/medium models by default.

### Optimize for cumulative learning

Cortex's memory system matters more for small models than for frontier models. Strong models can often reconstruct missing context; weaker models need it served cleanly.

The memory layer should prioritize:

- Project conventions.
- Architectural decisions.
- Known commands.
- Test strategy.
- Failed attempts.
- Preferred patterns.
- "Use X, not Y" corrections.
- File and module maps.
- Recurring bug causes.

The right memory at the right time can turn a small model from confused to adequate.

## Strategic recommendation

Do not chase Claude Code or OpenCode feature-for-feature. Build the harness around this narrower promise:

> Cortex makes small and medium models reliable by decomposing work, constraining tools, verifying every step, and carrying project memory forward.

The next highest-leverage work:

1. Add explicit permission modes.
2. Add patch/range-edit tooling.
3. Add LSP diagnostics and symbol tools.
4. Add active-session compaction.
5. Add automatic task shrinkage after failures.
6. Add isolated specialist workers.
7. Keep ABR and evals honest, especially against small-model baselines.

If those pieces become solid, Cortex's cognitive layer becomes a real differentiator instead of an impressive subsystem looking for a product around it.

## Bottom line

The instinct is sound. The win probably will not come from several small models "thinking harder." It will come from a harness that makes the work impossible to keep vague: small context, exact tools, hard verification, structured memory, and many patient loops.

