# REPL TUI Roadmap

> **Anchor.** This doc tracks the post-v1 work that turns the
> `--tui` mode (landed in commit `6d4300c`) into something
> genuinely pleasant to live in. v1 is a layout + sink-routing
> scaffold; everything below is what makes it feel like a real
> coding harness.
>
> **Status.** v1 shipped on branch `derek.s/repl-readiness`; this
> doc is open scope. Items move to "Done" with their commit hash
> as they land.

## Bedrock (immediate follow-ups from v1)

These came up the moment v1 met fingers. Land them before
manual-testing rounds (#5) so feedback lands on a fair surface.

- **Input anchored to the bottom.** `viewport.View()` doesn't pad
  to its full height when the transcript is short, so the
  input row floats up the terminal. Force fixed height via
  lipgloss so the cursor always sits on the bottom-most row.
- **Mouse wheel scroll.** `tea.WithMouseCellMotion()` is on but
  `tea.MouseMsg` isn't routed to the viewport. One Update branch.
- **`/verbose` mid-session toggle.** Currently `--verbose` at
  startup is sticky. `/verbose [on|off]` (and a no-arg form that
  flips) lets the operator dial event detail without restarting.
  Sink gains a `SetVerbose(bool)` method; StdoutSink + TUISink
  both implement.
- **DAG tracer routed through `Event`.** The `▪ <op> [nodeID] · ok
  · 24ms` lines flow as plain `Info` today, so the TUI can't
  color them by cortex function. Reshape `makeREPLDAGTracer` to
  call `ui.Event("dag.trace", payload{...})`; the StdoutSink keeps
  the existing one-line format; the TUI colors by
  `qualified_name`'s function prefix (sense=cyan, attend=yellow,
  decide=magenta, act=green, value=blue, model=white,
  maintain=grey).
- **Bootstrap progress in a dedicated row.** Today
  `[bootstrap] foo` lines land in the transcript. Move them to an
  "ambient" status row (above the regular status line) that
  appears only while bootstrap is active and disappears when it
  finishes. New kinded event: `bootstrap.progress`.

## Core ergonomics

- **Input history navigation.** Up / Down arrows recall prior
  prompts within the session. Persist a small history file
  (~/.cortex/history.txt) across sessions, capped at 200 entries.
- **Multi-line input.** Long prompts should support
  Ctrl+J / Alt+Enter to insert a newline; Enter still submits.
  Pasted multi-line text auto-detected (newlines in the paste
  buffer trigger multi-line mode for that submission).
- **Scrollback search.** `/search <term>` or Ctrl+R highlights
  matching lines and lets the user jump through hits.
- **Selection + copy.** Mouse drag to select; system-clipboard
  copy via `bubbles/clipboard` (the same dep we already pull in
  for textinput).
- **Slash-command autocomplete.** After `/` the input shows a
  pop-up of available commands with fuzzy match; Tab completes.
- **Inline help on slash commands.** `/help <cmd>` shows the
  per-command usage block instead of dumping the whole help.

## Visual polish

- **Per-cortex-function colors wired everywhere.** Not just DAG
  trace — also tool calls, attend.compress fallbacks, decide.next
  emitted plans. The visible-DAG story is what differentiates
  Cortex from Claude Code; the colors should make that the first
  thing a user notices.
- **Status line gains live metrics.** Current model + total turn
  spend (cumulative tokens, $) + last turn latency. Updates on
  every `coding.turn` event. Right-aligned counters; left-aligned
  identity.
- **Spinner during in-flight turns.** Small spinner next to the
  status line while the model is generating; clears on
  `coding.final`.
- **Configurable theme.** A `.cortex/theme.json` (or `theme:`
  block in config.json) lets the user override per-function
  colors, status-line accent, and divider style. Falls back to
  the built-in palette.
- **Truecolor + 256-color graceful degradation.** lipgloss handles
  this for the standard palette but custom themes should be
  written in hex and auto-degrade.

## Power features

- **`/diff` becomes a pop-up viewport.** Instead of dumping
  changed paths into the transcript, open a modal viewport with
  per-file unified diffs (uses `go-diff` or shells out to `git
  diff --no-color`).
- **`/undo` previews before applying.** Show the modal diff of
  what the undo will restore, then confirm/cancel.
- **`/models` becomes a pickable list.** Today /models prints a
  catalog; in TUI it should be a filterable list with Enter to
  pick.
- **Endpoint health indicator.** A dot next to the model name in
  the status line (green = revalidated OK, yellow = stale,
  red = last call failed). Click to re-validate.
- **Live trace pane (toggle).** `Ctrl+T` opens a right-hand pane
  showing the DAG tree of the current turn — node-by-node, with
  budget remaining per axis. Closes on `Ctrl+T` again or
  end-of-turn.
- **Cost ceiling warning.** When cumulative session cost crosses
  a threshold (configurable; default $1), flash a warning banner.
- **Session bookmarks.** `/bookmark <name>` saves the current
  workdir state + summary; `/restore <name>` rolls back. Lives
  alongside the existing snapshot stack but with names + notes.

## Cancellation + safety

- **Ctrl+C cancels the current turn.** Today it just clears the
  input. Should signal `ctx.Cancel()` on the running harness so
  the model stops mid-generation, tool calls finish their current
  iteration, and the turn is rolled back (or kept under
  `--keep-on-fail`).
- **Confirm before destructive shell.** When `run_shell` is about
  to execute a command matched by the destructive-op corpus
  (`rm -rf`, `git push --force`, `DROP TABLE`, etc.) and no
  policy explicitly allows it, surface a modal prompt for
  confirmation before the tool runs.
- **Per-turn "what changed" footer.** After every accepted turn,
  a one-line footer shows added/changed/removed file counts so
  the user notices unexpected writes immediately.

## Observability

- **DAG-trace pane (per turn).** See the live trace pane bullet
  above; this is the same feature, framed for the eval side.
- **Cost/latency sparkline.** A 20-cell sparkline in the status
  line trends per-turn cost (or latency, toggleable) across the
  last 20 turns. Useful for "are we getting worse on a long
  session" without leaving the REPL.
- **Last error explainer.** A `/why` slash command that re-runs
  the last failure through a small classifier (`value.detect_*`)
  and surfaces the likely cause: "model hit n_ctx" / "tool args
  malformed" / "verifier mismatch" / etc.

## Composition with project bootstrap (#3 deferred work)

- **Bootstrap is a first-class TUI surface.** Ambient row shows
  coverage % + insights extracted. Pause/resume via a
  `/bootstrap` slash command.
- **First-run wizard.** Detect a fresh `.cortex/` and walk the
  user through the model picker (which endpoints does my
  hardware support?), the system-prompt seed (project type), and
  the bootstrap kickoff in one screen.

## Long horizon (don't build yet)

- **Multi-pane / tabs.** Once the core flow is comfortable,
  consider a left-hand "history of sessions" pane that lets you
  resume past work (Phase 3 Slice 4 + Item 4 already put the
  summary substrate in place).
- **Inline file viewer.** Click a path in the transcript to open
  a side pane with the file contents.
- **Embedded terminal pane.** Run a shell inside the TUI for
  out-of-band commands without leaving the REPL.
- **Remote-driven mode.** TUI sink that connects to a remote
  Cortex daemon over a socket so a thin local frontend talks to
  a hosted backend.

## Done

| Item | Commit |
|---|---|
| v1 layout, persistent input, viewport scroll, status line, sink-routed Info/Warn/Error/Event/Banner, color-by-tool, --tui flag | `6d4300c` |

---

## Per-item scoping rules

- One PR / commit per item unless they're trivially small (mouse
  wheel + scroll keybindings: same commit). Each commit must
  pass the local gauntlet (`scripts/check.sh` + race tests).
- Tests for the Update handler are cheap — drive `Model.Update`
  with synthetic messages and assert on the resulting transcript.
  Land tests with each item.
- Don't ship new color palette additions without checking the
  lipgloss color-256 graceful-degradation path; truecolor-only
  themes regress on tmux without `set -ga terminal-overrides`.
- Mouse / keyboard chords land behind v1's existing keymap
  philosophy (Ctrl+letter for high-frequency actions, slash
  commands for everything else). Don't introduce two ways to do
  the same thing unless one of them is a documented power-user
  alternate.
