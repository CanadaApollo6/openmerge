#!/usr/bin/env python3
"""Offline ADR 0007 gates 2/4 using exported JSONL and local Git objects only.

The identical Go test overlay is compiled in each checkout. No DB, forge, fetch,
module download, or toolchain download is allowed. See docs/operating.md.
"""

import argparse
import base64
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
# state projection. Verified on a4b173f and e0e2824. Future adapters must preserve
# these semantics; keep the same adapter in BOTH trees for a comparison.
API_ADAPTERS = ["wire-v1 / queue.Apply / serve.worker.plannerConfig / queue.EncodeSnapshot"]
PROBE = r'''package serve

import (
 "encoding/base64"
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
   actions=append(actions,map[string]any{"key":key,"type":name,"payload_base64":base64.StdEncoding.EncodeToString(payload)})
  }
  snapshot,err:=queue.EncodeSnapshot(state); if err!=nil {t.Fatalf("event %d: %v",e.ID,err)}
  var fields map[string]json.RawMessage
  if err=json.Unmarshal(snapshot,&fields); err!=nil {t.Fatal(err)}
  revision:=fields["Revision"]
  if revision==nil {t.Fatal("snapshot adapter missing Revision")}
  record:=map[string]any{"kind":"checkpoint","event_id":e.ID,"queue_id":e.QueueID,"revision":revision,"actions":actions,"snapshot_base64":snapshot,"state_sha256":fmt.Sprintf("%x",sha256.Sum256(snapshot))}
  emit(record)
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
        if not path and (key in DERIVED_FIELDS or key == "Version"):
            continue
        name = f"{path}.{key}" if path else key
        l, r = left.get(key), right.get(key)
        if key not in left or key not in right:
            if nonempty(l) or nonempty(r):
                differences[name] = dict(baseline=l, candidate=r, new_field=True)
        elif isinstance(l, dict) and isinstance(r, dict):
            differences.update(legacy_differences(l, r, name))
        elif l != r:
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
        for key, item in value.items():
            expected = schema.get(key)
            name = f"{path}.{key}"
            if expected is None:
                if nonempty(item):
                    violations[name] = item
            elif isinstance(expected, dict):
                check(item, expected, name)
            elif type(item) is not expected:
                violations[name] = item

    for field, schema in DERIVED_FIELDS.items():
        values = fields.get(field) or {}
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


def compare_checkpoints(old, new, strict, repeated=None):
    actions_identical = state_identical = legacy_identical = total = 0
    first_action = first_state = first_derived = None
    counts = {field: 0 for field in DERIVED_FIELDS}
    unexpected = []
    repeats = iter(repeated if repeated is not None else new)
    for left, right in itertools.zip_longest(old, new):
        l, r = json.loads(left) if left else {}, json.loads(right) if right else {}
        if l.get("kind") != "checkpoint" and r.get("kind") != "checkpoint":
            continue
        again = next(repeats, None)
        again = json.loads(again) if again else {}
        total += 1
        identity = ("kind", "event_id", "queue_id")
        aligned = all(l.get(k) == r.get(k) for k in identity) and bool(l) and bool(r)
        detail = dict(event_id=l.get("event_id", r.get("event_id")),
                      queue_id=l.get("queue_id", r.get("queue_id")), baseline=left, candidate=right)
        if aligned and all(l.get(k) == r.get(k) for k in ("revision", "actions")):
            actions_identical += 1
        elif first_action is None:
            first_action = detail
        baseline, candidate, rerun = snapshot(l), snapshot(r), snapshot(again)
        differences = legacy_differences(baseline, candidate)
        if aligned and not differences:
            legacy_identical += 1
        equal = l.get("state_sha256") == r.get("state_sha256") if strict else not differences
        if aligned and equal:
            state_identical += 1
        elif first_state is None:
            first_state = dict(detail, differing_fields=differences)
        violations = derived_violations(candidate)
        violations.update({k: v for k, v in differences.items() if v.get("new_field")})
        if violations:
            unexpected.append(dict(event_id=r.get("event_id"), queue_id=r.get("queue_id"), fields=violations))
        for field in DERIVED_FIELDS:
            if candidate.get(field):
                counts[field] += 1
        if (any(r.get(k) != again.get(k) for k in identity) or
                any(candidate.get(k) != rerun.get(k) for k in DERIVED_FIELDS)) and first_derived is None:
            first_derived = dict(event_id=r.get("event_id"), queue_id=r.get("queue_id"),
                                 candidate={k: candidate.get(k) for k in DERIVED_FIELDS},
                                 repeated={k: rerun.get(k) for k in DERIVED_FIELDS})
    return dict(actions_identical=f"{actions_identical}/{total}",
                legacy_state_identical=f"{legacy_identical}/{total}",
                state_identical=f"{state_identical}/{total}",
                first_action_divergence=first_action, first_state_divergence=first_state,
                derived_nonempty_checkpoints=counts,
                derived_determinism="PASS" if first_derived is None else "FAIL",
                first_derived_divergence=first_derived, non_allowlisted_fields=unexpected)


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
            repeated = probe(ROOT, directory, "candidate-repeat", args)
        comparisons = compare_checkpoints(old, new, args.strict_state, repeated)
        difference = (comparisons["first_action_divergence"] or comparisons["first_state_divergence"] or
                      comparisons["first_derived_divergence"] or
                      next(iter(comparisons["non_allowlisted_fields"]), None))
        derived_ok = comparisons["derived_determinism"] == "PASS" and not comparisons["non_allowlisted_fields"]
        summaries = dict(baseline=stream_summary(old), candidate=stream_summary(new),
                         **{"candidate-repeat": stream_summary(repeated)})
        for name, summary in summaries.items():
            summary["path"] = str(args.stream_directory / f"{name}.jsonl")
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
                      **comparisons, state_mode="strict" if args.strict_state else "legacy-fields",
                      gate2="PASS" if difference is None and derived_ok else "FAIL", gate4="PASS" if gate4 else "FAIL",
                      verdict="PASS" if difference is None and derived_ok and gate4 else "FAIL")
        rendered = json.dumps(report, indent=2, sort_keys=True)
        print(rendered)
        report_path.write_text(rendered + "\n")
        return 0 if report["verdict"] == "PASS" else 1
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError, RuntimeError) as error:
        print(f"replay error: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
