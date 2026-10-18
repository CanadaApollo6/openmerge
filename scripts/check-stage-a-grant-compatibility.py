#!/usr/bin/env python3
"""Export synthetic legacy journals, then compare every revision's next actions.

Run under mise with a clean baseline checkout and an empty evidence directory:
  mise exec -- python3 scripts/check-stage-a-identity-compatibility.py BASELINE OUT
No database, credentials, GitHub API, or live journal is used. Both binaries
replay exactly the same exported journal at depths 1 and 4.
"""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
BASELINE = Path(sys.argv[1]).resolve()
OUT = Path(sys.argv[2]).resolve()
OUT.mkdir(parents=True, exist_ok=False)
PROBE = r'''package sim
import (
 "crypto/sha256"
 "encoding/json"
 "fmt"
 "os"
 "path/filepath"
 "testing"
 "time"
 "github.com/CanadaApollo6/openmerge/internal/config"
 "github.com/CanadaApollo6/openmerge/internal/queue"
)
type identityReplayEvent struct { At time.Time; Raw json.RawMessage }
type identityReplayJournal struct { Config queue.Config; Initial queue.State; Events []identityReplayEvent }
func TestStageAIdentityExport(t *testing.T) {
 dir:=os.Getenv("OM_IDENTITY_EVIDENCE")
 for _,depth:=range []int{1,4} {
  cfg:=DefaultConfig();cfg.Seed=42;cfg.PRs=40;cfg.ArrivalWindow=time.Hour
  cfg.BrokenRate=.2;cfg.FlakeRate=.1
  cfg.Planner.BatchMaxSize=8;cfg.Planner.BatchMaxWait=time.Minute
  cfg.Planner.SpeculationMaxDepth=depth;cfg.Planner.ChecksRetryFailed=1
  cfg.CI=&CIConfig{Runners:50,RerunsSupported:true}
  s,err:=newSimulation(cfg);if err!=nil {t.Fatal(err)}
  initial:=s.state
  if _,err=s.run();err!=nil {t.Fatal(err)}
  journal:=identityReplayJournal{Config:s.cfg.Planner,Initial:initial}
  for _,entry:=range s.events {
   _,raw,err:=queue.EncodeEvent(entry.Event);if err!=nil {t.Fatal(err)}
   journal.Events=append(journal.Events,identityReplayEvent{At:entry.At,Raw:raw})
  }
  raw,err:=json.Marshal(journal);if err!=nil {t.Fatal(err)}
  if err=os.WriteFile(filepath.Join(dir,fmt.Sprintf("depth-%d.json",depth)),raw,0600);err!=nil {t.Fatal(err)}
 }
}
func TestStageAIdentityReplay(t *testing.T) {
 dir:=os.Getenv("OM_IDENTITY_EVIDENCE")
 policy,_,err:=config.Load([]byte("version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n"))
 if err!=nil {t.Fatal(err)}
 t.Logf("IDENTITY-COMPAT default-digest %s",policy.Queues[0].Digest(true))
 for _,depth:=range []int{1,4} {
  raw,err:=os.ReadFile(filepath.Join(dir,fmt.Sprintf("depth-%d.json",depth)));if err!=nil {t.Fatal(err)}
  var journal identityReplayJournal;if err=json.Unmarshal(raw,&journal);err!=nil {t.Fatal(err)}
  t.Logf("IDENTITY-COMPAT depth=%d journal-sha256=%x events=%d",depth,sha256.Sum256(raw),len(journal.Events))
  state:=journal.Initial
  for revision,entry:=range journal.Events {
   event,err:=queue.DecodeEvent(entry.Raw);if err!=nil {t.Fatal(err)}
   state=queue.Apply(state,event)
   var actions []json.RawMessage
   for _,action:=range queue.Plan(journal.Config,state,entry.At) {
    _,encoded,err:=queue.EncodeAction(action);if err!=nil {t.Fatal(err)}
    actions=append(actions,encoded)
   }
   encoded,err:=json.Marshal(actions);if err!=nil {t.Fatal(err)}
   t.Logf("IDENTITY-COMPAT depth=%d revision=%d next=%s",depth,revision+1,encoded)
  }
 }
}
'''


def run(args, cwd, env=None):
    result = subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True)
    if result.returncode:
        raise SystemExit(f"{cwd}: exit={result.returncode}\n{result.stdout}{result.stderr}")
    return result.stdout


probe = OUT / "identity_probe_test.go"
probe.write_text(PROBE)
binaries = {}
for name, root in [("old", BASELINE), ("new", ROOT)]:
    overlay = OUT / f"{name}-overlay.json"
    overlay.write_text(json.dumps({"Replace": {str(root / "internal/sim/stage_a_identity_probe_test.go"): str(probe)}}))
    binary = OUT / f"{name}.test"
    run(["go", "test", "-c", "-overlay", str(overlay), "-o", str(binary), "./internal/sim"], root)
    binaries[name] = binary
    print(f"{name} source={run(['git', 'rev-parse', 'HEAD'], root).strip()} binary-sha256={hashlib.sha256(binary.read_bytes()).hexdigest()}", flush=True)

env = dict(os.environ, OM_IDENTITY_EVIDENCE=str(OUT))
run([str(binaries["old"]), "-test.run=^TestStageAIdentityExport$", "-test.v"], BASELINE, env)
records = {}
for name, root in [("old", BASELINE), ("new", ROOT)]:
    log = run([str(binaries[name]), "-test.run=^TestStageAIdentityReplay$", "-test.v"], root, env)
    (OUT / f"{name}-replay.log").write_text(log)
    records[name] = [line.split("IDENTITY-COMPAT ", 1)[1] for line in log.splitlines() if "IDENTITY-COMPAT " in line]
if records["old"] != records["new"]:
    raise SystemExit("next actions or default digest differ; inspect replay logs")
digest = hashlib.sha256("\n".join(records["new"]).encode()).hexdigest()
print(f"every-revision replay: {len(records['new'])} identical records; sha256={digest}")
for line in records["new"]:
    if "next=" not in line:
        print(line)
print("no new event types: both binaries decoded the exact baseline journals; exit=0")
