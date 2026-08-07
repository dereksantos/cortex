#!/usr/bin/env bash
# scripts/bench/polyglot/run.sh — score cortex on the Go slice of the
# Exercism polyglot benchmark set (the corpus Aider's polyglot benchmark uses).
#
# This wrapper owns reproducibility: it pins the exercise checkout to an exact
# commit, builds the cortex binary under test from the current tree, then hands
# off to the Go driver (scripts/bench/polyglot) which runs and scores the
# exercises and writes the structured record.
#
# Scoring is deterministic — the exercises ship unit tests, so the verdict is
# `go test ./...`'s exit code. No LLM judges anything.
#
# Usage:
#   ./scripts/bench/polyglot/run.sh --only 3                  # smoke test
#   ./scripts/bench/polyglot/run.sh --exercise wordy,matrix
#   ./scripts/bench/polyglot/run.sh --model coder-cuda --timeout 15m
#   ./scripts/bench/polyglot/run.sh                           # FULL 39-exercise slice
#
# The full slice occupies the local GPU for hours. Run it deliberately.
#
# Everything lives under .cortex/bench/ (gitignored): the pinned checkout, the
# built binary, and one directory per run holding run.json, results.jsonl,
# transcripts/ and work/.

set -euo pipefail

# POLYGLOT_COMMIT pins the exercise corpus. Bumping it changes the benchmark;
# results are only comparable within one pin. Recorded in every run.json.
POLYGLOT_REPO="https://github.com/Aider-AI/polyglot-benchmark.git"
POLYGLOT_COMMIT="7e0611e77b54e2dea774cdc0aa00cf9f7ed6144f"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
bench_root="${repo_root}/.cortex/bench"
src_dir="${bench_root}/polyglot-src"
bin_dir="${bench_root}/bin"

mkdir -p "$bench_root" "$bin_dir"

# --- 1. pin the exercise corpus ------------------------------------------
if [[ ! -d "${src_dir}/.git" ]]; then
  echo "==> cloning ${POLYGLOT_REPO}"
  git clone --quiet "$POLYGLOT_REPO" "$src_dir"
fi

have="$(git -C "$src_dir" rev-parse HEAD 2>/dev/null || echo none)"
if [[ "$have" != "$POLYGLOT_COMMIT" ]]; then
  echo "==> pinning polyglot corpus to ${POLYGLOT_COMMIT}"
  git -C "$src_dir" fetch --quiet origin "$POLYGLOT_COMMIT" 2>/dev/null \
    || git -C "$src_dir" fetch --quiet origin
  git -C "$src_dir" checkout --quiet --detach "$POLYGLOT_COMMIT"
fi
# The corpus must be pristine: a stray edit would silently change the score.
if [[ -n "$(git -C "$src_dir" status --porcelain)" ]]; then
  echo "✖ ${src_dir} has local modifications — the pinned corpus must be clean." >&2
  echo "  Reset with: git -C ${src_dir} checkout -- . && git -C ${src_dir} clean -fd" >&2
  exit 1
fi

# --- 2. build the binaries under test ------------------------------------
echo "==> building cortex"
(cd "$repo_root" && go build -o "${bin_dir}/cortex" ./cmd/cortex)
echo "==> building the benchmark driver"
(cd "$repo_root" && go build -o "${bin_dir}/polyglotbench" ./scripts/bench/polyglot)

# --- 3. run ---------------------------------------------------------------
exec "${bin_dir}/polyglotbench" \
  --src "$src_dir" \
  --out "$bench_root" \
  --cortex "${bin_dir}/cortex" \
  "$@"
