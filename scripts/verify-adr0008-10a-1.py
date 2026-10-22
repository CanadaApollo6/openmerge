#!/usr/bin/env python3
"""Offline 10a-1 proof; uses shared test Postgres schemas, never a scratch database."""
import argparse
import hashlib
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
BASE = "7aa71a3f20b7c7b1b7538aaaa5caf1fdf77dff3d"


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


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
        result["baseline"] = v.check_tree("baseline", args.baseline, BASE)
        result["candidate"] = v.check_tree("candidate", ROOT)
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
        measured(["make", "check"], ROOT, out, "make-check")
        measured(["go", "test", "-race", "-v", "-count=1", "./internal/audit"], ROOT, out, "foundation-full")
        burners = []
        try:
            for _ in range(8):
                burners.append(subprocess.Popen([sys.executable, "-c", "while True: pass"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
            measured(["go", "test", "-race", "-count=20", "-v", "-timeout=60m", "./internal/audit"], ROOT, out, "foundation-stress", env_extra={"GOMAXPROCS": "1"})
            if any(b.poll() is not None for b in burners):
                raise ValueError("CPU burner exited during stress")
            result["stress"] = {"iterations": 20, "GOMAXPROCS": 1, "race": True, "non_short_included": True, "owned_burners": [b.pid for b in burners]}
        finally:
            for b in burners:
                b.terminate()
            for b in burners:
                b.wait()
        result["stress"]["all_reaped"] = True
        for label, tree in (("before", args.baseline), ("after", ROOT)):
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
