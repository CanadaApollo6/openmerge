#!/usr/bin/env python3
"""Compare legacy tested and landed commit IDs through both local git trees.

The two source arguments are local Git checkouts.  The script copies their
tracked and non-ignored files into a job directory below ~/.cache, injects the
same test into internal/gitops, and compiles one package test binary per copy.
The injected test uses the existing real-git fixture and calls BuildTestRef
and Land for squash, merge, rebase, and fast-forward.  No forge, database,
systemd service, or module/toolchain download is used.
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


CHECK_NAME = "legacy-commit-id-per-method"
HARNESS_ID = CHECK_NAME + "/v1"

# This is deliberately a package test, not a production hook.  It has to use
# only helpers and APIs present at c2d9b3a as well as in the candidate tree.
HARNESS = r'''package gitops

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

const legacyCommitIDsHarnessName = "legacy-commit-id-per-method"
const legacyCommitIDsHarness = "legacy-commit-id-per-method/v1"

// deterministicEntropy is installed only in this test process.  The probe
// needs the same random nonce bytes for the marker and fence commits in both
// binaries; production crypto/rand.Reader is never changed.
type deterministicEntropy struct{ next byte }

func (r *deterministicEntropy) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.next
		r.next++
	}
	return len(p), nil
}

func TestLegacyCommitIDs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method forge.MergeMethod
	}{
		{"squash", forge.Squash},
		{"merge", forge.Merge},
		{"rebase", forge.Rebase},
		{"fast-forward", forge.FastForward},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Go 1.24+ requires cryptocustomrand=1 before permitting a custom
			// crypto/rand.Reader in some configurations.  The runner supplies
			// that setting; the assignment remains test-process local.
			crand.Reader = &deterministicEntropy{}
			f := newFixture(t)
			f.g.cfg.Author = Identity{Name: "Pinned Author", Email: "pinned-author@example.test"}
			f.g.cfg.Committer = Identity{Name: "Pinned Committer", Email: "pinned-committer@example.test"}
			f.g.cfg.Now = func() time.Time { return epoch }

			prs := f.prs[:2]
			if tc.method == forge.FastForward {
				// The fixture's PR heads are siblings.  Replace PR 2 with a
				// real child of PR 1 so this covers a stacked fast-forward.
				stacked := f.commit(f.prs[0].HeadSHA, "stacked PR 2", map[string]string{"stacked.txt": "stacked\n"})
				gitCommand(t, f.source, "", nil, "push", "--force", f.remote, stacked+":"+headRef(2))
				prs = []forge.BuildPR{{Number: 1, HeadSHA: f.prs[0].HeadSHA}, {Number: 2, HeadSHA: stacked}}
			}
			batch := "legacy-commit-ids-" + tc.name
			tested, err := f.g.BuildTestRef(context.Background(), testRepo, forge.BuildRequest{
				Ref: "openmerge/queue/main/" + batch, BaseSHA: f.base, PRs: prs,
				Method: tc.method, BatchID: batch,
			})
			must(t, err)
			fence, err := f.g.EnsureFence(context.Background(), testRepo, testFence)
			must(t, err)
			fastForwardHead := ""
			if tc.method == forge.FastForward {
				fastForwardHead = prs[len(prs)-1].HeadSHA
			}
			_, err = f.g.Land(context.Background(), testRepo, forge.LandRequest{
				Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested,
				Fence: testFence, ExpectedFence: fence, NextFenceID: "land/" + batch,
				PRs: prs, FastForwardHeadSHA: fastForwardHead,
			})
			must(t, err)
			landed := f.read("main")
			if landed == "" || landed == f.base {
				t.Fatalf("landing did not advance main: base=%s landed=%s", f.base, landed)
			}
			if tc.method == forge.FastForward {
				if landed != fastForwardHead || landed == tested {
					t.Fatalf("fast-forward landed=%s, want head=%s and marker tested=%s", landed, fastForwardHead, tested)
				}
			} else if landed != tested {
				t.Fatalf("%s landed=%s, want tested=%s", tc.method, landed, tested)
			}
			fmt.Printf("%s harness=%s method=%s tested=%s landed=%s\n",
				legacyCommitIDsHarnessName, legacyCommitIDsHarness, tc.name, tested, landed)
		})
	}
}
'''


class ToolError(Exception):
    """An input or local-tool failure, distinct from a tested mismatch."""


@dataclass(frozen=True)
class Source:
    role: str
    path: Path
    commit: str
    status: str
    tree_sha256: str
    files: tuple[str, ...]


def command(args: list[str], cwd: Path | None = None, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(args, cwd=cwd, env=env, text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              check=False)
    except OSError as error:
        raise ToolError(f"could not run {' '.join(args)}: {error}") from error


def git(source: Path, *args: str) -> str:
    result = command(["git", "-C", str(source), *args])
    if result.returncode:
        detail = (result.stderr or result.stdout).strip()
        raise ToolError(f"git -C {source} {' '.join(args)} failed: {detail}")
    return result.stdout


def source_files(source: Path) -> list[str]:
    raw = git(source, "ls-files", "-co", "--exclude-standard", "-z").encode()
    # git() decoded the NUL-delimited stream with text mode.  Re-encoding is
    # safe for Git's UTF-8 path protocol and keeps path handling straightforward.
    return [os.fsdecode(item) for item in raw.split(b"\0") if item]


def file_digest(source: Path, files: list[str]) -> str:
    digest = hashlib.sha256()
    for name in sorted(files):
        path = source / name
        try:
            info = path.lstat()
        except OSError as error:
            raise ToolError(f"source file disappeared: {path}: {error}") from error
        digest.update(name.encode("utf-8", "surrogateescape"))
        digest.update(b"\0")
        digest.update(str(info.st_mode).encode())
        digest.update(b"\0")
        if path.is_symlink():
            digest.update(os.readlink(path).encode("utf-8", "surrogateescape"))
        elif path.is_file():
            with path.open("rb") as stream:
                for block in iter(lambda: stream.read(1024 * 1024), b""):
                    digest.update(block)
        else:
            raise ToolError(f"source file is not a regular file or symlink: {path}")
        digest.update(b"\0")
    return digest.hexdigest()


def describe_source(role: str, path: Path) -> Source:
    path = path.expanduser().resolve()
    if not path.is_dir():
        raise ToolError(f"{role} source is not a directory: {path}")
    if not (path / "go.mod").is_file() or not (path / "internal/gitops").is_dir():
        raise ToolError(f"{role} source is not an OpenMerge checkout: {path}")
    commit = git(path, "rev-parse", "--verify", "HEAD^{commit}").strip()
    status = git(path, "status", "--porcelain=v1", "--untracked-files=all")
    files = source_files(path)
    return Source(role, path, commit, status, file_digest(path, files), tuple(files))


def cache_path(value: Path) -> Path:
    value = value.expanduser().resolve()
    temporary = Path(tempfile.gettempdir()).resolve()
    try:
        value.relative_to(temporary)
    except ValueError:
        return value
    raise ToolError(f"cache root must not be under {temporary}: {value}")


def copy_source(source: Source, destination: Path) -> None:
    destination.mkdir(parents=True, exist_ok=False)
    for name in source.files:
        original = source.path / name
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True)
        if original.is_symlink():
            target.symlink_to(os.readlink(original))
        else:
            shutil.copy2(original, target)
    probe = destination / "internal/gitops/legacy_commit_ids_probe_test.go"
    probe.write_text(HARNESS, encoding="utf-8")


def runner_environment(tmpdir: Path) -> dict[str, str]:
    env = dict(os.environ)
    env["GOPROXY"] = "off"
    env["GOTOOLCHAIN"] = "local"
    env["TMPDIR"] = str(tmpdir)
    env.setdefault("GOFLAGS", "-p=2")
    env.setdefault("GOMAXPROCS", "2")
    env.setdefault("GOMEMLIMIT", "4GiB")
    env.setdefault("GOTMPDIR", str(tmpdir.parent / "go-tmp"))
    env.setdefault("GOCACHE", str(tmpdir.parent / "go-cache"))
    godebug = [part for part in env.get("GODEBUG", "").split(",")
               if part and not part.startswith("cryptocustomrand=")]
    godebug.append("cryptocustomrand=1")
    env["GODEBUG"] = ",".join(godebug)
    return env


def build_and_run(source: Source, job: Path, go: str) -> dict[str, tuple[str, str]]:
    tree = job / source.role
    copy_source(source, tree)
    binary = job / f"{source.role}.legacy-commit-ids.test"
    env = runner_environment(job / f"{source.role}-tmp")
    for key in ("TMPDIR", "GOTMPDIR", "GOCACHE"):
        Path(env[key]).mkdir(parents=True, exist_ok=True)
    build = command([
        go, "test", "-trimpath", "-buildvcs=false", "-mod=readonly", "-c",
        "-o", str(binary), "./internal/gitops",
    ], cwd=tree, env=env)
    if build.returncode:
        raise ToolError(f"{source.role} test binary build failed:\n{build.stdout}{build.stderr}")
    print(f"{CHECK_NAME}-binary role={source.role} sha256={hashlib.sha256(binary.read_bytes()).hexdigest()}")
    run = command([
        str(binary), "-test.run=^TestLegacyCommitIDs$", "-test.v", "-test.count=1",
    ], cwd=tree, env=env)
    if run.returncode:
        raise ToolError(f"{source.role} harness failed (exit {run.returncode}):\n{run.stdout}{run.stderr}")
    records: dict[str, tuple[str, str]] = {}
    prefix = CHECK_NAME + " harness=" + HARNESS_ID + " method="
    for line in run.stdout.splitlines():
        if not line.startswith(prefix):
            continue
        fields = dict(item.split("=", 1) for item in line.split()[1:])
        method = fields.get("method", "")
        tested, landed = fields.get("tested", ""), fields.get("landed", "")
        if method in records or len(tested) not in (40, 64) or len(landed) not in (40, 64):
            raise ToolError(f"{source.role} emitted malformed or duplicate record: {line}")
        records[method] = (tested, landed)
    expected = {"squash", "merge", "rebase", "fast-forward"}
    if set(records) != expected:
        raise ToolError(f"{source.role} emitted {len(records)} method records, expected {sorted(expected)}:\n{run.stdout}{run.stderr}")
    return records


def print_source(source: Source) -> None:
    clean = "yes" if not source.status else "no"
    print(f"{CHECK_NAME}-source role={source.role} sha={source.commit} clean={clean} tree_sha256={source.tree_sha256}")
    if source.status:
        print(f"{CHECK_NAME}-source-status role={source.role} status={source.status!r}")


def require_clean(source: Source) -> None:
    if source.status:
        raise ToolError(f"{source.role} source is dirty; clean it before collecting SHA evidence: {source.path}")


def assert_source_unchanged(source: Source) -> None:
    current = describe_source(source.role, source.path)
    if (current.commit, current.status, current.tree_sha256, current.files) != (
            source.commit, source.status, source.tree_sha256, source.files):
        raise ToolError(f"{source.role} source changed during check: {source.path}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--old-source", "--baseline", dest="old_source", required=True,
                        type=Path, help="local checkout of the deployed/old source")
    parser.add_argument("--new-source", "--candidate", dest="new_source",
                        type=Path, default=Path(__file__).resolve().parents[1],
                        help="local checkout of the candidate source")
    parser.add_argument("--cache-root", type=Path,
                        default=Path.home() / ".cache/openmerge-legacy-commit-ids")
    parser.add_argument("--go", default="go", help="Go executable (default: go)")
    args = parser.parse_args()

    try:
        old = describe_source("old", args.old_source)
        new = describe_source("new", args.new_source)
        cache = cache_path(args.cache_root)
        try:
            cache.mkdir(parents=True, exist_ok=True)
            job = Path(tempfile.mkdtemp(prefix="compare-", dir=cache))
        except OSError as error:
            raise ToolError(f"cannot create cache job below {cache}: {error}") from error
        print(f"{CHECK_NAME}-harness id={HARNESS_ID} sha256={hashlib.sha256(HARNESS.encode()).hexdigest()}")
        print(f"{CHECK_NAME}-cache path={job}")
        print_source(old)
        print_source(new)
        require_clean(old)
        require_clean(new)
        results = {
            "old": build_and_run(old, job, args.go),
            "new": build_and_run(new, job, args.go),
        }
        assert_source_unchanged(old)
        assert_source_unchanged(new)
        for method in ("squash", "merge", "rebase", "fast-forward"):
            old_tested, old_landed = results["old"][method]
            new_tested, new_landed = results["new"][method]
            print(f"{CHECK_NAME} method={method} old_tested={old_tested} old_landed={old_landed} "
                  f"new_tested={new_tested} new_landed={new_landed}")
        mismatches = [method for method in ("squash", "merge", "rebase", "fast-forward")
                      if results["old"][method] != results["new"][method]]
        if mismatches:
            print(f"{CHECK_NAME} verdict=FAIL mismatch=" + ",".join(mismatches))
            return 1
        print(f"{CHECK_NAME} verdict=PASS methods=4")
        return 0
    except ToolError as error:
        print(f"legacy commit ID check error: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
