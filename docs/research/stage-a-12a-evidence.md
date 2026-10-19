# Stage A 12a reporting evidence

Trial deployment class: **Hold**. This reports the held Stage A 9/11a evidence
and is not deployable during the ADR 0009 trial. No claim of building and being
inert on `3e15360` is made. 11b owns publication/activation and policy digest
composition; 12b owns final Stage A/sandbox acceptance. Nothing was deployed.

Implementation worktree: `/home/riels/openmerge-sa-12a`, branch
`stage-a/12a-reporting`, based on `origin/main`
`6e2b50b9da5c098ebfd76e0b1b20a5990c2663bb`. The explicit main fetch succeeded.
The worktree creation command succeeded; its combined shell command returned 2
because the final optional `ls` found no parent AGENTS.md files. The detached
comparison worktree `/home/riels/openmerge-sa-12a-main` is the same main commit.
No config loader/schema, event/action wire, landing gate, Makefile, CI workflow,
or CI test flags were changed. Migration 007 contains reporting indexes only.

## Reporting obligations

- `TestChangeShowHTTPRevisionScopeAndAuth`: scoped IDs, unknown/cross-queue IDs,
  revision selection, historical heads, read-only authentication/method handling.
- `TestChangeDecisionsBoundedReadOnlyHTTP`: typed reader only (journal replay
  panics in the test source), bounded cursor/limit/revision parsing.
- `TestChangeDecisionsIndexedReadbackAndSettlement`: exact retained eligibility,
  all three outcomes, revision/cursor scope, unrelated undecodable event safety.
- `TestChangeDecisionsPaginationDoesNotConfirmPriorAttempt`: exclusive paging,
  original decision/eligibility event IDs, abandoned versus subsequent landing.
- `TestReportingIndexesExcludeUnrelatedHistory`: actual EXPLAIN ANALYZE plans
  for alias/page/settlement reads exclude unrelated journal event types.
- `TestLandingEvidenceIdentityIndexes`: 9b's existing migration-006 identity
  index and bounded typed read tests remain in place. The reader now also
  returns original decision and eligibility event IDs; it never invents them.
- `TestDecisionEvidenceRetentionNoPruning`: there is no journal compaction or
  persisted snapshot pruning today. The guard rejects new destructive SQL in
  production internal Go string literals and SQL migrations. A pruning change
  must replace it with integration proof retaining both decisions and their
  referenced eligibility events, including original read IDs after settlement.
- `TestChangeReportingGoldens`, `TestReviewerCheckLegacyGolden`, and
  `scripts/check-legacy-reporting.py`: HTTP/check fixtures, byte-identical legacy
  check versus main, and unchanged preexisting status fields after removing
  only the two new additive entry fields (`change`, `landing`).
- `TestChangeShowCLI`, `TestChangeShowExitCodesAndReadOnlyCommands`, and existing
  brief CLI tests: escaped queue selectors, byte-preserving JSON, stable status
  exit conventions, no submit/approve write commands, rendering/response limits.
- `TestReviewerCheckWarningsEscapingAndBounds`,
  `TestChangeResponseSizeLimit`, `TestDecisionViewDoesNotInferLanding`: warnings,
  escaping, bounded output and no intended-SHA-to-confirmed-landing inference.
- Existing 7b NextStep path/URL handling and default-branch rename documentation
  remain intact. Candidate policy requirements explicitly report unavailable
  until 11b's digest composition exists. This PR implements no composition.

The single existing check is extended only for an approval policy. Legacy queues
append the empty suffix, retain their bounded CI ledger and existing wording,
and emit no new policy events. Accountability distinguishes account, author,
submitter and humans; a token cannot establish that a person rather than an
agent acted. Receipt acceptance is historical; current digest binding is shown
separately and is not a second approval authorization result.

## Validation execution

Every build/test/helper waits for other agents' `go test`, `*.test` and replay
processes. One heavy command runs at a time. All inherit:

```sh
GOFLAGS=-p=2
GOMEMLIMIT=4GiB
GOTMPDIR=/home/riels/.cache/openmerge-12a/gotmp
GOCACHE=/home/riels/.cache/go-build
OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
```

Runner and raw logs: `/home/riels/.cache/openmerge-12a/run.py`, `results.jsonl`,
`logs/`. PostgreSQL tests create and drop isolated random test schemas only.
No live database/status API, systemd unit, or production `openmerge/*` ref was
accessed. No real forge API calls were added or used in code/tests/simulator.
Only the explicit main ref was fetched; only this branch will be pushed.

All validation commands and actual exits, including failed attempts, are
recorded below after the run finishes. Setup and read-only source inspection
are not test gates. There is no hidden test-flag or timeout relaxation.
<!-- validation-results -->

The first focused run failed on missing reporting imports. Focused attempts 2
and 3 then caught incorrect error names in the new CLI test's HTTP 409/400
fixtures; production exit mapping remained the existing queue mapping.
`check` failed lint (unchecked writes, exported DTO documentation, unused test
embedding). `check-2` failed the existing CLI help golden and also observed a
mixed build because reporting types/index fixtures were edited while Go was
compiling later packages. These failures are retained rather than presented as passing gates. Later
review removed repeated ID hashing in revision lookup and added human-readable
landed-commit/decision details, then reran reporting race tests and make check.
A final compatibility review widened the alias index/query from int to bigint
and added TestChangeDecisionsLargeLegacyPRAlias; the width-focused and final
make-check runs cover that final code commit.

Three **own waiting drivers** were stopped with SIGTERM to put fixture generation
and dependent CLI validation in one serial sequence. No test subprocess had
started under them and no foreign process or running heavy job was killed.
Waiting time is excluded from each command's measured run duration.

The first commit-ID attempt returned 2 because its clean-source guard rejected
the uncommitted candidate (both HEADs still named main); no SHA comparison was
claimed from that attempt. Committed-candidate comparisons are separate runs.

The strict replay uses this existing exported deployment pair, without connecting
to the live DB or status API:

- Events `events-20261018T051000Z.jsonl`, SHA-256
  `9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`.
- Queues `queues-20261018T051000Z.jsonl`, SHA-256
  `418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`.

### focused: actual exit 1

```sh
go test -short -count=1 ./internal/status ./cmd/openmerge ./internal/brief ./internal/store -run 'Test(Change|ReviewerCheck|Decision|Brief)'
```

Wall execution: 7.159s. Log SHA-256: `0d2af28d52e828743956a4d13c0316ac3192dc238fc402a029efc427afe87596`.

### focused-2: actual exit 1

```sh
go test -short -count=1 ./internal/status ./cmd/openmerge ./internal/brief ./internal/store -run 'Test(Change|ReviewerCheck|Decision|Brief)'
```

Wall execution: 5.84s. Log SHA-256: `efa31bf4822b5bb10ad4172f146ae39a171c4104c7e536ad47f6ca51ce3a72e1`.

### focused-3: actual exit 1

```sh
go test -short -count=1 ./internal/status ./cmd/openmerge ./internal/brief ./internal/store -run 'Test(Change|ReviewerCheck|Decision|Brief)'
```

Wall execution: 7.027s. Log SHA-256: `96e9b97a8a1345ce2c6f8597f888dacda68ede159379ac99c4177c5d9c52e912`.

### focused-4: actual exit 0

```sh
go test -short -count=1 ./internal/status ./cmd/openmerge ./internal/brief ./internal/store -run 'Test(Change|ReviewerCheck|Decision|Brief)'
```

Wall execution: 7.1s. Log SHA-256: `395946d5f294a9ea1240ab63a4d8dad0692799c823db20387839f693d0f824f3`.

### check: actual exit 2

```sh
make check
```

Wall execution: 4.788s. Log SHA-256: `b0257035274bccfd1b60e9d4ea79d390f9aac32169a2dabab47587e3231acce4`.

### check-2: actual exit 2

```sh
make check
```

Wall execution: 391.111s. Log SHA-256: `f0cb60b2e322a4681275b05765970c947811b3953d2dd1a434bc9fbd7e6afc2b`.

### legacy-reporting-record: actual exit 0

```sh
python3 scripts/check-legacy-reporting.py /home/riels/openmerge-sa-12a-main --record
```

Wall execution: 1.896s. Log SHA-256: `a5862563372cc6cea72b13045f3c9bf491607652274c329ada1ad5f3b1dd135c`.

### reporting-goldens: actual exit 0

```sh
env OPENMERGE_UPDATE_REPORTING_GOLDENS=1 go test -short -count=1 ./internal/status -run '^TestChangeReportingGoldens$'
```

Wall execution: 0.496s. Log SHA-256: `762aa0b98a436b51848e499a32e9d352436e466266e07d7e6099694070966697`.

### cli-golden: actual exit 0

```sh
env OPENMERGE_UPDATE_REPORTING_GOLDENS=1 go test -short -count=1 ./cmd/openmerge -run '^TestChangeShowCLIGolden$'
```

Wall execution: 1.029s. Log SHA-256: `6fb0bd1494002692f48eb10b6c0b0c420dfa65bc6254e1c37d12f6ac0bf3737e`.

### focused-5: actual exit 0

```sh
go test -short -count=1 ./internal/status ./cmd/openmerge ./internal/brief ./internal/store -run 'Test(Change|ReviewerCheck|Decision|Brief|LandingDecision|LandingEvidence|Reporting|MigrationsConcurrent|Run$)'
```

Wall execution: 6.253s. Log SHA-256: `dafb99600f5ea67074545193a495ce105206ad999af3b585612ce45e24eec5d1`.

### check-final: actual exit 0

```sh
make check
```

Wall execution: 404.968s. Log SHA-256: `2299358e9d7d1a288fc7aef3f4eebb16acfd93de39c9b4effe906869807c5b72`.

### sim-exits: actual exit 0

```sh
make test-sim-exits
```

Wall execution: 38.649s. Log SHA-256: `8078526db54ff0c78b1196e7639358a8aab94bc1dcea5e21488234df8673ba7f`.

### compatibility: actual exit 0

```sh
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-12a-main
```

Wall execution: 11.796s. Log SHA-256: `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749`.

### legacy-sql: actual exit 0

```sh
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-12a-main
```

Wall execution: 4.571s. Log SHA-256: `a6c504dd82f62eb30db665dae4b257854ceb1715577d438a58982454a9993bbf`.

### legacy-depths: actual exit 0

```sh
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-12a-main
```

Wall execution: 142.609s. Log SHA-256: `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453`.

### commit-ids: actual exit 2

```sh
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-12a-main --candidate /home/riels/openmerge-sa-12a
```

Wall execution: 0.131s. Log SHA-256: `007a743ec1b54614d477dbc8ce611e4210f1ecea0324c3aa94483c13c3af71e8`.

### focused-reviewed: actual exit 0

```sh
go test -race -short -count=1 ./internal/status ./cmd/openmerge -run 'Test(Change|ReviewerCheck|Decision|Brief|Run$)'
```

Wall execution: 18.36s. Log SHA-256: `66371c1a20c3c63e3a4b37ceb0ac5a2821d575cc34d683abe5c54f2c9090d23e`.

### check-reviewed: actual exit 0

```sh
make check
```

Wall execution: 412.468s. Log SHA-256: `12d8135729d878142744f280bd9f8ff37fbb66d0691aebe78d27d72b800a94da`.

### commit-ids-committed: actual exit 0

```sh
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-12a-main --candidate /home/riels/openmerge-sa-12a
```

Wall execution: 14.161s. Log SHA-256: `01559664285996c28e1534f30bdc1920dbaf32a3a4752ceeadbcc981886536d8`.

### replay-main: actual exit 0

```sh
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-12a-main --candidate /home/riels/openmerge-sa-12a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-12a/replay-main.json
```

Wall execution: 65.207s. Log SHA-256: `1566b20bec40c5dc8187c4e6ecf6802ad1d02c51578674f1e2460f16dc2a7121`.

### legacy-reporting: actual exit 0

```sh
python3 scripts/check-legacy-reporting.py /home/riels/openmerge-sa-12a-main
```

Wall execution: 0.822s. Log SHA-256: `a5862563372cc6cea72b13045f3c9bf491607652274c329ada1ad5f3b1dd135c`.

### width-focused: actual exit 0

```sh
go test -race -short -count=1 ./internal/store -run 'TestChangeDecisions|TestReportingIndexes|TestDecisionEvidenceRetention'
```

Wall execution: 3.534s. Log SHA-256: `24b437af64c06358986b25bee1315e95d4d1e39e4c9f9ab22a8287c71e09ed31`.

### check-width-final: actual exit 0

```sh
make check
```

Wall execution: 391.312s. Log SHA-256: `5031b858053500a6c72e47d6b27c1f156629047278029152c0e626e20566dbac`.

### legacy-sql-width: actual exit 0

```sh
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-12a-main
```

Wall execution: 4.24s. Log SHA-256: `a6c504dd82f62eb30db665dae4b257854ceb1715577d438a58982454a9993bbf`.

### commit-ids-width: actual exit 0

```sh
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-12a-main --candidate /home/riels/openmerge-sa-12a
```

Wall execution: 9.628s. Log SHA-256: `b34b225a8be7fd5d91ec1614fc002274a3a050ab814352bf4a6b6ddb59abcf85`.

### legacy-reporting-width: actual exit 0

```sh
python3 scripts/check-legacy-reporting.py /home/riels/openmerge-sa-12a-main
```

Wall execution: 0.477s. Log SHA-256: `a5862563372cc6cea72b13045f3c9bf491607652274c329ada1ad5f3b1dd135c`.


## Final result and source identity

All required final gates exited **0**. The last `make check` passed in
**391.312s**, with **0 lint issues**; serve passed in **251.447s**. These are
single-run wall/package durations, not a performance or trial-deployment claim.
The SQL differential detected all five negative controls. Depth 1/4 matched
227,662 short-corpus, 5,615,998 full-corpus and 188 fixture checkpoints, with
no nil-only exceptions. Squash, merge, rebase and fast-forward tested/landed
commit IDs match main. Strict exported-journal replay reports Gate 2 PASS and
Gate 4 PASS; no live DB, status API or mutable forge lookup is involved.

`TestChangeDecisionsLargeLegacyPRAlias` additionally proves PR `4294967297`
remains readable through the reporting alias index. `TestChangeShowLandedCLI`
proves the human CLI displays the recorded commit, attempt, settlement event
and decision-read URL. `TestBriefHTTPPolicyUnavailableAndMismatch`,
`TestBriefPublicLoaderStillRejectsCandidateKeys`, `TestR4VersionedApprovalRecovery`
and `TestR4UnknownDefaultBranchHTTP` remain green in the full suite.

Final validated code commit: `29dbac7fb6a515d056f75fbc08c8f21e5fb57122`. The subsequent evidence-only commit
changes this document alone; production/test sources remain identical.

Replay baseline: `6e2b50b9da5c098ebfd76e0b1b20a5990c2663bb`; candidate clean: `True`; state mode: `strict`.

Replay artifact: `/home/riels/.cache/openmerge-12a/replay-main.json`, SHA-256 `1566b20bec40c5dc8187c4e6ecf6802ad1d02c51578674f1e2460f16dc2a7121`.

Any future pruning must also preserve the PR alias and attempt settlement
boundaries needed by these reporting reads. The current guard rejects pruning
until integration tests establish the complete retention contract.

Trial deployment class remains **Hold**. No deployment was performed.

## Review round 1 fixes

This section supersedes the original implementation result above for the round-1
candidate. Trial class remains **Hold**. The round starts at `fb0b95c` against
main `6e2b50b`; only explicit main and `stage-a/12a-reporting` refspecs are
fetched. No deployment, live DB/status API, systemd unit or `openmerge/*` ref is
accessed. No Makefile, CI or test-flag changes are made.

- Status requests share one journal read and one queue replay. The ledger and
  explanation use that projection; queue lists do not render briefs. Only a
  selected PR with Stage A evidence gets a reviewer evidence view. Legacy
  `PRObserved` revisions still receive cheap ID/revision metadata.
  `TestLegacyReporting128EntriesAllocationBudget` covers Astra's 128 PRs plus
  512 refreshes for both PR lookup and queue list, with a 40.8 MB budget (twice
  the review's 20.4 MB main allocation). `BenchmarkLegacyReporting128` reports
  latency/bytes/allocations. `scripts/check-reporting-performance.py` repeats
  the same PR lookup seven times on main and candidate and asserts both median
  latency and allocated bytes are within 2x main.
- `TestApprovalWaitOperationalGatePrecedence` covers invalidation after an
  approval rejection and dequeue after invalidation retirement. Operational
  gates win; recorded approval reasons remain in `change.waiting_reasons`.
- Check reporting shares the ledger journal/projection cache. Recorded policy
  reconstruction failure intentionally degrades to a visible unavailable
  warning instead of retaining a stale check. Policy warnings are prioritized
  ahead of optional evidence, and the server logs the degradation.
  `TestStageACheckSharesLedgerSnapshotAndLogsUnavailablePolicy` detaches the
  store after ledger loading and proves both check text and logging still work.
- Receipt text derives from recorded receipts, including acceptance/binding.
  Waiting reasons and warnings are escaped and count-capped with `+N more`;
  warnings precede optional evidence and the suffix has an overall ~12 KB
  bound, leaving room below GitHub's 65,535-character limit.
  `TestReviewerCheckReceiptsWaitingAndWarningGolden` and
  `TestReviewerCheckWarningsEscapingAndBounds` cover that output.
  `TestReviewerCheckWarningsSurviveOversizedBriefURL` proves a large queue path
  cannot crowd safety warnings out of the bounded output.
- CLI decision reads support queue selectors, JSON, revision and bounded
  pagination with existing exit conventions. Tests:
  `TestChangeDecisionsCLIJSONPaginationAndRevision`,
  `TestChangeDecisionsCLIGolden`, `TestChangeDecisionsExitCodesAndValidation`,
  `TestChangeShowRejectsNonReviewerAudience`, `TestChangeShowCLI` and its golden.
  Show always prints the decisions link and rejects non-reviewer audiences.
- Change show checks the brief's found flag, returns 404 for absent revision
  evidence and 503 for projection errors, and attaches the same selected Entry
  evidence as the PR alias. Tests: `TestChangeShowAndPRAliasEntryMatch`,
  `TestChangeShowWithoutRevisionEvidenceIsNotFound` and
  `TestChangeShowHTTPRevisionScopeAndAuth`.
- Migration 007 guards numeric aliases (including bigint overflow) with a CASE
  expression shared by its indexed query. Tests:
  `TestReportingPRAliasIndexBuildIgnoresMalformedHistoricalValues`,
  `TestChangeDecisionsIgnoresMalformedLegacyPRAliases`,
  `TestChangeDecisionsLargeLegacyPRAlias`,
  `TestReportingIndexesExcludeUnrelatedHistory`. The page comment now names
  migration 007. `TestReportingIndexesBuildTiming` measures 2,600/260,000 mixed
  rows and verifies ShareLock blocks a writer until commit. Operating docs
  require the drained ADR 0009 period-B/Hold window, including lock-wait and
  production-size caveats.
- Absent landing metadata is omitted; remedy decision sources/requirements
  render as empty arrays. The extended legacy differential compares six
  fixtures: original golden, multi-entry, landed, dequeued, blocked and depth 4.
  It compares preexisting serialized bytes and field order after stripping
  only Entry `change`/`landing`, and rejects drift via negative controls.
- `TestDecisionEvidenceRetentionNoPruning` now scans `cmd/` as well as
  `internal/`, including index drops and payload replacement. Its pattern test
  covers destructive SQL and prose false positives. This is an unquoted SQL
  literal tripwire, not a parser or a database retention guarantee; dynamic or
  quoted SQL and external clients can bypass it. Future pruning still needs
  integration proof retaining decisions, referenced eligibility and read IDs.

Round-1 execution uses the same shared test Postgres and ~/.cache Go paths
listed above. The serial runner waits for other Go/test/replay jobs and sets
`GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`. Only this thread's own waiting final-gate driver was stopped before any test
child started, to sequence an added long-URL warning regression and its gates.
No foreign process was stopped.
Detailed exits, log hashes, final performance samples and source identity follow.
<!-- round-1-results -->

Early failures are retained: `r1-focused` (1) caught an unused import and
expected golden drift; `r1-goldens` (1) and `r1-regressions` (1) caught a test
that expected pending invalidation after the retiring BatchesInvalidated event.
`r1-store` (1) caught untyped fixture parameters and prose false positives in
the source tripwire. `r1-check` (2) stopped on CLI test formatting;
`r1-check2` (2) stopped on an unused callback parameter. Those were corrected;
no test-flag, timeout or CI relaxation was introduced. The first full make check passed at e4d6182. While waiting for the shared host,
an additional oversized-URL regression exposed that the check header could
crowd out warnings; warnings now precede the header, and reconstruction errors
are logged through a callback at the error boundary. Commit a006829 includes
that follow-up and explicit fixture-state assertions in the legacy comparator.
The final full make check passed after those Go edits were frozen. The legacy
comparator's new state assertion then caught an invalid batch ID in the landed
fixture (`r1-legacy-reporting-final`, exit 1); eaeec96 corrects it to the
canonical first batch ID. The six actual states now pass comparison
(`r1-legacy-reporting-final2`, exit 0). This fixture-only follow-up changes no
Go source from a006829; remaining gates initially ran against committed eaeec96. A subsequent terminal-entry
parity probe (`r1-redaction-probe`, exit 1) exposed a fresh sanitizer without the
request secret registry. Change show now refreshes metadata with the request
sanitizer; `TestChangeTerminalEntryRetainsRequestSanitizer` proves both routes
redact identically. The final read-only audit also found check gate recomputation
used zero timing configuration. `TestReviewerCheckUsesCurrentOperationalGate`
proves the check retains the applied max_wait gate. Both fixes are included in
the final verified gate series.

A preferred delegate model was at capacity for the store and legacy-tooling
assignments; fallback agents completed those edits. Delegates did not run heavy
jobs. One waiting serial driver waited on this thread's make check; only this thread's waiting final-gate driver was stopped with SIGTERM before
any test child started, to add the long-URL warning regression (actual exit 143).
No foreign job or process was stopped. Source syntax/diff checks and code commits exited 0.

### r1-focused: actual exit 1

```sh
go test -short -count=1 ./internal/status ./internal/serve -run 'Test(Change|ReviewerCheck|Brief|Ledger)'
```

Wall execution (excluding wait): 2.539s. Log SHA-256: `817861d693e07d78df501f0bda7609d2326e4767dc6a4d3289ca15d1398ba37e`.

### r1-focused2: actual exit 0

```sh
go test -short -count=1 ./internal/status ./internal/serve -run 'Test(ReviewerCheck|Brief|Ledger)'
```

Wall execution (excluding wait): 22.23s. Log SHA-256: `2b4ee73baec6f18c0b457c9644d7c3d88029dfde162d5bef948d804113389ceb`.

### r1-goldens: actual exit 1

```sh
env OPENMERGE_UPDATE_REPORTING_GOLDENS=1 go test -short -count=1 -v ./internal/status -run 'Test(ChangeReportingGoldens|LegacyReporting128|ApprovalWaitOperational|ChangeShowAndPRAlias|ReviewerCheckReceipts)'
```

Wall execution (excluding wait): 0.954s. Log SHA-256: `1e3c63cede834482cff96393924250d37018ea66f37347b4ff2551d0d42ec8f0`.

### r1-regressions: actual exit 1

```sh
go test -short -count=1 -v ./internal/status -run 'Test(ChangeReportingGoldens|LegacyReporting128|ApprovalWaitOperational|ChangeShowAndPRAlias|ReviewerCheckReceipts)' -bench BenchmarkLegacyReporting128 -benchtime=5x
```

Wall execution (excluding wait): 0.512s. Log SHA-256: `c6e5a58314a363d207b6432d5dbf6ee84265adef18c00c13dacbcbf85a297761`.

### r1-goldens2: actual exit 0

```sh
env OPENMERGE_UPDATE_REPORTING_GOLDENS=1 go test -short -count=1 -v ./internal/status ./internal/serve -run 'Test(ChangeReportingGoldens|LegacyReporting128|ApprovalWaitOperational|ChangeShowAndPRAlias|ReviewerCheckReceipts|StageACheckShares)' -bench BenchmarkLegacyReporting128 -benchtime=5x
```

Wall execution (excluding wait): 1.6s. Log SHA-256: `097ffdf9cb33d0404620fead2cef2d5206ac87c5eb3c38ef93265ea38d411746`.

### r1-store: actual exit 1

```sh
go test -short -count=1 -v ./internal/store -run 'Test(Reporting|ChangeDecisions|DecisionEvidenceRetention)'
```

Wall execution (excluding wait): 3.032s. Log SHA-256: `219241e5c9e13b9c108a34ba948bff81405234f675259acdd6c2cd3f2ca82873`.

### r1-status-final: actual exit 0

```sh
go test -short -count=1 ./internal/status ./internal/serve -run 'Test(Change|ReviewerCheck|Brief|Ledger|LegacyReporting|ApprovalWaitOperational|StageACheckShares)'
```

Wall execution (excluding wait): 21.934s. Log SHA-256: `765a55901df8864af10543e166bca4137a46ac1d40e7bf229850bc9371e5a768`.

### r1-store2: actual exit 0

```sh
go test -short -count=1 -v ./internal/store -run 'Test(Reporting|ChangeDecisions|DecisionEvidenceRetention)'
```

Wall execution (excluding wait): 3.359s. Log SHA-256: `bffa9282e037d9aa7587d4e0818bace5e41d1c5e4c05f95bab9c51b3d60e4579`.

### r1-cli: actual exit 0

```sh
env OPENMERGE_UPDATE_REPORTING_GOLDENS=1 go test -short -count=1 ./cmd/openmerge -run 'Test(Change|Run$)'
```

Wall execution (excluding wait): 1.097s. Log SHA-256: `fc81f7075d80a05ac0395c9ac8a927951c19c55c5b825ce9c9a4003bdc54664c`.

### r1-goldens3: actual exit 0

```sh
env OPENMERGE_UPDATE_REPORTING_GOLDENS=1 go test -short -count=1 ./internal/status -run 'Test(ChangeReportingGoldens|ReviewerCheckReceiptsWaitingAndWarningGolden)'
```

Wall execution (excluding wait): 0.506s. Log SHA-256: `6026e649823835c2320ab35f7b9f31ba4e918284b098b00af0961f039cad731c`.

### r1-check: actual exit 2

```sh
make check
```

Wall execution (excluding wait): 0.076s. Log SHA-256: `d38a4587a58d08ece5f4c75bca90916524e50b488af4bd7d362b84fe0821125b`.

### r1-check2: actual exit 2

```sh
make check
```

Wall execution (excluding wait): 2.737s. Log SHA-256: `d006bb4f3cdc873fcca29e27ce1ac3cf2d2580127db0c3191e53e121220a085e`.

### r1-check3: actual exit 0

```sh
make check
```

Wall execution (excluding wait): 413.134s. Log SHA-256: `99e1abb1c5aa14b8ad984b8608cf740e8ab5cacb80e54372fad8f2af89f29e78`.

### r1-perf: actual exit 0

```sh
python3 /home/riels/.cache/openmerge-12a/r1-perf.py
```

Wall execution (excluding wait): 1.217s. Log SHA-256: `57077e212c3c304120657f2c9fc4e6e7b482c19ff57a6dfb572f86a41b4c7f73`.

### r1-legacy-reporting: actual exit 0

```sh
python3 scripts/check-legacy-reporting.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 1.012s. Log SHA-256: `0b093c0fcd293596f31414c9b4c395f787edd8b553ce3df6ff5f5c65d914433d`.

### r1-sim-exits: actual exit 0

```sh
make test-sim-exits
```

Wall execution (excluding wait): 38.598s. Log SHA-256: `10525772002a6c4fac6087fa8effad15b55f3f26112a52ec28078b77a5f45b9a`.

### r1-compatibility: actual exit 0

```sh
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 11.1s. Log SHA-256: `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749`.

### r1-legacy-sql: actual exit 0

```sh
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 4.177s. Log SHA-256: `a6c504dd82f62eb30db665dae4b257854ceb1715577d438a58982454a9993bbf`.

### r1-depths: actual exit 0

```sh
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 140.719s. Log SHA-256: `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453`.

### r1-final-driver-stopped: actual exit 143

```sh
python3 /home/riels/.cache/openmerge-12a/r1-last.py
```

Wall execution (excluding wait): 0s. Log SHA-256: `3d86466d6ba034eee011cbb0965f662534e17a58c5aa428c77eb0d8452f0f219`.

### r1-warning-goldens: actual exit 0

```sh
env OPENMERGE_UPDATE_REPORTING_GOLDENS=1 go test -short -count=1 ./internal/status -run '^Test(ChangeReportingGoldens|ReviewerCheckReceiptsWaitingAndWarningGolden|ReviewerCheckWarningsSurviveOversizedBriefURL)$'
```

Wall execution (excluding wait): 0.535s. Log SHA-256: `c4a8e96a9829a321f815ecdbc3d829022c1cf86c0292ae857008e0a986306282`.

### r1-check4: actual exit 0

```sh
make check
```

Wall execution (excluding wait): 421.174s. Log SHA-256: `29bffb44e725f085db948802ff34230f4df27d1b5872cb5bbeefbc77ab826ab7`.

### r1-perf-final: actual exit 0

```sh
python3 scripts/check-reporting-performance.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 0.946s. Log SHA-256: `d12480bd29e32eba8c5d55e9fb54b992cb5d1b2330ae5ed9401daab14ea1cbb0`.

### r1-legacy-reporting-final: actual exit 1

```sh
python3 scripts/check-legacy-reporting.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 0.491s. Log SHA-256: `be46ee389414a9ceed4fdb46ef8cbeabbbc05db7fcf712640dfc3c37394bf049`.

### r1-legacy-reporting-final2: actual exit 0

```sh
python3 scripts/check-legacy-reporting.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 1.004s. Log SHA-256: `0b093c0fcd293596f31414c9b4c395f787edd8b553ce3df6ff5f5c65d914433d`.

### r1-sim-exits-final: actual exit 0

```sh
make test-sim-exits
```

Wall execution (excluding wait): 38.487s. Log SHA-256: `eb3f504b0bd6826ea3e7833fbd232fe98f77e85bdc64949f78d1308ed47ddc7b`.

### r1-compatibility-final: actual exit 0

```sh
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 11.17s. Log SHA-256: `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749`.

### r1-legacy-sql-final: actual exit 0

```sh
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 2.113s. Log SHA-256: `a6c504dd82f62eb30db665dae4b257854ceb1715577d438a58982454a9993bbf`.

### r1-depths-final: actual exit 0

```sh
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 141.486s. Log SHA-256: `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453`.

### r1-commit-ids: actual exit 0

```sh
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-12a-main --candidate /home/riels/openmerge-sa-12a
```

Wall execution (excluding wait): 9.404s. Log SHA-256: `4f6700b69aa1e9563669035d7fdb0953dbcb9d6cdf807b3c69623164d9bbcb5d`.

### r1-replay: actual exit 0

```sh
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-12a-main --candidate /home/riels/openmerge-sa-12a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-12a/r1-replay.json
```

Wall execution (excluding wait): 59.134s. Log SHA-256: `b2cf3a0c508c20049587ed7eed06b190cdaf7f6271ad1e248065025e8337c69a`.

### r1-benchmark-final: actual exit 0

```sh
go test -short -count=1 -v ./internal/status -run '^TestLegacyReporting128EntriesAllocationBudget$' -bench '^BenchmarkLegacyReporting128$' -benchtime=7x
```

Wall execution (excluding wait): 0.392s. Log SHA-256: `bfdd35dc742443cae8cda0c82d1270750cd33adfd3f9d2631291585198d27d21`.

### r1-redaction-probe: actual exit 1

```sh
go test -overlay /home/riels/.cache/openmerge-12a/redaction-overlay.json -short -count=1 -v ./internal/status -run '^TestChangeTerminalEntryRetainsRequestSanitizer$'
```

Wall execution (excluding wait): 0.609s. Log SHA-256: `8d532f881e5681869cfa39f67e604bd7ecbed3b1d35a1bba71651785567674b5`.

### r1-redaction-fixed: actual exit 0

```sh
go test -race -short -count=1 ./internal/status ./internal/serve -run 'Test(ChangeTerminalEntryRetainsRequestSanitizer|ChangeShowAndPRAliasEntryMatch|StageACheckSharesLedgerSnapshotAndLogsUnavailablePolicy)'
```

Wall execution (excluding wait): 4.545s. Log SHA-256: `c5b21cb05dc53450879d5c4402932d938b56a7ec1714456910550d5f0c58d725`.

### r1-gate-fixed: actual exit 0

```sh
go test -race -short -count=1 ./internal/status -run 'Test(ReviewerCheck|ChangeTerminalEntryRetainsRequestSanitizer)'
```

Wall execution (excluding wait): 2.253s. Log SHA-256: `b0e48e622b41a84bdead86cd1f48d0ba91dbfb63e163ddeebc13dfc668d946aa`.

### r1-check4-verified: actual exit 0

```sh
make check
```

Wall execution (excluding wait): 408.856s. Log SHA-256: `6fa7634ed162f5ee53ac431d595d5678e320be4cedeaf1e901be0984c9559cce`.

### r1-perf-final-verified: actual exit 0

```sh
python3 scripts/check-reporting-performance.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 0.956s. Log SHA-256: `09d2e6d862e25ef0d830e9b80cd077a572f7d81e260b2e26c671064ad9ded29f`.

### r1-legacy-reporting-final-verified: actual exit 0

```sh
python3 scripts/check-legacy-reporting.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 0.747s. Log SHA-256: `0b093c0fcd293596f31414c9b4c395f787edd8b553ce3df6ff5f5c65d914433d`.

### r1-sim-exits-final-verified: actual exit 0

```sh
make test-sim-exits
```

Wall execution (excluding wait): 38.652s. Log SHA-256: `608c23e44e201d85dab8ef8b4c820e163772b1f39c084d5939bb883b6eede20b`.

### r1-compatibility-final-verified: actual exit 0

```sh
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 11.169s. Log SHA-256: `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749`.

### r1-legacy-sql-final-verified: actual exit 0

```sh
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 2.071s. Log SHA-256: `a6c504dd82f62eb30db665dae4b257854ceb1715577d438a58982454a9993bbf`.

### r1-depths-final-verified: actual exit 0

```sh
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-12a-main
```

Wall execution (excluding wait): 140.82s. Log SHA-256: `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453`.

### r1-commit-ids-verified: actual exit 0

```sh
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-12a-main --candidate /home/riels/openmerge-sa-12a
```

Wall execution (excluding wait): 10.967s. Log SHA-256: `a38a11ee86a4aac613d5f94ed3277e3a7124d9f0aa79e7f6b9e74497ebc56b63`.

### r1-replay-verified: actual exit 0

```sh
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-12a-main --candidate /home/riels/openmerge-sa-12a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-12a/r1-replay-verified.json
```

Wall execution (excluding wait): 56.815s. Log SHA-256: `e12d86f6ade5e36a857da228da0df1d59927d400478e6cc89f6d7298a2a54222`.

### r1-benchmark-final-verified: actual exit 0

```sh
go test -short -count=1 -v ./internal/status -run '^TestLegacyReporting128EntriesAllocationBudget$' -bench '^BenchmarkLegacyReporting128$' -benchtime=7x
```

Wall execution (excluding wait): 0.386s. Log SHA-256: `f7095f9d72ed37539a63b41c6fb28e49ee95fa6c9bca106b3749d447428a3674`.

### Round-1 measurements and final source identity

Astra's original 128-PR/512-refresh sample: main **9.97 ms / 20.4 MB**;
pre-fix PR **1.724456795 s / 3,604,015,040 allocated bytes**. The initial
seven-read differential after the projection fix reports median main **9.482 ms /
20,399,880 bytes** and candidate **10.720 ms / 14,587,784 bytes**: **1.131x**
latency and **0.715x** allocated bytes versus main. Both enforced 2x budgets
pass. The pre-fix review sample and the new medians are different measurement
runs; no controlled speedup claim is inferred from comparing them. These are
local fake-source HTTP reads, excluding database latency, not deployment SLOs.
The final benchmark below reports queue list and PR lookup separately.

Final-source seven-read performance rerun:

```text
median main: 8.277 ms / 20473176 bytes; candidate: 8.943 ms / 14613912 bytes
candidate/main latency=1.080, allocations=0.714
PASS: legacy status latency and allocated bytes are within 2x main
```

```text
    reporting_regression_test.go:43: /v1/queues/owner/repo/main/prs/1: 10.969698ms allocated=14621608 response=90690
    reporting_regression_test.go:43: /v1/queues: 7.09005ms allocated=13133720 response=87700
BenchmarkLegacyReporting128//v1/queues/owner/repo/main/prs/1-16         	       7	   9328484 ns/op	14585518 B/op	   98084 allocs/op
BenchmarkLegacyReporting128//v1/queues-16                               	       7	   6051628 ns/op	13131225 B/op	   90800 allocs/op
```

The three migration-007 indexes took **1.80 ms / 65.76 ms** at 2,600 /
260,000 mixed synthetic rows in the initial timing subtest, which passed even
though unrelated fixture tests failed in that run. The successful `r1-store2`
run measured **1.58 ms / 64.69 ms**. Both confirmed ShareLock remains held and
writers block until commit. Timings exclude lock acquisition waiting and do not
predict production storage, payload or contention costs. Drained/Hold deploy
only, as documented in operating.md.

All required final gates exited **0**: full make check (see final execution duration above, zero lint
issues), simulator exits, compatibility, SQL plus five negative controls,
depth-1/4 differential, four-method commit IDs, strict exported-journal replay,
extended legacy reporting, and the performance regression. Reporting comparison
covers six check/status fixtures, preserving serialized field order and bytes;
five comparator negative controls also pass. The depth differential covers
227,662 short-corpus, 5,615,998 full-corpus and 188 fixture checkpoints.

Validated code commit: `68bbc55474b4151cbaaa1cd19c2b9c04af890088`.
Baseline: `6e2b50b9da5c098ebfd76e0b1b20a5990c2663bb` (`origin/main`).
The subsequent evidence-only commit changes this document alone. Strict replay
uses the exported file pair and hashes listed above, with a clean candidate and
strict state comparison; Gates 2 and 4 pass. All four tested/landed commit-ID
methods (squash, merge, rebase, fast-forward) match main.

Replay artifact: `/home/riels/.cache/openmerge-12a/r1-replay-verified.json`, SHA-256 `e12d86f6ade5e36a857da228da0df1d59927d400478e6cc89f6d7298a2a54222`.

Trial deployment class remains **Hold**. No deployment was performed.
