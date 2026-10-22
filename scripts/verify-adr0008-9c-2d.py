#!/usr/bin/env python3
"""Rerun the offline PR 9c-2d Hold evidence against a clean baseline checkout."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import uuid
import sys
import re
import time
sys.dont_write_bytecode = True
from adr0008_scratch_db import scratch_url


CLI_GO = r'''package main
import (
 "bytes"
 "encoding/json"
 "os"
 "testing"
 "github.com/CanadaApollo6/openmerge/internal/status"
)
func TestADR2dGithubCLI(t *testing.T) {
 raw,err:=os.ReadFile(os.Getenv("ADR_GITHUB_PROJECTIONS"));if err!=nil {t.Fatal(err)}
 var projections map[string]string
 if err=json.Unmarshal(raw,&projections);err!=nil {t.Fatal(err)}
 var response status.Response
 if err=json.Unmarshal([]byte(projections["/v1/queues"]),&response);err!=nil {t.Fatal(err)}
 var output bytes.Buffer
 if err=printStatus(&output,response);err!=nil {t.Fatal(err)}
 if err=os.WriteFile(os.Getenv("ADR_GITHUB_CLI"),output.Bytes(),0600);err!=nil {t.Fatal(err)}
}
'''


def scratch_sql(url, sql):
    result = subprocess.run(["psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", url],
                            input=sql, text=True, capture_output=True, timeout=120)
    if result.returncode:
        raise RuntimeError("scratch database operation failed")


def main():
    root = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--toolchain", type=Path, default=os.environ.get("GOROOT"))
    parser.add_argument("--lock", type=Path, default=Path.home() / ".cache/openmerge-heavy.lock")
    parser.add_argument("--events", type=Path, default=Path.home() / ".cache/om-pr72-fix1-input/events.jsonl")
    parser.add_argument("--queues", type=Path, default=Path.home() / ".cache/om-pr72-fix1-input/queues.jsonl")
    parser.add_argument("--timing-before-log", type=Path, help="reuse a completed d9ec975 CI-like timing stdout log; otherwise measure it")
    args = parser.parse_args()
    maintenance = scratch_url(os.environ.get("OPENMERGE_TEST_DATABASE_URL", ""))
    # Reuse the shared guard's strictly allowlisted scratch namespace.
    database_name = "om_9c2c_" + uuid.uuid4().hex
    database_url = scratch_url(maintenance, database_name)
    created = False
    original_database = os.environ.get("OPENMERGE_TEST_DATABASE_URL")
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    spec = importlib.util.spec_from_file_location("evidence", root / "scripts/adr-0008-1-verify.py")
    v = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(v)
    v.LOCK, v.EVENTS, v.QUEUES = args.lock, args.events, args.queues
    original_invoke = v.invoke
    def measured_invoke(cmd, cwd, run, name, **kwargs):
        started = time.monotonic()
        result = original_invoke(cmd, cwd, run, name, **kwargs)
        v.COMMANDS[-1]["wall_seconds"] = round(time.monotonic() - started, 3)
        v.COMMANDS[-1]["package_seconds"] = {
            package: float(seconds) for package, seconds in re.findall(
                r"^ok\s+(\S+)\s+(\d+(?:\.\d+)?)s", result.stdout, re.MULTILINE)}
        return result
    v.invoke = measured_invoke
    result = {"trial_class": "Hold", "slice": "9c-2d", "offline_only": True, "commands": v.COMMANDS}
    try:
        if args.toolchain is None:
            raise v.VerifyError("set GOROOT or pass --toolchain for Go 1.27.3")
        toolchain = str(args.toolchain.resolve())
        if os.environ.get("GOROOT") != toolchain or os.environ["PATH"].split(":")[0] != toolchain + "/bin":
            raise v.VerifyError("export both Go 1.27.3 GOROOT and its bin directory first in PATH")
        if (args.toolchain / "VERSION").read_text().splitlines()[0] != "go1.27.3":
            raise v.VerifyError("Go 1.27.3 is required")

        result["baseline"] = v.check_tree("baseline", args.baseline)
        if not result["baseline"].startswith("d9ec975"):
            raise v.VerifyError("baseline must be local main d9ec975")
        result["candidate"] = v.check_tree("candidate", root)
        os.environ.setdefault("GOMAXPROCS", "2")
        result["environment"] = {"GOROOT": toolchain, "PATH_prefix": toolchain + "/bin", "database": "localhost:54329, isolated test schemas"}
        result["exports"] = v.exports(v.EVENTS, v.QUEUES)
        result["wire"] = v.compare_fixtures(args.baseline, root)
        sources = sorted(root.glob("internal/*/authority_acceptance*_test.go")) + sorted(root.glob("cmd/openmerge/authority_acceptance*_test.go")) + [root / "scripts/verify-adr0008-9c-2d.py", root / "scripts/test_adr0008_scratch_db.py"]
        result["source_hashes"] = {str(path.relative_to(root)): v.sha(path) for path in sources}
        scratch_sql(maintenance, f'CREATE DATABASE "{database_name}"')
        created = True
        os.environ["OPENMERGE_TEST_DATABASE_URL"] = database_url
        print("make check", flush=True)
        v.invoke(["make", "check"], root, out, "make-check")
        print("focused propagation, operations, barrier and exact-output race tests", flush=True)
        v.invoke(["go", "test", "-race", "-short", "-v", "./internal/authority", "./internal/gitops", "./internal/serve", "./internal/store", "./internal/queue", "./internal/config", "./internal/status", "./cmd/openmerge", "-run", "AuthorityAcceptance|Completion|AuthorityBarrier|LegacyLineZero|StageAAccountableIdentityDiagnosticExact", "-count=1"], root, out, "focused")
        for label, tree in (("before", args.baseline), ("after", root)):
            if label == "before" and args.timing_before_log:
                raw = args.timing_before_log.read_text()
                packages = {package: float(seconds) for package, seconds in re.findall(r"^ok\s+(\S+)\s+(\d+(?:\.\d+)?)s", raw, re.MULTILINE)}
                expected = {"github.com/CanadaApollo6/openmerge/" + p for p in ("internal/serve", "internal/gitops", "internal/queue", "cmd/openmerge")}
                if set(packages) != expected or "FAIL" in raw:
                    raise v.VerifyError("incomplete baseline package timing log")
                (out / "timing-before.log").write_text(raw)
                result["timing_before"] = {"package_seconds": packages, "source_sha256": v.sha(args.timing_before_log), "reused": True}
                continue
            print("CI-like package timing " + label, flush=True)
            v.invoke(["go", "test", "-p=1", "-race", "-short", "-count=1", "-timeout=15m", "./internal/serve", "./internal/gitops", "./internal/queue", "./cmd/openmerge"], tree, out, "timing-" + label, env_extra={"GOMAXPROCS": "2"})
        print("byte-identical GitHub worker/status/API/health and CLI projections", flush=True)
        projection_paths = []
        cli_paths = []
        for label, tree in (("baseline", args.baseline), ("candidate", root)):
            work = out / (label + "-projections")
            work.mkdir(exist_ok=True)
            source = work / "compatibility_test.go"
            source.write_text((root / "internal/serve/authority_github_compatibility_test.go").read_text())
            overlay = work / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(tree / "internal/serve/authority_github_compatibility_test.go"): str(source)}}))
            projection = work / "projections.json"
            projection_paths.append(projection)
            v.invoke(["go", "test", "-overlay", str(overlay), "./internal/serve", "-run", "^TestAuthorityGithubProjectionBytes$", "-count=1"], tree, out, label + "-github-projections", env_extra={"ADR_GITHUB_PROJECTIONS": projection})
            cli_source = work / "cli_test.go"
            cli_source.write_text(CLI_GO)
            overlay.write_text(json.dumps({"Replace": {str(tree / "cmd/openmerge/adr_2d_compatibility_test.go"): str(cli_source)}}))
            cli = work / "cli.txt"
            cli_paths.append(cli)
            v.invoke(["go", "test", "-overlay", str(overlay), "./cmd/openmerge", "-run", "^TestADR2dGithubCLI$", "-count=1"], tree, out, label + "-github-cli", env_extra={"ADR_GITHUB_PROJECTIONS": projection, "ADR_GITHUB_CLI": cli})
        if projection_paths[0].read_bytes() != projection_paths[1].read_bytes() or cli_paths[0].read_bytes() != cli_paths[1].read_bytes():
            raise v.VerifyError("GitHub projection or CLI bytes differ")
        result["github_projections"] = {"identical": True, "status_api_health_author_sha256": v.sha(projection_paths[1]), "cli_sha256": v.sha(cli_paths[1])}
        print("same-repository mixed GitHub OIDs, permissions and event bytes", flush=True)
        mixed_paths = []
        mixed_source = (root / "internal/serve/authority_acceptance_compat_test.go").read_text()
        for label, tree in (("baseline", args.baseline), ("candidate", root)):
            work = out / (label + "-mixed")
            work.mkdir(exist_ok=True)
            source = work / "mixed_test.go"
            source.write_text(mixed_source)
            overlay = work / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(tree / "internal/serve/authority_acceptance_compat_test.go"): str(source)}}))
            output = work / "mixed.json"
            mixed_paths.append(output)
            v.invoke(["go", "test", "-overlay", str(overlay), "./internal/serve", "-run", "^TestAuthorityAcceptanceMixedGithubBytes$", "-count=1"], tree, out, label + "-mixed-github", env_extra={"ADR_ACCEPTANCE_GITHUB_BYTES": output})
        if mixed_paths[0].read_bytes() != mixed_paths[1].read_bytes():
            raise v.VerifyError("same-repository mixed GitHub bytes differ from main-built expectations")
        result["mixed_github"] = {"identical": True, "sha256": v.sha(mixed_paths[1])}
        paths = []
        for label, tree in (("baseline", args.baseline), ("candidate", root)):
            work = out / label
            work.mkdir(exist_ok=True)
            source = work / "contracts_test.go"
            source.write_text(v.DIFF_GO)
            overlay = work / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(tree / v.TARGET): str(source)}}))
            path = work / "contracts.json"
            paths.append(path)
            print(label + " depth contracts", flush=True)
            v.invoke(["go", "test", "-overlay", str(overlay), "./internal/queue", "-run", "^TestADR00081Differential$", "-count=1"], tree, out, label + "-contracts", env_extra={"ADR_DIFF": path})
        result["contracts"] = v.compare_contracts(*paths)
        if not result["contracts"]["identical"]:
            raise v.VerifyError("depth-1/4 contracts differ")
        result["contracts"] = {"identical": True, "records": len(json.loads(paths[1].read_text())), "sha256": v.sha(paths[1])}
        print("strict live-journal replay", flush=True)
        v.invoke([sys.executable, str(root / "scripts/check-live-journal-replay.py"), "--baseline", str(args.baseline), "--candidate", str(root), "--events", str(v.EVENTS), "--queues", str(v.QUEUES), "--strict-state", "--out", str(out / "replay.json")], root, out, "strict-replay", mem="6GiB")
        replay = json.loads((out / "replay.json").read_text())
        result["replay"] = {key: replay[key] for key in ("verdict", "state_mode", "actions_identical", "structural_state_identical", "snapshot_hashes_identical", "boundary_checkpoints_identical", "boundary_probe_count", "non_allowlisted_fields", "gate2", "gate4", "self_test", "self_test_exit_status")}
        result["replay"]["full_report_sha256"] = v.sha(out / "replay.json")
        if result["replay"]["verdict"] != "PASS":
            raise v.VerifyError("strict replay did not pass")
        print("legacy raw Git objects, refs and command bytes", flush=True)
        # This driver acquires the heavy lock around each Go child itself.
        legacy = subprocess.run([sys.executable, str(root / "scripts/check-adr0008-3a-legacy.py"), "--baseline", str(args.baseline), "--candidate", str(root), "--out", str(out / "legacy")], cwd=root, capture_output=True, text=True)
        (out / "legacy-stdout.log").write_text(legacy.stdout)
        (out / "legacy-stderr.log").write_text(legacy.stderr)
        if legacy.returncode:
            raise v.VerifyError("legacy Git probe failed; see legacy logs")
        result["legacy"] = json.loads((out / "legacy/report.json").read_text())
        print("seeded simulator in both trees", flush=True)
        result["simulator"] = v.compare_simulator(args.baseline, root, out)
        print("20-iteration race stress with eight owned CPU burners", flush=True)
        burners = []
        try:
            for _ in range(8):
                burners.append(subprocess.Popen([sys.executable, "-c", "while True: pass"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
            v.invoke(["go", "test", "-p=1", "-race", "-short", "-cpu=1", "-count=20", "-timeout=15m", "./internal/serve", "./internal/gitops", "./cmd/openmerge", "-run", "^TestAuthorityAcceptance"], root, out, "stress", env_extra={"GOMAXPROCS": "1"})
        finally:
            for burner in burners:
                burner.terminate()
            for burner in burners:
                burner.wait()
        result["stress"] = {"iterations": 20, "GOMAXPROCS": 1, "race": True, "owned_cpu_burners": len(burners), "all_reaped": True}
        binary = root / "bin/openmerge"
        result["binary"] = {"path": str(binary), "sha256": v.sha(binary)}
        result["verdict"] = "PASS"
    except Exception as error:
        result["verdict"] = "FAIL"
        result["error"] = str(error)
    finally:
        if original_database is None:
            os.environ.pop("OPENMERGE_TEST_DATABASE_URL", None)
        else:
            os.environ["OPENMERGE_TEST_DATABASE_URL"] = original_database
        if created:
            scratch_sql(maintenance, f'DROP DATABASE "{database_name}"')
    (out / "summary.json").write_text(json.dumps(result, indent=2) + "\n")
    manifest = {str(path.relative_to(out)): v.sha(path) for path in sorted(out.rglob("*")) if path.is_file() and path.name != "manifest.json"}
    (out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(json.dumps({key: result[key] for key in ("verdict", "error", "candidate") if key in result}), flush=True)
    return result["verdict"] != "PASS"


if __name__ == "__main__":
    sys.exit(main())
