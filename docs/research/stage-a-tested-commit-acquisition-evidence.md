# Stage A tested commit acquisition (#83)

Trial class: **Hold**. Baseline: `origin/main` at
`8197a2ec33cc5bea62e7ae7fa07aedd1b92ae4a9` (#82).

## Root cause and object lifetime

`cmd/openmerge/installations.go` constructs build and worker Git handles with
`WorkDir = <GIT_CACHE>/<installation>`. `gitops.Repo.openSession` and all local
readers derive `<WorkDir>/<sha256(repository path)>.git`. `BuildTestRef` executes
`commit-tree` in that bare repository: there is no temporary build worktree or
second object store. The push uses `<tested SHA>:<test branch>` on the remote,
without creating a local ref for that generated SHA. Fetched PR refs are deleted.
`openSession`'s deferred maintenance can run `git gc --prune=now` (hourly or at
512 MiB by default). This discards unreachable build/fetched objects, including
freshly pushed tested commits. Base objects may remain independently reachable.
A restart preserves disk objects but cannot restore objects already collected;
Postgres preserves the tested SHA, not the Git object. No live sandbox, DB,
service, API or remote queue ref was modified for this investigation.

## Fix and boundary audit

The Stage A tested-message callback explicitly acquires the recorded build base,
tested SHA, and pinned PR heads before any cache-only message/history read.
`forge.CommitAcquirer` separates this acquisition from immutable reading.
`gitops.AcquireCommits` validates full IDs and commit types, fetches missing exact
SHAs (never mutable refs), and bounds acquisition to one minute and at most the
shared member limit plus two IDs. Failure blocks landing; there is no ref-tip
fallback. Temporary private local refs retain each acquired graph across other
operations' GC until all tested-message reads complete. Releases are independent
and idempotent and require no credentials. Normal releases delete the refs;
a process interrupted before release can leave conservative retention refs.
A subsequent process reacquires by durable SHA regardless of whether its cache
is warm, collected, or entirely empty. Remote refusal to serve the recorded SHA
fails closed. Reader functions themselves remain unchanged and cache-only.

`FirstParentCommits` and `ReadCommitMessage` (including the fast-forward marker)
are the cache-only readers on this landing boundary. Rebase `CommitsBetween`
already acquires its inputs, but its deferred maintenance could collect the
previously fetched tested chain; the lease explicitly covers that interval.
Landing policy/classification uses `GoverningFiles`, which acquires the pinned
commit and reads its files under one cache lock, or retained immutable governing
evidence in `stageAGoverningCache`. Configuration ingestion uses pinned
`Driver.ReadFile`. `LocalConfig` and `LocalGoverningFiles` are used only by
`Server.BriefPolicy` for historical status display, not landing: their existing
cache-only contract deliberately forbids network I/O and remains intact. Their
behavior with explicitly acquired objects is covered by the acquisition tests.
No legacy queue path invokes this new acquisition API.

## Fake parity and real integration coverage

Related issue: [#83](https://github.com/CanadaApollo6/openmerge/issues/83).
The fake has one durable in-memory commit map serving both remote and local
operations. It has no installation cache, unreachable loose objects, GC, or
separate restartable Git handles. Treating that map as a real cache cannot test
this boundary: adding a set of acquired IDs alone would still omit Git's graph
reachability and maintenance behavior. Instead the production gate integration
uses real `gitops.BuildTestRef` → local bare remote push → forced GC → production
`stageAGate.ReadTestedMessages` → fenced real `gitops.Land`, with no fake history.
The fake implements explicit acquisition with the same input-count bound, full-ID
validation and existence checks. Shared `commit-acquisition` contract subtests run
against both implementations, rejecting empty/oversized inputs, mutable names
and missing commits, then checking acquired message availability and release.
The fake makes no claim to emulate cache lifetime. The integration test is the parity
check for this storage boundary, supplementing #83's shared validation contracts.

`TestStageAGateRealGitAcquisition` covers merge, squash, rebase, depths 1 and 4,
and both the builder's existing handle and a fresh handle after deleting the
cache. The base is locally pinned before building, matching the sandbox's
base-present/tested-missing condition. Rebase has two source commits per PR.
GC is forced after every acquiring operation. Every successful case lands the
exact tested SHA; overlong composition is still rejected. This cheap test runs
under `-short` without Postgres.

`TestAcquireCommitsRetainsExactObjectsAcrossMaintenance` covers all four methods,
including fast-forward messages, moved mutable remote refs, overlapping leases,
GC between acquisition and read, offline cache-only readers, idempotent cleanup,
and collection after release. `TestAcquireCommitsValidatesPinsAndPolicyObjects`
covers invalid IDs, missing objects, non-commit objects, canceled acquisition,
and offline pinned config/governing reads.

## Validation

The regression on unchanged baseline production code fails all 12 cases with
`local first-parent commit unavailable: exit status 128` (Go test exit **1**).
The same 12 cases on the fix pass (exit **0**). Acquisition unit tests and
`TestStageAReceiptGateAllMethods` pass (exit **0**).

Validated functional head: `6e66172b7e5b61f5720b65d99a25808b542bdb53`.
The final evidence-only commit changes no executable source. All commands ran
serially after confirming no other agent test/replay jobs, with:

```sh
export GOFLAGS=-p=2 GOMEMLIMIT=4GiB
export GOTMPDIR=/home/riels/.cache/openmerge-acquisition/tmp
export GOCACHE=/home/riels/.cache/go-build
export TMPDIR=/home/riels/.cache/openmerge-acquisition/tmp
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
```

Baseline checkout: `/home/riels/openmerge-stage-a-acquisition-baseline`.
Candidate: `/home/riels/openmerge-stage-a-acquire-tested-commit`.
Raw command logs: `/home/riels/.cache/openmerge-acquisition/logs`.
The baseline uses a Go overlay replacing only `stage_a_real_git_test.go` with
the candidate regression; all baseline production code stays at 8197a2e.

Commands and exit statuses:

```text
baseline: go test -overlay <baseline-overlay.json> -short ./internal/serve
  -run '^TestStageAGateRealGitAcquisition$' -count=1 -v                 1 (expected)
candidate: go test -short ./internal/serve
  -run '^TestStageA(GateRealGitAcquisition|ReceiptGateAllMethods)$'
  -count=1 -v                                                       0
candidate: go test -short ./internal/gitops
  -run '^TestAcquireCommits' -count=1 -v                             0
make check                                                         0
make test-sim-exits                                                 0
python3 scripts/check-phase2-compatibility.py <baseline>             0
python3 scripts/check-legacy-landing-sql.py <baseline>               0
python3 scripts/check-legacy-plan-action-depths-1-4.py <baseline>     0
python3 scripts/check-legacy-commit-ids.py
  --old-source <baseline> --new-source <candidate>                   0
make fmt lint (after shared-contract addition)                      0
go test -race -short ./internal/forge/fake ./internal/gitops
  -run '^(TestContract|TestContractWithoutRollbacks|TestGitContract)$/^commit-acquisition$'
  -count=1 -v                                                       0
```

The shared contract addition was checked separately under the race detector
because the full race run had already compiled those packages. `make check`
completed all packages and built the production binary at 6e66172. Formatting
and lint were repeated after that addition (zero issues).

Baseline output, repeated for each of the twelve matrix cases:

```text
--- FAIL: TestStageAGateRealGitAcquisition/depth-1/restart-false/squash
    local first-parent commit unavailable: exit status 128
--- FAIL: TestStageAGateRealGitAcquisition/depth-4/restart-true/rebase
    local first-parent commit unavailable: exit status 128
FAIL
```

Fixed output: all twelve matrix cases pass; the receipt gate's four methods
also pass (combined serve probe: 5.400s). The complete race-enabled short run
passed with queue 287.353s and serve 337.210s. No new test is skipped by `-short`.

Legacy evidence:

- Compatibility: 17 legacy exit records, 1,055 depth-1 retry envelopes/default
  digest records, and all 85 existing wire fixtures byte-identical. SHA-256s:
  `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`
  and `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
- SQL: all seven statements, transaction/lock ordering included, identical.
  Baseline, candidate, and each of the five mutant test executions exit 0;
  all five mutants produce a detected SQL difference (expected rejection):
  `authorize-extra-statement`, `claim-without-for-update`,
  `legacy-via-nil-policy-start`, `load-before-claim-check`, `emit-send-batch`.
- Depths 1/4: 214 short-corpus records per checkout and 227,662 exact wire
  checkpoints; 446 full-corpus records per checkout and 5,615,998 checkpoints;
  190 fixture-journal checkpoints. Every underlying Go test exits 0; plans,
  actions and wakes are exact, with no nil-only exceptions. Stream SHA-256s:
  `71f1ae2108d98685b675aa5a158834bbd96595ce3c9e5f3ede6cb9c575aec96a`,
  `05c71f7394b7a59e84b6e86cab4c219d39d3e60c6c82ebd336d3c3157b3dc4ed`,
  `a78423d9a03266d30cd54246b9827f305b6878d20ab1d60a8dacdf01e1397d4b`.
- Commit-ID harness preflight: identical-source exit 0 (expected 0), deliberate
  metadata mutant exit 1 (expected 1), dirty-source refusal exit 2 (expected 2).
  Actual comparison used clean baseline 8197a2e and clean candidate 6e66172,
  exits 0. Tested and landed IDs match for every method:

```text
squash       tested=ff69dbec39d51f58388ecb40939b116639245145
             landed=ff69dbec39d51f58388ecb40939b116639245145
merge        tested=bcc5548afcfa623368902566ffc0baa8b9cdc752
             landed=bcc5548afcfa623368902566ffc0baa8b9cdc752
rebase       tested=fb81d35ef3ab6f59feb8cd76b09047d01637c9fb
             landed=fb81d35ef3ab6f59feb8cd76b09047d01637c9fb
fast-forward tested=fc5c1bf85c90345690a94321d2afe65d7007018c
             landed=ccfc498745b4ed1f770d8e20c0c37e585564669a
```
