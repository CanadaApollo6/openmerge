# Stage A first-parent limit repair

## Round 1: bound approval rebases before CI (2026-10-19)

Rebased the PR onto `origin/main` at
`ac2f72a4d8b2d7be80178f2dfbe97cdb01d15233`.
The production/test implementation is `347112e`; `ecd6afd` adds only the
validator comment required by lint; `fd9dd75` adds the required wire golden
fixture. Final checked source HEAD: `fd9dd75a9651c09f3f85a227b4d0130f16dd732f`. The final evidence commit
changes this file only.

Approval queues (`w.stageA != nil`) now read each pinned rebase member's
`head ^ build-base` source history before construction. Each read uses the
shared `forge.MaxFirstParentCommits` limit (10,000), capped by the history-reader
contract, with the reader's one-commit truncation lookahead. A truncated member
is represented durably as limit+1; exact untruncated counts determine the batch
aggregate. The event `RebaseCompositionObserved` is journaled after the authorized
batch start and before `BuildTestRef` can push a CI ref. Its reducer:

- removes unsupported members and schedules a durable
  `rebase-composition-limit` dequeue, whose reported detail is
  `rebase composition exceeds 10000 commits; use squash or merge, or split the change`;
- retains the supported ordered prefix that fits the aggregate budget;
- leaves the overflowing suffix queued in an ordered pending group, without
  build-failure, bisection, or CI-failure accounting.

Rejected/deferred members are detached before dequeues are requested, so a
rejection does not invalidate a supported speculative composition. Metadata is
loaded from the resulting durable batch. Recovery resumes that subset;
duplicate/stale composition observations do not add a second deferred group.
The gate still checks the same pinned source differences and aggregate limit.

Legacy queues leave the executor callback nil: no source-count query or new
composition event is introduced. Their build and landing requests retain their
existing behavior. No Makefile, CI, schema, or live service changes were made.

`TestStageARebaseCompositionBeforeCI` uses a real bare Git remote, the production
executor build path and source reader, encoded/decoded journal events, the
production tested-message gate for approval queues, and real fenced landings.
It covers oversized members alone and beside a supported member, aggregate
splitting with both PRs eventually landing, an exact-limit composition, and a
legacy over-limit build/landing with zero history-reader calls. Source fixtures
use one `git fast-import` process. Under `-short`, the same preflight accepts an
injected limit of four; the non-short run exercises the unchanged production
10,000 limit (10,001 rejection and legacy acceptance, 9,999+2 splitting, 10,000
landing). The production-size run passed in 199.307 seconds; the final focused
short run passed in about 2.6 seconds.

`TestStageARealGitSpeculativeDepthFour/{merge,squash,rebase}` builds four distinct
speculative batches before landing any. Each pins the previous tested SHA as
its build base. All four then pass the production tested-message reader and
land in order, checking the actual remote main SHA after every landing. This is
separate from the original `depth-4` test, which covers four members in one batch.

`TestRebaseCompositionReplayAndResume` checks journal/snapshot recovery and
idempotent selection at speculation depths one and four, including retained
rejection and suffix state. The original real-git landing, history-boundary,
and shared real/fake reader contracts remain covered by `make check`.

Only the shared test Postgres endpoint at localhost:54329 was used, with isolated
test schemas. All Go jobs used `GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`, and cache/temp
directories under `~/.cache`; heavy jobs ran sequentially after checking for other
Go test/replay processes. The initial default fetch refreshed the local
remote-tracking `origin/openmerge/fence/main`; it wrote no remote ref. Subsequent
fetches were restricted to main. No live DB, live status API, sandbox server or
repository, systemd units, or live queue/fence refs were used or written.

### Round 1 verification and exit statuses

All final gates compare against clean main at `ac2f72a4d8b2d7be80178f2dfbe97cdb01d15233`.
The final branch-specific fetch still resolved main to that SHA.

| Run/log label | Exit | Result |
| --- | --- | --- |
| `round1-focused` | 1 | Development: fixture event encoding and fence chaining errors. |
| `round1-focused2` | 1 | Development: compile error while correcting fixture/decoder wiring. |
| `round1-focused3` | 0 | Initial real-Git preflight and stacked-batch cases pass. |
| `round1-focused4` | 1 | Development: duplicate-observation assertion incorrectly compared the journal revision. |
| `round1-focused-final` | 0 | All final focused short cases and original regressions pass. |
| `round1-production-limit` | 0 | All five production-limit real-Git cases pass, including legacy 10,001. |
| `round1-make-check` | 2 | First check stopped at lint: exported validator required a comment. |
| `round1-make-check-final` | 2 | Second check found the missing new-event golden fixture; remaining own test processes were terminated to restart the corrected suite. |
| `round1-wire` | 0 | Golden, registry, field-name, private-key, invalid-data, and composition replay tests pass. |
| `round1-make-check-complete` | 0 | Final make check: formatting, zero lint issues, full race/short suite, build. |
| `round1-sim-exits` | 0 | Final make test-sim-exits. |
| `round1-identity` | 0 | Depth-1/4 legacy event/action differential. |
| `round1-grant` | 1 | Original static guard rejects any wire.go source change before replay; the new event is additive. |
| `round1-grant-additive` | 0 | Exact three-block additive-event audit, then unchanged depth-1/4 exported-journal replay differential. |
| `round1-phase2` | 0 | Legacy exits, retry/default-digest records, and existing wire fixtures. |
| `round1-legacy-sql` | 0 | Legacy SQL differential plus all five negative controls. |
| `round1-commit-ids` | 0 | All four methods; mandatory identical/mutant/dirty self-tests. |
| `git diff --check` | 0 | No whitespace errors. |

SQL baseline/candidate and all five mutant test probes each exit **0**; the comparison detects all five changed SQL traces. Commit-ID self-test exits are **0 / 1 / 2** for identical / mutant / dirty, respectively (all expected).

Commands use `BASELINE=~/.cache/openmerge-first-parent-round1-main` and the same existing compatibility scripts documented below. The new focused commands were:

```sh
go test -short -count=1 -v ./internal/serve ./internal/queue \
  -run "TestStageA(RebaseCompositionBeforeCI|RealGitSpeculativeDepthFour|GateRealGitLanding|TestedHistoryLimits)$|TestRebaseCompositionReplayAndResume"
go test -count=1 -timeout=30m -v ./internal/serve \
  -run "^TestStageARebaseCompositionBeforeCI$"
go test -short -count=1 -v ./internal/queue \
  -run "TestWire|TestRebaseCompositionReplayAndResume"
```

The original grant checker requires all of `wire.go` to be byte-identical and exits 1 before replay on this additive event. The local `~/.cache/openmerge-first-parent/check-round1-grant-compatibility.py` first removes exactly the new event struct and its encoder/decoder switch cases from a comparison-only string; it requires that string to equal baseline `wire.go` byte-for-byte. The actual candidate source and binary retain the event. The rest of the checker exports baseline journals and replays both original binaries unchanged. No tracked checker, baseline, production source, Makefile, or CI file was modified for this audit. Its SHA-256 is `c2dc27899b84e8defcd345b584ce53a3568e4a9f475509fb774fec12e1d51638`.

Raw round-1 logs remain under `~/.cache/openmerge-first-parent/evidence/`. SHA-256:

- `round1-focused.log`: `b8c62ff29c13293192091904b2e32263d68861e7c9aeb46ff3b198d59ddad7a5`.
- `round1-focused2.log`: `5cbffbabeb0baa6bd56cf2cc31d7de301ff71a03c844e8ee9df30e1bb2b6456e`.
- `round1-focused3.log`: `68189588afd4af932815e4c6a0cfa77bb5009295acf03ef78f872ba93caa7b64`.
- `round1-focused4.log`: `7a2dc293c8a1e3c6a40fb26224d80e1715b0082f5ce8a8495bc45e01f53da67f`.
- `round1-focused-final.log`: `42df9fcb6e150ed6fe58003b75beaffea57ead36dd2e24c619790ecb5047e148`.
- `round1-production-limit.log`: `2a63dabf5247263a2516cac8cc3fc15379ddc729d9916b66f6b2fdcb951e5e51`.
- `round1-make-check.log`: `3e89cb0b5a161937148bec8d481081a5fa79083905152c3f2a03253a34d9a5c1`.
- `round1-make-check-final.log`: `8cf1a94428891b09f4d7fcfb937876efc41a2f820afd4702273d8cc8eb4de39b`.
- `round1-wire.log`: `3142699d99abd4a2a98ba0deb846dfdfd4171abca48f335e1ab3623121a9190e`.
- `round1-make-check-complete.log`: `19829ce9bbddc329d24adc2eaa0400d8b1a4da19cf948ff3522b9fad725b8a11`.
- `round1-sim-exits.log`: `2f8574a129ba9b59e84c2caa8a020ed284f260d121085589ad9bc81b9c400f8c`.
- `round1-identity.log`: `a63900b6455bd6585ddbba76a68ad3f00eb963d765bb9b2fc1da25a792921913`.
- `round1-grant.log`: `3f6bce486267b50b218ff3cec3579bd703cee998d75697100926f1e49ffae13b`.
- `round1-grant-additive.log`: `ff8802b1f73640198ac4169bc883e63a7da774372014754e7eaf202983432496`.
- `round1-phase2.log`: `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749`.
- `round1-legacy-sql.log`: `5fbca6f02dda4032822c0fb32412699c98f1b54faa8cf20e104b568cd8102eba`.
- `round1-commit-ids.log`: `596d4896fe83b97f6211d3f4b53728a92c62bc937fbef9c3b62168fa0516aa65`.

### Final compatibility output

```text
every-revision events/actions: 1753 checkpoints byte-identical; sha256=59e8f223916c504b10a20ead4d300f861f776f9f39dd91248f3294981dc2a6de
PASS: removing only the exact new-event struct/encoder/decoder restores byte-identical baseline wire.go
every-revision replay: 1775 identical records; sha256=e1ad29fe27dee98647f3eadcc90ace99e3272cc06d69ef4e453b45167dd947d4
legacy exits: 17 records byte-identical; sha256=bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6
depth-1 retry envelopes and default digest: 1055 records byte-identical; sha256=07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37
all 84 existing wire fixtures byte-identical; exit=0
PASS: identical 7 statements, including transaction and lock order
PASS: negative control authorize-extra-statement: detected SQL difference (expected rejection)
PASS: negative control claim-without-for-update: detected SQL difference (expected rejection)
PASS: negative control legacy-via-nil-policy-start: detected SQL difference (expected rejection)
PASS: negative control load-before-claim-check: detected SQL difference (expected rejection)
PASS: negative control emit-send-batch: detected SQL difference (expected rejection)
legacy-commit-id-per-method verdict=PASS methods=4
legacy-self-test case=identical exit=0 expected=0
legacy-commit-id-per-method verdict=FAIL mismatch=squash,rebase
legacy-self-test case=mutant exit=1 expected=1
legacy-self-test case=dirty exit=2 expected=2
legacy-commit-id-per-method verdict=PASS methods=4
```

## Original implementation evidence (before round 1)

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
