#!/usr/bin/env python3
"""Mandatory offline integration self-test for the legacy commit-ID gate."""
import argparse
import importlib.util
from pathlib import Path
import shutil
import sys
import tempfile

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("legacy", Path(__file__).with_name("check-legacy-commit-ids.py"))
legacy = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = legacy
spec.loader.exec_module(legacy)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--cache-root", type=Path, required=True)
    parser.add_argument("--go", default="go")
    parser.add_argument("--case", choices=("identical", "mutant", "dirty"))
    args = parser.parse_args()
    cache = legacy.cache_path(args.cache_root)
    cache.mkdir(parents=True, exist_ok=True)
    if args.case:
        sys.argv = [str(legacy.__file__), "--old-source", str(args.source), "--new-source", str(cache / "candidate"), "--cache-root", str(cache), "--go", args.go]
        return legacy.main(run_preflight=False)
    # Separate local repository: no branch/ref mutations in the caller's repo.
    with tempfile.TemporaryDirectory(prefix="self-test-", dir=cache) as directory:
        root = Path(directory)
        candidate = root / "candidate"
        result = legacy.command(["git", "clone", "--quiet", "--no-hardlinks", str(args.source), str(candidate)])
        if result.returncode:
            raise RuntimeError(result.stderr)
        try:
            for case, expected in (("identical", 0), ("mutant", 1), ("dirty", 2)):
                if case == "mutant":
                    for name in ("metadata.go", "build.go"):
                        path = candidate / "internal/gitops" / name
                        text = path.read_text()
                        needle = 'reserved := "OpenMerge-Batch"'
                        if needle not in text:
                            raise RuntimeError("reserved-prefix mutation target missing: " + name)
                        path.write_text(text.replace(needle, 'reserved := "OpenMerge-*"'))
                    commit = legacy.command(["git", "-c", "user.name=Riel St. Amand", "-c", "user.email=riel.stamand@gmail.com", "commit", "-am", "Self-test reserved-prefix mutant\n\nSigned-off-by: Riel St. Amand <riel.stamand@gmail.com>\nCo-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"], cwd=candidate)
                    if commit.returncode:
                        raise RuntimeError(commit.stderr)
                elif case == "dirty":
                    (candidate / "dirty-self-test.txt").write_text("dirty\n")
                run = legacy.command([sys.executable, __file__, "--source", str(args.source), "--cache-root", str(root), "--go", args.go, "--case", case])
                print(run.stdout, end="")
                print(f"legacy-self-test case={case} exit={run.returncode} expected={expected}")
                if run.returncode != expected:
                    raise RuntimeError(run.stderr)
                if case == "mutant" and "verdict=FAIL mismatch=squash,rebase" not in run.stdout:
                    raise RuntimeError("mutant did not expose the expected squash/rebase regression")
        finally:
            # TemporaryDirectory removes the clone and all copied build trees.
            shutil.rmtree(candidate, ignore_errors=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
