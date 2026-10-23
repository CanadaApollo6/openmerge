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
            kinds = ["settlement"] if source["type"] in ("landing_started", "landed") else []
            refs = []
            if name == "closure":
                owner = dict(queue_id=1, event_id=source["id"], batch_id="b1", attempt_id="attempt", change_id="", revision=0, head="", tested_sha="")
                edges = [("landing_started", '["b1"]', events[0]["source"])] if source["type"] == "landed" else [
                    ("landing_decision_recorded", '["b1","attempt"]', None),
                    ("landed", '["b1"]', events[1]["source"]),
                    ("landing_abandoned", '["b1"]', None)]
                for typ, identity, target in edges:
                    refs.append(dict(kind="settlement", relation="relied_on", status="recorded" if target else "not_recorded", owner=owner, expected_type=typ, identity=identity, generation=0, run_id=0, attempt=0, source=target))
                refs.sort(key=raw)
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
        if dependencies:
            streams["dependency_boundaries"] = raw(dict(queue_id=1, queue=dependencies[0]["queue"], first=dependencies[0]["id"], last=dependencies[-1]["id"], count=len(dependencies))) + b"\n"
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
    prior.EXTRA_PATTERN = "|TestAudit|TestCompleteExportV1|TestAuditBareRemote|TestAuditRealisticGrowth"
    prior.EXTRA_MUTATIONS = [
        ("pin-queue-scope", "evidence.go", "WHERE queue_id=$1 AND id<$2 AND id>=$3 AND type IN", "WHERE ($1::bigint IS NOT NULL) AND id<$2 AND id>=$3 AND type IN"),
        ("cli-manifest-selector-conflict", "cmd/openmerge/audit.go", 'case "repo", "installation", "from", "until":', 'case "unused":'),
        ("pin-cursor-no-queue-reset", "evidence.go", "cursor.queueID != queueID || before < cursor.before", "before < cursor.before"),
        ("pin-cursor-no-backward-reset", "evidence.go", "cursor.queueID != queueID || before < cursor.before", "cursor.queueID != queueID"),
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
                "--stress-pattern", "TestAudit|TestCompleteExportV1|TestGoverningPin"]
    return prior.main()


if __name__ == "__main__":
    sys.exit(main())
