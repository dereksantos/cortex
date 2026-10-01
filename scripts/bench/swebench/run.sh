#!/usr/bin/env bash
# scripts/bench/swebench/run.sh — run cortex on SWE-bench Verified and score
# it with the OFFICIAL SWE-bench evaluation harness.
#
# This wrapper owns reproducibility: it pins the harness version and the
# dataset revision, builds the cortex binary under test (static linux/amd64,
# to run inside each instance's container) from the current tree, then hands
# off to the Go driver, which runs, scores and records each instance.
#
# Usage:
#   ./scripts/bench/swebench/run.sh --dry-run               # show the seeded selection, no spend
#   ./scripts/bench/swebench/run.sh --n 5 --budget 5        # smoke run (default seed 106)
#   ./scripts/bench/swebench/run.sh --instance django__django-11099
#   ./scripts/bench/swebench/run.sh --n 50 --budget 60 --instance-cap 2   # probe
#
# Every run spends real money on OpenRouter. --budget is a hard cap enforced
# on the key's own usage counter; see README.md.
#
# Everything lives under .cortex/bench/ (gitignored): the harness venv, the
# dataset export, the built binaries, and one directory per run.

set -euo pipefail

# SWEBENCH_VERSION pins the evaluation harness; DATASET_REVISION pins the
# SWE-bench_Verified rows (Hugging Face commit). Results are only comparable
# within one pair — both are recorded in every run.json.
SWEBENCH_VERSION="5.0.2"
DATASET_NAME="SWE-bench/SWE-bench_Verified"
DATASET_REVISION="78f471bf655a3137b2e8a75af1501690ec009ec3"
PYTHON_BIN="${PYTHON_BIN:-python3.11}"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
bench_root="${repo_root}/.cortex/bench"
venv="${bench_root}/swebench-venv"
data_dir="${bench_root}/swebench-data"
bin_dir="${bench_root}/bin"
dataset_file="${data_dir}/verified-${DATASET_REVISION:0:12}.jsonl"

mkdir -p "$bench_root" "$data_dir" "$bin_dir"

# --- 1. the official harness, pinned --------------------------------------
if [[ ! -x "${venv}/bin/python" ]]; then
  echo "==> creating ${venv}"
  "$PYTHON_BIN" -m venv "$venv"
fi
have="$("${venv}/bin/python" -c 'import importlib.metadata as m; print(m.version("swebench"))' 2>/dev/null || echo none)"
if [[ "$have" != "$SWEBENCH_VERSION" ]]; then
  echo "==> installing swebench==${SWEBENCH_VERSION} (have: ${have})"
  "${venv}/bin/pip" install -q "swebench==${SWEBENCH_VERSION}"
fi

# --- 2. the dataset, pinned -----------------------------------------------
if [[ ! -s "$dataset_file" ]]; then
  echo "==> exporting ${DATASET_NAME}@${DATASET_REVISION}"
  "${venv}/bin/python" "${repo_root}/scripts/bench/swebench/dataset.py" \
    --name "$DATASET_NAME" --revision "$DATASET_REVISION" --out "$dataset_file"
fi

# --- 3. the binaries under test -------------------------------------------
echo "==> building cortex (linux/amd64, static) for the instance containers"
(cd "$repo_root" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "${bin_dir}/cortex-linux-amd64" ./cmd/cortex)
echo "==> building the benchmark driver"
(cd "$repo_root" && go build -o "${bin_dir}/swebenchbench" ./scripts/bench/swebench)

# --- 4. run ------------------------------------------------------------------
cd "$repo_root"
exec "${bin_dir}/swebenchbench" \
  --dataset-file "$dataset_file" \
  --dataset-name "$DATASET_NAME" \
  --dataset-revision "$DATASET_REVISION" \
  --python "${venv}/bin/python" \
  --swebench-version "$SWEBENCH_VERSION" \
  --cortex-linux "${bin_dir}/cortex-linux-amd64" \
  --out "${bench_root}/swebench" \
  "$@"
