# Hold writer prerequisite evidence — 2026-10-27

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
