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
