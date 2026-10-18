#!/usr/bin/env python3
"""Offline ADR 0007 gates 2/4 using exported JSONL and local Git objects only.

The identical Go test overlay is compiled in each checkout. No DB, forge, fetch,
module download, or toolchain download is allowed. See docs/operating.md.
"""

import argparse
import base64
from datetime import datetime
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
# Descriptive API label, not a registry or compatibility/version detector.
# Compilation fails closed if the checkout does not implement this probe API.
API_ADAPTERS = ["wire-v1 / queue.Apply / serve.worker.plannerConfig / queue.NextWake / reflection"]
PROBE = r'''package serve

import (
 "encoding/base64"
 "crypto/sha256"
 "encoding/json"
 "fmt"
 "os"
 "strconv"
 "reflect"
 "sort"
 "unsafe"
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
 Boundaries map[string][]time.Time
}
// Reflection deliberately bypasses the snapshot codec and visibility boundary.
// This read-only test overlay never enters a production binary.
type replayVisit struct {
 Type reflect.Type
 Pointer unsafe.Pointer
 Length int
}
func deep(v reflect.Value, path string, active map[replayVisit]string) any {
 if !v.IsValid() { return nil }
 if v.CanAddr() { v=reflect.NewAt(v.Type(),unsafe.Pointer(v.UnsafeAddr())).Elem() }
 // Track only the current ancestry: shared acyclic pointees are valid.
 // Maps and slices can also form cycles through interface values.
 if (v.Kind()==reflect.Pointer || v.Kind()==reflect.Map || v.Kind()==reflect.Slice) && !v.IsNil() {
  visit:=replayVisit{Type:v.Type(),Pointer:v.UnsafePointer()}
  if v.Kind()==reflect.Slice {visit.Length=v.Len()}
  if ancestor,ok:=active[visit]; ok {panic(fmt.Sprintf("structural cycle at %s (ancestor %s)",path,ancestor))}
  active[visit]=path
  defer delete(active,visit)
 }
 n:=map[string]any{"type":v.Type().String(),"kind":v.Kind().String(),"zero":v.IsZero()}
 if v.Type()==reflect.TypeOf(time.Time{}) {
  n["value"]=v.Interface().(time.Time).UTC().Format(time.RFC3339Nano); return n
 }
 switch v.Kind() {
 case reflect.Struct:
  fields:=map[string]any{}; for i:=0;i<v.NumField();i++ {fields[v.Type().Field(i).Name]=deep(v.Field(i),path+"."+v.Type().Field(i).Name,active)}; n["value"]=fields
 case reflect.Map:
  n["nil"]=v.IsNil(); fields:=map[string]any{}
  for _,k:=range v.MapKeys() {
   raw,err:=json.Marshal(deep(k,path+"[key]",active)); if err!=nil {panic(err)}
   key:=string(raw)
   if _,exists:=fields[key]; exists {panic(fmt.Sprintf("canonical map-key collision at %s; refusing to lose an entry",path))}
   fields[key]=deep(v.MapIndex(k),path+"["+key+"]",active)
  }; n["value"]=fields
 case reflect.Slice,reflect.Array:
  if v.Kind()==reflect.Slice {n["nil"]=v.IsNil()}; values:=[]any{}; for i:=0;i<v.Len();i++ {values=append(values,deep(v.Index(i),fmt.Sprintf("%s[%d]",path,i),active))}; n["value"]=values
 case reflect.Pointer,reflect.Interface:
  n["nil"]=v.IsNil(); if !v.IsNil() {n["value"]=deep(v.Elem(),path+".*",active)}
 case reflect.Bool: n["value"]=v.Bool()
 case reflect.String: n["value"]=v.String()
 case reflect.Int,reflect.Int8,reflect.Int16,reflect.Int32,reflect.Int64: n["value"]=v.Int()
 case reflect.Uint,reflect.Uint8,reflect.Uint16,reflect.Uint32,reflect.Uint64: n["value"]=v.Uint()
 case reflect.Float32,reflect.Float64: n["value"]=v.Float()
 default: panic(fmt.Sprintf("unsupported structural kind %s",v.Kind()))
 }
 return n
}
const boundaryCap=128
func wakeSet(cfg queue.Config,state queue.State,now time.Time) []time.Time {
 result:=[]time.Time{}
 for {at,ok:=queue.NextWake(cfg,state,now); if !ok {return result}; if !at.After(now) {panic("NextWake did not advance")}; result=append(result,at.UTC()); if len(result)>=boundaryCap {panic("deadline cap hit (128); refusing PASS")}; now=at}
}
func planned(cfg queue.Config,state queue.State,now time.Time) []any {
 result:=[]any{}
 for _,a:=range queue.Plan(cfg,state,now) {name,payload,err:=queue.EncodeAction(a); if err!=nil {panic(err)}; result=append(result,map[string]any{"key":a.Key(),"type":name,"payload_base64":base64.StdEncoding.EncodeToString(payload)})}
 return result
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
 checkpoint:=func(state queue.State,cfg queue.Config,e replayEvent,final bool) {
  actions:=[]any{}
  for _,a:=range queue.Plan(cfg,state,e.RecordedAt) {
   name,payload,err:=queue.EncodeAction(a); if err!=nil {t.Fatal(err)}
   key:=a.Key()
   // Test-only fault injection, applied to the candidate subprocess only.
   if os.Getenv("OM_REPLAY_PERTURB_EVENT")==strconv.FormatInt(e.ID,10) {key+="-injected-divergence"}
   actions=append(actions,map[string]any{"key":key,"type":name,"payload_base64":base64.StdEncoding.EncodeToString(payload)})
  }
  snapshot,err:=queue.EncodeSnapshot(state); if err!=nil {t.Fatalf("event %d: %v",e.ID,err)}
  var fields map[string]json.RawMessage
  if err=json.Unmarshal(snapshot,&fields); err!=nil {t.Fatal(err)}
  revision:=fields["Revision"]
  if revision==nil {t.Fatal("snapshot adapter missing Revision")}
  record:=map[string]any{"kind":"checkpoint","event_id":e.ID,"queue_id":e.QueueID,"revision":revision,"actions":actions,"snapshot_base64":snapshot,"state_sha256":fmt.Sprintf("%x",sha256.Sum256(snapshot))}
  record["final"]=final
  boundaryKey:=strconv.FormatInt(e.ID,10); if final {boundaryKey=fmt.Sprintf("final:%d",e.QueueID)}
  record["boundary_key"]=boundaryKey
  record["structural"]=deep(reflect.ValueOf(&state).Elem(),"State",map[replayVisit]string{})
  at,ok:=queue.NextWake(cfg,state,e.RecordedAt)
  record["next_wake"]=map[string]any{"at":at.UTC(),"present":ok}
  // NextWake intentionally omits elapsed timers. Enumerate the same worker
  // API from the zero clock as well, so a late observation cannot hide a
  // timeout's strict boundary. This does not mutate or execute the state.
  record["deadlines"]=wakeSet(cfg,state,time.Time{})
  probes:=[]any{}
  instantSet:=map[time.Time]bool{}
  for _,at:=range in.Boundaries[boundaryKey] {for _,delta:=range []time.Duration{-time.Nanosecond,0,time.Nanosecond} {instantSet[at.Add(delta)]=true}}
  instants:=[]time.Time{}; for at:=range instantSet {instants=append(instants,at)}; sort.Slice(instants,func(i,j int)bool{return instants[i].Before(instants[j])})
  if len(instants)>=boundaryCap {t.Fatalf("event %d queue %d final=%v: boundary instant cap hit: count=%d cap=%d; refusing PASS",e.ID,e.QueueID,final,len(instants),boundaryCap)}
  for _,at:=range instants {probes=append(probes,map[string]any{"at":at,"actions":planned(cfg,state,at)})}
  record["boundary_probes"]=probes
  emit(record)
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
  checkpoint(state,cfg,e,false)
  states[e.QueueID]=state
 }
 horizon:=in.Events[0].RecordedAt; for _,e:=range in.Events {if e.RecordedAt.After(horizon) {horizon=e.RecordedAt}}
 for _,q:=range in.Queues {
  state:=states[q.ID]
  selected,digest:=selectPolicy(in.Live,q.Base)
  w:=workers[q.ID]; w.cfg=selected; w.configDigest=state.ConfigDigest
  lastID:=int64(0); for _,e:=range in.Events {if e.QueueID==q.ID {lastID=e.ID}}
  checkpoint(state,w.plannerConfig(time.Duration(in.PollSeconds)*time.Second),replayEvent{ID:lastID,QueueID:q.ID,RecordedAt:horizon},true)
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
    output = args.stream_directory / f"{name}.jsonl"
    overlay = directory / f"{name}-overlay.json"
    replacements = {str(root / "internal/serve/live_journal_replay_probe_test.go"): str(directory / "probe_test.go")}
    if name == "baseline" and args.baseline_overlay:
        replacements.update(json.loads(args.baseline_overlay.read_text())["Replace"])
    if name.startswith("candidate") and args.candidate_overlay:
        replacements.update(json.loads(args.candidate_overlay.read_text())["Replace"])
    overlay.write_text(json.dumps({"Replace": replacements}))
    env = dict(os.environ, GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local",
               OM_REPLAY_INPUT=str(directory / "input.json"), OM_REPLAY_OUTPUT=str(output))
    env.pop("OM_REPLAY_PERTURB_EVENT", None)
    if name.startswith("candidate") and args.inject_divergence_event:
        env["OM_REPLAY_PERTURB_EVENT"] = str(args.inject_divergence_event)
    result = subprocess.run(["go", "test", "-overlay", str(overlay), "./internal/serve",
                             "-run", "^TestLiveJournalReplayProbe$", "-count=1", "-timeout=5m"],
                            cwd=root, env=env, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError(f"{name}: go test exit={result.returncode}\n{result.stdout}{result.stderr}")
    # Canonical lines are the stream compared and hashed, including final policy
    # records. Retain actual action order; sorting actions could hide a change.
    lines = [json.dumps(json.loads(line), sort_keys=True, separators=(",", ":"))
             for line in output.read_text().splitlines()]
    output.write_text("\n".join(lines) + "\n")
    return lines


# ADR 0007 PR 1: old journal observations establish this replayable side
# projection, without inventing accountability, classification, or approvals.
# This is the only top-level exemption from legacy state equality.
DERIVED_FIELDS = {
    "ChangeRevisions": {"ChangeID": str, "Revision": int, "HeadSHA": str,
                        "Basis": {"base_sha": str, "at": str}, "LogPosition": int},
    "BatchRevisions": {"PR": int, "Revision": int, "HeadSHA": str},
}


def snapshot(record):
    return json.loads(base64.b64decode(record["snapshot_base64"])) if record.get("snapshot_base64") else {}


def nonempty(value):
    return value not in (None, False, 0, "", [], {})


def legacy_differences(left, right, path=""):
    # Version is codec metadata. Shared fields, including null vs empty, remain
    # exact. Only absent, newly added zero fields are equivalent to absence.
    differences = {}
    for key in sorted(left.keys() | right.keys()):
        if not path and (key == "Version" or key in DERIVED_FIELDS and not nonempty(left.get(key))):
            continue
        name = f"{path}.{key}" if path else key
        l, r = left.get(key), right.get(key)
        if key not in left or key not in right:
            differences[name] = dict(baseline=l, candidate=r, new_field=True)
        elif isinstance(l, dict) and isinstance(r, dict):
            differences.update(legacy_differences(l, r, name))
        elif not exact(l, r):
            differences[name] = dict(baseline=l, candidate=r)
    return differences


def derived_violations(fields):
    # Positive schema validation prevents accountability/approval/classification
    # data from hiding inside an exempt container (including future additions).
    violations = {}

    def check(value, schema, path):
        if not isinstance(value, dict):
            violations[path] = value
            return
        for key in schema:
            if key not in value:
                violations[f"{path}.{key}"] = "missing required field"
        if type(value.get("Revision")) is int and value["Revision"] <= 0:
            violations[f"{path}.Revision"] = value["Revision"]
        if "HeadSHA" in schema and not value.get("HeadSHA"):
            violations[f"{path}.HeadSHA"] = "empty HeadSHA"
        for key, item in value.items():
            expected = schema.get(key)
            name = f"{path}.{key}"
            if expected is None:
                violations[name] = item
            elif isinstance(expected, dict):
                check(item, expected, name)
            elif type(item) is not expected:
                violations[name] = item

    for field, schema in DERIVED_FIELDS.items():
        values = fields.get(field)
        if values is None:
            values = {}
        if not isinstance(values, dict):
            violations[field] = values
            continue
        for key, value in values.items():
            if field == "BatchRevisions":
                if not isinstance(value, list):
                    violations[f"{field}.{key}"] = value
                    continue
                for index, item in enumerate(value):
                    check(item, schema, f"{field}.{key}[{index}]")
            else:
                check(value, schema, f"{field}.{key}")
    return violations


# Only these additions may be absent in the baseline, and only at zero.
# Populated after inspecting the real baseline -> main structural comparison.
STATE_EXCEPTIONS = {
    "landingDecisions": {
        "reason": "Stage A 9 adds frozen decision evidence; only an absent-to-nil map is permitted for legacy journals.",
        "reference": "ADR 0007 PR 9 (Hold)",
    },
    "approvalWaits": {
        "reason": "Stage A 8 adds dormant approval waits. Only an absent-to-nil map is allowed; populated waits fail legacy comparison.",
        "reference": "ADR 0007 PR 8 (Hold)",
    },
    "receipts": {
        "reason": "Stage A 7a adds dormant receipt acceptance/rejection evidence; only a nil map is allowed for a legacy deployment pair.",
        "reference": "ADR 0007 PR 7a",
    },
    "approvalFacts": {
        "reason": "PR #71 adds dormant approval evidence. Permit only the absent-to-nil field; populated evidence requires a separate activation review.",
        "reference": "PR #71; Stage A 6 inertness",
    },
    "outsidePushes": {
        "reason": "PR #53 adds the audit-only outside-push projection; the pre-Stage-A export has none. Permit only its nil/zero state; any recorded outside push fails.",
        "reference": "PR #53; ADR 0007 outside-push isolation",
    },
}
STRUCTURAL_DERIVED = {"changeRevisions": "ChangeRevisions", "batchRevisions": "BatchRevisions"}


def exact(left, right):
    if type(left) is not type(right):
        return False
    if isinstance(left, dict):
        return left.keys() == right.keys() and all(exact(left[k], right[k]) for k in left)
    if isinstance(left, list):
        return len(left) == len(right) and all(exact(a, b) for a, b in zip(left, right))
    return left == right


def structural_differences(left, right, path=""):
    differences = {}
    if isinstance(left, list) and isinstance(right, list):
        for i, (l, r) in enumerate(itertools.zip_longest(left, right)):
            differences.update(structural_differences(l, r, f"{path}[{i}]"))
        return differences
    if not isinstance(left, dict) or not isinstance(right, dict):
        return {} if exact(left, right) else {path: dict(baseline=left, candidate=right)}
    # Typed nodes retain metadata independently of their children.
    if type(left.get("kind")) is str and type(right.get("kind")) is str:
        lm = {k: v for k, v in left.items() if k not in ("value", "zero")}
        rm = {k: v for k, v in right.items() if k not in ("value", "zero")}
        if not exact(lm, rm):
            differences[path + ".@type"] = dict(baseline=lm, candidate=rm)
        differences.update(structural_differences(left.get("value"), right.get("value"), path))
        return differences
    for key in sorted(left.keys() | right.keys()):
        name = f"{path}.{key}" if path else key
        l, r = left.get(key), right.get(key)
        if not path and key in STRUCTURAL_DERIVED and (l is None or l.get("zero") is True or l.get("value") == {}):
            continue
        if key not in left or key not in right:
            if key not in left and name in STATE_EXCEPTIONS and r.get("zero") is True:
                nil_map_types = {"landingDecisions": "map[string]queue.LandingDecisionRecorded", "approvalFacts": "queue.approvalProjection", "approvalWaits": "map[queue.ChangeID]queue.ApprovalWait", "receipts": "queue.receiptProjection"}
                if name not in nil_map_types or (r.get("type") == nil_map_types[name] and r.get("kind") == "map" and r.get("nil") is True and r.get("value") == {}):
                    continue
            differences[name] = dict(baseline=l, candidate=r, new_field=True)
        else:
            differences.update(structural_differences(l, r, name))
    return differences


def plain(node):
    if not isinstance(node, dict) or "kind" not in node:
        return node
    value = node.get("value")
    if node.get("type") == "time.Time":
        return value
    if node["kind"] == "map":
        return {str(json.loads(k)["value"]): plain(v) for k, v in value.items()}
    if node["kind"] == "struct":
        return {k: plain(v) for k, v in value.items()}
    if node["kind"] in ("slice", "array"):
        return [plain(v) for v in value]
    if node["kind"] in ("ptr", "interface"):
        return plain(value)
    return value


def derived(record):
    state = plain(record.get("structural", {}))
    fields = {public: state.get(private) for private, public in STRUCTURAL_DERIVED.items()}
    changes = fields.get("ChangeRevisions") or {}
    if "shards" in changes:
        fields["ChangeRevisions"] = {k: v for shard in (changes["shards"] or []) if shard for k, v in shard.items()}
    # Internal observation basis uses Go names, unlike the diagnostic codec.
    for value in (fields.get("ChangeRevisions") or {}).values():
        if isinstance(value.get("Basis"), dict):
            value["Basis"] = {({"BaseSHA": "base_sha", "At": "at"}.get(k, k)): v
                              for k, v in value["Basis"].items()}
    return fields


def compare_checkpoints(old, new, strict, repeated=None):
    counts = {field: 0 for field in DERIVED_FIELDS}
    total = actions = states = boundaries = probes = snapshots = 0
    first_action = first_state = first_derived = first_boundary = None
    unexpected = []
    probe_counts = []
    boundary_cap_hit = False
    for left, right, repeat in itertools.zip_longest(old, new, repeated if repeated is not None else new):
        l, r, again = [json.loads(v) if v else {} for v in (left, right, repeat)]
        if l.get("kind") != "checkpoint" and r.get("kind") != "checkpoint":
            continue
        total += 1
        detail = dict(event_id=l.get("event_id", r.get("event_id")), queue_id=l.get("queue_id", r.get("queue_id")))
        aligned = bool(l) and bool(r) and all(exact(l.get(k), r.get(k)) for k in ("kind", "event_id", "queue_id", "final"))
        if aligned and all(exact(l.get(k), r.get(k)) for k in ("revision", "actions")):
            actions += 1
        elif first_action is None:
            first_action = dict(detail, baseline=l.get("actions"), candidate=r.get("actions"))
        differences = structural_differences(l.get("structural"), r.get("structural"))
        if not l.get("structural") or not r.get("structural"):
            differences["@structural"] = dict(baseline=l.get("structural"), candidate=r.get("structural"))
        if aligned and not differences:
            states += 1
        if exact(l.get("state_sha256"), r.get("state_sha256")):
            snapshots += 1
        if (not aligned or differences or strict and not exact(l.get("state_sha256"), r.get("state_sha256"))) and first_state is None:
            first_state = dict(detail, differing_fields=differences)
        candidate, baseline, rerun = derived(r), derived(l), derived(again)
        violations = derived_violations(candidate)
        for label, rec in (("candidate", r), ("baseline", l), ("repeated", again)):
            node = rec.get("structural", {}).get("value", {}).get("changeRevisions")
            if node:
                fields = node.get("value", {})
                if node.get("kind") != "struct" or set(fields) != {"shards"}:
                    violations[label + ".changeRevisions"] = "unreviewed projection wrapper shape"
                else:
                    shards = fields["shards"]
                    if shards.get("kind") != "ptr" or (not shards.get("nil") and shards.get("value", {}).get("kind") != "array"):
                        violations[label + ".changeRevisions.shards"] = "unreviewed projection shard shape"
        violations.update({"baseline." + k: v for k, v in derived_violations(baseline).items()})
        for field in DERIVED_FIELDS:
            if nonempty(candidate.get(field)):
                counts[field] += 1
            if nonempty(baseline.get(field)) and not exact(baseline.get(field), candidate.get(field)):
                violations[field] = "baseline projection already populated; exact equality required"
        violations.update({k: v for k, v in differences.items() if v.get("new_field")})
        if violations:
            unexpected.append(dict(detail, fields=violations))
        projection_nodes = lambda rec: {k: rec.get("structural", {}).get("value", {}).get(k) for k in STRUCTURAL_DERIVED}
        if (not exact(candidate, rerun) or not exact(projection_nodes(r), projection_nodes(again))) and first_derived is None:
            first_derived = dict(detail, candidate=candidate, repeated=rerun)
        boundary_cap_hit |= any(len(rec.get(field, [])) >= 128
                                for rec in (l, r, again)
                                for field in ("deadlines", "boundary_probes"))
        count = len(r.get("boundary_probes", []))
        probes += count
        probe_counts.append(dict(detail, final=r.get("final", False), count=count))
        if aligned and all(exact(l.get(k), r.get(k)) for k in ("next_wake", "deadlines", "boundary_probes")):
            boundaries += 1
        elif first_boundary is None:
            first_boundary = dict(detail, baseline={k: l.get(k) for k in ("next_wake", "deadlines", "boundary_probes")},
                                  candidate={k: r.get(k) for k in ("next_wake", "deadlines", "boundary_probes")})
    return dict(actions_identical=f"{actions}/{total}", structural_state_identical=f"{states}/{total}",
                legacy_state_identical=f"{states}/{total}", state_identical=f"{states}/{total}",
                snapshot_hashes_identical=f"{snapshots}/{total}", boundary_checkpoints_identical=f"{boundaries}/{total}",
                boundary_probe_count=probes, boundary_probe_counts=probe_counts,
                boundary_probe_max=max((p["count"] for p in probe_counts), default=0), boundary_cap_per_checkpoint=128, boundary_cap_hit=boundary_cap_hit,
                first_boundary_divergence=first_boundary, first_action_divergence=first_action,
                first_state_divergence=first_state, derived_nonempty_checkpoints=counts,
                derived_determinism="PASS" if first_derived is None else "FAIL",
                first_derived_divergence=first_derived, non_allowlisted_fields=unexpected)


def boundary_union(old, new):
    result = {}
    for line in old + new:
        record = json.loads(line)
        if record["kind"] != "checkpoint":
            continue
        key = record.get("boundary_key", str(record["event_id"]))
        result.setdefault(key, set()).update(record["deadlines"])
    # Nanosecond arithmetic is done by Go, avoiding Python datetime truncation.
    return {key: sorted(values) for key, values in result.items()}


def stream_summary(lines):
    return dict(checkpoints=sum(json.loads(line)["kind"] == "checkpoint" for line in lines),
                records=len(lines), sha256=hashlib.sha256(("\n".join(lines) + "\n").encode()).hexdigest(),
                policies=[json.loads(line) for line in lines if json.loads(line)["kind"] == "policy"])


def main():
    global ROOT
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", required=True, type=Path)
    parser.add_argument("--candidate", type=Path, default=ROOT, help="candidate checkout (default: script checkout)")
    parser.add_argument("--baseline-overlay", type=Path, help=argparse.SUPPRESS)
    parser.add_argument("--candidate-overlay", type=Path, help=argparse.SUPPRESS)
    parser.add_argument("--events", required=True, type=Path)
    parser.add_argument("--queues", required=True, type=Path)
    parser.add_argument("--config-file", help="path in candidate HEAD (default: discover OpenMerge config)")
    parser.add_argument("--out", type=Path)
    parser.add_argument("--strict-state", action="store_true", help="compare original snapshot hashes (same-codec comparisons)")
    parser.add_argument("--default-branch", default="main")
    parser.add_argument("--poll-interval-seconds", type=int, default=60)
    parser.add_argument("--keep-head-branch", action="store_true", help="match serve --delete-head-branch=false")
    parser.add_argument("--inject-divergence-event", type=int, help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.poll_interval_seconds <= 0:
        parser.error("poll interval must be positive")
    try:
        ROOT = args.candidate.resolve()
        args.baseline = args.baseline.resolve()
        commits = {}
        for name, checkout in (("baseline", args.baseline), ("candidate", ROOT)):
            if git(checkout, "status", "--porcelain"):
                raise ValueError(f"{name} checkout is dirty; refusing replay")
            commits[name] = git(checkout, "rev-parse", "HEAD")
        cache = Path.home() / ".cache/openmerge-replay"
        cache.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="preflight-", dir=cache) as temporary:
            preflight_result = Path(temporary) / "comparison.json"
            env = dict(os.environ, OM_REPLAY_PREFLIGHT_RESULT=str(preflight_result),
                       OM_REPLAY_PREFLIGHT_REQUEST=json.dumps({
                           "baseline": str(args.baseline), "candidate": str(ROOT),
                           "baseline_overlay": str(args.baseline_overlay.resolve()) if args.baseline_overlay else None,
                           "candidate_overlay": str(args.candidate_overlay.resolve()) if args.candidate_overlay else None}))
            selftest = subprocess.run([sys.executable, str(Path(__file__).with_name("test-live-journal-replay.py")), "--preflight"],
                                      capture_output=True, text=True, env=env)
            if not preflight_result.exists():
                raise RuntimeError("mandatory synthetic self-tests did not complete: " + selftest.stdout + selftest.stderr)
            synthetic = json.loads(preflight_result.read_text())
        synthetic_difference = next((synthetic[k] for k in (
            "first_action_divergence", "first_state_divergence", "first_boundary_divergence",
            "first_derived_divergence") if synthetic[k] is not None), None)
        if synthetic_difference is None and synthetic["non_allowlisted_fields"]:
            synthetic_difference = synthetic["non_allowlisted_fields"][0]
        if selftest.returncode or synthetic_difference is not None:
            synthetic_difference = dict(source="mandatory synthetic candidate preflight",
                                        detail=synthetic_difference, self_test_exit_status=selftest.returncode)
        inputs = prepare(args)
        cache = Path.home() / ".cache/openmerge-replay"
        cache.mkdir(parents=True, exist_ok=True)
        report_path = args.out.resolve() if args.out else cache / "report.json"
        report_path.parent.mkdir(parents=True, exist_ok=True)
        args.stream_directory = Path(tempfile.mkdtemp(prefix=report_path.stem + "-streams-", dir=report_path.parent))
        with tempfile.TemporaryDirectory(prefix="probe-", dir=cache) as temporary:
            directory = Path(temporary)
            (directory / "input.json").write_text(json.dumps(inputs))
            (directory / "probe_test.go").write_text(PROBE)
            old = probe(args.baseline, directory, "baseline", args)
            new = probe(ROOT, directory, "candidate", args)
            inputs["Boundaries"] = boundary_union(old, new)
            (directory / "input.json").write_text(json.dumps(inputs))
            old = probe(args.baseline, directory, "baseline", args)
            new = probe(ROOT, directory, "candidate", args)
            repeated = probe(ROOT, directory, "candidate-repeat", args)
        comparisons = compare_checkpoints(old, new, args.strict_state, repeated)
        difference = (comparisons["first_action_divergence"] or comparisons["first_state_divergence"] or
                      comparisons["first_derived_divergence"] or comparisons["first_boundary_divergence"] or
                      next(iter(comparisons["non_allowlisted_fields"]), None) or synthetic_difference or
                      (dict(source="boundary cap hit") if comparisons["boundary_cap_hit"] else None))
        derived_ok = comparisons["derived_determinism"] == "PASS" and not comparisons["non_allowlisted_fields"]
        summaries = dict(baseline=stream_summary(old), candidate=stream_summary(new),
                         **{"candidate-repeat": stream_summary(repeated)})
        for name, summary in summaries.items():
            summary["path"] = str(args.stream_directory / f"{name}.jsonl")
        gate4 = all(p["journal_digest"] and not p["would_emit_config_applied"]
                    for summary in summaries.values() for p in summary["policies"])
        watermark = dict(max_event_id=max(e["id"] for e in inputs["Events"]),
                         max_recorded_at=max((e["recorded_at"] for e in inputs["Events"]),
                                             key=lambda value: datetime.fromisoformat(value.replace("Z", "+00:00"))),
                         latest_config_applied=[dict(queue_id=q["id"], event_id=e["id"], digest=e["payload"]["data"]["digest"])
                            for q in inputs["Queues"]
                            for e in [next((e for e in reversed(inputs["Events"]) if e["queue_id"] == q["id"] and e["type"] == "config_applied"), None)] if e],
                         event_rows=len(inputs["Events"]), queue_rows=len(inputs["Queues"]))
        report = dict(tool_sha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                      self_test="PASS" if selftest.returncode == 0 and synthetic_difference is None else "FAIL",
                      synthetic_compatibility=synthetic, self_test_exit_status=selftest.returncode, self_test_output=selftest.stderr.strip(), baseline_clean=True, candidate_clean=True,
                      exceptions=STATE_EXCEPTIONS, watermark=watermark,
                      baseline_commit=git(args.baseline, "rev-parse", "HEAD"),
                      candidate_commit=git(ROOT, "rev-parse", "HEAD"),
                      adapter=API_ADAPTERS[0], events=len(inputs["Events"]), queues=len(inputs["Queues"]),
                      exports={str(p.resolve()): hashlib.sha256(p.read_bytes()).hexdigest()
                               for p in (args.events, args.queues)},
                      clock="event recorded_at", deadline_enumeration_clock="zero time (includes elapsed timers)", poll_interval_seconds=args.poll_interval_seconds,
                      delete_head_branch_default=not args.keep_head_branch, default_branch=args.default_branch,
                      config_sha256=hashlib.sha256(inputs["Live"].encode()).hexdigest(),
                      streams=summaries, first_difference=difference,
                      **comparisons, state_mode="strict" if args.strict_state else "structural",
                      gate2="PASS" if difference is None and derived_ok else "FAIL", gate4="PASS" if gate4 else "FAIL",
                      verdict="PASS" if difference is None and derived_ok and gate4 else "FAIL")
        for name, checkout in (("baseline", args.baseline), ("candidate", ROOT)):
            if git(checkout, "status", "--porcelain") or git(checkout, "rev-parse", "HEAD") != commits[name]:
                raise ValueError(f"{name} checkout changed during replay; refusing report")
        rendered = json.dumps(report, indent=2, sort_keys=True)
        print(rendered)
        report_path.write_text(rendered + "\n")
        return 0 if report["verdict"] == "PASS" else 1
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError, RuntimeError) as error:
        print(f"replay error: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
