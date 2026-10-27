# Hold writer prerequisite evidence — 2026-10-27

## PR #188 fix round 3

Starting head: `9200ef7baceac011f6a847d4cd747240d8cb064c`. Fetched the exact
requested branch/main refspec and created a separate local fix branch. Writers
remain **Hold**; no production caller, event field, migration, index, Makefile,
CI, Dockerfile or test flag changes.

- Recovery accepts only its landed companion and inventory chunks consumed by
  that companion's exact member generations. Each reconstruction matches the
  historical tested request and inventory digest after rebinding the generation
  and exact predecessor. Chunk assembly, completeness and predecessor CAS still
  pass through the shared reader. Unrelated inventories, dependency records,
  extra companions, mismatched generations and changed historical inputs refuse
  before insertion. Canonical reconstruction retries remain valid.
- Admission and recovery share the same dynamic retirement-reserve check.
  Recovery can bypass the configurable percentage but cannot consume the expanded
  bytes or entries needed to retire remaining active inventories and attempts.
  Settlement, net projection reduction and the other hard caps still apply.
- Short mode checks the maximum 4,128-record / 8,224-reference configuration
  arithmetically using one encoded maximum-width attempt, then applies/reloads
  4 attempts with 8 members each. The reserve test checks cap−1, cap and cap+1
  with 500 active generations and 32 attempt charges, then retires/reloads 32
  real escaped-key inventories. Record-count tests retain exact admission and
  strict-reader limits plus a 32-record real checkpoint. Explicit work counts
  prevent silently reducing these fixtures further. The original maximum-scale
  runs remain outside `-short`.

Both Astra reproducers failed before the fix (`baseline-repros.log`). The first
retained unrelated evidence while shrinking **401,061 → 398,564 bytes** above
**335,544-byte** high water. It now refuses atomically, after which the genuine
companion can still recover. An additional reconstruction test refuses a bundle
with an extra unrelated inventory. The second reproducer reconstructs only three
members, shrinks **1,909,678 → 1,896,494 bytes**, and would exceed the hard fence cap
by exactly **2,366 bytes** on queue retirement. Recovery now refuses for the
retirement reserve. It starts from a strict checkpoint in both modes; the full
run also verifies that the unsafe recovered projection itself reloads before
showing the subsequent retirement overrun.

Round-3 commands, overlays and logs live in `/home/riels/.cache/pr188-fix-r3/`.
`run_checks.py`, `run_supplementary.py` and `mutations.py` preserve exact commands;
all Go invocations use the required lock, Go 1.27.3, `GOFLAGS=-p=2`,
`GOMEMLIMIT=4GiB`, toolchain PATH and both designated PostgreSQL test URLs.

### Round-3 validation

All six affected packages passed `go test -race -short -count=1 -timeout=900s`.
Combined wall time was **304.598 s**. The journal package now finishes well below
180 seconds, including the new strict-checkpoint retirement reproducer.

| Package | CI-mode time |
| --- | ---: |
| `internal/journalreader` | 20.379 s |
| `internal/provenance/journal` | 69.995 s |
| `internal/status` | 10.436 s |
| `internal/store` | 196.382 s |
| `internal/audit` | 101.741 s |
| `cmd/openmerge` | 99.713 s |

The three optimized tests took **1.770 s** (maximum configuration), **1.540 s**
(active-inventory reserve) and **4.390 s** (record cap), versus Astra's earlier
150.790 / 89.960 / 49.600 s measurements. These are per-test wall times under
concurrent package execution, not isolated performance benchmarks.

The complete store run rejected the existing **223 field mutations** (41 valid
wire changes reaching historical binding and 182 strict-wire refusals), then
accepted identical duplicates. The audit suite retains #181 v1/v2 compatibility.

The full non-short maximum batch/depth, record ceiling, configured-reserve and
reconstructed-recovery tests also passed together in **40.233 s** without race
instrumentation. Astra's three-member overlay repeated all **446 additional
field mutations** against the middle and final members (82 valid-wire historical
binding refusals and 364 strict-wire refusals), then recovered identical
duplicates in all four variants; package time **102.604 s**. Combined with the
committed matrix this is **669 rejected field mutations**.

The legacy status/health overlay reproduced **21 outputs / 10,491 decoded bytes**
exactly; artifact SHA-256 remains
`4b976f6eccd68c2a440bda16547d7a62e6aa2e097b87e2fe6265b2447a850fdd`.
`go vet ./...` and `golangci-lint run` passed; lint reports `0 issues.`.
Whole-repository `gofmt -l .` returned no files and `git diff --check` passed.
The command-driver elapsed times for vet/lint/full boundaries include waiting
on the shared lock; package timings above exclude that wait.

All **18 source mutants compiled and failed behavioral assertions** in **102.478 s**.
The 12 prior guards were rerun, including active inventory reserve and recovery
high-water/reduction checks. Six added mutants remove the recovery-bundle
restriction, historical inventory digest binding, recovery retirement reserve,
maximum reference budget, record-count hard cap, or strict snapshot record-count
check. There were no survivors, build/setup failures or panic-only kills. The
short-mode replacements therefore retain the affected mutation coverage.

Commands after the common lock/environment prefix:

```sh
go test -json -race -short -count=1 -timeout=900s ./internal/store ./internal/journalreader ./internal/provenance/journal ./internal/status ./internal/audit ./cmd/openmerge
go test -count=1 -timeout=900s ./internal/provenance/journal -run '^(TestMaximumBatchAtMaximumSpeculationDepthFitsReferenceBudget|TestConfiguredReserveAccountsForActiveInventory|TestRetainedRecordCap|TestReconstructedRecoveryPreservesRetirementReserve)$'
go test -overlay /home/riels/.cache/pr188-fix-r3/three-member-overlay.json -race -short -count=1 -timeout=900s -v ./internal/store -run '^TestReviewR3ThreeMemberFields$'
go test -overlay /home/riels/.cache/pr188-fix-r3/status-overlay.json -race -short -count=1 -timeout=900s ./internal/status -run '^TestReviewR2LegacyOutputBytes$'
go vet ./...
golangci-lint run
```

The status overlay sets `REVIEW_STATUS_OUT` to the round-3 scratch artifact.
`python3 /home/riels/.cache/pr188-fix-r3/mutations.py` applies the same Go prefix
and CI flags to each isolated source overlay. Formatting was checked with
`/home/riels/.local/share/mise/installs/go/1.27.3/bin/gofmt -l .`; whitespace with
`git diff --check`. No mutation changes working-tree implementation files.

## PR #188 fix round 2

Starting head: `1157460a00e38db67faf97243951e89bbe2cca72`. Work stays on the
existing PR branch under Hold; production writers remain unwired.

- Recovery resolves the anchor and every member-layer reference, checking every
  referenced decision against the complete accepted historical decision. Both
  newly built and already-complete companions take this path. Identical duplicate
  decisions remain valid. Candidate observations resolve through the recovery
  bundle overlay, so reconstructing an inventory in that transaction still works.
- Admission reserves the expanded fence bytes and entry count needed to retire
  every current inventory and attempt. Generation IDs are immutable original
  chunk-zero IDs; attempt charges use the maximum uint64 ID. The 1–99% setting
  remains supported, but a configuration/inventory combination that could exceed
  the hard fence caps is refused even below its percentage threshold.
- Recovery uses the hard caps and a strict reduction in retained projection bytes
  instead of admission high water, and must settle the recovered attempt. A bundle
  that adds enough unrelated evidence to increase the projection is refused.
- The operating runbook covers indefinitely pinned attempts, the 32-attempt limit,
  refusal of new tested landings, immutable-input checks and maintainer escalation.
  There is no force-forget path.

`TestRecoverLandedDuplicateMemberDecision` carries Astra's singleton/two-member
reproducer. `TestRecoverLandedValidatesCompletedSecondaryDecision` covers the
already-complete branch. `TestRecoverLandedDecisionFieldMutations` walks every
wire field in the approval and generated-remedy fixtures: **223 mutations** are
rejected (**41 structurally valid**, **182 strict-wire refusals**), then the
identical secondary duplicate successfully recovers in both variants.

`TestConfiguredReserveAccountsForActiveInventory` uses Astra's strict, reloadable
checkpoint with **18,536 retired fences**, **132,873,478 expanded bytes** and
**500 active inventories**. Admission at 99% now refuses it. The test confirms
that bypassing admission would grow it beyond the 128 MiB cap on retirement,
then admits a smaller 100-inventory checkpoint at the same setting and reloads
it after retirement. `TestTerminalReserveAtActiveRecordLimit` retires and reloads
**4,255 inventories** at once. Recovery tests drain `Awaiting` above 1% high water,
reject growing bundles and retain the hard caps, and verify an inventory built
and referenced in the recovery bundle.

All Go commands use the lock/toolchain/environment prefix documented below and
both designated PostgreSQL test URL variables. Round-2 logs and the reproducible
Go-overlay mutation runner are in `/home/riels/.cache/pr188-fix-r2/`:
`ci-packages.log`, `ci-store-final.log`, `focused-store-final.log`,
`focused-remedy.log`, `mutations.py`, `mutations.json`, `mutation-*.log`,
`legacy-status.log`, `status-head.json`, `vet.log`, `lint.log`, and `gofmt.log`.

### Round-2 CI-mode results

The six affected suites passed `go test -race -short -count=1 -timeout=15m`;
total wall time was **444.222 s**. The final store rerun includes the additional
in-bundle reconstructed-inventory reference regression. Its time replaces the
first store run's 213.809 s below. All packages finish within six minutes,
comfortably below the 15-minute timeout; retention exceeds the five-minute aim.

| Package | CI-mode time |
| --- | ---: |
| `internal/store` (final rerun) | 170.219 s |
| `internal/journalreader` | 26.227 s |
| `internal/provenance/journal` | 349.852 s |
| `internal/status` | 11.075 s |
| `internal/audit` | 104.988 s |
| `cmd/openmerge` | 102.039 s |

The recovery-above-high-water probe reduced retained bytes from **400,869** to
**396,304**, both above the configured **335,544-byte** high water, and removed
`Awaiting`. It also rejected the same recovery bundled with unrelated evidence
that would increase retained bytes.

A separate Go-overlay run of Astra's legacy status/health probe reproduced all
**21 serialized outputs / 10,491 decoded bytes** exactly against the saved
`3bef3b3` baseline. The serialized comparison artifact's SHA-256 remains
`4b976f6eccd68c2a440bda16547d7a62e6aa2e097b87e2fe6265b2447a850fdd`.
The full audit suite covers #181 v1/v2 export compatibility, and the reader suite
retains the unchanged production-writer/import guards.

Commands following the common lock/environment prefix:

```sh
go test -race -short -count=1 -timeout=15m ./internal/store ./internal/journalreader ./internal/provenance/journal ./internal/status ./internal/audit ./cmd/openmerge
go test -race -short -count=1 -timeout=15m ./internal/store
go test -overlay /home/riels/.cache/pr188-fix-r2/status-overlay.json -race -short -count=1 -timeout=15m ./internal/status -run '^TestReviewR2LegacyOutputBytes$'
```

All **12 isolated source mutants** compiled and failed their intended behavioral
assertions under the same CI flags. Go overlays kept the implementation files
unchanged. The original eight guards remain effective, and the four additional
mutants remove secondary-decision validation, active-inventory retirement reserve,
recovery's high-water exemption, or its net-reduction requirement.

| Mutation | Failing test |
| --- | --- |
| `fence-byte-charge` | `TestRetireOwnedLandingsBranchSettlesAttempt` |
| `decision-binding` | `TestRecoveryRejectsContradictoryBindings/wrong-tested-decision` |
| `terminal-composition` | `TestRecoveryRejectsContradictoryBindings/wrong-landed-result` |
| `recovery-membership` | `TestRecoverCompanionRequiresExactBatchCoverage/incomplete-historical-tested` |
| `start-membership` | `TestStartCompanionRequiresExactBatchCoverage/missing` |
| `per-record-preflight` | `TestSideBundlePacksFencesOnceUnderLock` |
| `high-water` | `TestSideHighWaterReservesTerminalFenceHeadroom` |
| `pending-companion` | `TestRecoverySurvivesTerminalQueueEvents/close` |
| `anchor-only` | `TestRecoverLandedDuplicateMemberDecision/multi-altered` |
| `inventory-reserve` | `TestConfiguredReserveAccountsForActiveInventory` |
| `recovery-high-water` | `TestRecoverLandedAboveHighWaterDrainsPending` |
| `recovery-growth` | `TestRecoveryPreflightKeepsHardCapsAndReduction/(unchanged\|growth)` |

`go vet ./...` passed in **5.718 s**; `golangci-lint run` passed in **6.941 s**
with `0 issues.`. Whole-repository `gofmt -l .` returned no files, and
`git diff --check` passed. The mutation runner completed in **58.583 s**.

```sh
python3 /home/riels/.cache/pr188-fix-r2/mutations.py
# Prefix the next two commands with the common lock/environment prefix:
go vet ./...
golangci-lint run
/home/riels/.local/share/mise/installs/go/1.27.3/bin/gofmt -l .
git diff --check
```

The first focused run exposed a canceled subtest context in the mutation-table
cleanup. Cleanup now uses the parent test context; both later focused runs and
the final complete suites pass. No production source mutant was left applied.

## PR #188 fix round 1

Starting head: `3bef3b3f40a881d662056c440d06b465246ca03d`. Fetched the requested
branch and main refs, created the local fix branch, and merged main at `8a26d32`.

Recovery could settle contradictory or incomplete landed evidence, and side append
repacked lifetime fences for every candidate while holding the queue advisory.
This change fixes both while keeping ADR 0010/0011 writers **Hold**. No production
writer is enabled; the ordinary start still supplies a literal nil builder.

- Bind recovery to the historical batch's tested SHA, accepted decision, exact
  membership/revision/head coverage and terminal member's `NewBaseSHA` composition.
  Intermediate per-member results remain valid. Enforce coverage at the tested
  start seam as well, with Astra's singleton and two-member regression probes.
- Check capacity once per bundle and reuse the exact packed table in the final
  cursor encode. The lock-held work test includes multiple completed pairs:
  3/10/30 records each require one candidate pack (130/136/156 entry visits).
- Reserve capacity with configurable side-admission high water (80% by default),
  including expanded fence bytes and entries. Queue status JSON exposes fence
  count, projected bytes, cap and high-water bytes. Document alerting at 70%.
- Persist bounded accepted-`Landed` recovery markers in a separate strict v9
  `Awaiting` checkpoint shape. Close/dequeue/head-change retirement preserves
  the exact pending evidence until the landed companion completes, across restart.
  Earlier checkpoint shapes remain readable; deploy this reader before writing
  recovery markers. No existing event fields change.
- Return typed `LandedAlreadyCompleteError` when another dedupe key already holds
  the valid companion. Add the missing `RetireOwned` fence-byte accounting assertion.
- Update the root README and both codec READMEs: transaction factoring, missing
  companion recovery and capacity visibility are resolved under Hold.

**Still BLOCKING before enabling writers on a high-volume queue:** a structural
fence bound (for example, monotonic journal-position-derived identities so a
watermark can replace per-ID fences), or an explicit lifetime/rollover policy.
Compaction only delays the hard cliff to about 281k lifetime landings for the
reviewed identity distribution; admission stops earlier. Neither long-term policy
nor structural bound is implemented here. Production integration, authority/start
fencing, namespace recovery, owner opt-in and reader-first rollout remain gated.

The original atomic rollback, lock order (including FK `KEY SHARE` wait), lossless
fence packing, legacy byte identity and #181 compatibility guarantees remain.
No migrations, indexes, Makefile, CI, Dockerfile or test flags change. Main's
retention acceptance test update is merged into the branch.

### Validation (fix round 1)

The affected packages pass `go test -race -short -count=1 -timeout=15m` under the
required flock, Go 1.27.3, `GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`, and both designated
PostgreSQL test URL variables. Total wall time: **343.797 s**; every package is
under five minutes.

| Package | CI-mode time |
| --- | ---: |
| `internal/store` | 122.211 s |
| `internal/journalreader` | 20.593 s |
| `internal/provenance/journal` | 225.600 s |
| `internal/status` | 10.252 s |
| `internal/audit` | 101.014 s |
| `cmd/openmerge` | 101.714 s |

`go vet ./...` exits 0; `golangci-lint run` exits 0 with `0 issues.`. All 16 changed
Go files pass gofmt; `git diff --check` passes. The separate verbose work-count
run passes in 7.736 s. Full store/journalreader suites include legacy cursor byte
goldens and the Hold guards; full audit covers #181 v1 and v2 compatibility.

All **eight** new isolated mutants compiled and failed the intended behavioral
assertions under `-race -short`; sources were restored after each run:

| Mutation | Failing test |
| --- | --- |
| `fence-byte-charge` | `TestRetireOwnedLandingsBranchSettlesAttempt` |
| `decision-binding` | `TestRecoveryRejectsContradictoryBindings/wrong-tested-decision` |
| `terminal-composition` | `TestRecoveryRejectsContradictoryBindings/wrong-landed-result` |
| `recovery-membership` | `TestRecoverCompanionRequiresExactBatchCoverage/incomplete-historical-tested` |
| `start-membership` | `TestStartCompanionRequiresExactBatchCoverage/missing` |
| `per-record-preflight` | `TestSideBundlePacksFencesOnceUnderLock` |
| `high-water` | `TestSideHighWaterReservesTerminalFenceHeadroom` |
| `pending-companion` | `TestRecoverySurvivesTerminalQueueEvents/close` |

Restoring per-record preflight increases the 10-record bundle from **136** to
**402** entry visits, and the 30-record bundle from **156** to **1,470**. The final
implementation requires one candidate pack per bundle: **130/136/156** visits for
**3/10/30** records. Prefix checkpoint loading remains a fixed lifetime-table cost.

See `docs/research/writer-prerequisites-evidence.md` for commands, bounds and logs.

### Reproduction commands

Every Go command uses this prefix:

```sh
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3 PATH=/home/riels/.local/share/mise/installs/go/1.27.3/bin:$PATH go ...
```

Both `TEST_DATABASE_URL` and `OPENMERGE_TEST_DATABASE_URL` are set to
`postgres://postgres@localhost:54329/postgres?sslmode=disable`.
Commands following the common environment/lock prefix:

```sh
go test -race -short -count=1 -timeout=15m ./internal/store ./internal/journalreader ./internal/provenance/journal ./internal/status ./internal/audit ./cmd/openmerge
go test -race -short -count=1 -timeout=15m -v ./internal/store -run '^TestSideBundlePacksFencesOnceUnderLock$'
go vet ./...
golangci-lint run
gofmt -l internal/journalreader/snapshot.go internal/journalreader/terminal.go internal/provenance/journal/retention.go internal/provenance/journal/snapshot.go internal/provenance/journal/state.go internal/provenance/journal/terminal.go internal/provenance/journal/writer_prerequisites_test.go internal/status/status.go internal/store/journal.go internal/store/landing_start.go internal/store/side_append.go internal/store/store.go internal/status/provenance_capacity_test.go internal/store/recovery_bindings_test.go internal/store/recovery_membership_test.go internal/store/side_capacity_test.go
git diff --check
```

The mutation runner uses that same prefix and CI flags for each focused test.
It was run as `python /home/riels/.cache/pr188-fix/mutations.py` from the isolated
`/home/riels/.cache/pr188-fix/mutation-src/` copy. All eight mutants compiled and
were killed; none was applied to the implementation worktree. Baseline and final
logs, `mutations.json`, `mutation-*.log`, `work-counts.log`, `verification.json`,
`ci-final.log`, `vet.log`, `lint.log`, and `gofmt.log` remain in
`/home/riels/.cache/pr188-fix/`.
The earlier broad pass caught forbidden codec imports; the wrappers and gauge
type were moved into the existing allowed side-append boundary, and the clean
final run passes both unchanged import/writer guards.

No live database, systemd service, production writer, queue or merge was touched.

## Original implementation evidence (3bef3b3)

Base: fetched `origin/main` at `7e7942f` (#183), using the required explicit
refspec. Branch: `feat/writer-prerequisites`.

This is a **Hold** prerequisite change. No production writer is enabled. The
ordinary landing start supplies a literal nil companion builder; AST guards
reject production append/recovery callers and companion-builder captures.

- **Shared transaction:** `appendSideTx` contains the existing validation,
  retention preflight, cursor upgrade, insertion and checkpoint logic.
  `startLandingWithSide` passes actual inserted decision IDs to its pure builder.
  `TestLandingDecisionCompanionSharedTransaction` covers success, builder failure
  after decision insertion, side-validation failure, SQL failure, cursor/cache
  rollback and an active approval-read fence. `TestLandingSideAppendLockOrder`
  synchronizes both concurrent orders and observes a real FK row-lock wait while
  the other transaction waits on the queue advisory. Lock analysis includes
  actions, observations, queues `KEY SHARE` and notification `FOR UPDATE`.
- **Landed recovery:** `RecoverLanded` verifies an immutable, accepted `Landed`
  outcome against its preceding queue state, binds batch/attempt/decision/final
  result and derives a deterministic dedupe key. Retries compare complete
  canonical bundles. The crash/restart test verifies one append, retirement,
  unchanged retries and refusal of stale outcomes, wrong kinds and changed
  bundles. The caller must reconstruct the actual landed mapping from immutable
  evidence; the queue outcome alone cannot invent a commit mapping.
- **Fence compaction:** arbitrary string IDs have no monotonic allocation
  contract, so a watermark or range would be unsound. The new strict v9
  `FenceTable` shape preserves every exact key and original anchor using sorted
  prefix coding, lossless hex packing and zlib. Both earlier v9 shapes remain
  readable; legacy-only cursors remain byte-identical. Geometric measurements
  bound replay work; writer preflight and encoding measure the whole table.
  Decoding bounds binary/key expansion and allocation, including long valid
  JSON-escaped identifiers and alternate compatible zlib streams.
- **#186:** the focused `RetireOwned` landings-branch test asserts settlement,
  reloads the checkpoint, creates a fresh inventory and rejects attempt reuse.

## Fence decision and measured envelope

The full provenance fold of **200,001 completed tested/landed pairs** passes:
**23,855,490 checkpoint bytes (22.75 MiB)** versus **55,544,728 raw fence bytes**,
leaving **9.25 MiB** below the 32 MiB projection cap. It visits **1,523,608 fence
entries** for capacity measurements, below the asserted `8*N` work budget.
Short mode folds 400 pairs, asserts the same work bound and exercises packed
checkpoints. Reload, canonical re-encoding, suffix replay and generation/attempt
reuse rejection pass. Arbitrary unbounded identities cannot fit finite memory;
this is lossless compaction with enforced capacity, not a watermark or an
unlimited-lifetime promise. Expanded metadata is capped at 128 MiB / 2,097,152
fences consistently on write and read.

## Final CI-mode package timings

| Package | Elapsed |
| --- | ---: |
| `store` | 98.576 s |
| `journalreader` | 20.546 s |
| `provenance/journal` | 228.516 s |
| `dependency` | 7.705 s |
| `queue` | 145.546 s |
| `serve` | 294.952 s |
| `status` | 10.232 s |
| `audit` | 101.867 s |

Every affected package passed `-race -short -count=1 -timeout=15m` and completed
in under five minutes locally. No test flags, Makefile, CI, Dockerfile, event
fields, migrations or indexes changed.

## Mutation proofs

Each mutant was applied alone in an isolated scratch archive, run under
`-race -short -count=1 -timeout=2m` with its focused behavioral test, and restored.
All seven built successfully and failed assertions; compiler failures do not
count as kills. The working tree was never mutated for these experiments.

| Mutation | Failing behavioral test |
| --- | --- |
| Delete `RetireOwned` landings-branch settlement assignment | `TestRetireOwnedLandingsBranchSettlesAttempt` |
| Delete retired-generation rejection | `TestFenceTableLifetimeLandings` (reuse supplies the correct CAS anchor) |
| Delete settled-attempt rejection | `TestFenceTableLifetimeLandings` (fresh valid inventory) |
| Skip canonical dedupe comparison | `TestRecoverLandedCompanionAfterCrash` |
| Remove transaction-local audit cursor upgrade | `TestLandingDecisionCompanionSharedTransaction` |
| Ignore transaction-local append error | `TestLandingDecisionCompanionSharedTransaction` |
| Allow a non-nil production companion builder | `TestCompanionStartHoldGuard` |

## Reproduction and results

All Go commands used this exact prefix (and the same prefix for gofmt/lint):

```sh
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3 PATH=/home/riels/.local/share/mise/installs/go/1.27.3/bin:$PATH go ...
```

Database integration tests set both `TEST_DATABASE_URL` and
`OPENMERGE_TEST_DATABASE_URL` to
`postgres://postgres@localhost:54329/postgres?sslmode=disable`. Tests create
isolated schemas and use existing test fixtures/fakes; no live database,
services or forge APIs were used by the implementation/tests.

Commands after the common prefix:

```sh
go test -race -short -count=1 -timeout=15m ./internal/store ./internal/journalreader ./internal/provenance/journal ./internal/dependency ./internal/queue ./internal/serve ./internal/status ./internal/audit
go test -count=1 -timeout=10m ./internal/provenance/journal -run '^TestFenceTableLifetimeLandings$' -v
go test -race -short -count=1 -timeout=15m ./internal/provenance/journal ./internal/store ./internal/journalreader -run 'TestFenceTable|TestRetireOwnedLandings|TestLandingDecisionCompanion|TestLandingSideAppend|TestRecoverLanded|TestCompanionStartHoldGuard|TestNoProductionNewEvent' -v
go vet ./...
golangci-lint run
gofmt -w internal/store/side_append.go internal/store/landing_start.go internal/store/locks.go internal/store/writer_prerequisites_test.go internal/provenance/journal/fence_table.go internal/provenance/journal/retention.go internal/provenance/journal/snapshot.go internal/provenance/journal/state.go internal/provenance/journal/writer_prerequisites_test.go internal/journalreader/writers_test.go
```

Results: all pass; full scale test **163.727 s**; final focused packages
provenance/journal **7.567 s**, store **3.506 s**, journalreader **1.486 s**;
vet exits 0; lint exits 0 with `0 issues.`; gofmt exits 0.
The focused rerun includes the final expansion/long-escaped-key guards.
Full store/journalreader runs include the complete legacy cursor goldens;
full audit includes #181 v1 byte/receipt and provenance v2 compatibility tests.


Logs and the mutation runner are retained in the requested scratch directory:
`/home/riels/.cache/writer-prereqs/` (`ci-final.log`, `fence-full-final.log`,
`focused-final.log`, `mutations.log`, individual mutation logs, `vet.log`,
`lint.log`). No production writer, authority opt-in, queue or merge was enabled.
