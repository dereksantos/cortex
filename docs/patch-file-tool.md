# `act.patch_file` — surgical line-range writes

> **Status:** spec, not implemented. Successor to the chunker /
> read-pagination fixes in PR for `derek.s/chunker-and-handoff`. The
> incident motivating this doc is recorded in repl.go's Iter-8
> `defaultREPLSystemPrompt` history note.

## Why

`write_file(path, content)` is full-file-replace only — there is no
patch / diff / edit-by-range tool on the surface. So a model that wants
to make a "small readability improvement" to a large file has no way
to express "change these lines"; its only option is to emit the whole
file.

Three constraints collide on big files:

1. `write_file` requires full content.
2. `maxWriteFileBytes` = 128 KiB cap. Real source files exceed this
   (the failure-case `repl.go` is ~225 KiB).
3. The coder only ever sees paginated chunks (Piece 2 in the same PR),
   so it never holds the whole file in context.

The observed failure: an exploration prompt ("explain the core files
in this project") prompted Qwen3-Coder-30B to write a 138-line
fabricated stub over a 4986-line file. The model imported packages
that don't exist in this project — classic "I can't fit the original,
so I'll hallucinate something coherent" failure.

**Read paginates IN; write should paginate OUT.** That's the symmetry
this doc proposes.

## What

A new act-typed tool:

```
act.patch_file(path, start_line, end_line, content) → {path, bytes_before, bytes_after}
```

Semantics:

- `start_line` / `end_line` are 1-indexed inclusive, matching
  `read_file`'s line-range params (Piece 2) verbatim — so the model
  can pipeline `read_file(path, start_line=N, end_line=M)` →
  `patch_file(path, start_line=N, end_line=M, content=...)` without
  doing line-math.
- `content` replaces the inclusive line range. The replacement may
  have a different number of lines than the original range (insertions
  and deletions both expressible).
- `start_line=0, end_line=0` → append `content` after the final line.
  Lets the model add a new function / import without round-tripping
  the whole file.
- `start_line=N, end_line=N-1` (i.e. an "empty range" at line N) →
  insert `content` BEFORE line N without replacing anything. Symmetric
  with append; lets the model insert at the head or middle.
- `end_line > EOF` clips silently at EOF, same as `read_file`. Makes
  the read→patch pipeline robust against off-by-one in the model's
  arithmetic.
- Returns `{bytes_before, bytes_after}` so the operator (and the
  model's next-turn prompt) can see the size delta — a 1-line edit
  that flips bytes_before=200000 → bytes_after=500 is a strong signal
  the model misjudged the range.

## What about `apply_patch(path, unified_diff)`?

Closer to what coder-tuned models are trained on (PR diffs), but:

- Unified-diff format has 30+ years of edge cases (context lines,
  hunk headers, --- / +++ paths, "No newline at end of file"). Every
  one is a place for a small model to emit malformed output that
  silently corrupts the file.
- Diffs require the model to remember surrounding context lines to
  anchor the patch — but Cortex's chunker shows the model labeled
  chunks, not raw context. A line-range tool matches what the model
  already sees.
- Telemetry is simpler. `start_line=920, end_line=970, lines_added=3,
  lines_removed=51` is a clear row in `dag_traces.jsonl`. A diff
  blob would need parsing to surface the same signal.

Tradeoff acknowledged: this gives up the LLM's diff-training prior.
If empirical use shows the line-range tool misfires on bigger edits,
`apply_patch` can be added as a second surface alongside — they are
not mutually exclusive.

## Axis-5 contract

`patch_file` is a mutator. Per the existing pattern (`write_file`):
`Mutator: true, RequiresConfirmation: false`. The user's `cortex` invocation
is the consent surface; per-call confirm would block every edit and is
already not enforced for `write_file`.

## Safety bounds

- Same byte cap as `write_file` (128 KiB) on the `content` arg. The
  size delta protection is per-call, not per-file — a model writing
  10× 50 KiB patches in a session is still bounded by the agent
  loop's no_progress detector and the user's review of each turn.
- Same path-containment + symlink rejection as `read_file` /
  `write_file`. Reuse `containPath` / lstat checks verbatim.
- Atomic replacement via temp-file + rename (mirroring `write_file`).
  A crash mid-patch never leaves a half-written file visible to a
  subsequent `read_file`.
- **Reject inverted ranges** (`end_line < start_line` unless it's the
  exact `N, N-1` insert form) with an error, not a silent no-op. A
  silent no-op would let the model "edit" a file and see success
  while nothing changed — the worst possible failure mode.

## Chunker integration

The chunker's truncation marker (Piece 2) already emits concrete
`start_line=N, end_line=M`. After `patch_file` ships, the marker can
optionally also emit a hint at the bottom of every chunked read:

```
[chunk 5/35, lines 2001-2500]
... content ...
[to edit, call patch_file(path, start_line=N, end_line=M, content=...)]
```

Decide later — the bare line-range advice may be enough for the LLM to
infer the symmetry without an explicit hint, especially after seeing
the `patch_file` spec in its tool catalog.

## Tool catalog

`decide.tool_call`'s `formatActToolsCatalog` lists `act.patch_file` to
the xLAM specialist alongside `act.write_file`. When both are
applicable, the specialist picks: full-file replacement when the
prompt says "rewrite X" / "scaffold a new Y"; surgical patch when the
prompt says "change line N" / "rename Foo to Bar in file Z" or when
the file size from prior `read_file` exceeds the write cap.

## Tests

Mirror the `read_file` line-range test set (`tool_read_file_test.go`):

- `TestPatchFile_ReplacesRange` — replace lines 2-4 of a 5-line file
- `TestPatchFile_AppendWhenZeroRange` — `(0, 0)` appends content at EOF
- `TestPatchFile_InsertBeforeLine` — `(N, N-1)` inserts without
  replacing
- `TestPatchFile_EndPastEOF_ClipsSilently` — `(2, 100)` on a 3-line
  file replaces lines 2-3 only
- `TestPatchFile_RejectsInvertedRange` — `(10, 5)` errors loudly
- `TestPatchFile_SpecAdvertisesParams` — tool spec carries
  `start_line`, `end_line`, `content` in JSON-schema params
- `TestPatchFile_AtomicOnCrash` — temp file is cleaned up; original
  file unchanged when the rename step is interrupted (hard test, may
  skip if non-trivial to fault-inject)
- `TestPatchFile_RejectsSymlink` — same surface as write_file
- `TestPatchFile_RejectsAbovWriteCap` — `len(content) > 128 KiB` errors

Plus one integration test that wires `act.patch_file` into the act
registry alongside `act.write_file` and confirms `decide.tool_call`'s
xLAM specialist can select either based on the prompt.

## Out of scope (for the follow-up that ships this)

- Editing binary files. Reject non-UTF-8 input.
- Multi-file patches in one call.
- Tree-sitter-aware "rename symbol everywhere" — that's a different
  tool (`act.rename_symbol`?) operating on a different surface.
- Three-way merge / conflict resolution. The harness is single-writer
  per turn; concurrent agents are a separate problem.

## Open questions

1. Does `end_line=0` (open-ended) mean "through EOF" (read_file
   semantics) or "no end_line specified, fail loudly"? Symmetry says
   through-EOF; safety says fail. Default to through-EOF, document.
2. Should `patch_file` accept a `verify_match: string` arg — the model
   echoes back the EXACT content it expects to be replacing, the tool
   refuses to patch if it doesn't match? Mitigates the model patching
   a file that was edited between read and patch. Probably yes; size
   it as a small optional param.
3. Should the response include the post-edit file's hash so the model
   can detect "I edited X but the file looks different than I think"?
   Useful for the bounded-emergence story; cheap to add.
