#!/usr/bin/env python3
"""Write the paired-comparison task set as a local HF dataset for mini-SWE-agent.

mini-SWE-agent's `mini-extra swebench --subset <dir> --split test` loads
`<dir>/test.jsonl`. Writing it from the SAME pinned export the cortex runner
and the official harness use guarantees both harnesses see identical problem
statements, in the same (seeded) order.

Only the fields an inference run may use are written — no hints, gold patch,
test patch, or test lists — so the dataset itself cannot leak them.

Usage:
  mini_dataset.py --export verified.jsonl --ids a,b,c --out <dir>
"""
import argparse
import json
import os
import sys

KEEP = ("instance_id", "repo", "base_commit", "problem_statement", "version", "created_at")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--export", required=True, help="pinned dataset export from dataset.py")
    ap.add_argument("--ids", required=True, help="comma-separated instance ids, in run order")
    ap.add_argument("--out", required=True, help="output directory (gets test.jsonl)")
    args = ap.parse_args()

    ids = [i.strip() for i in args.ids.split(",") if i.strip()]
    rows = {}
    with open(args.export) as f:
        for line in f:
            r = json.loads(line)
            if r["instance_id"] in ids:
                rows[r["instance_id"]] = {k: r[k] for k in KEEP}
    missing = [i for i in ids if i not in rows]
    if missing:
        print("unknown ids: " + ", ".join(missing), file=sys.stderr)
        return 1
    os.makedirs(args.out, exist_ok=True)
    with open(os.path.join(args.out, "test.jsonl"), "w") as f:
        for i in ids:
            f.write(json.dumps(rows[i], ensure_ascii=False) + "\n")
    print(f"wrote {len(ids)} rows to {args.out}/test.jsonl")
    return 0


if __name__ == "__main__":
    sys.exit(main())
