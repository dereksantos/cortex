#!/usr/bin/env python3
"""Export a pinned SWE-bench dataset revision to a local JSONL file.

The Go driver (scripts/bench/swebench) reads this file for the problem
statement, base commit and image of each instance, and hands the SAME file to
the official evaluation harness (`python -m swebench.harness.run_evaluation
-d <file>`), so inference and scoring are guaranteed to see identical rows even
if the upstream dataset is revised mid-run.

Every row is exported verbatim (including the gold `patch`, `test_patch`,
`hints_text`, FAIL_TO_PASS/PASS_TO_PASS — the harness needs them to grade).
The driver reads only the fields a submission may use: instance_id, repo,
base_commit, problem_statement, image. It never shows the model anything else.

Usage:
  dataset.py --name SWE-bench/SWE-bench_Verified --revision <sha|main> --out <file.jsonl>

Writes <file.jsonl> and <file.jsonl>.meta.json ({name, split, revision, rows}).
"""
import argparse
import json
import os
import sys


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--name", default="SWE-bench/SWE-bench_Verified")
    ap.add_argument("--split", default="test")
    ap.add_argument("--revision", default="main",
                    help="HF dataset revision (commit sha) to pin; 'main' resolves to the current head")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    from datasets import load_dataset
    from huggingface_hub import HfApi

    # Resolve 'main' to a concrete commit sha so the export is reproducible.
    sha = HfApi().dataset_info(args.name, revision=args.revision).sha
    ds = load_dataset(args.name, split=args.split, revision=sha)

    tmp = args.out + ".tmp"
    with open(tmp, "w") as f:
        for row in ds:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
    os.replace(tmp, args.out)
    meta = {"name": args.name, "split": args.split, "revision": sha, "rows": len(ds)}
    with open(args.out + ".meta.json", "w") as f:
        json.dump(meta, f, indent=2)
        f.write("\n")
    print(json.dumps(meta))
    return 0


if __name__ == "__main__":
    sys.exit(main())
