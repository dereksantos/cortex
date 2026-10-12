#!/usr/bin/env bash
# scripts/bench/swebench/run_mini.sh — the mini-SWE-agent side of a paired
# comparison: same tasks, same model, same pinned OpenRouter provider as the
# cortex runner, scored by the same pinned official harness.
#
# Usage:
#   ./scripts/bench/swebench/run_mini.sh --run-id pair10 --ids a,b,c \
#       --usage-cap 106.36 [--instance-cost 2.0]
#
# --usage-cap is an ABSOLUTE ceiling on the OpenRouter key's cumulative usage
# counter (GET /api/v1/key). Instances run one at a time; the next one starts
# only if (current usage + --instance-cost + headroom) <= --usage-cap, and
# mini's own agent.cost_limit (mini_overlay.yaml) stops each instance at
# --instance-cost. The cap is shared with anything else spending on the key.
#
# Key handling: read from the macOS keychain into this script's environment
# only (never argv, never a file). mini's docker environment forwards no env
# to the container (forward_env is empty in the official config).
#
# Output: the durable benchmark store (docs/benchmarks.md) —
# $CORTEX_BENCH_HOME (default ~/.cortex/benchmarks)/runs/swebench/<run-id>/mini/:
# mini's own trajectories (<id>/<id>.traj.json), preds.json, minisweagent.log,
# predictions.jsonl, a usage.jsonl ledger, scoring in .../mini/eval/, and a
# results.jsonl (one {"instance_id","resolved","eval_status"} row per
# prediction, from the official reports) that the store's index summarizes.
# Give the cortex side a SIBLING id (e.g. pair-X/cortex vs pair-X): a mini run
# nested inside a cortex run's directory is not found by `benchstore reindex`.

set -euo pipefail

MINI_VERSION="2.4.6"
SWEBENCH_VERSION="5.0.2"
DATASET_REVISION="78f471bf655a3137b2e8a75af1501690ec009ec3"
HEADROOM="0.25"   # OpenRouter's usage counter can lag a few seconds

run_id="" ids="" usage_cap="" instance_cost="2.0" key_service="cortex-openrouter" no_score=0
backend="openrouter" endpoint="" model="" temperature="1.0" wall_limit_s=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --run-id) run_id="$2"; shift 2 ;;
    --ids) ids="$2"; shift 2 ;;
    --usage-cap) usage_cap="$2"; shift 2 ;;
    --instance-cost) instance_cost="$2"; shift 2 ;;
    --key-service) key_service="$2"; shift 2 ;;
    --no-score) no_score=1; shift ;;
    --backend) backend="$2"; shift 2 ;;            # openrouter (default) | litellm
    --endpoint) endpoint="$2"; shift 2 ;;          # litellm: OpenAI-compatible root, e.g. http://chatterbox:4000
    --model) model="$2"; shift 2 ;;                # litellm: the proxy's model alias
    --temperature) temperature="$2"; shift 2 ;;
    --wall-limit) wall_limit_s="$2"; shift 2 ;;    # seconds per instance; 0 = none
    *) echo "unknown flag $1" >&2; exit 2 ;;
  esac
done
case "$backend" in
  openrouter) [[ -n "$run_id" && -n "$ids" && -n "$usage_cap" ]] || { echo "--run-id, --ids, --usage-cap are required" >&2; exit 2; } ;;
  litellm)    [[ -n "$run_id" && -n "$ids" && -n "$endpoint" && -n "$model" ]] || { echo "--backend litellm needs --run-id, --ids, --endpoint, --model" >&2; exit 2; } ;;
  *) echo "--backend must be openrouter or litellm" >&2; exit 2 ;;
esac

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
bench_root="${repo_root}/.cortex/bench"
mini_venv="${bench_root}/mini-venv"
sb_venv="${bench_root}/swebench-venv"
export_file="${bench_root}/swebench-data/verified-${DATASET_REVISION:0:12}.jsonl"
# The run dir comes from the durable store: outside any git working tree,
# created exclusively, recorded in the index at start.
mkdir -p "${bench_root}/bin"
(cd "$repo_root" && go build -o "${bench_root}/bin/benchstore" ./scripts/bench/benchstore)
out="$(cd "$repo_root" && "${bench_root}/bin/benchstore" begin --bench swebench --run-id "${run_id}/mini" --label harness=mini-swe-agent)"

# --- pinned harnesses ------------------------------------------------------------
if [[ ! -x "${mini_venv}/bin/python" ]]; then "${PYTHON_BIN:-python3.11}" -m venv "$mini_venv"; fi
have="$("${mini_venv}/bin/python" -c 'import importlib.metadata as m; print(m.version("mini-swe-agent"))' 2>/dev/null || echo none)"
[[ "$have" == "$MINI_VERSION" ]] || "${mini_venv}/bin/pip" install -q "mini-swe-agent==${MINI_VERSION}"
have_sb="$("${sb_venv}/bin/python" -c 'import importlib.metadata as m; print(m.version("swebench"))')"
[[ "$have_sb" == "$SWEBENCH_VERSION" ]] || { echo "swebench ${have_sb} != ${SWEBENCH_VERSION}; run run.sh first" >&2; exit 1; }
[[ -s "$export_file" ]] || { echo "missing ${export_file}; run run.sh first" >&2; exit 1; }

# mini prints a startup banner on stdout at import; keep only the path line.
official_cfg="$(MSWEA_SILENT_STARTUP=1 "${mini_venv}/bin/python" -c 'from minisweagent.config import builtin_config_dir; print(builtin_config_dir / "benchmarks" / "swebench.yaml")' | tail -n 1)"
"${mini_venv}/bin/python" "${repo_root}/scripts/bench/swebench/mini_dataset.py" \
  --export "$export_file" --ids "$ids" --out "${out}/dataset"

# --- the key: environment only ------------------------------------------------------
overlay="${repo_root}/scripts/bench/swebench/mini_overlay.yaml"
if [[ "$backend" == openrouter ]]; then
  OPENROUTER_API_KEY="$(security find-generic-password -s "$key_service" -w)"
  export OPENROUTER_API_KEY
else
  # Local proxy: same official config, only the model pointed at the proxy.
  # No key, no bill — cost tracking off and cost_limit 0 (= no limit); the
  # official step_limit (250) and --wall-limit are the bounds.
  overlay="${out}/mini_overlay_litellm.yaml"
  cat > "$overlay" <<YAML
model:
  model_class: litellm
  model_name: openai/${model}
  cost_tracking: ignore_errors
  model_kwargs:
    api_base: ${endpoint%/}/v1
    api_key: none
    temperature: ${temperature}
agent:
  cost_limit: 0
environment:
  # SWE-bench images are linux/amd64 only; on an arm64 host docker must be
  # told, or \`docker run\` finds no matching manifest.
  run_args: ["--rm", "--platform", "linux/amd64"]
YAML
fi
# A run-owned global config dir: the user's ~/.config/mini-swe-agent/.env must not
# change the system under test, and mini must not write the key anywhere.
export MSWEA_GLOBAL_CONFIG_DIR="${out}/mini-config"
export MSWEA_SILENT_STARTUP=1
mkdir -p "$MSWEA_GLOBAL_CONFIG_DIR"

usage() {
  [[ "$backend" == litellm ]] && { echo 0; return; }
  curl -s --max-time 20 https://openrouter.ai/api/v1/key -H @<(printf 'Authorization: Bearer %s\n' "$OPENROUTER_API_KEY") \
    | "${mini_venv}/bin/python" -c 'import json,sys; print(json.load(sys.stdin)["data"]["usage"])'
}

cat > "${out}/run.json" <<EOF
{"run_id": "${run_id}", "harness": "mini-swe-agent", "mini_version": "${MINI_VERSION}",
 "official_config": "${official_cfg}", "overlay": "${overlay}", "backend": "${backend}", "wall_limit_s": ${wall_limit_s},
 "swebench_version": "${SWEBENCH_VERSION}", "dataset_revision": "${DATASET_REVISION}",
 "instance_cost_cap": ${instance_cost}, "usage_cap": ${usage_cap:-0}, "ids": "${ids}",
 "cortex_commit": "$(git -C "$repo_root" rev-parse HEAD)", "started_at": "$(date -u +%FT%TZ)"}
EOF

IFS=',' read -r -a id_list <<< "$ids"
pulled=()
for id in "${id_list[@]}"; do
  u0="$(usage)"
  if [[ "$backend" == openrouter ]] && ! "${mini_venv}/bin/python" -c "import sys; sys.exit(0 if ${u0} + ${instance_cost} + ${HEADROOM} <= ${usage_cap} else 1)"; then
    echo "budget: usage ${u0} + ${instance_cost} + ${HEADROOM} > cap ${usage_cap} — stopping before ${id}"
    echo "{\"instance_id\":\"${id}\",\"skipped_for_budget\":true,\"usage_before\":${u0}}" >> "${out}/usage.jsonl"
    continue
  fi
  echo "==> mini ${id} (usage ${u0})"
  # Pull outside the wall clock (cortex's runner also pulls before its turn
  # timer starts); mini's own pull has a 120s timeout a 2–5 GB image can miss.
  image="swebench/sweb.eval.x86_64.${id//__/_1776_}:latest"
  image="$(echo "$image" | tr '[:upper:]' '[:lower:]')"
  if ! docker image inspect "$image" >/dev/null 2>&1; then
    docker pull -q --platform linux/amd64 "$image" >/dev/null && pulled+=("$image")
  fi
  t0=$(date +%s)
  # --redo-existing so an instance is never silently skipped; the loop runs each once.
  "${mini_venv}/bin/mini-extra" swebench \
    --subset "${out}/dataset" --split test --filter "^${id}\$" \
    -c "$official_cfg" -c "$overlay" \
    -o "$out" -w 1 --redo-existing > "${out}/${id}.stdout.log" 2>&1 &
  mini_pid=$!
  timed_out=false
  while kill -0 "$mini_pid" 2>/dev/null; do
    if (( wall_limit_s > 0 && $(date +%s) - t0 >= wall_limit_s )); then
      timed_out=true
      pkill -TERM -P "$mini_pid" 2>/dev/null; kill -TERM "$mini_pid" 2>/dev/null
      sleep 5; pkill -KILL -P "$mini_pid" 2>/dev/null; kill -KILL "$mini_pid" 2>/dev/null
      # mini's instance container outlives a killed driver; remove it.
      # (-w 1: one instance at a time, so any minisweagent-* container is this one's.)
      docker ps -q --filter "name=minisweagent-" | xargs docker rm -f >/dev/null 2>&1 || true
      echo "    wall limit ${wall_limit_s}s hit for ${id}"
      break
    fi
    sleep 10
  done
  wait "$mini_pid" 2>/dev/null || $timed_out || echo "mini exited non-zero for ${id}"
  t1=$(date +%s)
  u1="$(usage)"
  echo "{\"instance_id\":\"${id}\",\"usage_before\":${u0},\"usage_after\":${u1},\"wall_s\":$((t1 - t0)),\"timed_out\":${timed_out}}" >> "${out}/usage.jsonl"
  echo "    done in $((t1 - t0))s, account delta $("${mini_venv}/bin/python" -c "print(round(${u1} - ${u0}, 4))")"
done
unset OPENROUTER_API_KEY 2>/dev/null || true

# preds.json (mini's dict) → predictions.jsonl (official list format)
"${mini_venv}/bin/python" - "$out" <<'EOF'
import json, sys, pathlib
out = pathlib.Path(sys.argv[1])
preds = json.loads((out / "preds.json").read_text()) if (out / "preds.json").exists() else {}
with open(out / "predictions.jsonl", "w") as f:
    for p in preds.values():
        p["model_patch"] = p.get("model_patch") or ""
        f.write(json.dumps(p) + "\n")
print(f"{len(preds)} predictions")
EOF

if [[ "$no_score" == 0 ]]; then
  mkdir -p "${out}/eval"
  # The harness puts run_id into docker container names ([a-zA-Z0-9_.-] only),
  # so a nested --run-id like night/mini-<id> is flattened for scoring.
  (cd "${out}/eval" && "${sb_venv}/bin/python" -m swebench.harness.run_evaluation \
    --dataset_name "$export_file" --predictions_path "${out}/predictions.jsonl" \
    --run_id "${run_id//\//_}-mini" --max_workers 1 --timeout 1800 --report_dir "${out}/eval") \
    > "${out}/eval/harness.log" 2>&1 || echo "harness exited non-zero"
  tail -15 "${out}/eval/harness.log"
fi
# results.jsonl in the store's shape, from the harness's per-instance reports;
# a prediction with no report (scoring failed or skipped) is an error row.
"${mini_venv}/bin/python" - "$out" <<'EOF'
import json, sys, pathlib
out = pathlib.Path(sys.argv[1])
reports = {}
for rp in (out / "eval").rglob("report.json"):
    try:
        reports.update(json.loads(rp.read_text()))
    except (OSError, ValueError):
        pass
with open(out / "results.jsonl", "w") as f:
    for line in (out / "predictions.jsonl").read_text().splitlines():
        iid = json.loads(line)["instance_id"]
        r = reports.get(iid)
        status = "error_no_report" if r is None else ("resolved" if r.get("resolved") else "unresolved")
        f.write(json.dumps({"instance_id": iid, "resolved": bool(r and r.get("resolved")), "eval_status": status}) + "\n")
EOF
(cd "$repo_root" && "${bench_root}/bin/benchstore" finish --bench swebench --run-id "${run_id}/mini")
# Images this run pulled (2–5 GB each), removed after scoring like the cortex runner.
(( ${#pulled[@]} )) && docker rmi "${pulled[@]}" >/dev/null 2>&1 || true
