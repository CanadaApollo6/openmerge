#!/usr/bin/env python3
"""Offline ADR0008 PR1 boundary checks plus the existing differential replay."""
from __future__ import annotations
import argparse, hashlib, json, os, re, shlex, subprocess, sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CACHE = Path.home() / ".cache/adr0008-1"
LOCK = Path("/home/riels/.cache/openmerge-heavy.lock")
BASELINE = Path.home() / ".cache/adr0008-1-main"
EVENTS = Path.home() / ".cache/om-pr72-fix1-input/events.jsonl"
QUEUES = Path.home() / ".cache/om-pr72-fix1-input/queues.jsonl"
REPLAY = ROOT / "scripts/check-live-journal-replay.py"
TARGET = "internal/queue/cancellation_snapshot_test.go"
BASELINE_COMMIT = "e7000f8cc1df54372f5220617361f21aaca58b0a"
EXPORT_HASHES = {"events": "9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf",
                 "queues": "418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886"}

class VerifyError(Exception): pass
COMMANDS = []

def sha(p):
    h = hashlib.sha256()
    with p.open("rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""): h.update(block)
    return h.hexdigest()

def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], text=True).strip()

def check_tree(name, root, expected=None):
    if not root.is_dir(): raise VerifyError(f"{name} checkout missing: {root}")
    head = git(root, "rev-parse", "HEAD")
    dirty = git(root, "status", "--porcelain")
    if expected and head != expected: raise VerifyError(f"{name} HEAD {head} != {expected}")
    if dirty: raise VerifyError(f"{name} checkout is dirty; replay requires a clean tree: {dirty}")
    return head

def exports(events, queues):
    for label, path in (("events", events), ("queues", queues)):
        if not path.is_file(): raise VerifyError(f"offline {label} export missing: {path}")
        actual = sha(path)
        if actual != EXPORT_HASHES[label]: raise VerifyError(f"{label} hash {actual} != documented {EXPORT_HASHES[label]}")
    ev = [json.loads(x) for x in events.read_text().splitlines() if x.strip()]
    qs = [json.loads(x) for x in queues.read_text().splitlines() if x.strip()]
    if len(ev) != 2158 or len(qs) != 1: raise VerifyError(f"unexpected export rows: events={len(ev)} queues={len(qs)}")
    if [x["id"] for x in ev] != list(range(1, 2159)): raise VerifyError("event IDs are not the documented 1..2158 sequence")
    if any(x["payload"]["type"] != x["type"] for x in ev): raise VerifyError("event envelope/type mismatch")
    return {"events": {"path": str(events), "sha256": sha(events), "rows": len(ev), "max_event_id": ev[-1]["id"]},
            "queues": {"path": str(queues), "sha256": sha(queues), "rows": len(qs)}}

def state_overlay(baseline, candidate, out, candidate_test, baseline_test):
    text = (candidate / "internal/queue/types.go").read_text()
    block = re.search(r"(?ms)^type State struct \{.*?^\}", text)
    field = re.search(r"(?m)^\s*(\w+)\s+\*authorityProjection\s*$", block.group(0)) if block else None
    if not field: raise VerifyError("candidate State has no *authorityProjection field")
    field_name, field_decl = field.group(1), field.group(0).strip()
    old = (baseline / "internal/queue/types.go").read_text()
    if "type authorityProjection struct" not in old:
        old = old.replace("type State struct {", "type authorityProjection struct{}\n\ntype State struct {", 1)
    b = re.search(r"(?ms)^type State struct \{.*?^\}", old)
    if not b: raise VerifyError("baseline State declaration not found")
    if "*authorityProjection" not in b.group(0):
        old = old[:b.end()-2] + "\n\t" + field_decl + old[b.end()-2:]
    base_types = out / "baseline-types.go"
    base_types.write_text(old)
    overlays = {str(candidate / TARGET): str(candidate_test), str(baseline / TARGET): str(baseline_test),
                str(baseline / "internal/queue/types.go"): str(base_types)}
    overlay = out / "overlay.json"
    overlay.write_text(json.dumps({"Replace": overlays}, indent=2) + "\n")
    return overlay, field_name, field_decl

DIFF_GO = r'''package queue
import("crypto/sha256";"encoding/json";"fmt";"os";"testing";"time")
type ADRDiff struct{Case string;Depth,Revision,SnapshotBytes,PlanBytes,PlanCount int;SnapshotSHA,PlanSHA string}
func adrMust(t *testing.T,e error){if e!=nil{t.Fatal(e)}}
func adrCheckpoint(t *testing.T,out *[]ADRDiff,kind string,depth,revision int,cfg Config,s State){raw,e:=EncodeSnapshot(s);adrMust(t,e);items:=[]map[string]any{};for _,a:=range Plan(cfg,s,time.Date(2026,10,18,0,0,0,0,time.UTC)){n,p,e:=EncodeAction(a);adrMust(t,e);items=append(items,map[string]any{"type":n,"key":a.Key(),"payload":json.RawMessage(p)})};plan,e:=json.Marshal(items);adrMust(t,e);*out=append(*out,ADRDiff{kind,depth,revision,len(raw),len(plan),len(items),fmt.Sprintf("%x",sha256.Sum256(raw)),fmt.Sprintf("%x",sha256.Sum256(plan))})}
func adrApproval(t *testing.T,out *[]ADRDiff){raw,e:=os.ReadFile("testdata/landing-start-activation.json");adrMust(t,e);var f struct{Events []json.RawMessage;Config Config};adrMust(t,json.Unmarshal(raw,&f));for _,depth:=range []int{1,4}{cfg:=f.Config;cfg.SpeculationMaxDepth=depth;cfg.SpeculationMinDepth=0;s:=State{BaseBranch:"main",Fence:"fence"};for i,b:=range f.Events{e,err:=DecodeEvent(b);adrMust(t,err);s=Apply(s,e);adrCheckpoint(t,out,"approval",depth,i+1,cfg,s)}}}
func adrLegacy(t *testing.T,out *[]ADRDiff){names:=[]string{"pr_observed","enqueue_requested","pause_requested","action_poisoned","action_poisoned","pause_requested","rules_blocked","rules_source_blocked","resume_requested","rules_cleared","rules_source_cleared","rules_cleared","rules_source_cleared","pause_requested","resume_requested","resume_requested","test_ref_build_failed","test_ref_build_failed","test_ref_build_failed","resume_requested","test_ref_built","checks_completed","pause_requested","action_poisoned","resume_requested","landing_started","rules_source_blocked","pause_requested","action_poisoned","resume_requested","rules_cleared","landing_abandoned"};for _,depth:=range []int{1,4}{cfg:=Config{MergeMethod:Merge,SpeculationMaxDepth:depth,CheckTimeout:time.Hour,RefreshInterval:time.Minute};s:=State{BaseBranch:"main",BaseSHA:"base",Fence:"fence"};for i,name:=range names{if i==16{s=Apply(s,BatchStarted{BatchID:"b1",BaseSHA:"base",PRs:[]BatchPR{{PR:2,HeadSHA:"head"}},At:time.Date(2026,10,18,0,0,0,0,time.UTC)})};b,e:=os.ReadFile("testdata/wire/v1/event_"+name+".json");adrMust(t,e);event,e:=DecodeEvent(b);adrMust(t,e);s=Apply(s,event);adrCheckpoint(t,out,"legacy-block",depth,i+1,cfg,s)}}}
func TestADR00081Differential(t *testing.T){out:=[]ADRDiff{};adrApproval(t,&out);adrLegacy(t,&out);raw,e:=json.Marshal(out);adrMust(t,e);adrMust(t,os.WriteFile(os.Getenv("ADR_DIFF"),raw,0600))}
'''
CANDIDATE_GO = DIFF_GO + r'''func TestADR00081(t *testing.T){legacy,e:=EncodeSnapshot(State{});adrMust(t,e);var h struct{Version int};adrMust(t,json.Unmarshal(legacy,&h));if h.Version!=6{t.Fatal("legacy is not v6")};raw,e:=os.ReadFile("testdata/wire/v1/variants/policy_authority_applied_activation.json");adrMust(t,e);event,e:=DecodeEvent(raw);adrMust(t,e);pin:=event.(PolicyAuthorityApplied);s:=Apply(State{},ConfigApplied{Commit:pin.GoverningCommit,Digest:pin.EffectiveDigest});s=Apply(s,pin);v7,e:=EncodeSnapshot(s);adrMust(t,e);adrMust(t,json.Unmarshal(v7,&h));if h.Version!=7{t.Fatal("authority is not v7")};_,e=DecodeSnapshot(v7);adrMust(t,e);adrMust(t,os.WriteFile(os.Getenv("ADR_V6"),legacy,0600));adrMust(t,os.WriteFile(os.Getenv("ADR_V7"),v7,0600));fmt.Fprintln(os.Stdout,"candidate: v6 legacy exact shape and fixture-backed v7 authority decode passed")}
'''
BASELINE_GO = DIFF_GO.replace('"testing"', '"bytes";"testing"') + r'''type ADROutsideLanding struct{AttemptID,SHA,BaseSHA string;Consumed bool};type ADROutsideMove struct{ID int64;PreviousSHA,NewSHA string;PossibleStaleObservation bool;At time.Time};type ADRPendingOutsideMove struct{ADROutsideMove;AttemptID string;Own bool};type ADROutsidePushProgress struct{ScannedThrough,AuditedBaseMove int64;State json.RawMessage;ObservedBase string;LastLanding ADROutsideLanding;Pending []ADRPendingOutsideMove}
func TestADR00081(t *testing.T){legacy,e:=os.ReadFile(os.Getenv("ADR_V6"));adrMust(t,e);var h struct{Version int};adrMust(t,json.Unmarshal(legacy,&h));if h.Version!=6{t.Fatalf("legacy version %d",h.Version)};cursor:=ADROutsidePushProgress{42,41,legacy,"base",ADROutsideLanding{"attempt","landed","base",true},[]ADRPendingOutsideMove{{ADROutsideMove{43,"base","outside",false,time.Date(2026,10,18,0,0,0,0,time.UTC)},"attempt",true}}};raw,e:=json.Marshal(cursor);adrMust(t,e);var got ADROutsidePushProgress;adrMust(t,json.Unmarshal(raw,&got));round,e:=json.Marshal(got);adrMust(t,e);if !bytes.Equal(raw,round)||!bytes.Equal(got.State,legacy){t.Fatal("outside-push cursor wrapper changed")};s,e:=DecodeSnapshot(got.State);adrMust(t,e);roundState,e:=EncodeSnapshot(s);adrMust(t,e);if !bytes.Equal(legacy,roundState){t.Fatal("legacy snapshot changed under old cursor decoder")};v7,e:=os.ReadFile(os.Getenv("ADR_V7"));adrMust(t,e);if _,e=DecodeSnapshot(v7);e==nil{t.Fatal("old decoder accepted candidate v7")}}
'''

def invoke(cmd, cwd, run, name, mem="4GiB", check=True, env_extra=None):
    d = run / name; d.mkdir(parents=True, exist_ok=True)
    env = {"GOFLAGS": "-p=2", "GOMEMLIMIT": mem, "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local",
           "GOTMPDIR": str(d / "go-tmp"), "GOCACHE": "/home/riels/.cache/go-build", "TMPDIR": str(d / "tmp")}
    for v in ("GOTMPDIR", "TMPDIR"): Path(env[v]).mkdir(exist_ok=True)
    if env_extra: env.update({k: str(v) for k, v in env_extra.items()})
    wrapped = ["flock", str(LOCK), "env"] + [f"{k}={v}" for k, v in env.items()] + cmd
    p = subprocess.run(wrapped, cwd=cwd, capture_output=True, text=True)
    (d / "stdout.log").write_text(p.stdout); (d / "stderr.log").write_text(p.stderr)
    (d / "command.txt").write_text(shlex.join(wrapped) + "\n")
    COMMANDS.append({"name": name, "command": shlex.join(wrapped), "exit": p.returncode, "logs": str(d)})
    if check and p.returncode: raise VerifyError(f"{name} exit {p.returncode}; see {d}")
    return p

def projection_counts(path, field):
    result = {"checkpoints": 0, "field_present": 0, "nil": 0, "non_nil": 0}
    for line in path.read_text().splitlines():
        r = json.loads(line)
        if r.get("kind") != "checkpoint": continue
        result["checkpoints"] += 1
        node = r.get("structural", {}).get("value", {}).get(field)
        if node is None: continue
        result["field_present"] += 1
        result["nil" if node.get("nil") else "non_nil"] += 1
    return result

def compare_contracts(base, candidate):
    left, right = json.loads(base.read_text()), json.loads(candidate.read_text())
    groups = {}
    for side, rows in (("baseline", left), ("candidate", right)):
        for row in rows:
            key = f"{row['Case']}/depth{row['Depth']}"; g = groups.setdefault(key, {})
            x = g.setdefault(side, {"checkpoints": 0, "plan_count": 0, "snapshot_bytes": 0, "plan_bytes": 0})
            x["checkpoints"] += 1; x["plan_count"] += row["PlanCount"]; x["snapshot_bytes"] += row["SnapshotBytes"]; x["plan_bytes"] += row["PlanBytes"]
    for g in groups.values():
        for name in ("checkpoints", "plan_count", "snapshot_bytes", "plan_bytes"):
            g[f"delta_{name}"] = g.get("candidate", {}).get(name, 0) - g.get("baseline", {}).get(name, 0)
    first = next((i for i, pair in enumerate(zip(left, right)) if pair[0] != pair[1]), None)
    return {"identical": left == right, "baseline_sha256": sha(base), "candidate_sha256": sha(candidate),
            "baseline_checkpoints": len(left), "candidate_checkpoints": len(right), "first_difference_index": first,
            "numeric_by_case_depth": groups, "first_difference": ({"baseline": left[first], "candidate": right[first]} if first is not None else None)}

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--baseline", type=Path, default=BASELINE); ap.add_argument("--candidate", type=Path, default=ROOT)
    ap.add_argument("--events", type=Path, default=EVENTS); ap.add_argument("--queues", type=Path, default=QUEUES); ap.add_argument("--out", type=Path, default=CACHE / "report.json")
    args = ap.parse_args(); args.out.parent.mkdir(parents=True, exist_ok=True); run = CACHE / "runs"; run.mkdir(parents=True, exist_ok=True)
    evidence = {"script": str(Path(__file__).resolve()), "script_lines": sum(1 for _ in Path(__file__).open()), "commands": COMMANDS, "offline_only": True}
    try:
        evidence["exports"] = exports(args.events, args.queues)
        evidence["baseline_commit"] = check_tree("baseline", args.baseline, BASELINE_COMMIT)
        evidence["candidate_commit"] = check_tree("candidate", args.candidate)
        work = run / args.out.stem; work.mkdir(parents=True, exist_ok=True)
        cand_test = work / "candidate_test.go"; base_test = work / "baseline_test.go"
        cand_test.write_text(CANDIDATE_GO); base_test.write_text(BASELINE_GO)
        _, field, decl = state_overlay(args.baseline, args.candidate, work, cand_test, base_test)
        cand_test.write_text(CANDIDATE_GO.replace("__FIELD__", field))
        cand_overlay = work / "candidate-overlay.json"; cand_overlay.write_text(json.dumps({"Replace": {str(args.candidate / TARGET): str(cand_test)}}, indent=2) + "\n")
        base_overlay = work / "baseline-overlay.json"; base_overlay.write_text(json.dumps({"Replace": {str(args.baseline / TARGET): str(base_test), str(args.baseline / "internal/queue/types.go"): str(work / "baseline-types.go")}}, indent=2) + "\n")
        old_overlay = work / "old-decoder-overlay.json"; old_overlay.write_text(json.dumps({"Replace": {str(args.baseline / TARGET): str(base_test)}}) + "\n")
        v6, v7 = work / "legacy-v6.json", work / "authority-v7.json"; cand_diff, base_diff = work / "candidate-differential.json", work / "baseline-differential.json"
        invoke(["go", "test", "-overlay", str(cand_overlay), "./internal/queue", "-run", "^TestADR00081$|^TestADR00081Differential$", "-count=1", "-timeout=10m", "-v"], args.candidate, run, "candidate-boundary", env_extra={"ADR_V6": v6, "ADR_V7": v7, "ADR_DIFF": cand_diff})
        invoke(["go", "test", "-overlay", str(old_overlay), "./internal/queue", "-run", "^TestADR00081$|^TestADR00081Differential$", "-count=1", "-timeout=10m", "-v"], args.baseline, run, "baseline-old-decoder", env_extra={"ADR_V6": v6, "ADR_V7": v7, "ADR_DIFF": base_diff})
        evidence["contract_differential"] = compare_contracts(base_diff, cand_diff)
        if not evidence["contract_differential"]["identical"]: raise VerifyError("approval/legacy depth differential diverged")
        evidence["boundary"] = {"legacy_v6_sha256": sha(v6), "authority_v7_sha256": sha(v7), "legacy_version": 6, "authority_version": 7, "authority_fixture": "internal/queue/testdata/wire/v1/variants/policy_authority_applied_activation.json", "baseline_old_decoder": "rejected v7; preserved v6 bytes and outside-push cursor fields", "outside_push_cursor_shape": "ScannedThrough, AuditedBaseMove, State, ObservedBase, LastLanding, Pending", "field": field, "field_declaration": decl}
        replay_out = work / "replay.json"
        replay_cmd = [sys.executable, str(REPLAY), "--baseline", str(args.baseline), "--candidate", str(args.candidate), "--baseline-overlay", str(base_overlay), "--events", str(args.events), "--queues", str(args.queues), "--config-file", ".openmerge.yml", "--strict-state", "--out", str(replay_out)]
        replay = invoke(replay_cmd, ROOT, run, "differential-replay", mem="6GiB", check=False)
        if not replay_out.exists(): raise VerifyError(f"replay exit {replay.returncode} produced no report; see {run / 'differential-replay'}")
        inner = json.loads(replay_out.read_text()); streams = inner.get("streams", {})
        evidence["replay"] = {"exit": replay.returncode, "verdict": inner.get("verdict"), "gate2": inner.get("gate2"), "gate4": inner.get("gate4"), "state_mode": inner.get("state_mode"), "actions_identical": inner.get("actions_identical"), "structural_state_identical": inner.get("structural_state_identical"), "snapshot_hashes_identical": inner.get("snapshot_hashes_identical"), "boundary_checkpoints_identical": inner.get("boundary_checkpoints_identical"), "non_allowlisted_fields": inner.get("non_allowlisted_fields"), "first_difference": inner.get("first_difference"), "streams": {k: {"path": v.get("path"), "authority_projection": projection_counts(Path(v["path"]), field)} for k, v in streams.items() if v.get("path")}}
        evidence["comparator_note"] = "Existing comparator source unchanged. Baseline overlay adds the same nil *authorityProjection field, so additive dormancy is visible and exact; no comparator allowance was added. strict-state remains enabled for legacy snapshot bytes."
        evidence["verdict"] = "PASS" if replay.returncode == 0 and inner.get("verdict") == "PASS" else "FAIL"
    except (VerifyError, OSError, json.JSONDecodeError, subprocess.CalledProcessError) as e:
        evidence["verdict"] = "FAIL"; evidence["error"] = str(e)
    args.out.write_text(json.dumps(evidence, indent=2, sort_keys=True) + "\n"); print(json.dumps(evidence, indent=2, sort_keys=True))
    return 0 if evidence["verdict"] == "PASS" else 1

if __name__ == "__main__": sys.exit(main())
