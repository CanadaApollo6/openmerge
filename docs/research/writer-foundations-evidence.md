# Writer foundations verification

Base: `b7d9b11dde5f78df7d7db637002d1ce82a0e560d` (#178).
Audit v2 compatibility source: #181 at
`f8fbe12b041fd6c1eeaede2de5de0a608b8338f8`, tested in a separate scratch archive
with these provenance fold/codec changes overlaid. No main-checkout or live
service changes, migrations, indexes, forge reads, or writer activation.

## Retention and cost

Original journal evidence is retained forever. Only the derived projection is
compacted. Completed tested/landed pairs and unpinned superseded inventories
become compact generation/CAS and completed-attempt fences. Current/partial
inventories and unfinished companions remain. New work after a retired inventory
requires a fresh generation against the retained predecessor. The resource budget
is 4096 active records, 32 MiB including compact fences, 16 unfinished companions,
and 256 per-companion distinct reference targets in total. Writer preflight stays
strictly below record/byte ceilings, including intermediate bundle states.

`TestSideEvidenceLargeHistoryBoundedAuditAndReloadCost` uses 260,000 ordinary
records plus 2,000 completed attempts (8,000 more records, across distinct changes).
The compact cursor is **394,710 bytes**. Three idle polls each execute one empty
bounded suffix query and **zero reference lookups**. A subsequent real suffix and
idle resume preserve the same bound; original journal count stays 268,000 before
the suffix. A duplicate old generation remains rejected after restore. The test
also checks the existing index's idle plan uses at most 16 shared buffers
(observed: 3 shared buffers, 0.014 ms SQL execution).

`TestSidePendingV9ReloadHasFixedReferenceBudget` retains 16 unfinished attempts:
16 exact-ID queries plus one empty suffix query, observed at approximately 21 ms
on this test host. Query counts and target/buffer bounds are assertions; elapsed
time is diagnostic, not a portable latency guarantee. The aggregate 256-target
budget is independently pinned by `TestRetentionReferenceBudgetPrecedesLookup`.

## Transaction and compatibility proofs

- `TestSideAppendValidatesBeforeAnyInsert`: late bad references, wrong attempt,
  chunk ordering, member/inventory disagreement, ambiguous variants and forbidden
  queue transitions leave the journal and complete cursor bytes unchanged.
- `TestSideAppendCursorUpgradeAtomicAndRollback`: a DB trigger checks v9 in the
  INSERT transaction. An injected late insert failure rolls back earlier inserts
  and the cursor upgrade. Successful retries deduplicate the complete bundle.
- `TestSideAppendStaleAuditCannotUndoUpgrade`: channel sync points put a stale
  audit commit behind the writer lock; its CAS fails and v9 survives restart.
- `TestSideAppendConcurrentGenerationCAS`: exactly one contender with the same
  predecessor wins. `TestSideAppendNoCursorAndDependencyBinding` covers v9 at
  watermark zero and independently supplied namespace binding.
- Retention tests cover 2,000 completed/superseded generations, pinned unfinished
  evidence, every snapshot/tail cut, compact snapshot corruption, duplicate fences,
  stale predecessors, reused generations/attempts and active cap headroom.
- Existing #178 full legacy cursor goldens, main replay vectors and conflicting
  member identity guards remain unchanged and load-bearing. Runtime caller,
  encoder and raw-kind-literal tripwires still pass, with only the unwired append
  adapter allowlisted for side encoding. No serve/queue source is changed.

## Commands

All Go commands use the required wrapper:

```sh
export TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
export OPENMERGE_TEST_DATABASE_URL="$TEST_DATABASE_URL"
go_safe() {
  flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB \
    GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3 \
    PATH=/home/riels/.local/share/mise/installs/go/1.27.3/bin:$PATH go "$@"
}
go_safe test ./internal/store ./internal/journalreader ./internal/provenance/journal ./internal/dependency ./internal/queue ./internal/serve ./internal/status ./internal/audit
go_safe test -race ./internal/store ./internal/serve
go_safe vet ./...
```

Lint uses the same lock, toolchain and resource environment, with `GOMAXPROCS=2`,
running `golangci-lint run`. Changed Go files are formatted with toolchain gofmt;
`git diff --check` is clean. Database tests use isolated schemas and channel sync
points on the designated test Postgres instance.

Scratch logs and mutation copies: `/home/riels/.cache/writer-foundations/`.

## Recorded results and mutations

All eight affected package suites passed. Full race suites passed for store
(229.516 s) and serve (541.811 s); vet passed. The strengthened before-insert
trigger test also passed under `-race`. The separate #181 archive passed:

```sh
go_safe test ./internal/audit -run 'TestProvenance|TestLegacyMainV1' -count=1 -v
```

Ten isolated mutations compiled and failed their intended assertion/tripwire:

- Omit shared validation before INSERT: `TestSideAppendValidatesBeforeAnyInsert`.
- Omit durable cursor upgrade: `TestSideAppendCursorUpgradeAtomicAndRollback`.
- Omit compaction: `TestRetentionThousandsOfSettledAndSupersededGenerations`.
- Omit generation CAS: `TestSideAppendConcurrentGenerationCAS`.
- Omit unfinished-attempt budget: `TestRetentionPendingBoundAndCompactSnapshotCorruption`.
- Omit aggregate reference budget: `TestRetentionReferenceBudgetPrecedesLookup`.
- Omit checkpoint watermark agreement: compact snapshot watermark corruption test.
- Add production `AppendSide` method-value capture: writer guard.
- Add production raw side-kind literal: writer guard.
- Add production side-event encoder outside the adapter: writer guard.

The first validation mutation initially did not compile because it left a loop
variable unused; that run is excluded. The corrected mutation compiled and failed
with `invalid bundle accepted`. `mutations.json` records the counted runs and the
corrected log; `mutations-initial.json` preserves the initial result. All source
mutations were confined to scratch copies. Lint's initial missing exported-helper
comment was fixed without changing behavior, then lint was rerun.

Final lint rerun: **PASS, 0 issues**. Gofmt produced no unformatted paths and
`git diff --check` passed. All requested checks are complete.
