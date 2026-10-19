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
