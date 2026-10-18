#!/usr/bin/env python3
"""Offline fixture replay, legacy JSON bytes and optional simulator comparison.

Usage: check-stage-a-4b-inert.py BASELINE [BASELINE_SIM_LOG CANDIDATE_SIM_LOG]
Both checkouts must contain already-cached Go dependencies. Never reads a DB or
forge. The same temporary Go overlay runs at both commits, at every checkpoint.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
PROBE = r'''package queue_test
import (
 "encoding/json"
 "fmt"
 "os"
 "reflect"
 "testing"
 "time"
 "github.com/CanadaApollo6/openmerge/internal/config"
 "github.com/CanadaApollo6/openmerge/internal/queue"
)
func TestStageA4bInertReplay(t *testing.T) {
 raw,err:=os.ReadFile("../../.openmerge.yml");if err!=nil{t.Fatal(err)}
 parsed,_,err:=config.Load(raw);if err!=nil{t.Fatal(err)}
 fmt.Println("INERT tracked-config-digest",parsed.Queues[0].Digest(true))
 names:=[]string{"pr_observed","enqueue_requested","pause_requested","action_poisoned","action_poisoned","pause_requested","rules_blocked","rules_source_blocked","resume_requested","rules_cleared","rules_source_cleared","rules_cleared","rules_source_cleared","pause_requested","resume_requested","resume_requested","test_ref_build_failed","test_ref_build_failed","test_ref_build_failed","resume_requested","test_ref_built","checks_completed","pause_requested","action_poisoned","resume_requested","landing_started","rules_source_blocked","pause_requested","action_poisoned","resume_requested","rules_cleared","landing_abandoned"}
 at:=time.Date(2026,10,15,12,0,0,0,time.UTC)
 for _,depth:=range []int{1,4} {
  cfg:=parsed.Queues[0].QueueConfig();cfg.SpeculationMaxDepth=depth
  state:=queue.State{BaseBranch:"main",BaseSHA:"base",Fence:"fence"}
  step:=0
  emit:=func(name string) {
   if !reflect.ValueOf(state).FieldByName("classifications").IsNil(){t.Fatal("legacy classifications allocated")}
   actions:=[]json.RawMessage{};keys:=[]string{}
   for _,action:=range queue.Plan(cfg,state,at.Add(24*time.Hour)) {
    _,wire,err:=queue.EncodeAction(action);if err!=nil{t.Fatal(err)}
    actions=append(actions,wire);keys=append(keys,action.Key())
   }
   row:=struct{Depth,Step int;Event string;Actions []json.RawMessage;Keys []string}{depth,step,name,actions,keys}
   data,err:=json.Marshal(row);if err!=nil{t.Fatal(err)}
   fmt.Println("INERT",string(data));step++
  }
  for i,name:=range names {
   if i==16 {state=queue.Apply(state,queue.BatchStarted{BatchID:"b1",BaseSHA:"base",PRs:[]queue.BatchPR{{PR:2,HeadSHA:"head"}},At:at});emit("batch_started")}
   raw,err:=os.ReadFile("testdata/wire/v1/event_"+name+".json");if err!=nil{t.Fatal(err)}
   event,err:=queue.DecodeEvent(raw);if err!=nil{t.Fatal(err)}
   state=queue.Apply(state,event);emit(name)
  }
 }
}
'''


def replay(root, directory):
    source = directory / "inert_probe_test.go"
    source.write_text(PROBE)
    overlay = directory / (root.name + "-overlay.json")
    overlay.write_text(json.dumps({"Replace": {str(root / "internal/queue/sa4b_inert_probe_test.go"): str(source)}}))
    env = dict(os.environ, GOPROXY="off", GOTOOLCHAIN="local")
    result = subprocess.run(["go", "test", "-overlay", str(overlay), "./internal/queue", "-run", "^TestStageA4bInertReplay$", "-v", "-count=1"], cwd=root, env=env, capture_output=True, text=True)
    if result.returncode:
        raise SystemExit(f"{root}: replay exit={result.returncode}\n{result.stdout}{result.stderr}")
    return [line for line in result.stdout.splitlines() if line.startswith("INERT ")]


def compare(name, old, new):
    assert old == new, f"{name} differs"
    raw = (json.dumps(new, indent=2) + "\n").encode()
    print(f"{name}: {len(new)} identical records; sha256={hashlib.sha256(raw).hexdigest()}", flush=True)


def blocks(path):
    result, current = [], []
    for line in path.read_text().splitlines():
        if re.match(r"^\s+\w+\.go:\d+:", line):
            if current:
                result.append("\n".join(current))
            current = [line]
        elif line.startswith(("===", "---", "    ---", "PASS", "FAIL", "ok ", "go test", "? ")):
            if current:
                result.append("\n".join(current))
                current = []
        elif current and line.strip():
            current.append(line)
    if current:
        result.append("\n".join(current))
    assert result, "no simulator records"
    return sorted(result)


baseline = Path(sys.argv[1]).resolve()
tracked = subprocess.check_output(["git", "ls-files", "-z", "*testdata*.json"], cwd=baseline).decode().split("\0")
fixtures = [name for name in tracked if name]
for name in fixtures:
    assert (baseline / name).read_bytes() == (ROOT / name).read_bytes(), name
print(f"all {len(fixtures)} existing JSON fixtures byte-identical", flush=True)
with tempfile.TemporaryDirectory(prefix="om-sa4b-inert-") as directory:
    old, new = replay(baseline, Path(directory)), replay(ROOT, Path(directory))
    compare("fixture replay, nil classifications, actions/keys, tracked digest", old, new)
    print(new[0], flush=True)
if len(sys.argv) == 4:
    compare("simulator result blocks (runner timing/framing excluded)", blocks(Path(sys.argv[2])), blocks(Path(sys.argv[3])))
print("exit=0")
