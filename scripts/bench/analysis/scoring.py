#!/usr/bin/env python3
"""How cortex's SWE-bench attempts were scored and what its sessions did.

For every valid cortex run in the batch dirs given (runs labeled invalid are
skipped), from the run's own files in the store:

  collision   the official test patch failed to apply during scoring
              ("already exists in working directory" / "patch does not apply")
              — the agent's prediction created or changed a test file the
              test patch also creates, so the official tests never ran
  regression  the patch broke tests that passed before it (PASS_TO_PASS failures)
  suite       the session ran the project's own test suite (django's
              runtests.py, or pytest/unittest pointed into the repo's test
              tree) rather than only scratch scripts — a text match on the
              bash commands, so approximate
  cap         the session ended at the tool-call cap (--cap, default 100)

and, with --mini, the same suite/regression facts for mini-SWE-agent's run of
the same task in the same batch dir.

    python3 scripts/bench/analysis/scoring.py ~/.cortex/benchmarks/runs/swebench/baseline-sv50-s106-20261013 --mini
"""
import argparse
import glob
import json
import os
import re

SUITE = re.compile(r"runtests\.py|(pytest|unittest)[^|;&]*\b(tests?|testing|sklearn|sympy|astropy|xarray|lib)/[\w/.-]*test")
COLLISION = re.compile(r"already exists in working directory|patch does not apply|error: patch failed")


def valid(d):
    try:
        return not json.load(open(os.path.join(d, "run.v1.json"))).get("labels", {}).get("invalid")
    except (OSError, json.JSONDecodeError):
        return True


def report(d, task):
    reps = glob.glob(os.path.join(d, "**", task, "report.json"), recursive=True)
    if not reps:
        return None
    r = json.load(open(reps[0])).get(task, {})
    return {"resolved": bool(r.get("resolved")),
            "p2p_broken": len(r.get("tests_status", {}).get("PASS_TO_PASS", {}).get("failure", []))}


def collided(d):
    for log in glob.glob(os.path.join(d, "**", "test_output.txt"), recursive=True):
        if COLLISION.search(open(log, errors="replace").read()):
            return True
    return False


def bash_commands(d):
    cmds = []
    for tr in glob.glob(os.path.join(d, "trajs", "*.jsonl")):  # cortex session transcript
        for line in open(tr):
            m = json.loads(line)
            for tc in (m.get("tool_calls") or []) if m.get("role") == "assistant" else []:
                f = tc.get("function", {})
                if f.get("name") == "bash":
                    try:
                        cmds.append(json.loads(f.get("arguments") or "{}").get("command", ""))
                    except json.JSONDecodeError:
                        pass
    for tr in glob.glob(os.path.join(d, "**", "*.traj.json"), recursive=True):  # mini trajectory
        for m in json.load(open(tr)).get("messages", []):
            for tc in (m.get("tool_calls") or []) if m.get("role") == "assistant" else []:
                try:
                    cmds.append(json.loads(tc["function"]["arguments"]).get("command", ""))
                except (KeyError, TypeError, json.JSONDecodeError):
                    pass
    return cmds


def tool_calls(d):
    try:
        return json.loads(open(os.path.join(d, "results.v1.jsonl")).readline()).get("tool_calls", 0) or 0
    except (OSError, json.JSONDecodeError):
        return 0


def task_of(name):
    # cortex-<task>, <arm>-<task>, mini-<task>, with an optional .rN re-run suffix
    return re.sub(r"\.r\d+$", "", re.sub(r"^[a-z0-9]+-", "", name, count=1))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("batch_dirs", nargs="+")
    ap.add_argument("--cap", type=int, default=100)
    ap.add_argument("--mini", action="store_true", help="also summarize mini-SWE-agent runs in the same dirs")
    a = ap.parse_args()
    for bd in a.batch_dirs:
        rows = []
        for d in sorted(glob.glob(os.path.join(bd, "*"))):
            name = os.path.basename(d)
            if not os.path.isdir(d) or name.startswith("mini-") or not valid(d):
                continue
            task = task_of(name)
            rep = report(d, task)
            if rep is None:
                continue
            rows.append({"task": task, "run": name, **rep, "collision": collided(d),
                         "suite": any(SUITE.search(c) for c in bash_commands(d)), "cap": tool_calls(d) == a.cap})
        fails = [r for r in rows if not r["resolved"]]
        print(f"{os.path.basename(bd)}: {len(rows)} cortex runs, {len(fails)} failed")
        print(f"  failed by a test-patch collision: {sum(r['collision'] for r in fails)}  {sorted(r['run'] for r in fails if r['collision'])}")
        print(f"  failed and broke existing tests:  {sum(1 for r in fails if r['p2p_broken'])}  {sorted(r['task'] for r in fails if r['p2p_broken'])}")
        print(f"  ran the project's own suite:      {sum(r['suite'] for r in rows)}/{len(rows)}")
        cap = [r for r in rows if r["cap"]]
        print(f"  ended at the {a.cap}-call cap:        {len(cap)} (solved {sum(r['resolved'] for r in cap)})")
        if a.mini:
            mrows = []
            for d in sorted(glob.glob(os.path.join(bd, "mini-*", "mini"))):
                if not valid(d):
                    continue
                task = task_of(os.path.basename(os.path.dirname(d)))
                rep = report(d, task)
                if rep is not None:
                    mrows.append({**rep, "suite": any(SUITE.search(c) for c in bash_commands(d))})
            print(f"  mini-SWE-agent: {len(mrows)} scored runs; ran the suite in {sum(r['suite'] for r in mrows)}; "
                  f"broke existing tests in {sum(1 for r in mrows if r['p2p_broken'])}")


if __name__ == "__main__":
    main()
