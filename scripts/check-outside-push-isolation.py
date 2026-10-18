#!/usr/bin/env python3
"""Verify PR 53's non-material paths and legacy fixtures against a Git ref.

Run from any directory: python3 scripts/check-outside-push-isolation.py origin/main
Only audit-loop startup and the history-provider option may differ in serve.go.
This reads Git objects and the working tree; it never contacts a forge or DB.
"""

import difflib
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
BASELINE = sys.argv[1] if len(sys.argv) > 1 else "origin/main"


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT)


def baseline(path):
    return git("show", f"{BASELINE}:{path}")


def same(path, current=None):
    old = baseline(path)
    new = (ROOT / path).read_bytes() if current is None else current
    if old != new:
        diff = "".join(difflib.unified_diff(
            old.decode().splitlines(True), new.decode().splitlines(True),
            fromfile=f"{BASELINE}:{path}", tofile=path,
        ))
        raise SystemExit(diff)


paths = [
    "internal/serve/reconcile.go", "internal/serve/settle.go",
    "internal/serve/worker.go", "internal/queue/plan.go",
    "internal/queue/chain.go",
]
paths += git("ls-tree", "-r", "--name-only", BASELINE, "internal/executor").decode().splitlines()
for path in paths:
    same(path)
print(f"baseline: {git('rev-parse', BASELINE).decode().strip()}")
print("settlement/reconciliation/planning diff (including all executor files): empty")

serve = (ROOT / "internal/serve/serve.go").read_bytes()
allowed = [
    b"\t// OutsidePushHistory must use independent credentials, caches and locks;\n"
    b"\t// audit enrichment must never contend with settlement's Git operations.\n"
    b"\tOutsidePushHistory func(context.Context, store.QueueKey) (forge.OutsidePushHistory, error)\n",
    b"\twg.Go(func() { s.outsidePushLoop(ctx) })\n",
]
for addition in allowed:
    if serve.count(addition) != 1:
        raise SystemExit("missing or duplicated allowed audit startup/provider declaration")
    serve = serve.replace(addition, b"")
same("internal/serve/serve.go", serve)
print("serve.go: cycle, worker type, loop and configuration unchanged; only provider option + independent Run goroutine added")

apply = (ROOT / "internal/queue/apply.go").read_bytes()
start = apply.index(b"\t// Audit observations do not advance planner revisions")
end = apply.index(b"\tif _, poisoned :=", start)
same("internal/queue/apply.go", apply[:start] + apply[end:])
print("queue.Apply: audit-only early return; every existing event path and cloneState byte-identical")

fixtures = git("ls-tree", "-r", "--name-only", BASELINE, "internal/queue/testdata").decode().splitlines()
for fixture in fixtures:
    same(fixture)
print(f"legacy queue fixtures: {len(fixtures)} byte-identical")
print("exit=0")
