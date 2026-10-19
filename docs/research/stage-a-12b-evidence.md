# Stage A 12b acceptance evidence

Trial deployment class: **Hold**, depending on held Stage A work. No deployment,
production opt-in or real sandbox action was performed. Organization-sandbox
acceptance remains **PENDING maintainer-provided resources** in the
[ready-to-fill artifact](stage-a-sandbox-evidence.md). The
[exit report](stage-a-acceptance.md) reports the remaining limits explicitly.

## Source and scope

Fresh worktree `/home/riels/openmerge-sa-12b`, branch `stage-a/12b-acceptance`,
from `origin/main` **d45e709afaf57b4df2ecebebe5ce9ec75c27dfac** (11b #78;
12a #79 and outside-pushes #53 are included). Comparison checkout:
`/home/riels/openmerge-sa-12b-main`, detached at the same baseline. Explicit
`git fetch origin refs/heads/main:refs/remotes/origin/main`, both worktree
creations, and baseline SHA verification exited **0**. Only this branch is to
be pushed. No automatic/all-ref fetch was used.

Production changes are limited to four carry-overs: decisions reject non-reviewer
`--audience` just as show does; terminal `LedgerEntry.Change` retains the request
sanitizer; the single check retains a bounded brief locator/digest after safety
warnings when truncating rows; and login-only grant failure has a login-specific
diagnostic instead of printing identical expected/observed IDs. Legacy reporting
bytes are checked against main. No configuration, event/action, landing gate,
Makefile, CI, test-flag or dogfood configuration change is included.

`TestStageAAcceptanceCLI` drives the production CLI handlers, HTTP projections,
worker, planner, executor and PostgreSQL store with a fake forge. It uses an
agent account's queue comment, scripted exact-head human reviews and a human's
`@openmerge approve <brief-digest>` comment. Every confirmed landing reads back
original immutable decision, eligibility and settlement event IDs, revision,
attempt and tested/landed SHAs. Receipt read-back is compared to the **posted**
digest, independent of transient display-policy contention. A repeated settled
decision read must be byte-identical. A legacy sibling is audited for unchanged
config digest, absent Stage A observations/pins and absent Stage A trailers;
the preexisting `OpenMerge-Batch` trailer is preserved.

Short mode covers depth 1's five scenarios:

- `TestStageAAcceptanceCLI/depth-1/low-risk-none`
- `TestStageAAcceptanceCLI/depth-1/exact-head-human`
- `TestStageAAcceptanceCLI/depth-1/receipt-intent`
- `TestStageAAcceptanceCLI/depth-1/owned-non-policy`
- `TestStageAAcceptanceCLI/depth-1/uncovered-non-policy`

Full mode repeats all five at depths 1 and 4. `none` is a docs-matching rule
with a count-bearing default (the published loader forbids `default: none`).
The owned/uncovered cases use independent `paths: ['**']` count and
`touches: ['**']` code-owner rules; the owned case waits until the exact-head count review is recorded
and ownership is still unsatisfied before adding the owner review. The uncovered
case must retain the count requirement and acquire no owner obligation.
The synthetic CI producer succeeds only on built queue refs. Fake immutable
Git objects supply the reporting cache; no real forge APIs are used.

## Execution constraints and failed setup attempts

All heavy jobs wait until no other agent's `go test`, `*.test` or replay is
running. One job runs at a time. The runner inherits:

```sh
GOFLAGS=-p=2
GOMEMLIMIT=4GiB # 6GiB for journal replay
GOTMPDIR=/home/riels/.cache/openmerge-12b/gotmp
GOCACHE=/home/riels/.cache/go-build
OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
```

The shared test Postgres hosts isolated random test schemas, created/dropped by
test cleanup. No `initdb`, package installation, live database, status API,
systemd operation or production `openmerge/*` ref was used. Ref-shaped names in
test data refer to the in-memory fake or temporary fixture repositories only.
No organization/repository/App/install/review/comment was created externally.

Runner and raw logs live at `/home/riels/.cache/openmerge-12b/run.py`,
`results.jsonl` and `logs/`. Commands below are exact validation invocations;
every recorded exit is a real process return code. Routine source inspections
are not gates. An initial inspection in the original checkout exited **1**
because that older checkout lacked the 12a evidence file; it did not run tests.

Runner setup first matched its own command line and waited. That **own waiting
process** (PID 1577700) was stopped with SIGTERM before it started a test;
its enclosing command exited **143**. No foreign process was killed. The next
runner setup exited **1** while parsing the `ps` header as a PID; it started no
test. The runner now excludes itself and requests headerless process output.

Focused attempts are retained below. The failures exposed fixture mistakes,
not waived production gates: required CI predicates were initially absent;
wrong-head review evidence is intentionally filtered from the current stance
view; notification delivery is needed to refresh fake mutable review facts;
one edit referenced the server before declaration; `default: none` is forbidden;
legacy commits already carry `OpenMerge-Batch`; and a busy brief read cannot be
used in place of the original posted receipt digest. These were corrected
without changing configuration or landing semantics.

<!-- validation-results -->

### focused: actual exit 1

```sh
go test -short -count=1 ./cmd/openmerge ./internal/status ./internal/forge/github -run "Test(StageAAcceptanceCLI|ChangeShowAndDecisions|LedgerEntryRetains|ReviewerCheckWarnings|WriteGrantInvalid)"
```

Wall execution: **3.577s**. Log SHA-256: `9ac4de184c2ca4895902038aef249b6fb9fc90596c05964867a57a5a62563f21`.

### focused-2: actual exit 1

```sh
go test -short -count=1 ./cmd/openmerge ./internal/status ./internal/forge/github -run "Test(StageAAcceptanceCLI|ChangeShowAndDecisions|LedgerEntryRetains|ReviewerCheckWarnings|WriteGrantInvalid)"
```

Wall execution: **121.313s**. Log SHA-256: `7ed6679e824067ca55f83873bf840042817228e8de452f98089ee8ed9ea73609`.

### focused-3: actual exit 1

```sh
go test -v -short -count=1 ./cmd/openmerge ./internal/status ./internal/forge/github -run "Test(StageAAcceptanceCLI|ChangeShowAndDecisions|LedgerEntryRetains|ReviewerCheckWarnings|WriteGrantInvalid)"
```

Wall execution: **0.456s**. Log SHA-256: `ad7912d857121db3d5ddbd3d1f592e90dc728b4cc25576c9a876df9188208a7c`.

### focused-4: actual exit 1

```sh
go test -v -short -count=1 ./cmd/openmerge ./internal/status ./internal/forge/github -run "Test(StageAAcceptanceCLI|ChangeShowAndDecisions|LedgerEntryRetains|ReviewerCheckWarnings|WriteGrantInvalid)"
```

Wall execution: **52.155s**. Log SHA-256: `f778377960e812e872655aaa4fbeccce2525e5238f5a6b7b00d2fdf4ea3c4bfc`.

### focused-5: actual exit 1

```sh
go test -v -short -count=1 ./cmd/openmerge ./internal/status ./internal/forge/github -run "Test(StageAAcceptanceCLI|ChangeShowAndDecisions|LedgerEntryRetains|ReviewerCheckWarnings|WriteGrantInvalid)"
```

Wall execution: **32.55s**. Log SHA-256: `25db2f6df07470e28ceb2edecc13076f89606f1060bc728c6bfcff120f9c8669`.

### check: actual exit 2

```sh
make check
```

Wall execution: **450.253s**. Log SHA-256: `31f98883af257088a096bbd3078db7b2e390996ec063c8dee309cbf14de1277e`.

### focused-6: actual exit 0

```sh
go test -race -v -short -count=1 ./cmd/openmerge ./internal/status ./internal/forge/github -run "Test(StageAAcceptanceCLI|ChangeShowAndDecisions|LedgerEntryRetains|ReviewerCheck|WriteGrantInvalid)"
```

Wall execution: **65.954s**. Log SHA-256: `4a36d7e476d003b2eda866e5bbfd68cd926e1f580a6898e8a9b9aeba863a63ac`.

### check-final: actual exit 0

```sh
make check
```

Wall execution: **445.448s**. Log SHA-256: `03f7ace4f33abd51e878e26e14d4fb610862a5e1c5e5f2c555d2521988113ded`.

### sim-exits: actual exit 0

```sh
make test-sim-exits
```

Wall execution: **38.718s**. Log SHA-256: `9162227f2311a8c97d3ff82a59c3492cbb5a4e47377af8bae46e67597c3a09b0`.

### compatibility: actual exit 0

```sh
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-12b-main
```

Wall execution: **12.518s**. Log SHA-256: `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749`.

### legacy-sql: actual exit 0

```sh
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-12b-main
```

Wall execution: **4.659s**. Log SHA-256: `2e28b07110e99f19df96151da21d0a7479da030169e5e6b0b66c9d5961221646`.

### legacy-depths: actual exit 0

```sh
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-12b-main
```

Wall execution: **143.396s**. Log SHA-256: `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453`.

### legacy-reporting: actual exit 0

```sh
python3 scripts/check-legacy-reporting.py /home/riels/openmerge-sa-12b-main
```

Wall execution: **1.184s**. Log SHA-256: `f7ea0120d2797f0849daf8651b225136ca8d252b6ffd39758c2b5a908222b28a`.

### acceptance-full: actual exit 0

```sh
go test -v -count=1 ./cmd/openmerge -run "^TestStageAAcceptanceCLI$"
```

Wall execution: **87.379s**. Log SHA-256: `279b4ee060ecf43e4c4dc6c1e07c1dd2208a57e8b69f0d5d98c79fe182c086ee`.

### commit-ids: actual exit 0

```sh
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-12b-main --candidate /home/riels/openmerge-sa-12b
```

Wall execution: **10.123s**. Log SHA-256: `654c5b79f64434a16d132455fa9245ac8bd7c6576b508b529c5d27c3fd002260`.

### replay-main: actual exit 0

```sh
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-12b-main --candidate /home/riels/openmerge-sa-12b --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-12b/replay-main.json
```

Wall execution: **65.016s**. Log SHA-256: `01c692f2729f3332d554ec46c4c684e6ffd65aad34980ad22da935072c07f799`.

## Final result and source identity

Every required final automated gate exited **0**. `make check` passed in
**445.448s**, with **0 lint issues** and serve package time **309.311s** under
`-race -short -count=1 -timeout=15m`. The CLI package passed in **63.017s**;
the acceptance scenarios add no serve-package tests. The earlier failed check
exited **2**, solely on the owned-file fixture's invalid expectation that a
recorded brief supplies fresh count eligibility; serve passed that attempt in
**286.683s**. The corrected test observes the exact-head count-review source
and the unmet owner obligation, then verifies independent count/owner sources
in the final decision. These are single-run durations, not performance claims.

Full acceptance exited **0** in **87.379s** (test package **86.958s**), with all
five scenarios passing at each of depths 1 and 4. Short-mode focused race
acceptance and all carry-over/check regressions exited **0**. Warnings remain
first even with adversarial HTML expansion; the complete bounded brief URL
and digest survive row truncation. An overlong URL gets an explicit CLI locator
fallback instead of a silently discarded or broken URL line.

Compatibility retained 17 legacy exits, 1,055 depth-1 retry/default-digest
records and all 84 wire fixtures byte-for-byte. The SQL differential matched
all seven statements including transaction/lock order and detected all five
negative controls. Depth 1/4 matched **227,662** short-corpus, **5,615,998**
full-corpus and **188** fixture checkpoints with no nil-only exceptions.
All six legacy check texts and preexisting serialized status fields/order
remain identical to main. Squash, merge, rebase and fast-forward tested/landed
commit IDs match main.

The commit-ID harness also proved its own controls: identical-source self-test
exit **0** (expected 0), mutant self-test exit **1** (expected 1; squash/rebase
mismatch detected), and dirty-source self-test exit **2** (expected 2). Those
expected nonzero child exits are controls, not waived failures of the final
main-versus-candidate comparison, which exited **0**. Temporary fixture commits
stay in the harness cache, outside this branch.

Strict journal replay exited **0** with **Gate 2 PASS**, **Gate 4 PASS** and
**2,159/2,159** identical action, strict state and snapshot checkpoints; repeated
candidate streams are deterministic. The replay's 22-test preflight exited 0.
It used only this existing immutable exported pair, never the live DB or API:

- `events-20261018T051000Z.jsonl`, SHA-256
  `9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`.
- `queues-20261018T051000Z.jsonl`, SHA-256
  `418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`.

Executable code was committed as
**e6da552fb63c1aa8ad7967d46997da90c501f68a**. Commit-ID and replay comparisons
used that clean commit against main
**d45e709afaf57b4df2ecebebe5ce9ec75c27dfac**. The final evidence-only commit
updates this record; executable source is unchanged. All branch commits use
the requested Riel author and exact Signed-off-by/Claude co-author trailers.

Final integrity commands both exited **0**:

```sh
git diff --check origin/main...HEAD
git diff --exit-code origin/main -- Makefile .github/workflows .openmerge.yml .openmerge.yaml openmerge.yml openmerge.yaml
```

No real sandbox acceptance is claimed. The sandbox artifact remains PENDING;
conflict grace is excluded, depth-1 cancellation is not claimed, and automatic
old-worker detection remains unimplemented because leases lack capability IDs.
Outside pushes #53 are included. Trial class remains **Hold**.

## CI ordering regression follow-up (2026-10-19)

CI run 39224861756 failed at head 470cab5: receipt-intent had already
landed with accepted receipt 101, but the test required a current brief
digest/binding in its polling predicate. The dump omitted brief warnings.

The acceptance test now observes accepted receipts and immutable decisions
after both main and legacy fixture landings, with workers quiesced to remove
policy-reader mutex contention. It compares the posted digest with the receipt
and decision, and checks source/receipt ID, actor and revision binding. The
low-risk-none case no longer waits for a pre-landing brief. Exact-head-human
and uncovered-non-policy proceed directly to durable settlement after review;
receipt and owned preconditions remain blocked by evidence the test withholds.
All five scenarios share these assertions at both depths. Short mode remains
five depth-1 cases. No Makefile/CI changes.

There is no exposed fake clock or step entry point for this production
serve.Run + HTTP + CLI fixture. Existing deterministic queue/simulator steps
do not drive that server. Necessary asynchronous startup/blocked/landing waits
remain bounded and context-aware, using a ticker instead of fixed sleeps;
settled evidence reads are immediate assertions.

### Settled reporting investigation

This follow-up changes **no product code**. A landed change does not inherently
lose its brief digest: `RecordedBrief` reconstructs historical facts from the
journal, and a settled `change decisions` response retains the decision's
brief digest and receipt source. `BriefPolicy` intentionally uses `TryLock`;
when a worker holds the policy mutex, reporting emits `policy_busy` and the
current brief digest/binding is `not recorded`. That is unavailable current
reconstruction, not lost receipt/decision evidence. The original failure dump
did not include warnings, so it cannot identify which policy-unavailability
response occurred on that specific read.

The initial focused race probe passed all five depth-1 scenarios (exit **0**,
**62.405s**). After stopping workers, every landed brief had a 64-character
digest, including the receipt brief's original posted digest
`0229b683b80765e144d1baa4940b3fbba7e9a19ca4f6c036748c816ca9777e96`.
The final assertions require historical digest reconstruction and the accepted
receipt's `current` binding after quiescence, plus matching decision/receipt
digest, receipt ID, reviewer ID and revision. Reused JSON destinations are
zeroed before each read so omitted fields cannot survive from an earlier poll.

The regression is the test's reliance on an unblocked live brief projection
while workers continue running, rather than the recorded acceptance and
settlement facts. Landing and policy-read contention change scheduling under
CI load. Both depths now deliberately read successful approval evidence after
settlement; blocked preconditions are only observed while the test still
withholds required evidence. No production reporting fallback was added.

### Follow-up gates: actual process exits

#### ci-race-fix-probe: exit 0

```sh
GOMAXPROCS=2 go test -race -v -short -count=1 ./cmd/openmerge -run "^TestStageAAcceptanceCLI$"
```

Wall execution: **62.405s**. Log SHA-256: `15a4d8c1c0fbf05864b997088c625acda400b26db031d7ce45e9818746313b9e`.

#### ci-race-fix-repeat: exit 0

```sh
GOMAXPROCS=2 go test -race -v -count=20 -timeout=60m ./cmd/openmerge -run "^TestStageAAcceptanceCLI$"
```

Wall execution: **2447.384s**. Log SHA-256: `8a3ec89dee828377783230ae29c8cc74accada463ad6725295fa832756c8fdb5`.

The full repeated race gate passed **20/20 iterations**, **200/200 scenario executions**: each of the five scenarios passed **20/20 at depth 1** and **20/20 at depth 4**. There were **zero failures**. No `-short` flag was used for this stress gate; short coverage is also exercised by `make check`.

#### ci-race-fix-check: exit 0

```sh
GOMAXPROCS=2 make check
```

Wall execution: **408.913s**. Log SHA-256: `7747cd7a93f056c9235613f6565ca445819dd0154dfedfce0fea6052fe26e617`.

#### ci-race-fix-legacy-reporting: exit 0

```sh
GOMAXPROCS=2 python3 scripts/check-legacy-reporting.py /home/riels/openmerge-sa-12b-main
```

Wall execution: **0.584s**. Log SHA-256: `f7ea0120d2797f0849daf8651b225136ca8d252b6ffd39758c2b5a908222b28a`.

`make check` reported **0 lint issues**; the CLI package (including short
acceptance) passed in **62.300s**, queue in **282.939s**, and serve in
**236.322s**. These are local observations with `GOMAXPROCS=2`, not predictions
for CI's runner or a performance claim. No queue/serve tests were added.
The reporting differential again matched all six legacy check texts and
preexisting serialized status fields/order against main
`d45e709afaf57b4df2ecebebe5ce9ec75c27dfac`; both child probes exited **0**.

All follow-up validation processes exited **0**, with no failed or terminated
validation attempts. The initial inspection's overbroad home-directory search
reported a permission error; it completed with exit 0 and changed no files.
Some subsequent exploratory `rg`/`sed` reads named nonexistent source paths
and returned exit 2; these were inspections, not gates. No foreign process
was stopped. All heavy jobs waited for the other reviewers' jobs to drain;
one validation job ran at a time with the constrained environment above.

Final follow-up integrity checks exited **0**:

```sh
git diff --check
git diff --exit-code 470cab5 -- Makefile .github/workflows .openmerge.yml .openmerge.yaml openmerge.yml openmerge.yaml
```

The follow-up diff contains only `cmd/openmerge/stage_a_acceptance_test.go`
and this evidence record. The PR's four preexisting product carry-overs are
unchanged. No live OpenMerge database/API, systemd unit, production ref, real
forge test/simulator call or sandbox action was used. Trial class remains
**Hold** and organization-sandbox acceptance remains **PENDING**.

## Resumed CI-fix validation and permissions documentation (2026-10-19)

This round validates committed acceptance fix
`626e90ec42f0757c2cf2a7021ea46450c8c3585f` and corrects the round-1
permission-documentation finding. `ValidateTeamPolicyPermissions` has one
non-test caller, `DiagnoseInstallationPermissions`, reached through the explicit
`openmerge setup permissions` command. Runtime team policy resolution calls
`ResolveTeam` and `TeamMembers`; definite identity/membership unavailability
creates a durable affected-queue `ConfigSource` block, while transient or
incomplete reads retry without a durable configuration block. The operating
guide now says so. The sandbox and acceptance reports did not repeat the false
caller claim; both now explicitly distinguish the setup diagnostic from runtime
reads. No automatic installation-grant check is promised.

The transient-wait audit covers all five cases at both depths through their
shared fixture. `low-risk-none` may settle on its first read and waits only for
agent identity and durable landings. `exact-head-human` and
`uncovered-non-policy` withhold exact-head review while observing their initial
blocked brief, then wait for durable landings. `owned-non-policy` additionally
withholds the owner review until the exact-head count review is recorded and
the independent owner obligation remains unmet. `receipt-intent` withholds its
receipt until native review ingestion is visible. All successful receipt,
digest and decision assertions run after both fixture landings and worker stop.
There are no fixed sleeps in `stage_a_acceptance_test.go`; the wait helper uses
a context deadline and 20ms ticker. The 10ms server poll interval and 1s/2s
fixture review timestamps are not sleeps.

**Reporting answer: by design, not a reporting bug.** `BriefPolicy` deliberately
uses nonblocking registry/worker locks. A busy local reconstruction yields a
`policy_busy` warning and an unavailable current digest/binding (`not recorded`),
including on a landed change. Landing does not erase historical evidence.
`RecordedBrief` reconstructs that evidence once the pinned local policy can be
read; immutable decisions retain their original digest and receipt sources.
The original CI dump omitted warnings, so its particular unavailability cause
cannot be established from that dump alone. No product reporting change was
made; the stress test requires a reconstructed digest after worker quiescence
in every case and matching posted receipt/decision digests in receipt-intent.

Fresh raw validation logs for this resumed round are under
`/home/riels/.cache/openmerge-80-resume/`. Heavy jobs run serially after host
process checks, with `GOMAXPROCS=2 GOFLAGS=-p=2 GOMEMLIMIT=4GiB`, `GOTMPDIR`
and `GOCACHE` under `~/.cache`, and the supplied isolated-schema test Postgres.

### Fresh validation of 626e90e

```sh
GOMAXPROCS=2 go test -race -json -count=20 -timeout=60m ./cmd/openmerge -run '^TestStageAAcceptanceCLI$'
```

Actual process exit: **0**. Go package duration: **2450.227s**.
`repeat.jsonl` SHA-256:
`acd163b2ffd6cae7de296eb931acaf594bdcd7810ffb4a7fd45a8b70dee2cb4f`.
The committed test source was unchanged throughout this run. No `-short` flag
was used. There were **20/20 full iterations, 200/200 scenario passes, zero
failures**:

| Case | Depth 1 | Depth 4 |
| --- | --- | --- |
| low-risk-none | 20/20 | 20/20 |
| exact-head-human | 20/20 | 20/20 |
| receipt-intent | 20/20 | 20/20 |
| owned-non-policy | 20/20 | 20/20 |
| uncovered-non-policy | 20/20 | 20/20 |

The small progress-summary helper initially failed to sort the final package's
null test name alongside string subtest names (Python `TypeError`); filtering
that package-only key fixed the helper. This was log summarization after the
successful test exit, not a test failure. An early optional log inspection also
returned exit 2 because `make check` had not yet created its log. Neither altered
source or validation outcomes; all actual gate exit statuses are reported here.

### Resumed final gates

`make check` exited **0** in **388.138s**.
Log SHA-256: `b674773262456f66c1093f920b56416eac8b274f5d4a3e29bb0b15de241331ca`.

`python3 scripts/check-legacy-reporting.py /home/riels/openmerge-sa-12b-main` exited **0** in **0.59s**.
Log SHA-256: `f7ea0120d2797f0849daf8651b225136ca8d252b6ffd39758c2b5a908222b28a`.

`make check` reported **0 lint issues**. Package durations were
`cmd/openmerge` **61.717s**, `internal/serve` **217.717s**, and
`internal/queue` **283.217s**. These are local gate durations, not CI timings.
The legacy differential against main `d45e709afaf57b4df2ecebebe5ce9ec75c27dfac`
passed all six check texts and preexisting serialized status fields/order; both
child probes exited **0**. The enclosing gate runner also exited **0**.

Final whitespace and protected-path integrity checks exited **0**. This resumed
round changes only the operating guide and three research docs. No test or
product code, Makefile, workflow, or configuration was changed. No foreign
process was stopped; no live database/status API, systemd unit, production ref,
real forge call from code/tests, PR comment, or sandbox action was used.
New-head CI will be tracked separately on PR #80. Trial class remains **Hold**;
real organization-sandbox acceptance remains **PENDING**.
