#!/usr/bin/env python3
"""Paired result of one batch from the exported tables (benchstore export).

Reads $CORTEX_BENCH_HOME/exports/tasks.csv (or --tasks) and, for the batch
given, prints each arm's solves on the tasks every arm was scored on (pass or
fail), the tasks only one arm solved, and — for two arms — a two-sided exact
sign test on those. With --table, also one row per task.

    python3 scripts/bench/analysis/paired.py baseline-sv50-s106-20261013
    python3 scripts/bench/analysis/paired.py ab-redgreen-cap-20261014 --table
"""
import argparse
import csv
import math
import os
from collections import defaultdict


def sign_test(b, c):
    n = b + c
    if n == 0:
        return 1.0
    tail = sum(math.comb(n, i) for i in range(min(b, c) + 1))
    return min(1.0, 2 * tail / 2 ** n)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("batch")
    ap.add_argument("--tasks", default=os.path.join(os.environ.get("CORTEX_BENCH_HOME", os.path.expanduser("~/.cortex/benchmarks")), "exports", "tasks.csv"))
    ap.add_argument("--table", action="store_true", help="also print one row per task")
    a = ap.parse_args()

    by = defaultdict(dict)
    for r in csv.DictReader(open(a.tasks)):
        if r["batch"] == a.batch:
            by[r["task"]][r["arm"]] = r
    arms = sorted({arm for t in by.values() for arm in t}, key=lambda x: (x != "cortex", x))
    shared = sorted(t for t, rs in by.items() if all(rs.get(arm, {}).get("status") in ("pass", "fail") for arm in arms))
    print(f"batch {a.batch}: {len(by)} tasks, arms {arms}, scored by every arm: {len(shared)}")
    for arm in arms:
        n = sum(by[t][arm]["status"] == "pass" for t in shared)
        print(f"  {arm:12} {n}/{len(shared)} ({n / len(shared):.0%})" if shared else f"  {arm}: -")
    if len(arms) == 2 and shared:
        x, y = arms
        only_x = [t for t in shared if by[t][x]["status"] == "pass" and by[t][y]["status"] != "pass"]
        only_y = [t for t in shared if by[t][y]["status"] == "pass" and by[t][x]["status"] != "pass"]
        print(f"  only {x}: {len(only_x)}  only {y}: {len(only_y)}  sign test p = {sign_test(len(only_x), len(only_y)):.3f}")
        print(f"  only {x}: {' '.join(only_x)}")
        print(f"  only {y}: {' '.join(only_y)}")
    if a.table:
        print("\n" + "task".ljust(34) + "".join(arm.ljust(14) for arm in arms))
        for t in sorted(by):
            cells = []
            for arm in arms:
                r = by[t].get(arm)
                cells.append(("PASS" if r and r["status"] == "pass" else (r["failure_class"] or r["status"]) if r else "-").ljust(14))
            print(t.ljust(34) + "".join(cells))


if __name__ == "__main__":
    main()
