# REPL Polish — Scope

> **Status. Tracks 1–2 BUILT (2026-10-09, branch
> `feat/repl-polish-foundation`); tracks 3–5 PROPOSED.** Track 1 landed as:
> `internal/style` (roles, NO_COLOR, `Width`/`Clip`/`Justify`/`Wrap`,
> `ContentWidth`); literal-string render goldens in
> `internal/tools/render_golden_test.go` and
> `cmd/cortex/render_golden_test.go` (clock seam `tools.Now`); the startup
> header in `cmd/cortex/header.go`. Track 2 landed as: one line per tool call,
> printed on completion (`internal/tools/toolline.go`); read-only runs folded
> (`fold.go`, printed when the run ends — open question 1); answers indented
> under the gutter and wrapped by us, not glamour (`render.go`'s
> `wrapRendered`); the turn footer (`cmd/cortex/footer.go`); the cursor-first
> prompt row with the model + gauge on the right and a timestamped echo of the
> input left in scrollback (cost moved to the footer — open question 3); the
> live status row names a running call with the same short verb
> (`tools.ShortAction`). Visual mock of the proposed turn, startup, input,
> picker and confirm surfaces:
> <https://claude.ai/artifact/5a9HJ1ZEqhEc3yhvedRD4Q> (hand-written lines, not
> real output).

## Goal

Make the interactive REPL feel finished and easy to drive while staying
minimal. The gap today is rhythm and affordances, not a missing framework:
turns run together without spacing, read-only tool calls each take a row,
prose runs the full terminal width, and input has no completion.

## Constraints (decided; not reopened here)

- **Timestamps stay** (2026-10-09): every event line keeps its `HH:MM:SS`
  gutter. Polish works around the gutter, not by removing it.

- **Plain text** (2026-07-19, reconfirmed 2026-08-06): no icon set, no
  decorative sigils, no spinner frames, no box-drawing. Information-bearing
  character cells (the `/context` grid) are allowed. Polish comes from color,
  spacing and alignment.
- **Hybrid TUI** (2026-08-06): the REPL stays scrollback-native (copy, paste,
  piping intact). Whole-screen surfaces are on-demand alt-screen inspectors
  (`internal/lineedit/inspect.go`) that restore scrollback on exit. No
  full-screen app; `cortex serve` + the web UI is the pane-based surface.
- **Degradation paths stay byte-exact**: `NO_COLOR`, `CORTEX_LOOP_RENDER=0`,
  non-TTY stdout, and `deps.Quiet()`. Every proposed element must still read
  with color stripped.
- **Language-agnostic harness**: nothing here may add Go-specific text to the
  prompt or tool surface.

## Tracks

Ordered by dependency, then payoff.

### 1. Foundation

- **Semantic palette.** Color constants are spread across
  `internal/tools/ui.go` (`Green`, `Gray`, `Color`), `cmd/cortex/main.go`
  (`gray`, `withColor`) and `cmd/cortex/display.go` (`brightCyan`,
  `brightGreen`). Replace them with one set of role tokens (`dim`, `faint`,
  `accent`, `ok`, `warn`, `err`) in one place, with `NO_COLOR` resolved there.
- **Layout helper.** One width-aware place for clip, right-align and
  prose-wrap with a column cap. `clipRunes` (`internal/tools/nesting.go`) and
  `termWidth` are the starting points.
- **Render goldens.** Golden-file tests for a canned turn rendered at fixed
  widths (80, 120, no-TTY) and under `NO_COLOR`, so every visual change is a
  reviewable diff. Follows the pattern already in
  `cmd/cortex/serve_sse_golden_test.go`.
- **Startup header.** One dim line: version · project path · model. On
  resume, a second line: session id · turns · outlined vs live · note count.

### 2. Turn rhythm (largest visible change)

- **Turn as a block.** One blank line between turns and before the answer.
  The `HH:MM:SS` gutter (`gutterPrefix`, `cmd/cortex/display.go`) **stays on
  every event line** — user input, each tool line, the answer's first line
  (decision 2026-10-09). Continuation lines (wrapped prose, diff bodies,
  footer) pad under the gutter so the times read as one clean column.
- **Aligned tool column.** `edit`, `bash`, `read` verbs in one column, the
  target next, and a right-hand result (`+9 -2`, `ok 6.8s`). Drop the
  `tool: ` prefix: the indent and verb column already say it's a tool.
- **Fold read-only runs.** Consecutive `outline`/`grep`/`read_file`/
  `memory_read`/`recall` calls collapse into one dim summary line
  (`outline learn.go · grep 1 · read 2 files`). Writes, `bash`, subagents and
  failures never fold. The full list stays one keypress away (track 5).
  Scrollback is append-only, so the fold has to be decided when the line is
  printed. Two options: print the line when the run ends, or print it
  progressively on the anchored status row while the run is live.
- **Prose width cap.** Assistant markdown wraps at
  `min(termWidth, ~100)` columns. Code blocks are exempt.
- **Turn footer.** One dim line after the answer: elapsed · tool count ·
  files changed · gauge · cost. This replaces the status prefix on the prompt
  line as the per-turn summary.
- **Prompt bar.** The cursor comes first (`. > `); model + gauge move to the
  right edge of the anchored row. The state light (`.`/`*`/`~`) is
  unchanged.

### 3. Input

- **Slash completion.** Tab completes `/commands`. A dim inline preview shows
  the rest of the best match, and up to six candidates with one-line
  descriptions list under the prompt, then clear. The descriptions come from
  the existing `/help` table.
- **`@path` mentions.** Tab completes workspace paths after `@`, confined
  like `ConfinePath`. The mention is plain text in the input; whether it also
  attaches content is a separate decision (open question 2).
- **Paste chips.** A multi-line paste renders as one atomic token
  (`[pasted 42 lines]`) that backspace deletes whole. A key (proposed
  `ctrl-x e`) expands it into an editable view. This builds on the existing
  `renderSummary` (`internal/lineedit/render.go`).
- **Multi-line input** (last in this track). Alt-Enter inserts a newline.
  This is the only item that changes the anchor's geometry. The live anchor
  documents "at most two rows" with a fixed wrap-free erase
  (`internal/lineedit/live.go`), so it lands only after the goldens exist.

### 4. Inspectors (on the existing alt-screen harness)

Each view is a `Title`/`Lines(width)` implementation called through
`Terminal.Inspect`, the same way `/context` (`cmd/cortex/context_view.go`)
works. Views that pick something need a selection + enter contract added to
the harness.

- `/sessions` — filterable list; enter resumes.
- `/model` — role bindings + what the backend serves (data from
  `cortex model`); enter switches.
- Turn detail — the last turn's unfolded tool list, full tool output, full
  diffs.
- `/memory` — browse notes and skills read-only. Edits stay model-driven
  through the memory tools.

### 5. Affordances

- **`ctrl-o`** opens the turn-detail inspector for the most recent turn
  (works on the idle prompt and while a turn is anchored, following the
  `Anchor.Confirm` "serve it from the loop that owns the terminal" rule).
- **Risky-command confirm** names the command, a one-line consequence, and
  its keys: `y run once · n skip · e edit command`. No "always" option, since
  shellrisk judgements are per-command against `turnIntent`.
- **`?` on an empty prompt** prints a single line of key hints. This is
  separate from `/help`.

## Order

1. Track 1 (palette, layout helper, goldens) — no visible change by itself.
2. Track 2 — tool column, turn block, footer, prose cap, then fold.
3. Track 3 — slash completion, paste chips, `@path`.
4. Track 4 — `/sessions` and `/model` pickers (selection contract), then
   turn detail + `ctrl-o`.
5. Track 3 — multi-line input.
6. Remaining track 4/5 items.

Each step is a separately reviewable change; tracks 2–5 each land with
golden updates.

## Acceptance

- Goldens at 80/120/no-TTY/`NO_COLOR` for each touched surface.
- `CORTEX_LOOP_RENDER=0` and non-TTY output unchanged byte-for-byte, or
  changed only by an explicit golden diff that the change's review approves.
- Inspector exit restores scrollback byte-for-byte (existing
  `inspect_test.go` contract extended to each new view).
- Discord and `cortex serve` renderers unaffected (they consume the turn
  seam, not stdout).

## Open questions

1. ~~Fold timing~~ — decided 2026-10-09: the folded line prints when the run
   ends; while it runs, the live status row names the current call.
2. Does `@path` attach file content to the turn, or just name the path for
   the model to read with its own tools? The second keeps context
   model-driven, consistent with `docs/memory-tools.md`.
3. ~~Footer vs prompt-bar status~~ — decided 2026-10-09: the prompt row
   keeps model + gauge; version moved to the startup header, cost to the
   footer.
