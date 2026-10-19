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
compiling later packages. Source edits stopped before the final validation
sequence; these failures are retained rather than presented as passing gates.

Three **own waiting drivers** were stopped with SIGTERM to put fixture generation
and dependent CLI validation in one serial sequence. No test subprocess had
started under them and no foreign process or running heavy job was killed.
Waiting time is excluded from each command's measured run duration.

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

