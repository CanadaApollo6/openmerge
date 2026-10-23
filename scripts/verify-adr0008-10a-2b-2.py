#!/usr/bin/env python3
"""Final v1 reference and command acceptance; offline, guarded scratch DB only."""
import argparse
from decimal import Decimal
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

sys.dont_write_bytecode = True
from adr0008_scratch_db import scratch_url
ROOT = Path(__file__).resolve().parents[1]
DATA = ROOT / "internal/audit/testdata"


def canonical(value):
    if isinstance(value, Decimal):
        if value == 0:
            return "0"
        text = format(value, "f")
        return text.rstrip("0").rstrip(".") if "." in text else text
    if isinstance(value, dict):
        return "{" + ",".join(canonical(k) + ":" + canonical(value[k]) for k in sorted(value, key=lambda k: k.encode())) + "}"
    if isinstance(value, list):
        return "[" + ",".join(canonical(v) for v in value) + "]"
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).replace(chr(0x85), r"\u0085").replace(chr(0x2028), r"\u2028").replace(chr(0x2029), r"\u2029")


def raw(value):
    return canonical(value).encode()


def sha(value):
    return hashlib.sha256(value).hexdigest()


# Independent semantic selectors over literal journal inputs, not Go exports.
KINDS = {typ: kind for kind, types in {
    "settlement": "landing_decision_recorded landing_started landed landing_abandoned",
    "classification": "change_revision_observed change_classified",
    "eligibility": "approval_eligibility_observed", "accountability": "change_accountability_observed",
    "receipt": "approval_receipt_recorded review_stance_observed", "authority": "policy_authority_applied",
    "check": "batch_started test_ref_built landing_checks_recorded checks_observed checks_completed check_rerun_requested check_rerun_dispatch_progress check_rerun_dispatched checks_failure_confirmed",
}.items() for typ in types.split()}


def references(item, journal):
    source, data = item["source"], item["payload"].get("data", {})
    typ = source["type"]
    events = [e for e in journal if e["source"]["queue_id"] == source["queue_id"]]
    def select(kind, predicate=lambda d: True, before=None):
        found = [e for e in events if e["source"]["type"] == kind and predicate(e["payload"]["data"]) and (before is None or e["source"]["id"] < before)]
        return max(found, key=lambda e: e["source"]["id"]) if found else None
    owner = dict(queue_id=source["queue_id"], event_id=source["id"], batch_id=data.get("batch_id", ""), attempt_id=data.get("attempt_id", ""), change_id="", revision=0, head="", tested_sha="")
    refs = []
    def edge(kind, identity, target):
        refs.append(dict(kind=KINDS[kind], relation="relied_on", status="recorded" if target else "not_recorded", owner=owner, expected_type=kind, identity=identity, generation=0, run_id=0, attempt=0, source=target["source"] if target else None))
    batch = data.get("batch_id")
    by_batch = lambda d: d.get("batch_id") == batch
    by_attempt = lambda d: by_batch(d) and d.get("attempt_id") == owner["attempt_id"]
    if typ == "landing_decision_recorded":
        owner["tested_sha"] = data["tested_sha"]
        pin = select("policy_authority_applied", before=source["id"])
        edge("policy_authority_applied", ["governing"], pin)
        openmerge = pin and pin["payload"]["data"]["authority"] == "openmerge"
        if pin and not openmerge: refs[-1]["status"] = "inapplicable"
        approval = data.get("approval")
        if approval:
            basis, identity = approval["basis"], approval["identity"]
            owner.update(change_id=basis["change_id"], revision=basis["revision"], head=basis["head"])
            edge("change_revision_observed", [basis["change_id"], str(basis["revision"])], select("change_revision_observed", lambda d: d["change_id"] == basis["change_id"] and d["revision"] == basis["revision"]))
            edge("batch_started", [batch], select("batch_started", by_batch))
            edge("approval_eligibility_observed", [approval["eligibility_read_id"]], select("approval_eligibility_observed", lambda d: d["basis"]["read_id"] == approval["eligibility_read_id"]))
            edge("change_classified", [identity["change_id"], str(identity["revision"]), str(identity["generation"])], select("change_classified", lambda d: d["evidence"]["Classification"]["Inputs"]["Generation"] == identity["generation"]))
            edge("change_accountability_observed", [basis["change_id"], str(basis["revision"])], select("change_accountability_observed", lambda d: d["basis"]["change_id"] == basis["change_id"], before=source["id"]))
            for relied in approval["sources"]:
                s = relied["source"]
                kind = "approval_receipt_recorded" if relied["receipt"] else "review_stance_observed"
                edge(kind, [s["channel"], s["id"]], select(kind, lambda d: d.get("comment_id", d.get("stance", {}).get("Source", {}).get("ID")) == s["id"] and d["basis"]["read_id"] == relied["basis"]["read_id"]))
        edge("landing_checks_recorded", [batch, owner["attempt_id"]], select("landing_checks_recorded", by_attempt) if openmerge else None)
        edge("landing_started", [batch, owner["attempt_id"]], select("landing_started", by_attempt))
    elif typ == "landing_checks_recorded":
        owner["tested_sha"] = data["tested_sha"]
        edge("batch_started", [batch], select("batch_started", by_batch))
        edge("test_ref_built", [batch, data["tested_sha"]], select("test_ref_built", by_batch))
        for check in data["checks"]:
            identity = [check["source"], check["name"]]
            def matching(e):
                return [r for r in e["payload"]["data"]["runs"] if r["name"] == check["name"] and r["source"] == check["source"]]
            observed = [e for e in events if e["source"]["type"] == "checks_observed" and by_batch(e["payload"]["data"]) and matching(e)]
            preceding = [e for e in observed if e["source"]["id"] < source["id"]]
            def rank(e):
                return max((r["id"], max(r["attempt"], 1), e["source"]["id"]) for r in matching(e))
            observation = max(preceding, key=rank) if preceding else None
            edge("checks_observed", identity, observation)
            if observation:
                chosen = max(matching(observation), key=lambda r: (r["id"], max(r["attempt"], 1)))
                refs[-1].update(run_id=chosen["id"], attempt=max(chosen["attempt"], 1))
            request = select("check_rerun_requested", lambda d: by_batch(d) and any(r["name"] == check["name"] and r["source"] == check["source"] for r in d["runs"]), before=source["id"])
            generation = request["payload"]["data"]["generation"] if request else 0
            for kind in ("check_rerun_requested", "check_rerun_dispatch_progress", "check_rerun_dispatched"):
                target = request if kind == "check_rerun_requested" else select(kind, lambda d: by_batch(d) and d["generation"] == generation and (kind != "check_rerun_dispatch_progress" or not check["workflow_run_id"] or d["target"]["workflow_run_id"] == check["workflow_run_id"]), before=source["id"])
                edge(kind, identity, target)
                if target: refs[-1]["generation"] = target["payload"]["data"]["generation"]
            later = [e for e in observed if e["source"]["id"] > source["id"]]
            edge("checks_observed", identity + ["later"], min(later, key=lambda e: e["source"]["id"]) if later else None)
            if later: refs[-1]["relation"] = "excluded_later"
    elif typ == "landing_started":
        decisions = [e for e in events if e["source"]["type"] == "landing_decision_recorded" and by_attempt(e["payload"]["data"])]
        for decision in decisions or [None]: edge("landing_decision_recorded", [batch, owner["attempt_id"]], decision)
    elif typ in ("landed", "landing_abandoned"):
        start = select("landing_started", by_batch, before=source["id"])
        if start: owner["attempt_id"] = start["payload"]["data"]["attempt_id"]
        edge("landing_started", [batch], start)
    if typ in ("landing_started", "landing_decision_recorded"):
        for kind in ("landed", "landing_abandoned"): edge(kind, [batch], select(kind, by_batch))
    return sorted(refs, key=raw)


def reference(write=False):
    """Expected bytes derive only from literal inputs and explicit relationships.

    No Go invocation, exported file, saved manifest or saved selection is an input.
    The closure vector independently specifies its three start edges and land edge.
    """
    journal = json.loads((DATA / "journal-v1.json").read_text(), parse_int=Decimal, parse_float=Decimal)
    total = 0
    for name, literal in journal.items():
        request, events, watermarks = literal["request"], literal["events"], literal["watermarks"]
        streams = {k: b"" for k in ("events", "selection", "watermarks", "dependency_boundaries")}
        previous = "0" * 64
        dependencies = []
        for item in events:
            source, payload, role = item["source"], item["payload"], item["role"]
            kinds = [KINDS[source["type"]]] if source["type"] in KINDS else []
            refs = references(item, events)
            meta = dict(source=source, role=role, references=refs, evidence_kinds=kinds)
            selection = dict(meta, payload_sha256=sha(raw(payload)), metadata_sha256=sha(raw(meta)))
            envelope = dict(meta, kind="source", payload=payload, previous_sha256=previous)
            line = raw(envelope)
            previous = sha(line)
            streams["events"] += line + b"\n"
            streams["selection"] += raw(selection) + b"\n"
            if role == "dependency":
                dependencies.append(source)
        for mark in watermarks:
            streams["watermarks"] += raw(mark) + b"\n"
        for mark in watermarks:
            deps = [d for d in dependencies if d["queue_id"] == mark["queue_id"]]
            if deps:
                streams["dependency_boundaries"] += raw(dict(queue_id=mark["queue_id"], queue=mark["queue"], first=deps[0]["id"], last=deps[-1]["id"], count=len(deps))) + b"\n"
        manifest = dict(manifest_sha256="", version=1, request=request, count=len(events), terminal_sha256=previous)
        for stream, contents in streams.items():
            manifest[stream] = stream + ".jsonl"
            manifest[stream + "_sha256"] = sha(contents)
        manifest["manifest_sha256"] = sha(raw(manifest))
        files = {k + ".jsonl": v for k, v in streams.items()}
        files["manifest.json"] = raw(manifest) + b"\n"
        for file, contents in files.items():
            path = DATA / (name + "-" + file)
            if write:
                path.write_bytes(contents)
            elif path.read_bytes() != contents:
                raise ValueError("independent complete-export mismatch: " + path.name)
            total += 1
    return {"exports": len(journal), "files": total, "reference": "literal journal + independent Python Decimal encoder", "identical": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write-reference", action="store_true")
    parser.add_argument("--baseline", type=Path)
    parser.add_argument("--out", type=Path)
    parser.add_argument("--phase", choices=("all", "regression"), default="all")
    args = parser.parse_args()
    result = reference(args.write_reference)
    print(json.dumps(result), flush=True)
    subprocess.run(["node", str(ROOT / "scripts/verify-audit-v1-node.cjs")], check=True)
    if args.baseline is None:
        return 0
    scratch_url(os.environ.get("OPENMERGE_TEST_DATABASE_URL", ""))  # before any subprocess
    # act sets BOTH CI=true and GITHUB_ACTIONS=true; use the same allowlist.
    spec = importlib.util.spec_from_file_location("prior", ROOT / "scripts/verify-adr0008-10a-2a.py")
    prior = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(prior)
    prior.BASE = "2d6792c"
    prior.SLICE = "10a-2b-2"
    prior.CLASSIFICATION = "Hold; deploy only after period B drains"
    prior.EXTRA_PACKAGES = ["./cmd/openmerge"]
    prior.EXTRA_PATTERN = "|TestAuditUsageAndSanitization|TestAuditSelectOnlyCommand|TestCompleteExportV1|TestIdentityJSONArray|TestAuditSnapshotTimeout|TestAuditBareRemote"
    prior.EXTRA_MUTATIONS = [
        ("pin-queue-scope", "evidence.go", "WHERE queue_id=$1 AND id<$2 AND id>=$3 AND type IN", "WHERE ($1::bigint IS NOT NULL) AND id<$2 AND id>=$3 AND type IN"),
        ("cli-manifest-selector-conflict", "cmd/openmerge/audit.go", 'case "repo", "installation", "from", "until":', 'case "unused":'),
        ("pin-cursor-no-queue-reset", "evidence.go", "cursor.queueID != queueID || before < cursor.before", "before < cursor.before"),
        ("pin-cursor-no-backward-reset", "evidence.go", "cursor.queueID != queueID || before < cursor.before", "cursor.queueID != queueID"),
        ("cli-canonical-installation", "cmd/openmerge/audit.go", "strconv.FormatInt(id, 10) != *installation", "false"),
        ("cli-positional", "cmd/openmerge/audit.go", "flags.NArg() != 0", "false"),
        ("cli-inverted-range", "cmd/openmerge/audit.go", "start.After(end)", "start.After(end) && false"),
        ("cli-zero-installation", "cmd/openmerge/audit.go", "id <= 0", "false"),
        ("cli-leak-connection", "cmd/openmerge/audit.go", 'fmt.Fprintln(stderr, "audit: connection, source, integrity or publication failure")', 'fmt.Fprintln(stderr, os.Getenv("OPENMERGE_DATABASE_URL"))'),
        ("cli-exit-source", "cmd/openmerge/audit.go", 'fmt.Fprintln(stderr, "audit: connection, source, integrity or publication failure")\n\t}\n\treturn 1', 'fmt.Fprintln(stderr, "audit: connection, source, integrity or publication failure")\n\t}\n\treturn 0'),
    ]
    # The fold now has its own sanitized diagnostic, retaining fail-closed semantics.
    original = prior.mutation_proofs
    def mutations(measured, out, pattern):
        return original(measured, out, pattern + prior.EXTRA_PATTERN)
    prior.mutation_proofs = mutations
    sys.argv = [str(prior.ROOT), "--baseline", str(args.baseline), "--out", str(args.out), "--phase", args.phase,
                "--stress-pattern", "TestAudit|TestCompleteExportV1|TestGoverningPin|TestIdentityJSONArray|TestEvidenceIdentity|TestEvidenceReferenceComparator|TestRound3|TestSpoolRejectsConflictingDuplicateRecords|TestSourceReplayRejectsScopeShapeAndCrossQueueOrder"]
    return prior.main()


if __name__ == "__main__":
    sys.exit(main())
