#!/usr/bin/env python3
"""legacy-plan-action-depths-1-4: exact offline planner/action differential.

Usage: python3 scripts/check-legacy-plan-action-depths-1-4.py /path/to/main

A test-only Go overlay observes every simulation.plan call in the baseline's
short simulator corpus and the complete required full simulator corpus. At each
checkpoint it calls the planner at BOTH depths 1 and 4. It hashes the ordered wire payload
bytes, action types, keys, first next action, nil-plan distinction and NextWake;
no summary metrics substitute for those bytes. Per-simulation streams are
sorted only after hashing, so parallel test completion cannot reorder actions.
The same overlay and baseline test selection run in both checkouts.

Fixture coverage includes the coherent legacy-block replay journal and every
baseline wire event fixture as an independent one-event journal. Registry
fixtures are NOT presented as a coherent chronological stream. Approval-enabled
activation is outside this no-approvals compatibility gate. Optional multi-day
adaptive sizing exits requiring -sim-adaptive-full-exit are outside this corpus;
required Team/Slow 8x30-day profiles are included.
No database or external forge calls, git fetch, module or toolchain downloads
are used.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
SIM_PROBE = r'''package sim
import (
 "crypto/sha256"
 "encoding/base64"
 "encoding/json"
 "fmt"
 "hash"
 "os"
 "sort"
 "sync"
 "testing"
 "time"
 "github.com/CanadaApollo6/openmerge/internal/queue"
)
type legacyWire struct { Key, Type, Payload string }
type legacyCheckpoint struct {
 At time.Time
 Nil bool
 Plan []legacyWire
 Next *legacyWire
 Wake time.Time
 HasWake bool
}
type legacyStream struct { Config string; Depth int; Checkpoints int; Digest hash.Hash }
var legacyMu sync.Mutex
var legacyStreams []*legacyStream
func legacyObserve(s *simulation) {
 legacyMu.Lock()
 defer legacyMu.Unlock()
 streams:=s.legacyCompatibility
 if streams==nil {streams=map[int]*legacyStream{};s.legacyCompatibility=streams}
 for _,depth:=range []int{1,4} {
  cfg:=s.cfg.Planner
  cfg.SpeculationMinDepth=1;cfg.SpeculationMaxDepth=depth
  stream:=streams[depth]
  if stream==nil {
   raw,err:=json.Marshal(s.cfg);if err!=nil {panic(err)}
   stream=&legacyStream{Config:string(raw),Depth:depth,Digest:sha256.New()};streams[depth]=stream;legacyStreams=append(legacyStreams,stream)
  }
  actions:=queue.Plan(cfg,s.state,s.now)
  record:=legacyCheckpoint{At:s.now,Nil:actions==nil}
  for _,a:=range actions {
   name,payload,err:=queue.EncodeAction(a);if err!=nil {panic(err)}
   record.Plan=append(record.Plan,legacyWire{a.Key(),name,base64.StdEncoding.EncodeToString(payload)})
  }
  if len(record.Plan)>0 {record.Next=&record.Plan[0]}
  record.Wake,record.HasWake=queue.NextWake(cfg,s.state,s.now)
  if err:=json.NewEncoder(stream.Digest).Encode(record);err!=nil {panic(err)}
  stream.Checkpoints++
 }
}
func TestMain(m *testing.M) {
 code:=m.Run()
 lines:=[]string{}
 for _,stream:=range legacyStreams {
  raw,err:=json.Marshal(struct {Config string;Depth,Checkpoints int;SHA256 string}{stream.Config,stream.Depth,stream.Checkpoints,fmt.Sprintf("%x",stream.Digest.Sum(nil))});if err!=nil {panic(err)}
  lines=append(lines,string(raw))
 }
 sort.Strings(lines)
 output,err:=os.Create(os.Getenv("OM_LEGACY_OUTPUT"));if err!=nil {panic(err)}
 for _,line:=range lines {fmt.Fprintln(output,line)}
 if err:=output.Close();err!=nil {panic(err)}
 os.Exit(code)
}
'''
FIXTURE_PROBE = r'''package queue
import (
 "encoding/base64"
 "encoding/json"
 "os"
 "path/filepath"
 "testing"
 "time"
)
type legacyFixtureWire struct { Key, Type, Payload string }
func TestLegacyPlanActionFixtureCompatibility(t *testing.T) {
 raw,err:=os.ReadFile(os.Getenv("OM_LEGACY_FIXTURES"));if err!=nil {t.Fatal(err)}
 var paths []string;if err:=json.Unmarshal(raw,&paths);err!=nil {t.Fatal(err)}
 output,err:=os.Create(os.Getenv("OM_LEGACY_OUTPUT"));if err!=nil {t.Fatal(err)};defer output.Close()
 enc:=json.NewEncoder(output)
 emit:=func(name string,index,depth int,s State,at time.Time) {
  cfg:=testConfig;cfg.SpeculationMinDepth=1;cfg.SpeculationMaxDepth=depth
  actions:=Plan(cfg,s,at)
  var plan []legacyFixtureWire
  for _,a:=range actions {kind,wire,err:=EncodeAction(a);if err!=nil {t.Fatal(err)};plan=append(plan,legacyFixtureWire{a.Key(),kind,base64.StdEncoding.EncodeToString(wire)})}
  var next *legacyFixtureWire;if len(plan)>0 {next=&plan[0]}
  wake,hasWake:=NextWake(cfg,s,at)
  record:=struct{Name string;Index,Depth int;Nil bool;Plan []legacyFixtureWire;Next *legacyFixtureWire;Wake time.Time;HasWake bool}{name,index,depth,actions==nil,plan,next,wake,hasWake}
  if err:=enc.Encode(record);err!=nil {t.Fatal(err)}
 }
 initial:=func()State{return State{BaseBranch:"main",BaseSHA:"base",Fence:"fence"}}
 for _,path:=range paths {
  raw,err:=os.ReadFile(path);if err!=nil {t.Fatal(err)}
  event,err:=DecodeEvent(raw);if err!=nil {t.Fatal(err)}
  state:=Apply(initial(),event)
  for _,depth:=range []int{1,4} {emit("independent-event/"+filepath.Base(path),1,depth,state,epoch.Add(24*time.Hour))}
 }
 // This is the existing TestLegacyBlockReplayGolden journal, including its
 // batch-start insertion. Do not manufacture a stream by sorting registry names.
 names:=[]string{"pr_observed","enqueue_requested","pause_requested","action_poisoned","action_poisoned","pause_requested","rules_blocked","rules_source_blocked","resume_requested","rules_cleared","rules_source_cleared","rules_cleared","rules_source_cleared","pause_requested","resume_requested","resume_requested","test_ref_build_failed","test_ref_build_failed","test_ref_build_failed","resume_requested","test_ref_built","checks_completed","pause_requested","action_poisoned","resume_requested","landing_started","rules_source_blocked","pause_requested","action_poisoned","resume_requested","rules_cleared","landing_abandoned"}
 state:=initial()
 for i,name:=range names {
  if i==16 {state=Apply(state,BatchStarted{BatchID:"b1",BaseSHA:"base",PRs:[]BatchPR{{PR:2,HeadSHA:"head"}},At:epoch})}
  raw,err:=os.ReadFile(filepath.Join(os.Getenv("OM_LEGACY_FIXTURE_ROOT"),"event_"+name+".json"));if err!=nil {t.Fatal(err)}
  event,err:=DecodeEvent(raw);if err!=nil {t.Fatal(err)}
  state=Apply(state,event)
  for _,depth:=range []int{1,4} {emit("legacy-block-replay",i+1,depth,state,epoch.Add(24*time.Hour))}
 }
}
'''


def run(root, directory, name, package, tests, replacements, short=False):
    output = directory / f"{name}.jsonl"
    overlay = directory / f"{name}-overlay.json"
    overlay.write_text(json.dumps({"Replace": replacements}))
    env = dict(os.environ, GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local",
               OM_LEGACY_OUTPUT=str(output), OM_LEGACY_FIXTURES=str(directory / "fixtures.json"),
               OM_LEGACY_FIXTURE_ROOT=str(directory / "fixture-root"))
    command = ["go", "test", "-overlay", str(overlay), package, "-run", tests,
               "-count=1", "-parallel=1", "-timeout=30m"]
    if short:
        command.append("-short")
    result = subprocess.run(command, cwd=root, env=env, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError(f"{name}: go test exit={result.returncode}\n{result.stdout}{result.stderr}")
    records = output.read_bytes().splitlines()
    if not records:
        raise RuntimeError(f"{name}: no planner observations; refusing PASS")
    print(f"{name}: go test exit=0; records={len(records)}", flush=True)
    return records


def compare(name, old, new):
    if old != new:
        for index in range(max(len(old), len(new))):
            left = old[index].decode() if index < len(old) else "<missing>"
            right = new[index].decode() if index < len(new) else "<missing>"
            if left != right:
                raise RuntimeError(f"{name}: first differing record {index + 1}\nbaseline={left}\ncandidate={right}")
    digest = hashlib.sha256(b"\n".join(new) + b"\n").hexdigest()
    checkpoints = sum(json.loads(row).get("Checkpoints", 1) for row in new)
    depths = sorted({json.loads(row)["Depth"] for row in new})
    if depths != [1, 4]:
        raise RuntimeError(f"{name}: missing required depth: {depths}")
    print(f"{name}: PASS; depths=1,4; exact wire checkpoint streams={checkpoints}; sha256={digest}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("baseline", type=Path)
    parser.add_argument("--candidate", type=Path, default=ROOT)
    args = parser.parse_args()
    baseline, candidate = args.baseline.resolve(), args.candidate.resolve()
    cache = Path.home() / ".cache/openmerge-legacy-plan"
    cache.mkdir(parents=True, exist_ok=True)
    try:
        with tempfile.TemporaryDirectory(prefix="depths-1-4-", dir=cache) as temporary:
            directory = Path(temporary)
            sim_probe = directory / "sim_probe_test.go"
            sim_probe.write_text(SIM_PROBE)
            fixture_probe = directory / "fixture_probe_test.go"
            fixture_probe.write_text(FIXTURE_PROBE)
            fixture_root = directory / "fixture-root"
            fixture_root.mkdir()
            fixtures = sorted((baseline / "internal/queue/testdata/wire/v1").glob("event_*.json"))
            if not fixtures:
                raise RuntimeError("baseline event fixtures missing")
            for path in fixtures:
                (fixture_root / path.name).write_bytes(path.read_bytes())
            (directory / "fixtures.json").write_text(json.dumps([str(fixture_root / p.name) for p in fixtures]))
            # Pin baseline names; candidate-only tests cannot alter corpus size.
            tests = sorted({name for path in (baseline / "internal/sim").glob("*_test.go")
                            for name in re.findall(r"^func (Test\w+)\(t \*testing.T\)", path.read_text(), re.M)})
            if not tests:
                raise RuntimeError("baseline simulator tests missing")
            corpus = "^(?:" + "|".join(tests) + ")$"
            for label, selection, short in (("simulator-short-corpus", corpus, True),
                                             ("simulator-required-full-corpus", corpus, False)):
                streams = []
                for name, root in (("baseline", baseline), ("candidate", candidate)):
                    source = (root / "internal/sim/sim.go").read_text()
                    needle = "func (s *simulation) plan() ([]queue.Action, error) {"
                    if source.count(needle) != 1:
                        raise RuntimeError(f"{root}: simulator plan overlay anchor changed")
                    instrumented = directory / f"{name}-sim.go"
                    source = source.replace(needle, needle + "\n legacyObserve(s)")
                    struct_anchor = "type simulation struct {"
                    if source.count(struct_anchor) != 1:
                        raise RuntimeError(f"{root}: simulation struct overlay anchor changed")
                    source = source.replace(struct_anchor, struct_anchor + "\n legacyCompatibility map[int]*legacyStream")
                    instrumented.write_text(source)
                    replacements = {str(root / "internal/sim/sim.go"): str(instrumented),
                                    str(root / "internal/sim/legacy_plan_action_probe_test.go"): str(sim_probe)}
                    streams.append(run(root, directory, f"{label}-{name}", "./internal/sim", selection, replacements, short))
                compare(label, *streams)
            streams = []
            for name, root in (("baseline", baseline), ("candidate", candidate)):
                replacements = {str(root / "internal/queue/legacy_plan_action_fixture_probe_test.go"): str(fixture_probe)}
                streams.append(run(root, directory, f"fixture-journals-{name}", "./internal/queue",
                                   "^TestLegacyPlanActionFixtureCompatibility$", replacements))
            compare("fixture-journals", *streams)
        print("legacy-plan-action-depths-1-4: PASS; nil-only exceptions: none (plans/actions/wakes are exact); exit=0")
        return 0
    except (OSError, RuntimeError) as error:
        print(f"legacy-plan-action-depths-1-4: FAIL: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
