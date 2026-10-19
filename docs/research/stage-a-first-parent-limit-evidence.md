# Stage A first-parent limit repair

Trial class: **Hold** (Stage A landing gate).

## Reproduction and scope

Baseline: `origin/main` at `39e6d73` (Stage A 12b). Implementation worktree:
`~/openmerge-fix-first-parent`, branch `fix/stage-a-first-parent-limit`.
The original checkout was not edited.

`TestStageAGateRealGitLanding` builds actual Git objects through
`gitops.Repo.BuildTestRef`, passes the resulting exact base/head/member pins to
`Server.stageAGate(...).ReadTestedMessages`, validates the member metadata, and
lands those exact objects through `gitops.Repo.Land` in a temporary local bare
remote. It covers batches of one and four members for merge, squash, and rebase;
each source has two commits, so rebase must handle two and eight generated
commits. It also rejects a real extra generated composition segment. The probe
exercises the production gate's tested-object boundary; the existing
`TestStageAReceiptGateAllMethods` covers eligibility and atomic persistence.
It needs no database or GitHub driver and runs under `-short` in about one second.

Before any production edits, the new regression on baseline production code
exited **1**, failing all six subtests with:

```text
TestStageAGateRealGitLanding/depth-1/merge: invalid first-parent history limit
TestStageAGateRealGitLanding/depth-1/squash: invalid first-parent history limit
TestStageAGateRealGitLanding/depth-1/rebase: invalid first-parent history limit
TestStageAGateRealGitLanding/depth-4/merge: invalid first-parent history limit
TestStageAGateRealGitLanding/depth-4/squash: invalid first-parent history limit
TestStageAGateRealGitLanding/depth-4/rebase: invalid first-parent history limit
```

After the repair, all six subtests pass and the actual local base refs equal the
tested SHAs. Overlong generated chains are rejected before landing.

## Bounds and fail-closed behavior

`forge.MaxFirstParentCommits = 10_000` is shared by the gate and both reader
implementations. Merge/squash request exactly the number of batch members;
BuildTestRef adds no extra fixed first-parent commits for these methods.
Rebase counts the pinned source segments first, with each source read bounded by
`min(forge.MaxCommitHistoryCommits, MaxFirstParentCommits - accumulated length)`.
It requests the sum of the actual source lengths for the generated chain and
selects each segment's final replay for metadata parsing. Contributor merge
parents and contributor metadata remain outside that selection.

Empty batches, oversized member counts, empty rebase source segments, exhausted
aggregate budgets, truncated sources, truncated compositions, and inconsistent
structures all reject landing. Truncation diagnostics identify the composition
limit or the PR and its remaining budget. Fast-forward retains its existing
marker-message path.

The fake and real readers validate the same inclusive range and return the same
error text: `invalid first-parent history limit` or `invalid commit history
limit`. Fake validation happens before reading the graph, as in gitops.

## Caller audit

Repository-wide searches for `MaxAncestryCommits`, `FirstParentCommits`,
`CommitsBetween`, and internal limit validation found these production sites:

| Call site | Callee and bound | Result |
| --- | --- | --- |
| `internal/serve/stage_a_gate.go:174`, `testedMemberCommits` | `gitops.Repo.FirstParentCommits`: 1–10,000 | Fixed: formerly 100,000; now member count for merge/squash or exact bounded source total for rebase. |
| `internal/serve/stage_a_gate.go:158`, rebase source enumeration | `gitops.Repo.CommitsBetween`: 1–10,000 | Fixed second mismatch: formerly 100,000; now remaining composition budget, capped by shared `MaxCommitHistoryCommits`. |
| `internal/gitops/diff.go:77`, `RevisionDiff` | `revisionDiff` streaming `ancestryCounter`: supplied `commitLimit` (100,000), plus 30-second deadline | Compatible; this is the intended ancestry-verification budget, with no smaller hidden cap. |
| `internal/serve/outside_push.go:21`, `outsidePush` | `gitops.Repo.CommitsBetween`: 1–10,000 | Compatible: `queue.OutsidePushCommitLimit = 100`; unchanged. |

The shared contract's existing metadata reader requests 1,000 from
`CommitsBetween` (within 10,000). Other history call sites are tests, small explicit
limits, or forwarding test hooks. No other oversized production calls were found.

## Tests and verification

New tests:

- `TestStageAGateRealGitLanding/depth-{1,4}/{merge,squash,rebase}`.
- `TestStageATestedHistoryLimits`: exact depth-four limits, rebase segment
  selection, composition truncation, invalid member counts, source truncation,
  aggregate exhaustion, exact maximum, and empty sources.
- Shared `history-limits` contract, invoked by real `TestGitContract` and fake
  `TestContract` / `TestContractWithoutRollbacks`. Both implementations receive
  the same logical four-entry composition and limits `-1, 0, 1, 4, 10000, 10001`;
  it checks exact error messages, newest-first results, and truncation.

Every Go invocation uses `GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`, and cache/temp paths
under `~/.cache`. Database tests use only
`postgres://postgres@localhost:54329/postgres?sslmode=disable` and isolated test
schemas. Heavy jobs run sequentially after checking that no other Go test or
replay job is active. No live DB, sandbox server, systemd unit, external sandbox
repository, live status API, or remote OpenMerge ref was accessed. Git writes in
these tests are confined to disposable local repositories.

Gate results will be recorded below after completion.
