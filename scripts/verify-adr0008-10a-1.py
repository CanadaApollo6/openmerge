#!/usr/bin/env python3
"""Offline 10a-1 proof; uses shared test Postgres schemas, never a scratch database."""
import argparse
import hashlib
from decimal import Decimal
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

sys.dont_write_bytecode = True
from adr0008_scratch_db import scratch_url

ROOT = Path(__file__).resolve().parents[1]
BASE = "59194fd"


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def independent_goldens():
    """Independent Python Decimal reference; does not invoke Go serialization."""
    def canonical(value):
        if isinstance(value, Decimal):
            if value == 0:
                return "0"
            text = format(value, "f")
            return text.rstrip("0").rstrip(".") if "." in text else text
        if isinstance(value, dict):
            return "{" + ",".join(canonical(k) + ":" + canonical(value[k]) for k in sorted(value, key=lambda k: k.encode("utf-8"))) + "}"
        if isinstance(value, list):
            return "[" + ",".join(canonical(v) for v in value) + "]"
        return json.dumps(value, ensure_ascii=False, separators=(",", ":")).replace(chr(0x85), r"\u0085").replace(chr(0x2028), r"\u2028").replace(chr(0x2029), r"\u2029")

    def decode(raw):
        def number(token):
            if re.fullmatch(r"-?0(?:\.0+)?(?:[eE][+-]?[0-9]+)?", token):
                return Decimal(0)
            return Decimal(token)
        return json.loads(raw, parse_int=number, parse_float=number)

    def sha(raw):
        return hashlib.sha256(raw).hexdigest()

    data = ROOT / "internal/audit/testdata"
    vectors = json.loads((data / "canonical-v1.json").read_text())
    for vector in vectors:
        raw = canonical(decode(vector["Input"])).encode()
        if raw != vector["Bytes"].encode() or sha(raw) != vector["SHA256"]:
            raise ValueError("independent canonical golden mismatch")
    for prefix in ("full", "empty"):
        manifest = decode((data / (prefix + "-manifest.json")).read_bytes())
        claimed = manifest["manifest_sha256"]
        manifest["manifest_sha256"] = ""
        if sha(canonical(manifest).encode()) != claimed:
            raise ValueError("independent manifest golden mismatch")
        previous, count = "0" * 64, 0
        for name in ("events", "selection", "watermarks", "dependency_boundaries"):
            raw = (data / (prefix + "-" + name + ".jsonl")).read_bytes()
            if sha(raw) != manifest[name + "_sha256"]:
                raise ValueError("independent file golden mismatch")
            for line in raw.split(b"\n"):
                if not line:
                    continue
                obj = decode(line)
                if canonical(obj).encode() != line:
                    raise ValueError("independent record golden mismatch")
                if name == "events":
                    if obj["previous_sha256"] != previous:
                        raise ValueError("independent chain golden mismatch")
                    previous, count = sha(line), count + 1
        if previous != manifest["terminal_sha256"] or count != manifest["count"]:
            raise ValueError("independent terminal golden mismatch")
    return {"vectors": len(vectors), "complete_exports": 2, "reference": "Python Decimal and UTF-8 byte-key sorting", "identical": True}


def mutation_proofs(measured, out):
    """Overlay mutations never edit the checkout or reviewer scratch files."""
    mutations = []
    def add(name, file, old, new, test):
        source = (ROOT / "internal/audit" / file).read_text()
        if old not in source:
            raise ValueError("mutation target changed: " + name)
        mutations.append((name, file, source.replace(old, new, 1), test))

    add("restore-port-54329-guard", "source_test.go", 'u.Hostname() != "localhost"', 'u.Hostname() != "localhost" || u.Port() != "54329"', "^TestSourceDatabaseURLGuardAllowsLocalhostPorts$")
    add("remove-oversized-transfer-guard", "source.go", "CASE WHEN octet_length(payload)<=4194304 THEN payload ELSE NULL END", "payload", "^TestSourcePageRejectsOversizedBeforeTransfer$")
    add("omit-empty-queue-watermark-check", "export.go", "if len(waters) != len(identities) {", "if false && len(waters) != len(identities) {", "^TestSourceReplayRejectsForgedWatermarks$/^missing$")
    paging = "^TestSourcePagingAcrossQueuesAndCollation$"
    add("duplicate-queue-boundary", "source.go", "for i := range inventory {", "inventory = append(inventory[:pageSize], inventory[pageSize-1:]...)\nfor i := range inventory {", paging)
    add("skip-queue-boundary", "source.go", "for i := range inventory {", "inventory = append(inventory[:pageSize], inventory[pageSize+1:]...)\nfor i := range inventory {", paging)
    add("lane-before-base", "source.go", "{a.Base, b.Base}, {a.Lane, b.Lane}", "{a.Lane, b.Lane}, {a.Base, b.Base}", paging)
    add("database-collation-order", "source.go", "sort.Slice(inventory, func(i, j int) bool { return keyLess(inventory[i].Queue, inventory[j].Queue) })", "_ = sort.Slice", paging)
    add("event-keyset-inclusive", "source.go", "id>$2", "id>=$2", paging)
    add("truncate-bounds", "source.go", "floor = floor.Add(time.Microsecond)", "floor = floor", "^TestSourceSubmicrosecondBoundaries$")
    add("omit-file-hash", "export.go", "hex.EncodeToString(h.Sum(nil)) != item.sha", "false", "^TestSourceReplayRejectsEventCorruptionWithoutManifestRehash$")
    add("reject-v1-dependencies", "export.go", 'return role == "scope" || role == "dependency"', 'return role == "scope"', "^TestV1DependencyAndHistoricalAbsenceReplay$")
    add("reject-v1-historical-absence", "export.go", 'r.Status == "not_recorded"', 'false', "^TestV1DependencyAndHistoricalAbsenceReplay$")
    source = (ROOT / "internal/audit/export.go").read_text()
    for condition in ("!ok || actual.Queue != w.Queue || w.ID > actual.ID", "previousWater != nil && !sourceLess(*previousWater, current)", "len(waters) != len(identities)", "!ok || w.Queue != s.Source.Queue || s.Source.ID > w.ID"):
        old = "if " + condition + " {"
        if old not in source:
            raise ValueError("watermark mutation target changed")
        source = source.replace(old, "if false && (" + condition + ") {", 1)
    source = source.replace("; duplicate {", "; false && duplicate {", 1)
    mutations.append(("accept-forged-watermarks", "export.go", source, "^TestSourceReplayRejectsForgedWatermarks$"))
    current = (ROOT / "internal/audit/source.go").read_text()
    original = subprocess.check_output(["git", "show", "eada85c:internal/audit/source.go"], cwd=ROOT, text=True)
    old_queues = original[original.index("func queues("):original.index("func scan(")]
    start, end = current.index("func queues("), current.index("// keyLess")
    mutations.append(("restore-repeated-queue-scan", "source.go", (current[:start] + old_queues + current[end:]).replace('\n\t"sort"', ""), "^TestSourceQueueIdentityQueryOnce$"))
    unbounded = current.replace("WHERE page_bytes<=4194304 OR prior_bytes<=4194304", "WHERE true").replace("pageBytes+len(e.payload) > MaxBytes", "false")
    mutations.append(("remove-cumulative-byte-bound", "source.go", unbounded, "^TestSourceLargePayloadPagesBounded$"))
    results = []
    for name, file, source, test in mutations:
        work = out / "mutations" / name
        work.mkdir(parents=True, exist_ok=True)
        replacement = work / file
        replacement.write_text(source)
        overlay = work / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(ROOT / "internal/audit" / file): str(replacement)}}))
        flags = [] if name == "remove-cumulative-byte-bound" else ["-short"]
        result = measured(["go", "test", "-overlay", str(overlay)] + flags + ["-count=1", "-timeout=30s", "./internal/audit", "-run", test], ROOT, out, "mutation-" + name, check=False)
        if result.returncode == 0 or "build failed" in result.stdout or "[setup failed]" in result.stdout:
            raise ValueError("mutation survived or failed to build: " + name)
        results.append({"mutation": name, "killed": True, "test": test})
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    # Guard before any subprocess; do not propagate a caller-controlled libpq route.
    maintenance = scratch_url(os.environ.get("OPENMERGE_TEST_DATABASE_URL", ""))
    os.environ["OPENMERGE_TEST_DATABASE_URL"] = maintenance
    toolchain = "/home/riels/.local/share/mise/installs/go/1.27.3"
    if os.environ.get("GOROOT") != toolchain or os.environ["PATH"].split(":")[0] != toolchain + "/bin":
        raise ValueError("export pinned Go GOROOT and PATH")
    os.environ["GOMAXPROCS"] = "2"
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    v = module("foundation_evidence", ROOT / "scripts/adr-0008-1-verify.py")
    previous = module("projection_evidence", ROOT / "scripts/verify-adr0008-9c-3.py")
    invoke = v.invoke

    def measured(cmd, cwd, work, name, **kwargs):
        print(name, flush=True)
        start = time.monotonic()
        result = invoke(cmd, cwd, work, name, **kwargs)
        v.COMMANDS[-1]["wall_seconds_including_lock"] = round(time.monotonic() - start, 3)
        v.COMMANDS[-1]["package_seconds"] = dict(re.findall(r"^ok\s+(\S+)\s+(\d+(?:\.\d+)?)s", result.stdout, re.MULTILINE))
        return result

    v.invoke = measured
    result = {"slice": "10a-1", "classification": "Inert; depends on Hold PR 8 and 3a–4; deploy after B", "commands": v.COMMANDS}
    try:
        result["baseline"] = v.check_tree("baseline", args.baseline, v.git(args.baseline, "rev-parse", BASE))
        result["candidate"] = v.check_tree("candidate", ROOT)
        result["independent_goldens"] = independent_goldens()
        result["exports"] = v.exports(v.EVENTS, v.QUEUES)
        result["wire"] = v.compare_fixtures(args.baseline, ROOT)
        # Compare every existing fixture, not only registered wire JSON.
        fixtures = [p for p in v.git(args.baseline, "ls-files").splitlines() if "/testdata/" in p]
        for path in fixtures:
            if (args.baseline / path).read_bytes() != (ROOT / path).read_bytes():
                raise ValueError("existing fixture changed")
        result["existing_fixtures_unchanged"] = len(fixtures)
        changed = v.git(ROOT, "diff", "--name-only", BASE, "HEAD").splitlines()
        if any(not (p.startswith("internal/audit/") or p in ("docs/research/adr-0008-10a-design.md", "docs/research/adr-0008-10a-1-evidence.md", "scripts/verify-adr0008-10a-1.py")) for p in changed):
            raise ValueError("out-of-scope source change")
        result["source_sha256"] = {p: v.sha(ROOT / p) for p in changed}
        # Validate the CI authority port while routing to the permitted shared sandbox.
        from urllib.parse import urlsplit, urlunsplit, parse_qsl, urlencode
        ci = urlsplit(maintenance)
        params = dict(parse_qsl(ci.query))
        params["port"] = "54329"
        ci_shape = urlunsplit((ci.scheme, "postgres@localhost:5432", ci.path, urlencode(params), ""))
        os.environ["OPENMERGE_TEST_DATABASE_URL"] = ci_shape
        try:
            measured(["go", "test", "-count=1", "./internal/audit", "-run", "^TestSource(SnapshotLateCommitReplay|ReadOnlyTenancyAndPaging|StrictReplayIntegrity|SubmicrosecondBoundaries)$"], ROOT, out, "ci-dsn-shape")
        finally:
            os.environ["OPENMERGE_TEST_DATABASE_URL"] = maintenance
        result["ci_dsn_shape"] = {"authority_port": 5432, "sandbox_route_port": 54329, "tests": 4, "passed": True}
        result["mutations"] = mutation_proofs(measured, out)
        measured(["make", "check"], ROOT, out, "make-check")
        measured(["go", "test", "-race", "-v", "-count=1", "./internal/audit"], ROOT, out, "foundation-full")
        burners = []
        try:
            for _ in range(8):
                burners.append(subprocess.Popen([sys.executable, "-c", "while True: pass"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
            measured(["go", "test", "-race", "-count=20", "-v", "-timeout=60m", "./internal/audit", "-run", "TestCanonical|TestExportV1|TestStrictDecode|TestPublication|TestV1|TestReplay|TestSource(Snapshot|ReadOnly|Strict|Submicrosecond|DatabaseURL|Paging|Replay|Equivalent|Page|Queue|LargePayload)"], ROOT, out, "foundation-stress", env_extra={"GOMAXPROCS": "1"})
            if any(b.poll() is not None for b in burners):
                raise ValueError("CPU burner exited during stress")
            result["stress"] = {"iterations": 20, "GOMAXPROCS": 1, "race": True, "new_and_changed_tests": True, "non_short_included": True, "owned_burners": [b.pid for b in burners]}
        finally:
            for b in burners:
                b.terminate()
            for b in burners:
                b.wait()
        result["stress"]["all_reaped"] = True
        for label, tree in (("before", args.baseline), ("after", ROOT)):
            packages = ["./internal/serve"]
            if (tree / "internal/audit").is_dir():
                packages.insert(0, "./internal/audit")
            measured(["go", "test", "-race", "-short", "-count=1"] + packages, tree, out, "ci-packages-" + label)
            measured(["go", "test", "-race", "-short", "-count=1", "-p=1", "-timeout=15m", "./..."], tree, out, "timing-" + label, env_extra={"GOMAXPROCS": "2"})
        projections, cli, mixed, contracts = [], [], [], []
        for label, tree in (("baseline", args.baseline), ("candidate", ROOT)):
            work = out / label
            work.mkdir(exist_ok=True)
            def overlay(path, text):
                source = work / (Path(path).name + ".overlay")
                source.write_text(text)
                file = work / "overlay.json"
                file.write_text(json.dumps({"Replace": {str(tree / path): str(source)}}))
                return str(file)
            projection = work / "projections.json"
            projections.append(projection)
            ov = overlay("internal/serve/authority_github_compatibility_test.go", (ROOT / "internal/serve/authority_github_compatibility_test.go").read_text())
            measured(["go", "test", "-overlay", ov, "./internal/serve", "-run", "^TestAuthorityGithubProjectionBytes$", "-count=1"], tree, out, label + "-projections", env_extra={"ADR_GITHUB_PROJECTIONS": projection})
            output = work / "cli.txt"
            cli.append(output)
            ov = overlay("cmd/openmerge/adr_2d_compatibility_test.go", previous.CLI_GO)
            measured(["go", "test", "-overlay", ov, "./cmd/openmerge", "-run", "^TestADR2dGithubCLI$", "-count=1"], tree, out, label + "-cli", env_extra={"ADR_GITHUB_PROJECTIONS": projection, "ADR_GITHUB_CLI": output})
            output = work / "mixed.txt"
            mixed.append(output)
            ov = overlay("internal/serve/authority_acceptance_compat_test.go", (ROOT / "internal/serve/authority_acceptance_compat_test.go").read_text())
            measured(["go", "test", "-overlay", ov, "./internal/serve", "-run", "^TestAuthorityAcceptanceMixedGithubBytes$", "-count=1"], tree, out, label + "-mixed", env_extra={"ADR_ACCEPTANCE_GITHUB_BYTES": output})
            measured(["go", "test", "-race", "-short", "-count=1", "./internal/serve", "./internal/config", "-run", "StageAAccountableIdentityDiagnosticExact|LegacyLineZeroDiagnosticsRemainExact"], tree, out, label + "-stage-a")
            output = work / "contracts.json"
            contracts.append(output)
            ov = overlay(v.TARGET, v.DIFF_GO)
            measured(["go", "test", "-overlay", ov, "./internal/queue", "-run", "^TestADR00081Differential$", "-count=1"], tree, out, label + "-contracts", env_extra={"ADR_DIFF": output})
        for name, paths in (("github_projections", projections), ("github_cli", cli), ("mixed_github", mixed), ("contracts", contracts)):
            if paths[0].read_bytes() != paths[1].read_bytes():
                raise ValueError(name + " differs")
            result[name] = {"identical": True, "sha256": v.sha(paths[1])}
        if result["mixed_github"]["sha256"] != "cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee":
            raise ValueError("mixed GitHub hash changed")
        result["contracts"]["records"] = len(json.loads(contracts[1].read_text()))
        measured([sys.executable, str(ROOT / "scripts/check-live-journal-replay.py"), "--baseline", str(args.baseline), "--candidate", str(ROOT), "--events", str(v.EVENTS), "--queues", str(v.QUEUES), "--strict-state", "--out", str(out / "replay.json")], ROOT, out, "strict-replay", mem="6GiB")
        replay = json.loads((out / "replay.json").read_text())
        result["replay"] = {k: replay[k] for k in ("verdict", "actions_identical", "structural_state_identical", "snapshot_hashes_identical", "boundary_checkpoints_identical", "boundary_probe_count", "non_allowlisted_fields", "gate2", "gate4", "self_test_exit_status")}
        if replay["verdict"] != "PASS":
            raise ValueError("strict replay failed")
        result["simulator"] = v.compare_simulator(args.baseline, ROOT, out)
        result["verdict"] = "PASS"
    except Exception as error:
        result["verdict"] = "FAIL"
        # Failure detail is intentionally local; never echo database exceptions/DSNs.
        result["error_class"] = type(error).__name__
        print("verification failed; inspect private logs", file=sys.stderr)
    (out / "summary.json").write_text(json.dumps(result, indent=2) + "\n")
    manifest = {str(p.relative_to(out)): v.sha(p) for p in sorted(out.rglob("*")) if p.is_file() and p.name != "manifest.json"}
    (out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    return result["verdict"] != "PASS"


if __name__ == "__main__":
    sys.exit(main())
