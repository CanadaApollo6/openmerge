#!/usr/bin/env python3
"""Offline 10a-2a proof; uses shared test Postgres schemas, never a scratch database."""
import argparse
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
BASE = "25d309d80e7a8b2944d642bd17c2277c127fe274"


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def mutation_proofs(measured, out, pattern):
    """Compile overlay mutants, then require focused semantic tests to kill them."""
    targets = [
        ("reuse-spool-decode-storage", "spool.go", "c.value = spoolRecord{}", "// reuse cursor storage"),
        ("missing-required-source", "evidence.go", "required && len(found) != 1", "false"),
        ("oldest-observation", "evidence.go", "DESC,e.id DESC LIMIT 1", "ASC,e.id ASC LIMIT 1"),
        ("accept-wrong-check-result", "evidence.go", "string(check.Result) != newest.State", "false"),
        ("accept-invalid-role", "export.go", 'return role == "scope" || role == "dependency"', "return true"),
        ("omit-retained-closure", "export.go", "return verifyRetained(spoolDir, requiredPath, selectedPath)", "return nil"),
    ]
    results = []
    for name, file, old, new in targets:
        source = ROOT / "internal/audit" / file
        text = source.read_text()
        # Whitespace changes are irrelevant to the mutation intent.
        match = re.search(r"\s*".join(re.escape(x) for x in old.split()), text)
        if match is None:
            raise ValueError("mutation target changed: " + name)
        text = text[:match.start()] + new + text[match.end():]
        if name == "accept-invalid-role":
            # Remove both independent role guards for this one conceptual
            # mutant; leaving the range/role guard intact masks enum removal.
            guard = 'if (s.Role == "scope") != inRange {'
            if guard not in text:
                raise ValueError("role/range mutation target changed")
            text = text.replace(guard, "if false && inRange {", 1)
        work = out / "mutations" / name
        work.mkdir(parents=True, exist_ok=True)
        replacement = work / file
        replacement.write_text(text)
        overlay = work / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(source): str(replacement)}}))
        test = measured(["go", "test", "-overlay", str(overlay), "-short", "-count=1", "-timeout=2m", "./internal/audit", "-run", pattern], ROOT, out, "mutation-" + name, check=False)
        output = test.stdout + test.stderr
        if test.returncode == 0 or "build failed" in output or "[setup failed]" in output or "--- FAIL:" not in output:
            raise ValueError("mutation survived or failed before assertions: " + name)
        results.append({"mutation": name, "killed": True, "pattern": pattern})
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--phase", choices=("all", "regression"), default="all")
    parser.add_argument("--stress-pattern", default="TestEvidence|TestClosure|TestCheckAttempt|TestSourceReplayRejectsScopeShapeAndCrossQueueOrder|TestExportV1SemanticExpectations")
    parser.add_argument("--baseline-timing-log", type=Path, help="reuse an already completed identical baseline timing; requires adjacent exit-status.txt containing 0")
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
    result = {"slice": "10a-2a", "classification": "Inert; depends on Hold PR 8 and 3a–4; deploy after B", "commands": v.COMMANDS}
    try:
        result["baseline"] = v.check_tree("baseline", args.baseline, v.git(args.baseline, "rev-parse", BASE))
        result["candidate"] = v.check_tree("candidate", ROOT)
        result["exports"] = v.exports(v.EVENTS, v.QUEUES)
        result["wire"] = v.compare_fixtures(args.baseline, ROOT)
        # Compare every existing fixture, not only registered wire JSON.
        fixtures = [p for p in v.git(args.baseline, "ls-files").splitlines() if "/testdata/" in p and not p.startswith("internal/audit/testdata/")]
        for path in fixtures:
            if (args.baseline / path).read_bytes() != (ROOT / path).read_bytes():
                raise ValueError("existing fixture changed")
        result["existing_fixtures_unchanged"] = len(fixtures)
        changed = v.git(ROOT, "diff", "--name-only", BASE, "HEAD").splitlines()
        permitted = {"internal/store/landing_evidence.go", "scripts/adr0008_scratch_db.py",
                     "scripts/test_adr0008_scratch_db.py", "scripts/verify-adr0008-10a-2a.py",
                     "docs/research/adr-0008-10a-2-size-gate.md",
                     "docs/research/adr-0008-10a-2a-evidence.md"}
        if any(not (p.startswith("internal/audit/") or p in permitted) for p in changed):
            raise ValueError("change outside approved audit/helper/evidence scope")
        result["source_sha256"] = {p: v.sha(ROOT / p) for p in changed}
        callers = [p for p in v.git(args.baseline, "ls-files", "internal/store/*test.go").splitlines()
                   if "LandingDecisionEvidence" in (args.baseline / p).read_text()]
        for path in callers:
            if (args.baseline / path).read_bytes() != (ROOT / path).read_bytes():
                raise ValueError("existing LandingDecisionEvidence caller test changed")
        result["unchanged_landing_caller_test_files"] = callers
        measured(["go", "test", "-race", "-count=1", "./internal/store", "-run", "Landing|Decision"], ROOT, out, "existing-landing-caller")
        # Both authority ports are parsed without connecting to CI's endpoint.
        # Database integration remains on the explicitly guarded caller endpoint.
        if ":5432/" not in scratch_url("postgres://postgres@localhost:5432/postgres?sslmode=disable"):
            raise ValueError("CI scratch endpoint rejected")
        measured(["go", "test", "-count=1", "./internal/audit", "-run", "^TestSourceDatabaseURLGuardAllowsLocalhostPorts$"], ROOT, out, "ci-dsn-guard")
        result["ci_dsn_guard"] = {"ports": [5432, 54329], "connection_free": True}
        if args.phase != "regression":
            result["mutations"] = mutation_proofs(measured, out, args.stress_pattern)
            measured(["make", "check"], ROOT, out, "make-check")
            measured(["go", "test", "-race", "-v", "-count=1", "./internal/audit"], ROOT, out, "evidence-full")
            burners = []
            try:
                for _ in range(8):
                    burners.append(subprocess.Popen([sys.executable, "-c", "while True: pass"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
                before_cpu = {b.pid: int(Path(f"/proc/{b.pid}/stat").read_text().split()[13]) for b in burners}
                stress = measured(["go", "test", "-race", "-count=20", "-v", "-timeout=60m", "./internal/audit", "-run", args.stress_pattern], ROOT, out, "evidence-stress", env_extra={"GOMAXPROCS": "1"})
                passed = re.findall(r"^--- PASS: (\S+)", stress.stdout, re.MULTILINE)
                if not passed or any(passed.count(name) != 20 for name in set(passed)):
                    raise ValueError("stress did not execute each selected test 20 times")
                cpu_ticks = {b.pid: int(Path(f"/proc/{b.pid}/stat").read_text().split()[13]) - before_cpu[b.pid] for b in burners}
                if any(ticks <= 0 for ticks in cpu_ticks.values()):
                    raise ValueError("CPU burner failed to consume CPU during stress")
                if any(b.poll() is not None for b in burners):
                    raise ValueError("CPU burner exited during stress")
                result["stress"] = {"iterations": 20, "GOMAXPROCS": 1, "race": True, "new_and_changed_tests": True, "non_short_included": True, "owned_burners": [b.pid for b in burners], "burner_cpu_ticks": cpu_ticks, "passed_tests": sorted(set(passed))}
            finally:
                for b in burners:
                    b.terminate()
                for b in burners:
                    b.wait()
            result["stress"]["all_reaped"] = True
            for label, tree in (("before", args.baseline), ("after", ROOT)):
                if label == "before" and args.baseline_timing_log:
                    log = args.baseline_timing_log.resolve()
                    if log.with_name("exit-status.txt").read_text().strip() != "0":
                        raise ValueError("baseline timing did not pass")
                    package_seconds = dict(re.findall(r"^ok\s+(\S+)\s+(\d+(?:\.\d+)?)s", log.read_text(), re.MULTILINE))
                    if not package_seconds or "FAIL" in log.read_text():
                        raise ValueError("baseline timing log incomplete")
                    result["baseline_timing_reused"] = {"log": str(log), "sha256": v.sha(log), "package_seconds": package_seconds, "exit": 0}
                else:
                    measured(["go", "test", "-race", "-short", "-count=1", "-p=1", "-timeout=15m", "./..."], tree, out, "timing-" + label, env_extra={"GOMAXPROCS": "2"})
        projections, cli, mixed, contracts = [], [], [], []
        for label, tree in (("baseline", args.baseline), ("candidate", ROOT)):
            work = out / label
            work.mkdir(exist_ok=True)
            def overlay(path, text):
                source = work / (Path(path).name + ".overlay")
                source.write_text(text)
                file = work / (Path(path).stem + "-overlay.json")
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
        if result["contracts"]["records"] != 90:
            raise ValueError("expected 90 contract records")
        measured([sys.executable, str(ROOT / "scripts/check-live-journal-replay.py"), "--baseline", str(args.baseline), "--candidate", str(ROOT), "--events", str(v.EVENTS), "--queues", str(v.QUEUES), "--strict-state", "--out", str(out / "replay.json")], ROOT, out, "strict-replay", mem="6GiB")
        replay = json.loads((out / "replay.json").read_text())
        result["replay"] = {k: replay[k] for k in ("verdict", "actions_identical", "structural_state_identical", "snapshot_hashes_identical", "boundary_checkpoints_identical", "boundary_probe_count", "non_allowlisted_fields", "gate2", "gate4", "self_test_exit_status")}
        if replay["verdict"] != "PASS" or any(replay[k] != "2159/2159" for k in ("actions_identical", "structural_state_identical", "snapshot_hashes_identical", "boundary_checkpoints_identical")):
            raise ValueError("strict replay failed or unexpected checkpoint count")
        result["simulator"] = v.compare_simulator(args.baseline, ROOT, out)
        if result["simulator"]["blocks"] != 146:
            raise ValueError("expected 146 simulator blocks")
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
