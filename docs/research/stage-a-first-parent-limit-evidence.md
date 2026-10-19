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

### Exit statuses

All required gates passed. The final code/test tree is commit
`51a853e4e16635e6e762ad43765456b47e536a20`; the follow-up commit only records
these results. A final fetch still resolved `origin/main` to
`39e6d7349ca6f26ccfb5dbf3603dcb6aeee4ba36`, so no rebase was needed.

| Invocation / probe | Exit | Result |
| --- | --- | --- |
| Initial real-git regression against unchanged main production code | 1 | Expected red: all six cases report `invalid first-parent history limit`. |
| Final regression source overlaid into the clean main worktree | 1 | Expected red again, with the exact final test source; all six cases report the original diagnostic. |
| Focused real/fake `history-limits` contracts and new serve tests | 0 | Both reader implementations and all regression/boundary cases pass. |
| Final `TestStageAGateRealGitLanding` + `TestStageATestedHistoryLimits` | 0 | Six actual local landings, real overlong-chain rejection, and all unit boundary cases. |
| `make check` | 0 | Formatting, lint (0 issues), complete race/short suite, and build all pass. Queue 286.983s; serve 315.174s. |
| `make test-sim-exits` | 0 | Complete simulator suite passes (38.662s). |
| `scripts/check-stage-a-identity-compatibility.py BASELINE` | 0 | 1,753 event/action checkpoints byte-identical at depths 1 and 4. |
| `scripts/check-stage-a-grant-compatibility.py BASELINE OUT` | 0 | 1,775 exported-journal replay records byte-identical; same default digest. |
| `scripts/check-phase2-compatibility.py BASELINE` | 0 | 17 legacy exit records, 1,055 retry/default-digest records, and all 84 wire fixtures identical. |
| `scripts/check-legacy-landing-sql.py BASELINE` | 0 | Identical seven-statement sequence, including transaction/lock order. |
| SQL baseline/candidate probes and five mutant probes | 0 each | All execute successfully; all five mutants produce a detected SQL difference. |
| `scripts/check-legacy-commit-ids.py --old-source BASELINE --new-source CANDIDATE` | 0 | Tested and landed IDs identical for all four methods. Mandatory self-test passes. |
| Commit-ID self-test: identical / mutant / dirty | 0 / 1 / 2 | All expected: mutant detects squash/rebase mismatch; dirty source refused. |
| `git diff --check` | 0 | No whitespace errors. |

`BASELINE=~/openmerge-first-parent-main`,
`CANDIDATE=~/openmerge-fix-first-parent`,
`OUT=~/.cache/openmerge-first-parent/grant-replay`.
The focused command was:

```sh
go test -short -count=1 -v ./internal/forge/fake ./internal/gitops ./internal/serve \
  -run 'Test(GitContract|Contract|ContractWithoutRollbacks)/history-limits|TestStageA(TestedHistoryLimits|GateRealGitLanding)$'
```

The final regression command was:

```sh
go test -short -count=1 -v ./internal/serve \
  -run '^TestStageA(GateRealGitLanding|TestedHistoryLimits)$'
```

The clean-main reproduction used a Go overlay that adds only the final
`internal/serve/stage_a_real_git_test.go` to the baseline checkout, then ran:

```sh
go test -overlay ~/.cache/openmerge-first-parent/regression-main-overlay.json \
  -short -count=1 -v ./internal/serve -run '^TestStageAGateRealGitLanding$'
```

SQL negative controls detected: `authorize-extra-statement`,
`claim-without-for-update`, `legacy-via-nil-policy-start`,
`load-before-claim-check`, and `emit-send-batch`.

Compatibility record SHA-256 values:

- Identity event/action checkpoints: `59e8f223916c504b10a20ead4d300f861f776f9f39dd91248f3294981dc2a6de`.
- Grant replay records: `e1ad29fe27dee98647f3eadcc90ace99e3272cc06d69ef4e453b45167dd947d4`.
- Legacy exits: `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`.
- Retry/default-digest records: `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.

Legacy tested/landed IDs (identical old and new):

| Method | Tested | Landed |
| --- | --- | --- |
| Squash | `ff69dbec39d51f58388ecb40939b116639245145` | `ff69dbec39d51f58388ecb40939b116639245145` |
| Merge | `bcc5548afcfa623368902566ffc0baa8b9cdc752` | `bcc5548afcfa623368902566ffc0baa8b9cdc752` |
| Rebase | `fb81d35ef3ab6f59feb8cd76b09047d01637c9fb` | `fb81d35ef3ab6f59feb8cd76b09047d01637c9fb` |
| Fast-forward | `fc5c1bf85c90345690a94321d2afe65d7007018c` | `ccfc498745b4ed1f770d8e20c0c37e585564669a` |

The commit-ID harness hash is
`6480d8da0dadc62e5cea983986971af4e1b18fd780cf65193d906967667c6f30`.
The old/new source-tree hashes are respectively
`29ca84a57036562a488a81b8c1e3f24246e72730ecab8df0989c6e055ed6aea2` and
`bfc0cce51a8a157f83c6376dcd7c41fdb6a4931228bed2027344660bfc4ef9c3`.
The old/new gitops test-binary hashes are respectively
`4877176427192e0d992976a3e8edb964dfabe32f9221f9acb94fda03c792a751` and
`d7cd0ef70e199797024df246a75d269d45eab9d167d2ddedfba152b23491c71e`.

Raw local logs are retained under
`~/.cache/openmerge-first-parent/evidence/` (and exported journals under the
`grant-replay` directory above). Log SHA-256 values:


- `regression-main.log`: `287da759c021ce8a3ffb8db96b585efce0679a6f0aee3ae97ebedcf6584e4d04`.
- `regression-main-overlay.log`: `2f6fcb6ddd6f5bbafef8cbf9f516380ebd28e8b140d6d7c9cf5e8b40aecda5fe`.
- `focused.log`: `158c921cbc6705856cb772f72423e8e5e6bba93cb850bf421f85a190aa5bb364`.
- `regression-final.log`: `aa638d89f518ce8b46233e162ff604c149b2920a5aa2421c28bb001416f69c42`.
- `make-check.log`: `2a0ed80e1185fe5632b90e4719364edc5565edaf5055b490efad377ac78e3442`.
- `sim-exits.log`: `7d7bc57264d7544b668e17fb35568a8b441edb4a346665ac701fa3c40dff3d03`.
- `compatibility-identity.log`: `a63900b6455bd6585ddbba76a68ad3f00eb963d765bb9b2fc1da25a792921913`.
- `compatibility-grant.log`: `c24c76f1a6dd20f51d3cbd5ecd8ea39cef492400fadc93865491cc38b8ddf1e3`.
- `compatibility-phase2.log`: `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749`.
- `legacy-sql.log`: `75177d4ccefddfb00f49ff85b7b576d939a825fa08a920837c362ecf4f85bc76`.
- `legacy-commit-ids.log`: `e92a786e72838ec9cab8464e1ae85fd5d0414e914d4a0398e42b972d19f246d6`.
