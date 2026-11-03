#!/usr/bin/env python3
"""Refresh the pinned public Actions recordings. Requires gh and local Git history.

Reads metadata only. Does not read logs, artifacts, secrets, or a live journal.
Use `python3 scripts/record-ci-doctor-fixtures.py` from the repository root.
"""
import concurrent.futures
import json
import pathlib
import re
import subprocess

ROOT = pathlib.Path("internal/cidoctor/testdata")
REPO = "CanadaApollo6/openmerge"


def api(path):
    return json.loads(subprocess.check_output(["gh", "api", f"repos/{REPO}/{path}"]))


def graph(run):
    sha = run["head_sha"]
    try:
        workflow = subprocess.check_output(
            ["git", "show", f"{sha}:.github/workflows/ci.yml"], text=True)
        policy = subprocess.check_output(
            ["git", "show", f"{sha}:.openmerge.yml"], text=True)
    except subprocess.CalledProcessError:
        return None
    # These pinned revisions have either independent jobs or the explicit
    # post-217 aggregate. Refuse unknown shapes instead of inferring a graph.
    needs_lines = re.findall(r"^    needs:.*$", workflow, re.MULTILINE)
    if needs_lines not in ([], ["    needs: [lint, test, build]"]):
        return None
    if "check-success = check" not in policy or "check-success = sim-exits" not in policy:
        return None
    needs = {job["name"]: [] for job in run["jobs"]}
    if needs_lines:
        needs["check"] = sorted(name for name in needs if
                               name in ("lint", "build") or name.startswith("test ("))
    return {"head_sha": sha,
            "source": f".github/workflows/ci.yml and .openmerge.yml at {sha}",
            "needs": needs, "required": ["check", "sim-exits"]}


def record(pinned):
    raw = api(f"actions/runs/{pinned['id']}")
    attempt = pinned["run_attempt"]
    jobs = []
    page = 1
    while True:
        response = api(f"actions/runs/{pinned['id']}/attempts/{attempt}/jobs?per_page=100&page={page}")
        jobs.extend(response["jobs"])
        if len(jobs) >= response["total_count"]:
            break
        if not response["jobs"]:
            raise RuntimeError("incomplete jobs pagination")
        page += 1
    run = {k: raw[k] for k in ("id", "workflow_id", "path", "head_branch", "head_sha", "created_at", "event")}
    run["run_attempt"] = attempt
    run["jobs_complete"] = len(jobs) == response["total_count"]
    run["jobs"] = []
    for job in jobs:
        trimmed = {k: job[k] for k in ("id", "name", "created_at", "started_at", "completed_at", "conclusion", "labels", "run_attempt")}
        trimmed["steps"] = [{k: step[k] for k in ("name", "number", "started_at", "completed_at", "conclusion")} for step in job["steps"]]
        run["jobs"].append(trimmed)
    dependency_graph = graph(run)
    if dependency_graph is not None:
        run["graph"] = dependency_graph
    return run


def write(path, actions):
    # One run per line keeps the trimmed metadata compact; json.tool can expand it.
    head = {k: v for k, v in actions.items() if k != "runs"}
    prefix = json.dumps(head, indent=2)[:-2]
    path.write_text(prefix + ',\n  "runs": [\n' + ',\n'.join(
        '    ' + json.dumps(run, separators=(',', ':')) for run in actions["runs"])
        + '\n  ]\n}\n')


def main():
    for period in ("before", "after"):
        path = ROOT / f"openmerge-{period}.actions.json"
        actions = json.loads(path.read_text())
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            actions["runs"] = list(pool.map(record, actions["runs"]))
        write(path, actions)
        print(f"Refreshed {path}: {len(actions['runs'])} workflow runs")


if __name__ == "__main__":
    main()
