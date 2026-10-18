#!/usr/bin/env python3
"""Offline ADR 0007 gates 2/4 using exported JSONL and local Git objects only.

The identical Go test overlay is compiled in each checkout. No DB, forge, fetch,
module download, or toolchain download is allowed. See docs/operating.md.
"""

import argparse
import hashlib
import itertools
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
CONFIG_PATHS = (".openmerge.yml", ".openmerge.yaml", ".github/openmerge.yml", ".github/openmerge.yaml")
# Deliberately fail closed on an unknown API, rather than substitute a partial
# state projection. Verified on a4b173f and ac7ceb0. Future adapters must preserve
# these semantics; keep the same adapter in BOTH trees for a comparison.
API_ADAPTERS = ["wire-v1 / queue.Apply / serve.worker.plannerConfig / queue.EncodeSnapshot"]
PROBE = r'''package serve

import (
 "bytes"
 "crypto/sha256"
 "encoding/json"
 "fmt"
 "os"
 "strconv"
 "testing"
 "time"

 "github.com/CanadaApollo6/openmerge/internal/config"
 "github.com/CanadaApollo6/openmerge/internal/queue"
)

type replayQueue struct {
 ID int64
 Base string
 InitialFence string `json:"initial_fence"`
}
type replayEvent struct {
 ID int64
 QueueID int64 `json:"queue_id"`
 Type string
 Payload json.RawMessage
 RecordedAt time.Time `json:"recorded_at"`
 Policy string
}
type replayInput struct {
 Queues []replayQueue
 Events []replayEvent
 Policies map[string]string
 Live string
 DefaultBranch string
 PollSeconds int
 DeleteHead bool
}
func TestLiveJournalReplayProbe(t *testing.T) {
 raw,err:=os.ReadFile(os.Getenv("OM_REPLAY_INPUT")); if err!=nil {t.Fatal(err)}
 var in replayInput
 if err=json.Unmarshal(raw,&in); err!=nil {t.Fatal(err)}
 output,err:=os.Create(os.Getenv("OM_REPLAY_OUTPUT")); if err!=nil {t.Fatal(err)}
 defer output.Close()
 enc:=json.NewEncoder(output)
 emit:=func(v any) {if err:=enc.Encode(v); err!=nil {t.Fatal(err)}}
 states:=map[int64]queue.State{}
 workers:=map[int64]*worker{}
 selectPolicy:=func(content,base string) (config.Queue,string) {
  cfg,_,err:=config.Load([]byte(content)); if err!=nil {t.Fatal(err)}
  selected,diagnostics:=cfg.ResolveBranch(base,in.DefaultBranch)
  if len(diagnostics)!=0 {t.Fatal(diagnostics)}
  cleanup:=in.DeleteHead
  if cfg.DeleteHeadBranch!=nil {cleanup=*cfg.DeleteHeadBranch}
  return selected,selected.Digest(cleanup)
 }
 for _,q:=range in.Queues {
  if q.Base=="" {t.Fatalf("queue %d: missing base",q.ID)}
  states[q.ID]=queue.State{BaseBranch:q.Base,Fence:q.InitialFence}
  workers[q.ID]=&worker{}
 }
 for _,e:=range in.Events {
  if e.RecordedAt.IsZero() {t.Fatalf("event %d: missing recorded_at",e.ID)}
  state:=states[e.QueueID]
  w:=workers[e.QueueID]
  event,err:=queue.DecodeEvent(e.Payload); if err!=nil {t.Fatalf("event %d: %v",e.ID,err)}
  selected,digest:=selectPolicy(in.Policies[e.Policy],state.BaseBranch)
  // Historical digests bind the policy, including the cleanup default. Legacy
  // rows before ConfigApplied use the config at their BaseMoved commit, but
  // retain the empty planner digest just as pre-policy journals did.
  if applied,ok:=event.(queue.ConfigApplied); ok && applied.Digest!=digest {
   t.Fatalf("event %d: historical digest %s != recomputed %s",e.ID,applied.Digest,digest)
  }
  state=queue.Apply(state,event)
  w.cfg=selected; w.configDigest=state.ConfigDigest
  cfg:=w.plannerConfig(time.Duration(in.PollSeconds)*time.Second)
  actions:=[]any{}
  for _,a:=range queue.Plan(cfg,state,e.RecordedAt) {
   name,payload,err:=queue.EncodeAction(a); if err!=nil {t.Fatal(err)}
   key:=a.Key()
   // Test-only fault injection, applied to the candidate subprocess only.
   if os.Getenv("OM_REPLAY_PERTURB_EVENT")==strconv.FormatInt(e.ID,10) {key+="-injected-divergence"}
   actions=append(actions,map[string]any{"key":key,"type":name,"payload":json.RawMessage(payload)})
  }
  snapshot,err:=queue.EncodeSnapshot(state); if err!=nil {t.Fatalf("event %d: %v",e.ID,err)}
  // Canonicalize maps and wire JSON independently of encoder struct field order.
  var canonical any
  decoder:=json.NewDecoder(bytes.NewReader(snapshot)); decoder.UseNumber()
  if err=decoder.Decode(&canonical); err!=nil {t.Fatal(err)}
  snapshot,err=json.Marshal(canonical); if err!=nil {t.Fatal(err)}
  var fields map[string]json.RawMessage
  if err=json.Unmarshal(snapshot,&fields); err!=nil {t.Fatal(err)}
  revision:=fields["Revision"]
  if revision==nil {t.Fatal("snapshot adapter missing Revision")}
  emit(map[string]any{"kind":"checkpoint","event_id":e.ID,"queue_id":e.QueueID,"revision":revision,"actions":actions,"state_sha256":fmt.Sprintf("%x",sha256.Sum256(snapshot))})
  states[e.QueueID]=state
 }
 for _,q:=range in.Queues {
  state:=states[q.ID]
  selected,digest:=selectPolicy(in.Live,q.Base)
  // Same comparison as serve/reconcile.go:configuration, including the
  // same-digest bounds initialization for old speculative journals.
  wouldApply:=state.ConfigDigest!=digest || selected.QueueConfig().MaxDepth()>1 && !state.WindowInitialized()
  emit(map[string]any{"kind":"policy","queue_id":q.ID,"journal_digest":state.ConfigDigest,"recomputed_digest":digest,"would_emit_config_applied":wouldApply})
 }
}
'''


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], text=True).strip()


def rows(path):
    with path.open() as source:
        return [json.loads(line) for line in source]


def config_at(commit, path=None):
    # ls-tree distinguishes an absent blob from missing local history. Never fetch.
    paths = [path] if path else CONFIG_PATHS
    present = git(ROOT, "ls-tree", "--name-only", commit, "--", *paths).splitlines()
    if len(present) != 1:
        raise ValueError(f"{commit}: expected one config among {paths}, found {present}")
    return subprocess.check_output(["git", "-C", str(ROOT), "show", f"{commit}:{present[0]}"], text=True)


def prepare(args):
    events, queues = rows(args.events), rows(args.queues)
    if not events or not queues:
        raise ValueError("empty export cannot establish deployment gates")
    ids = [q["id"] for q in queues]
    if len(set(ids)) != len(ids):
        raise ValueError("duplicate queue id")
    previous = 0
    active = {}
    policies = {}
    # Before ConfigApplied existed, configuration was pinned by the base tip.
    # Resolve that local blob instead of incorrectly using today's policy.
    initial = {}
    for e in events:
        if e["type"] == "base_moved":
            initial.setdefault(e["queue_id"], e["payload"]["data"]["sha"])
    for e in events:
        if e["id"] <= previous or e["queue_id"] not in ids:
            raise ValueError(f"event {e['id']}: unordered/duplicate id or unknown queue")
        previous = e["id"]
        if e["payload"]["type"] != e["type"]:
            raise ValueError(f"event {e['id']}: row/envelope type mismatch")
        qid = e["queue_id"]
        data = e["payload"]["data"]
        if e["type"] == "config_applied":
            active[qid] = data["commit"]
        if qid not in active:
            if e["type"] == "base_moved":
                initial[qid] = data["sha"]
            commit = initial.get(qid)
            if not commit:
                raise ValueError(f"event {e['id']}: no historical policy; missing BaseMoved/ConfigApplied")
        else:
            commit = active[qid]
        if commit not in policies:
            policies[commit] = config_at(commit)
        e["Policy"] = commit
    return dict(Queues=sorted(queues, key=lambda q: q["id"]), Events=events,
                Policies=policies, Live=config_at("HEAD", args.config_file),
                DefaultBranch=args.default_branch, PollSeconds=args.poll_interval_seconds,
                DeleteHead=not args.keep_head_branch)


def probe(root, directory, name, args):
    output = directory / f"{name}.jsonl"
    overlay = directory / f"{name}-overlay.json"
    overlay.write_text(json.dumps({"Replace": {
        str(root / "internal/serve/live_journal_replay_probe_test.go"): str(directory / "probe_test.go")}}))
    env = dict(os.environ, GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local",
               OM_REPLAY_INPUT=str(directory / "input.json"), OM_REPLAY_OUTPUT=str(output))
    env.pop("OM_REPLAY_PERTURB_EVENT", None)
    if name == "candidate" and args.inject_divergence_event:
        env["OM_REPLAY_PERTURB_EVENT"] = str(args.inject_divergence_event)
    result = subprocess.run(["go", "test", "-overlay", str(overlay), "./internal/serve",
                             "-run", "^TestLiveJournalReplayProbe$", "-count=1", "-timeout=5m"],
                            cwd=root, env=env, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError(f"{name}: go test exit={result.returncode}\n{result.stdout}{result.stderr}")
    # Canonical lines are the stream compared and hashed, including final policy
    # records. Retain actual action order; sorting actions could hide a change.
    return [json.dumps(json.loads(line), sort_keys=True, separators=(",", ":"))
            for line in output.read_text().splitlines()]


def compare(old, new):
    for left, right in itertools.zip_longest(old, new):
        if left != right:
            record = json.loads(left or right)
            return dict(event_id=record.get("event_id"), queue_id=record["queue_id"],
                        baseline=left, candidate=right)
    return None


def stream_summary(lines):
    return dict(checkpoints=sum(json.loads(line)["kind"] == "checkpoint" for line in lines),
                records=len(lines), sha256=hashlib.sha256(("\n".join(lines) + "\n").encode()).hexdigest(),
                policies=[json.loads(line) for line in lines if json.loads(line)["kind"] == "policy"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", required=True, type=Path)
    parser.add_argument("--events", required=True, type=Path)
    parser.add_argument("--queues", required=True, type=Path)
    parser.add_argument("--config-file", help="path in candidate HEAD (default: discover OpenMerge config)")
    parser.add_argument("--out", type=Path)
    parser.add_argument("--default-branch", default="main")
    parser.add_argument("--poll-interval-seconds", type=int, default=60)
    parser.add_argument("--keep-head-branch", action="store_true", help="match serve --delete-head-branch=false")
    parser.add_argument("--inject-divergence-event", type=int, help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.poll_interval_seconds <= 0:
        parser.error("poll interval must be positive")
    try:
        args.baseline = args.baseline.resolve()
        inputs = prepare(args)
        cache = Path.home() / ".cache/openmerge-replay"
        cache.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="probe-", dir=cache) as temporary:
            directory = Path(temporary)
            (directory / "input.json").write_text(json.dumps(inputs))
            (directory / "probe_test.go").write_text(PROBE)
            old = probe(args.baseline, directory, "baseline", args)
            new = probe(ROOT, directory, "candidate", args)
        difference = compare(old, new)
        summaries = dict(baseline=stream_summary(old), candidate=stream_summary(new))
        gate4 = all(p["journal_digest"] and not p["would_emit_config_applied"]
                    for summary in summaries.values() for p in summary["policies"])
        report = dict(baseline_commit=git(args.baseline, "rev-parse", "HEAD"),
                      candidate_commit=git(ROOT, "rev-parse", "HEAD"),
                      adapter=API_ADAPTERS[0], events=len(inputs["Events"]), queues=len(inputs["Queues"]),
                      exports={str(p.resolve()): hashlib.sha256(p.read_bytes()).hexdigest()
                               for p in (args.events, args.queues)},
                      clock="event recorded_at", poll_interval_seconds=args.poll_interval_seconds,
                      delete_head_branch_default=not args.keep_head_branch, default_branch=args.default_branch,
                      config_sha256=hashlib.sha256(inputs["Live"].encode()).hexdigest(),
                      streams=summaries, first_difference=difference,
                      gate2="PASS" if difference is None else "FAIL", gate4="PASS" if gate4 else "FAIL",
                      verdict="PASS" if difference is None and gate4 else "FAIL")
        rendered = json.dumps(report, indent=2, sort_keys=True)
        print(rendered)
        if args.out:
            args.out.write_text(rendered + "\n")
        return 0 if report["verdict"] == "PASS" else 1
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError, RuntimeError) as error:
        print(f"replay error: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
