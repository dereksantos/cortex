#!/usr/bin/env bash
# scripts/bench/dashboard-export.sh — bring the durable bench store up to date
# and write the trend dashboard's three tables.
#
# Usage:
#   ./scripts/bench/dashboard-export.sh                    # this machine's store only
#   ./scripts/bench/dashboard-export.sh --pull chatterbox  # first copy runs another host recorded
#
# --pull HOST (repeatable) copies HOST's store runs (its ~/.cortex/benchmarks/runs)
# into this store with rsync --update: new runs and runs still growing arrive,
# nothing is deleted, and files this store rewrote since (reindex's v1 files)
# are kept. Then: reindex (a copied run keeps the labels in its own
# run.v1.json), sync (store git + R2), and export to $STORE/exports/
# (runs.csv, tasks.csv, failures.csv; the store's git ignores that dir).
#
# The dashboard page reads only those three tables, so refreshing it is
# uploading them and pointing its three datasets at the new files — the page
# itself never changes for new data (docs/benchmarks.md, "Trend dashboard").

set -euo pipefail

store="${CORTEX_BENCH_HOME:-$HOME/.cortex/benchmarks}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
pulls=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --pull) pulls+=("$2"); shift 2 ;;
    *) echo "unknown flag $1" >&2; exit 2 ;;
  esac
done

for host in "${pulls[@]+"${pulls[@]}"}"; do
  echo "==> pulling runs from ${host}"
  rsync -a --update "${host}:.cortex/benchmarks/runs/" "${store}/runs/"
done

cd "$repo_root"
go run ./scripts/bench/benchstore reindex
go run ./scripts/bench/benchstore sync
go run ./scripts/bench/benchstore export --out "${store}/exports"
