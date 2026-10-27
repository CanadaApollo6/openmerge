# Hold writer prerequisite evidence — 2026-10-27

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
