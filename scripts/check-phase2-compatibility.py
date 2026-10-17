#!/usr/bin/env python3
"""Compare legacy exits and depth-1 retry envelopes to a clean main checkout.

Run under mise: python3 scripts/check-phase2-compatibility.py /path/to/main
No forge or GitHub network calls are made. Both checkouts use the same Go overlay.
"""

import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
BASELINE = Path(sys.argv[1]).resolve()
PROBE = r'''package sim
import (
 "sort"
 "testing"
 "time"
 "github.com/CanadaApollo6/openmerge/internal/config"
 "github.com/CanadaApollo6/openmerge/internal/queue"
)
func TestDepth1RetryWireCompatibility(t *testing.T) {
 policy,_,err:=config.Load([]byte("version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n"))
 if err!=nil {t.Fatal(err)}
 t.Logf("COMPAT digest %s",policy.Queues[0].Digest(true))
 cfg:=DefaultConfig()
 cfg.PRs=40;cfg.Seed=42;cfg.ArrivalWindow=time.Hour
 cfg.BrokenRate=.2;cfg.FlakeRate=.1
 cfg.Planner.BatchMaxSize=8;cfg.Planner.BatchMaxWait=time.Minute
 cfg.Planner.ChecksRetryFailed=1
 cfg.CI=&CIConfig{Runners:50,RerunsSupported:true}
 s,_:=runScenario(t,cfg)
 for _,entry:=range s.events {
  _,raw,err:=queue.EncodeEvent(entry.Event);if err!=nil {t.Fatal(err)}
  t.Logf("COMPAT event %s",raw)
 }
 keys:=make([]string,0,len(s.planned));for key:=range s.planned {keys=append(keys,key)};sort.Strings(keys)
 for _,key:=range keys {
  _,raw,err:=queue.EncodeAction(s.planned[key]);if err!=nil {t.Fatal(err)}
  t.Logf("COMPAT action %s",raw)
 }
}
'''


def run(root, args):
    result = subprocess.run(["go", "test", *args], cwd=root, capture_output=True, text=True)
    if result.returncode:
        raise SystemExit(f"{root}: go test exit={result.returncode}\n{result.stdout}{result.stderr}")
    return result.stdout.splitlines()


def records(root):
    lines = run(root, ["./internal/sim", "-run", "^TestExit(500PRDay|2000PRDayBatch8)$", "-v", "-count=1"])
    values = [line[line.index('{'):] for line in lines if '"config"' in line and '"ci_runs_per_merged_pr"' in line]
    assert len(values) == 17, len(values)
    return values


def retry_records(root, directory):
    probe = directory / "retry_probe_test.go"
    probe.write_text(PROBE)
    overlay = directory / (root.name + "-overlay.json")
    overlay.write_text(json.dumps({"Replace": {str(root / "internal/sim/retry_compat_probe_test.go"): str(probe)}}))
    lines = run(root, ["-overlay", str(overlay), "./internal/sim", "-run", "^TestDepth1RetryWireCompatibility$", "-v", "-count=1"])
    return [line.split("COMPAT ", 1)[1] for line in lines if "COMPAT " in line]


def compare(name, old, new):
    assert old == new, f"{name} differs"
    digest = hashlib.sha256("\n".join(new).encode()).hexdigest()
    print(f"{name}: {len(new)} records byte-identical; sha256={digest}", flush=True)


compare("legacy exits", records(BASELINE), records(ROOT))
with tempfile.TemporaryDirectory(prefix="om-depth1-compat-") as directory:
    compare("depth-1 retry envelopes and default digest", retry_records(BASELINE, Path(directory)), retry_records(ROOT, Path(directory)))
fixtures = list((BASELINE / "internal/queue/testdata/wire/v1").glob("*.json"))
for fixture in fixtures:
    assert fixture.read_bytes() == (ROOT / fixture.relative_to(BASELINE)).read_bytes(), fixture
print(f"all {len(fixtures)} existing wire fixtures byte-identical; exit=0")
