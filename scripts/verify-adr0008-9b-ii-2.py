#!/usr/bin/env python3
"""Rerun the offline PR 9b-ii-2a Hold evidence against a clean baseline checkout."""
import argparse
import importlib.util
import json
import os
import sys
from pathlib import Path

sys.dont_write_bytecode = True


def main():
    root = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    spec = importlib.util.spec_from_file_location("evidence", root / "scripts/adr-0008-1-verify.py")
    v = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(v)
    result = {"trial_class": "Hold", "slice": "9b-ii-2a", "offline_only": True, "commands": v.COMMANDS}
    try:
        toolchain = "/home/riels/.local/share/mise/installs/go/1.27.3"
        if os.environ.get("GOROOT") != toolchain or os.environ["PATH"].split(":")[0] != toolchain + "/bin":
            raise v.VerifyError("export both Go 1.27.3 GOROOT and its bin directory first in PATH")
        if os.environ.get("OPENMERGE_TEST_DATABASE_URL") != "postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable":
            raise v.VerifyError("use the shared test Postgres URL on localhost:54329")
        result["baseline"] = v.check_tree("baseline", args.baseline)
        result["candidate"] = v.check_tree("candidate", root)
        result["environment"] = {"GOROOT": toolchain, "PATH_prefix": toolchain + "/bin", "database": "localhost:54329, isolated test schemas"}
        result["exports"] = v.exports(v.EVENTS, v.QUEUES)
        result["wire"] = v.compare_fixtures(args.baseline, root)
        print("make check", flush=True)
        v.invoke(["make", "check"], root, out, "make-check")
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
        print("strict live-journal replay", flush=True)
        v.invoke([sys.executable, str(root / "scripts/check-live-journal-replay.py"), "--baseline", str(args.baseline), "--candidate", str(root), "--events", str(v.EVENTS), "--queues", str(v.QUEUES), "--strict-state", "--out", str(out / "replay.json")], root, out, "strict-replay", mem="6GiB")
        result["replay"] = json.loads((out / "replay.json").read_text())
        if result["replay"]["verdict"] != "PASS":
            raise v.VerifyError("strict replay did not pass")
        print("seeded simulator in both trees", flush=True)
        result["simulator"] = v.compare_simulator(args.baseline, root, out)
        binary = root / "bin/openmerge"
        result["binary"] = {"path": str(binary), "sha256": v.sha(binary)}
        result["verdict"] = "PASS"
    except Exception as error:
        result["verdict"] = "FAIL"
        result["error"] = str(error)
    (out / "summary.json").write_text(json.dumps(result, indent=2) + "\n")
    manifest = {str(path.relative_to(out)): v.sha(path) for path in sorted(out.rglob("*")) if path.is_file() and path.name != "manifest.json"}
    (out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(json.dumps({key: result[key] for key in ("verdict", "error", "candidate") if key in result}), flush=True)
    return result["verdict"] != "PASS"


if __name__ == "__main__":
    sys.exit(main())
