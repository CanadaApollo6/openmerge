#!/usr/bin/env python3
"""Compare synthetic legacy journals and every next plan at depths 1 and 4.

Run under mise against a clean baseline checkout. This does not read a live
journal or authorize deployment; a deployment still needs its exported-journal
shadow replay. All forge activity uses the simulator's fake forge.
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
 "crypto/sha256"
 "encoding/json"
 "strings"
 "testing"
 "time"
 "github.com/CanadaApollo6/openmerge/internal/queue"
)
func TestStageAIdentityDifferentialReplay(t *testing.T) {
 for _,depth:=range []int{1,4} {
  cfg:=DefaultConfig()
  cfg.PRs=40;cfg.Seed=42;cfg.ArrivalWindow=time.Hour
  cfg.BrokenRate=.2;cfg.FlakeRate=.1
  cfg.Planner.BatchMaxSize=4;cfg.Planner.BatchMaxWait=time.Minute
  cfg.Planner.SpeculationMaxDepth=depth
  cfg.Planner.ChecksRetryFailed=1
  cfg.CI=&CIConfig{Runners:50,RerunsSupported:true}
  initial,err:=newSimulation(cfg);if err!=nil {t.Fatal(err)}
  state:=initial.state
  s,_:=runScenario(t,cfg)
  journal:=sha256.New()
  for i,entry:=range s.events {
   name,raw,err:=queue.EncodeEvent(entry.Event);if err!=nil {t.Fatal(err)}
   if strings.HasPrefix(name,"change_") {t.Fatalf("legacy queue emitted %s",name)}
   _,_=journal.Write(raw);_,_=journal.Write([]byte{'\n'})
   event,err:=queue.DecodeEvent(raw);if err!=nil {t.Fatal(err)}
   state=queue.Apply(state,event)
   type action struct {Key string;Wire json.RawMessage}
   actions:=[]action{}
   for _,a:=range queue.Plan(s.cfg.Planner,state,entry.At) {
    _,wire,err:=queue.EncodeAction(a);if err!=nil {t.Fatal(err)}
    actions=append(actions,action{a.Key(),wire})
   }
   checkpoint:=struct{Event json.RawMessage;Actions []action}{raw,actions}
   encoded,err:=json.Marshal(checkpoint);if err!=nil {t.Fatal(err)}
   t.Logf("COMPAT depth=%d revision=%d sha256=%x",depth,i+1,sha256.Sum256(encoded))
  }
  t.Logf("JOURNAL depth=%d events=%d sha256=%x",depth,len(s.events),journal.Sum(nil))
 }
}
'''


def records(root, directory):
    probe = directory / "identity_probe_test.go"
    probe.write_text(PROBE)
    overlay = directory / (root.name + "-identity-overlay.json")
    overlay.write_text(json.dumps({"Replace": {
        str(root / "internal/sim/stage_a_identity_probe_test.go"): str(probe)
    }}))
    result = subprocess.run(
        ["go", "test", "-overlay", str(overlay), "./internal/sim", "-run",
         "^TestStageAIdentityDifferentialReplay$", "-v", "-count=1"],
        cwd=root, capture_output=True, text=True,
    )
    if result.returncode:
        raise SystemExit(f"{root}: exit={result.returncode}\n{result.stdout}{result.stderr}")
    checkpoints = [line.split("COMPAT ", 1)[1] for line in result.stdout.splitlines()
                   if "COMPAT " in line]
    journals = [line.split("JOURNAL ", 1)[1] for line in result.stdout.splitlines()
                if "JOURNAL " in line]
    if not checkpoints or len(journals) != 2:
        raise SystemExit("missing differential replay evidence")
    return checkpoints, journals


with tempfile.TemporaryDirectory(prefix="om-stage-a-identity-") as temporary:
    directory = Path(temporary)
    old, old_journals = records(BASELINE, directory)
    new, new_journals = records(ROOT, directory)
    if old != new or old_journals != new_journals:
        raise SystemExit("legacy journal or next plan differs")
    for journal in new_journals:
        print(f"synthetic journal {journal}")
    digest = hashlib.sha256("\n".join(new).encode()).hexdigest()
    print(f"every-revision events/actions: {len(new)} checkpoints byte-identical; sha256={digest}")
    print("no change_* events emitted; exit=0")
