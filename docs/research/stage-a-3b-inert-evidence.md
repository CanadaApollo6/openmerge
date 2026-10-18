# Stage A 3b inert component evidence

Baseline: `18d585526d1a77f180a2ccb471d12b7631ef515d` (`origin/main`).
Implementation checked by the replay harness: `2cdc9a13e8bfa28194a42a3faa78981cd0e6700b`.
The fix-round evidence commit updates only this note; its SHA is in the PR body.
Round 1 retains the inert boundary and fixes dismissal actor validation/conflicts,
issue-comment namespace isolation and later-page failure classification.
Go: `go1.27.2 linux/amd64`. Dedicated worktrees: `/tmp/om-sa-3b` and
`$HOME/.cache/om-pr63-baseline` (detached baseline).
PR #56 landed during verification; the three implementation/evidence commits
were rebased cleanly onto it, retaining the new gitops/forge diff capability.

This is merge/component evidence. No live journal, live database, App permissions,
service units, workflows, queue configuration or `openmerge/*` refs were changed.
Full checks use the existing **test** Postgres 18.6 at localhost:54329, with the
repository's per-test schema isolation. Forge tests use localhost HTTP fixtures;
none calls the real GitHub API. No production path calls the new capability.
Deployment still requires the operator's exported live-journal and effective
configuration comparisons under ADR 0007's trial gates; this note is not a
claim that those live deployment gates have been run.

## Scope and call-site evidence

The diff is confined to new forge reader/DTO, fake, conformance and test files,
the forge README and this note. The sole edit to an existing Go file is one
private fake storage field in `internal/forge/fake/fake.go`. The legacy approval
reader, planner, executor, gate, config/schema, event/action registries and all
existing fixtures are unchanged. The implementation is below the 3,000-line
review ceiling (final count in the PR body).

```sh
go list -deps ./cmd/openmerge > $HOME/.cache/om-pr63-fix1-rebased-builds/new-deps.txt
rg -n 'NativeReviews|NativeReviewReader|nativePages|nativeFallback' \
  --glob '*.go' --glob '!native_reviews*' .
go tool nm "$HOME/.cache/om-pr63-fix1-rebased-builds/new" > "$HOME/.cache/om-pr63-fix1-rebased-builds/new-nm.txt"
```

The call-site search has exactly one result: the new private storage field in
`fake.go`. All invocations and optional-interface assertions live in the new
reader and contract/test files. There is no invocation or capability assertion
in `cmd`, `serve`, `executor`, `queue`, or any other production consumer.
`go list -deps` exits 0 and is byte-identical between baseline and candidate:
forge, github and fake already compile into the binary (fake through the simulator
CLI). Thus package exclusion is **not** an inertness proof for this change.
`go tool nm` confirms that the new exported GitHub/fake reader methods and
acquisition helpers are linked. They are present but uncalled; no new runtime
selection or request path was added.

## Reproducible builds

In each checkout, with the corresponding output path:

```sh
CGO_ENABLED=0 go build -trimpath -buildvcs=false \
  -ldflags '-X main.version=stage-a-3b-evidence' \
  -o "$HOME/.cache/om-pr63-fix1-rebased-builds/new" ./cmd/openmerge
# Baseline output: $HOME/.cache/om-pr63-fix1-rebased-builds/old
sha256sum "$HOME/.cache/om-pr63-fix1-rebased-builds/"{old,new}
cmp "$HOME/.cache/om-pr63-fix1-rebased-builds/"{old,new}
```

Both builds exit **0**. Binary hashes:

- Baseline: `ad285f2023fe617072585f2a9e57cba0db9142ec6cc73e3d0b3d924fa6e8b0e8`.
- Candidate: `3f4d3faece52834de4f25b3965ba19c8ed369058e7fa4835673a07760f9f4571`.

`cmp` exits **1**: the binaries are not byte-identical because the reader and DTOs
compile into the existing forge packages, with new exported driver/fake methods
and helpers present in the linked binary (see symbol evidence above). The
fixed-version, path-independent build removes VCS/path differences; the new
linked code remains. Behavioral evidence therefore uses the comparisons below.

## Legacy differential evidence

Commands (both comparison tools are unchanged from main):

```sh
python3 scripts/check-phase2-compatibility.py "$HOME/.cache/om-pr63-baseline" \
  > ~/.cache/om-pr63-compat-rebased.log 2>&1; echo exit=$?
python3 scripts/check-stage-a-grant-compatibility.py "$HOME/.cache/om-pr63-baseline" \
  "$HOME/.cache/om-pr63-fix1-rebased-replay" > ~/.cache/om-pr63-replay-rebased.log 2>&1; echo exit=$?
```

Both exit **0**. The latter requires clean input checkouts and retains its Go
overlay, two compiled replay binaries, baseline synthetic journals and per-revision
outputs. The old/new binaries decode the same exact baseline journal bytes.
No database or external API is involved.

- All **74** baseline wire fixtures are byte-identical; no new fixture or wire
  type is introduced. All existing queue fixtures have zero diff. The SHA-256
  of `git ls-files internal/queue/testdata | xargs sha256sum | sha256sum` is
  `9394fe83b8321d878a4d32c2cd4ef1b7d8f5b85cbaf174180a1ed35a0adbf1bd`.
- All **17** legacy simulator exit records match with identical seeds/inputs:
  `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`.
- All **1,055** retry-envelope/default-digest records match:
  `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
- All **1,775** replay records match, including next actions after **697** depth-1
  and **1,075** depth-4 events:
  `e1ad29fe27dee98647f3eadcc90ace99e3272cc06d69ef4e453b45167dd947d4`.
- Baseline journal hashes: depth 1
  `94417e636146833d3b0cb9ac808fc7af94c9e9145b9b5ef7d890a6f91587c0e7`;
  depth 4 `6fcee2ebba08efbe6fdcb754c2b609d1fb4e95faea2dc94fedab8c7285fb1b6e`.
- The tested legacy default config digest remains
  `75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa`.
- Public schema SHA-256 remains
  `1bf2d6072fa4acaf89409bf8243b6ff79b3d07baf9c5f1b1b6d984e63706cbe9`.

## Reader verification

The fake and GitHub-over-localhost-HTTP share `forgetest.RunNativeReviewReader`,
including `rename-or-reassignment-between-reads`: an observed review/comment
by ID A cannot borrow a subsequent grant for B or a missing response ID. Both
also retain stable IDs after rename, all review states, duplicate/out-of-order
reviews, equal source times, direct/timeline dismissal evidence and explicit
fallback provenance. Driver tests cover all paginated collections, 304 response
Date revalidation, missing/invalid Date, missing dismissal events/times, ambiguous
multiple dismissal events, malformed/cyclic/over-limit/truncated pagination,
403/404 and malformed/partial responses, plus settlement's reserved capacity.
Round 1 adds valid-vs-ID-less dismissal
actor joins in both directions, valid-ID conflicts (including duplicate event
IDs), invalid logins, missing actors and explicit `ghost` actors/authors with
and without IDs. Valid ghost IDs remain source identities; ID-less authors fail
acquisition and ID-less actors remain unknown without erasing valid evidence.
Issue comments discard payload review/commit references while inline comments
retain them. Later-page 404, 206 and null responses wrap both failure sentinels
on every collection and discard the whole observation.

PR 6's durable persistence, restart and recovery are deliberately not implemented
here. The README and capability comments specify pinning the first fallback
barrier, retaining it on re-observation, and requiring strictly newer source time
(or a new revision if no comparable bound exists). The re-observation test proves
that acquisition supplies the stable identity and independently revalidated
clock inputs that durable pinning needs.

```sh
go test -race -count=1 ./internal/forge/... > ~/.cache/om-pr63-race.log 2>&1; echo exit=$?
OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' \
  make check > ~/.cache/om-pr63-fix1.log 2>&1; echo exit=$?
```

The focused race suite exits **0**. An initial focused test run exited **1**
(unknown-account representation and a fixture intercepting local token requests);
both were corrected and the focused rerun exits **0**. The earlier full
`make check` exits **0**, retained as
`~/.cache/om-pr63-fix1-before-rebase.log`. The final rebased `make check` exits **0**, including formatting, lint,
the full short race suite, shared-test-Postgres integration and production build.
All scratch builds use `GOTMPDIR="$HOME/.cache/om-pr63-tmp"`; no Postgres instance
was initialized.
