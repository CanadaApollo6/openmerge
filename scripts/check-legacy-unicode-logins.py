#!/usr/bin/env python3
"""Offline non-ASCII forge-login status and four-method commit-ID differential.

Uses clean source copies, the stock commit-ID harness and fake HTTP only.
Status output includes executor success/ambiguity, not only normalized strings.
"""
import argparse
import importlib.util
from pathlib import Path
import shutil
import sys
import tempfile

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("ids", Path(__file__).with_name("check-legacy-commit-ids.py"))
ids = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = ids
spec.loader.exec_module(ids)


def commit_harness(login, primary, stable):
    h = ids.HARNESS.replace("Other <other@example.test>", "Zoë <zoë@éxample.test>")
    h = h.replace("prs := make([]forge.BuildPR, 2)", "prs := make([]forge.BuildPR, 2)\n linked := map[int][]forge.CommitAuthor{}")
    old = 'parent = f.commit(parent, fmt.Sprintf("Commit %d/%d\\n\\n%s", i, j, prose), map[string]string{name: name+"\\n"})'
    new = '''previous := parent
                parent = f.commit(parent, fmt.Sprintf("Commit %d/%d\\n\\n%s", i, j, prose), map[string]string{name: name+"\\n"})
                parent = gitCommand(t, f.source, fmt.Sprintf("Commit %d/%d\\n\\n%s", i, j, prose), []string{"GIT_AUTHOR_NAME=Élodie", "GIT_AUTHOR_EMAIL=élodie@éxample.test"}, "commit-tree", parent+"^{tree}", "-p", previous)
                linked[i+1] = append(linked[i+1], forge.CommitAuthor{SHA: parent, Login: "LOGIN", ID: STABLE})'''.replace("LOGIN", login).replace("STABLE", str(stable))
    assert old in h
    h = h.replace(old, new)
    h = h.replace('Author: "Contributor", AuthorEmail:', 'Author: "' + primary + '", AuthorID: 12, AuthorEmail:')
    h = h.replace('batch := "legacy-commit-ids-" + tc.name', '''f.g.cfg.CommitAuthors = func(_ context.Context, _ forge.RepoRef, n int) ([]forge.CommitAuthor,error) { return linked[n],nil }
            batch := "legacy-commit-ids-" + tc.name''')
    return h


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--candidate", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    sources = [ids.describe_source(role, path) for role, path in (("old", args.baseline), ("new", args.candidate))]
    for source in sources:
        ids.require_clean(source)
        ids.print_source(source)
    cache = Path.home() / ".cache/openmerge-legacy-unicode"
    cache.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=cache) as directory:
        root = Path(directory)
        original = ids.HARNESS
        status = (args.candidate / "internal/forge/github/status_unicode_test.go").read_text()
        outputs = []
        for source in sources:
            tree = root / source.role
            ids.copy_source(source, tree)
            (tree / "internal/forge/github/status_unicode_test.go").write_text(status)
            env = ids.runner_environment(root / (source.role + "-tmp"))
            for key in ("TMPDIR", "GOTMPDIR", "GOCACHE"):
                Path(env[key]).mkdir(parents=True, exist_ok=True)
            run = ids.command(["go", "test", "-count=1", "-v", "./internal/forge/github", "-run", "^TestStatusUnicodeLegacyEquivalence$"], cwd=tree, env=env)
            print(f"unicode-status role={source.role} exit={run.returncode}")
            print(run.stdout, end="")
            if run.returncode:
                raise ids.ToolError(run.stderr or run.stdout)
            records = [line for line in run.stdout.splitlines() if line.startswith("UNICODE_STATUS ")]
            if len(records) != 4:
                raise ids.ToolError("missing status records")
            outputs.append(records)
            shutil.rmtree(tree)
        if outputs[0] != outputs[1]:
            return 1
        print("unicode-status verdict=PASS pairs=4")
        scenarios = [
            ("ascii-control", "CONTRIBUTOR", "Contributor", 0),
            ("kelvin-fallback", "Kontributor", "Kontributor", 0),
            ("accent-fallback", "élodie", "Élodie", 0),
            ("unicode-primary", "Élodie", "élodie", 0),
            ("stable-id-control", "Kontributor", "Kontributor", 12),
        ]
        for name, login, primary, stable in scenarios:
            ids.HARNESS = commit_harness(login, primary, stable)
            job = root / name
            job.mkdir()
            results = [ids.build_and_run(source, job, "go") for source in sources]
            for method in results[0]:
                print(f"unicode-commit-ids fixture={name} method={method} main={results[0][method]} candidate={results[1][method]}")
            if results[0] != results[1]:
                return 1
            print(f"unicode-commit-ids fixture={name} verdict=PASS methods=4")
            shutil.rmtree(job)
        ids.HARNESS = original
    for source in sources:
        ids.assert_source_unchanged(source)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except ids.ToolError as error:
        print(str(error), file=sys.stderr)
        sys.exit(2)
