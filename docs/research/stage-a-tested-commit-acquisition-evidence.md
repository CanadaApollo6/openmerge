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
The fake implements explicit acquisition by validating IDs and existence; it
makes no claim to emulate cache lifetime. The integration test is the parity
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

Full required gate results will be recorded below after completion.
