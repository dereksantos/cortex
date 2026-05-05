# Library-service eval — archived runs

Snapshots of full eval runs, one file per run. Each captures:

- Date and methodology (model, harness, --bare or not, hooks state)
- The full results table with Δ
- Per-session activity (duration, files changed, preamble sizes)
- Interpretation in plain text
- Caveats specific to this run

Raw run JSONs and execution logs are kept under `/tmp/cortex-libsvc-runs/`
on the operator's machine and **not** committed (large, ephemeral, easy
to regenerate via `cmd/library-eval/`). Only the human-readable summary
is archived here.

## Naming convention

`YYYY-MM-DD-{model-shortname}-{caveat-tag}.md`

Examples:

- `2026-05-04-haiku-hooks-active.md` — first run, before `--bare` patch
- `2026-05-05-haiku-bare.md` — clean methodology baseline (TBD)
- `2026-05-05-sonnet-bare-frontier.md` — frontier ceiling reference (TBD)
- `2026-05-XX-qwen-aider-bare.md` — actual thesis test (TBD)

## When to add a new file

Add an entry whenever the run produces data worth comparing against
later — change in model, harness, methodology, or any other axis the
table doesn't capture. Don't re-archive nominally-identical re-runs;
those go in a per-run-file "additional runs" section.
