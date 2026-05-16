# Conway's Game of Life — CLI specification

Implement a Go program (single `main.go` at the workdir root) that
reads an initial grid from **stdin** and writes successive generations
to **stdout** until the requested number of generations has elapsed.

## Charset

- `.` (period) — dead cell
- `#` (hash) — alive cell

Any other character on a grid line is a hard error.

## Input format

One line per grid row. Rows are equal-length. Trailing newline is
optional. Example (5×5 blinker, vertical phase):

```
.....
..#..
..#..
..#..
.....
```

## Output format

The program emits the **initial** generation first (echo of input,
normalized to the charset), then one frame per generation, separated
by a single **blank line** (i.e. two `\n` between successive frames).
A single trailing newline at the end of the output is acceptable.

For `--generations 4`, the output contains **5 frames** total:
generation 0 (the input) plus four computed generations.

## Required flag

- `--generations N` (default 5) — how many successor generations to
  compute. The program does NOT animate; it emits all frames as a
  single, ordered, blank-line-separated block.

## Rules (Conway, fixed-bounded grid)

Cells outside the grid are considered dead. For each cell, count the
live cells among its eight neighbors:

- A live cell with 2 or 3 live neighbors stays alive.
- A dead cell with exactly 3 live neighbors becomes alive.
- Otherwise, the cell is dead next generation.

The grid size is fixed (matches the input); cells do not wrap around.

## Verification

Your implementation will be checked two ways:

1. **Frame diff** — for canonical patterns (blinker, glider), your
   output must match the golden frame sequence exactly (after trailing-
   whitespace normalization).
2. **Judge LLM** — for a free-form input, an LLM judge will verify
   that successive generations are valid Conway successors of the
   previous frame.

## Tips

- The blinker oscillates with period 2 (vertical ↔ horizontal).
- The glider translates one cell diagonally every 4 generations.
- Use `go build`, `go test`, or `go run` to verify your work before
  declaring done.
