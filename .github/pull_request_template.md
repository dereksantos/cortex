## Summary

What does this change and why?

## Test plan

- [ ] `go test ./...` passes
- [ ] `go vet ./...` clean
- [ ] `./scripts/check.sh` passes (gofmt, vet, golangci-lint)
- [ ] Manual verification (describe):

### Terminal render checklist (until tmux/Windows Terminal can be automated — #112)

The golden snapshots pin the shape of every render path (width, color, NO_COLOR,
CORTEX_LOOP_RENDER=0, non-TTY) but a real terminal can still surprise us. For any
change that touches a render path (tool lines, the diff, nested subagent lines, the
approval prompt, the status row, the `/context` grid, the spinner), confirm each of
these by eye in a real terminal:

**tmux (pinned prompt / anchored status row)**

- [ ] The status row ("thinking... Ns") stays pinned one row above the input and
  does not scroll the screen or leak a second copy as the model streams.
- [ ] An approval prompt (`run it? [y/N]`) renders on the status row, bright (not
  dimmed), and y/n/Enter dismiss it cleanly without a stranded line.
- [ ] A tool call's diff (and, for `agent`/`study`, the nested child lines) renders
  under its action line at the tmux width, without wrapping mid-word or doubling.

**Windows Terminal (no anchored prompt path)**

- [ ] The plain spinner line ("thinking... Ns") repaints in place and `Stop` clears
  it fully — no residue, no doubled frame on a non-TTY pipe (e.g. `cortex turn`).
- [ ] Tool lines and the diff render at the Windows Terminal's column width, clipping
  at the right edge rather than wrapping.
- [ ] `/context` grid: the 8×16 glyph map, its gutter ladder, the demote tick, and the
  legend render without box-drawing or icon glyphs (2026-07-19 plain-text decision),
  and the NO_COLOR form is the same map with the ANSI stripped.

## Notes

Anything reviewers should know — tradeoffs, follow-ups, related issues.
