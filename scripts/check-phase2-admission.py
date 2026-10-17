#!/usr/bin/env python3
"""Compare retained and relaxed admission on eight one-day planner workloads.

Run under mise. This is a non-gating policy experiment, not the 30-day exit.
The relaxed policy is a Go overlay; the checkout is never edited.
"""

import json
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
CHAIN = ROOT / "internal/queue/chain.go"
source = CHAIN.read_text()
for guard in (' || parent.ProvisionalFailure != ""', ' || b.ProvisionalFailure != ""',
              ' || b.CheckRetry != nil && b.CheckRetry.RerunState == "failed"'):
    if source.count(guard) != 1:
        raise SystemExit(f"admission guard must occur once: {guard}")
    source = source.replace(guard, "")

with tempfile.TemporaryDirectory(prefix="om-admission-") as directory:
    folder = Path(directory)
    changed = folder / "chain.go"
    changed.write_text(source)
    overlay = folder / "overlay.json"
    overlay.write_text(json.dumps({"Replace": {str(CHAIN): str(changed)}}))
    for policy, flags in (("conservative", []), ("relaxed", ["-overlay", str(overlay)])):
        result = subprocess.run(["go", "test", *flags, "./internal/sim", "-run",
                                 "^TestAdmissionPolicyEvidence$", "-v", "-count=1"],
                                cwd=ROOT, capture_output=True, text=True)
        print(f"{policy}: exit={result.returncode}", flush=True)
        if result.returncode:
            raise SystemExit(result.stdout + result.stderr)
        for line in result.stdout.splitlines():
            if " ADMISSION " in line:
                print(line.split(" ADMISSION ", 1)[1], flush=True)
