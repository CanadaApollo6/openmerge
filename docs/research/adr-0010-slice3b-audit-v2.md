# ADR 0010 slice 3b: read-only audit export v2 (Inert)

Fix round 2 starts from reviewed head `93ce2e9` and merges current main
(including PRs #178, #182 and #184) without rewriting the reviewed history. Export reads raw immutable `events`
through its own read-only snapshot; it never loads the composed reader's retained
side projection or depends on its pruning/compaction policy. No writers change.

## Version contract

`Export` and the CLI choose the version after transitive dependency selection.
They emit byte-identical v1 when neither scope nor closure contains
`change_provenance_observed` or `landing_provenance_recorded`. A legacy decision
with an out-of-range provenance companion therefore selects v2. `--version 2`
forces v2, including for empty and legacy-only ranges; `--version 1` fails when
provenance is present. `--manifest` preserves its saved version and rejects
`--version`. V1 replay deliberately retains the frozen v1 resolver, including
its opaque handling of then-unknown sources. It does not reinterpret old anchors
as certification of provenance policy.

V2 uses the same five files, canonical JSON rules, size/depth bounds, ordering,
LF framing, chain construction and manifest self-hash as v1. Its manifest has
`version: 2`; the manifest shape is unchanged. That discriminator selects the
complete format contract, including the evidence vocabulary and selectors.
Existing strict v1 readers reject version 2 before replay. There is no fallback
or downgrade. New readers accept only versions 1 and 2. V2 adds exactly one
evidence kind, `provenance`, for the two slice 3a event types. Other source types
retain v1 kinds. Journal payloads are canonicalized as JSONB, never rewritten
or extended. Declaration inputs, outcomes, policy source, layer-local inputs,
truth values, digests and tested/landed mappings remain in those typed payloads.

## Provenance closure

All lookups occur within the same repeatable-read, read-only transaction and
saved queue watermark. No Git, network, forge or checkpoint reads occur.

- An observation retains every exact-request generation chunk and its nonzero
  previous-observation fence. Chunk identities are
  `["inventory", change, generation, decimal-index]`. Self references identify
  the complete generation consistently from any selected chunk.
- A landing companion retains its exact decision ID, all member observation
  IDs, every layer reference, and (for landed stage) the tested companion for
  the same attempt. ID identities are `[relation, decimal-ID]`; the tested
  companion uses `[attempt, "tested"]`.
- A legacy decision retains each present tested/landed companion whose
  `decision_id` names it, even outside the requested range. Its identities are
  `["decision", decimal-decision-ID, stage]`. Legacy decisions without these
  companions gain no extra absence edge and keep their v1 bytes.
- New references use the existing complete owner/source fields and
  `relied_on`/`recorded` semantics. Required evidence never becomes
  `not_recorded`. Missing, duplicate, foreign-queue, malformed, future-fenced
  or inconsistent targets fail publication.

The export-local adapter builds a temporary disk index with one streaming
SELECT per queue/watermark. It strictly decodes provenance-shaped rows before
indexing their identities. Malformed companions, including JSONB `170.0` decision
IDs, fail export in auto, v1 and v2 modes before absence can be inferred. The
index serves chunk, decision and attempt lookups without repeated unindexed SQL.
Legacy queues pay one additional scan, independent of decision count; they gain
no absence edges and keep their v1 bytes.

After closure, a single `(queue,id)` pass folds only selected provenance records.
`journal.ClosureFold` loads each record's bounded group/attempt neighborhood from
private disk scratch and uses the unchanged pure `journal.Apply` validation.
It does not retain or clone a whole prefix, and the live projection's 4,096-record
and aggregate byte limits do not cap historical or selected export size. Disk
scratch grows with the journal index and required closure up to the configured
budget; working memory stays bounded by one event and its codec-bounded dependency
neighborhood.

The provenance index and fold share one per-export/replay budget: by default
256 MiB of accounted bytes and 65,536 files/directories. Each entry is charged at
least 4 KiB, rounded up to a 4 KiB unit, before creation, append or replacement.
`--scratch-max-bytes` and `--scratch-max-files` accept positive overrides;
`Reader.ScratchLimits` provides the same controls to Go callers (zero fields use
defaults). Replacements account for their previous size without a growing path
map. Exhaustion returns terminal `*audit.ScratchLimitError` wrapping
`audit.ErrScratchLimit`; staging is removed and no bundle is published. The CLI
reports which flags can be raised after provisioning disk.

Scratch lives alongside the target bundle on its filesystem. Provision the
budget **plus filesystem metadata/allocation overhead, selection/merge spools,
and the final bundle**. The limits cover provenance indexing and fold state;
legacy sorting/closure spools and final output still scale with selected data.
A narrow time range may require indexing all provenance history up to its saved
watermark, so input range size alone does not predict the required scratch disk.

This preserves generation fences, complete assembled inventory/declaration
digests, exact member/chunk bindings, mapping completeness and tested/landed
separation. Fixed-width raw chunk-zero ID files additionally check actual prior
and latest generation fences with bounded binary searches, even when an
intervening generation is outside closure or absent from a pruned projection.
External decisions must match member identity, batch, attempt and stage result;
layer targets must decode as known evidence in this queue. Replay recomputes
exact closure, so rehashing cannot hide missing or extraneous dependencies.

All database access uses existing indexes and SELECTs, with no migration or
runtime reader change. Projection retention remains the composed reader's
responsibility; export validity depends on immutable journal retention instead.

## ADR 0011 dependency evidence decision

Defer typed ADR 0011 dependency evidence to its audit rollout slice (row 5),
which explicitly requires scoped cross-repository export and authorization.
Its audit section does not assign that work to ADR 0010 v2. V2 here has no
`dependency` evidence kind or cross-repository selector. In-range dependency
source events retain the frozen opaque source behavior; a provenance layer
that requires an unsupported dependency target fails rather than certifying
incomplete evidence. A later version must define those kinds/selectors and
permission rules explicitly; do not silently expand this v2 vocabulary.

## Proofs

- `TestProvenanceV2IndependentVectors`: four independently derived complete
  bundles cover observation, tested companion, legacy decision and landed
  companion roots, cross-range closure, default v2, exact replay, explicit v1
  refusal and v2 anchor acceptance. `scripts/derive-audit-v2.py` derives bytes
  and hashes without invoking Go, from literal 3a payloads and the existing
  independently frozen v1 relationships.
- `TestProvenanceMissingAndInvalidEvidence`: required chunk/decision/layer and
  tested-stage absence, wrong kind/queue, digest/build/attempt mismatch,
  duplicate chunks, future references, unknown fields and payload-kind mismatch
  fail with no published bundle.
- `TestV2ReplayRejectsStrippedProvenance`: fully rehashed missing evidence and
  manifest downgrade fail semantic replay.
- `TestLegacyMainV1BytesAndReceipt`: the fresh main export and its receipt in
  `internal/audit/testdata/main-v1/` stay byte-identical under default export
  and replay; explicit v2 replays and anchors, but cannot reuse the v1 receipt.
  Captured before implementation using main's exporter against SELECT-only
  scratch Postgres. Existing v1 goldens are unchanged.
- `TestAuditSelectOnlyCommand`: CLI explicit v2 and manifest replay run with a
  SELECT-only role. Usage tests reject version overrides during manifest replay
  and unsupported versions.
- `TestNoRuntimeProvenanceJournalImports`: permits the existing composed
  reader/store and the read-only export adapter. This fix adds no live wiring
  or journal writers.
- Existing `TestCompleteExportV1`, anchor tamper/overlap, retention, strict
  manifest and publication tests continue to apply unchanged, except the
  unsupported-version negative case now uses version 3.

The 10b verifier keeps the same receipt syntax and replay prerequisite for
both versions. It compares the version as part of bundle identity and refuses
unsupported versions. V1 receipts and existing v1 bundles remain valid.

## Fix-round regression proofs

- `TestProvenanceMalformedCompanionID`: Astra's out-of-range `170.0` repro,
  scaled exponent, string, null and fractional IDs, in auto/v1/v2 modes.
- `TestProvenanceLinearExport`: 32/96 selected records; exactly one provenance
  SQL scan, no prefix queries, and only scope-page query growth.
- `TestProvenanceLargeClosure`: 4,097 selected records through the production
  disk-backed audit closure validator; exactly two state files per inventory
  plus one shared directory, independent of live projection retention.
- `TestClosureFoldLinearWork`: exactly two scratch reads and two writes per
  independent observation for 100/300/1,000/5,000 records; no timing assertions.
- `TestProvenanceHistoricalRetentionLimit`: one selected observation from 5,000
  immutable historical records exports and replays successfully.
- `TestProvenanceLegacyCost`: 10/30 decisions on a 300,000-event legacy queue;
  automatic export has v1 replay's semantic SQL lookup counts plus one provenance
  scan. Timing is logged only, never asserted.
- `TestProvenancePreviousGenerationClosure`: generation 2 names nonzero prior
  observation 161; both generations' chunks are retained across the time range.
  The schema has no retained side projection, proving it is unnecessary.
- `TestProvenanceRawGenerationFences`: rejects omitted previous fences and stale
  members even when the intervening raw generation is outside closure.
- Isolated decision attempt and member-identity mismatch tests keep the rest of
  the evidence internally consistent, so each named binding guard is necessary.
- `TestClosureFoldMatchesRetainedFold`: valid and invalid fold neighborhoods
  agree with the original retained fold; storage failures poison the fold.
- The Python anchor contract includes positive provenance v2 overlap and full
  coverage. Explicit v1 refusal has a dedicated actionable CLI diagnostic.

Validation and mutation command logs for fix round 1 are retained under
`/home/riels/.cache/pr181-fix/`. The byte oracle runs in scratch; no frozen
reference files are regenerated in the repository.

## Fix round 1 validation recorded

All Go commands used the requested `flock`/Go 1.27.3/`GOFLAGS=-p=2`/`GOMEMLIMIT=4GiB` wrapper and both localhost:54329 test URL variables. Python entrypoints used `-I`.

- `go test ./internal/audit/... ./cmd/openmerge/... ./internal/provenance/... -count=1 -v` — PASS (audit 173.696s; CLI 96.632s; provenance 0.011s; journal 5.722s).
- `go vet ./...` — PASS.
- `golangci-lint run` — PASS, 0 issues.
- `gofmt` on changed Go files and `git diff --check` — PASS.
- `python3 -I scripts/test_verify_audit_anchor.py` — PASS, including positive v2 overlap/full coverage.
- `scripts/derive-audit-v2.py`, loaded with `python3 -I` and its output root redirected to scratch — all 20 frozen v2 files reproduce byte-for-byte; repository fixtures untouched.
- `node scripts/verify-audit-v1-node.cjs` — all 100 frozen v1 files identical.
- Seven compiling Go behavioral mutants killed: ignore malformed companions, always-true member match, ignore decision attempt, omit previous-generation closure, skip closure fold, ignore raw generation fence, repeat fold-store reads. The corrected raw-fence mutant is included; a compiler-invalid draft is excluded.
- Python anchor sweep: 18 non-equivalent mutants killed (including reject-all-v2), two documented equivalent cases.

Measured scale (timings are evidence, never CI assertions):

- 100 / 300 / 1,000 / 4,097 selected observations: 0.189 / 0.573 / 1.811 / 7.446 seconds; 9 / 15 / 37 / 134 SQL queries, with exactly one provenance scan each.
- Pure fold: exactly 2 reads + 2 writes per independent observation through 5,000 records (10,000 reads + 10,000 writes at 5,000).
- 5,000 historical observations / 1 selected: export + replay 3.041 seconds. Unrelated bad assembled digests are not folded.
- 300,000-event legacy queue: 10 decisions export 2.218s versus unchanged v1 replay 2.227s; 30 decisions export 6.755s versus v1 replay 6.053s. Semantic lookup counts match v1, with one additional queue-wide provenance scan instead of three additional scans per decision.


## Fix round 2 regression coverage

- `TestReviewR2InterveningGeneration` imports Astra's raw-journal 161/162/163
  repro. The control fold rejects 163 with `ErrBinding`; automatic and explicit
  v2 export both reject it. Nonzero chunks must belong to the latest raw chunk-zero
  ID before their own ID, even when that intervening generation is out of range.
- The read-only codec guard allows only `internal/audit/provenance.go` to import
  the provenance decoder and name its two kinds. Its event/snapshot encoder bans
  still apply. Export matches decoded sealed types to the journal kind column.
- `TestProvenanceDuplicateMarker` checks two and three identical lookup keys:
  selected ambiguities fail closed; unrelated ambiguities permit export/replay.
- `TestProvenanceScratchLimits` exercises byte/file exhaustion in index and fold
  storage for export and replay, including typed errors and staging cleanup.
  `TestScratchBudgetReservesBeforeWrite` verifies failed reservations leave bytes
  intact and replacements preserve correct byte/inode accounting.
- Small end-to-end exports and the 4,097-record closure run use valid complete
  empty inventories. Separate tests retain nonempty multi-chunk/declaration
  coverage. Query/storage work counts assert linearity, with timings logged only
  as evidence. A full 4,097-record export was also measured before decomposing the
  test: 5.046s normally and 55.488s under `-race -short`, both with 134 queries and
  one provenance scan. The latter was too close to the fixture's one-minute
  snapshot deadline, so the large automated case now targets the production
  closure validator where the projection's 4,096-record ceiling was removed.
  This avoids repeatedly serializing/hashing the full large bundle in CI.
- Main's #184 deterministic cancellation regression is included by the merge.

The Makefile's CI test command is `go test -race -short -count=1 -timeout=15m
./...`; `.github/workflows/ci.yml` does not set an explicit job timeout. Neither
file nor any test flags were changed. Fix round 2 command logs and oracle output
are retained under `/home/riels/.cache/pr181-fix-r2/`.


## Fix round 2 validation recorded

- `go test ./internal/audit/... ./cmd/openmerge/... ./internal/provenance/... ./internal/journalreader/... -count=1 -v` — PASS (audit 163.397s; CLI 96.705s; provenance 0.014s; journal 5.806s; journalreader 0.207s).
- `go test -race -short -count=1 -timeout=15m -v ./internal/audit/...` — PASS, **97.303s** total locally with CI's flags. The small export growth tests took 1.89s; the 4,097-record closure test took 16.25s and used 8,195 budgeted entries / 33,566,720 accounted bytes. Both are well inside the 15-minute package limit; the large closure case has no database snapshot deadline.
- `go vet ./...` — PASS.
- `golangci-lint run` — PASS, 0 issues (the initial guard-condition style finding was corrected).
- `gofmt` on changed Go files, repository-wide `gofmt -l .`, and `git diff --check` — PASS.
- `python3 -I scripts/test_verify_audit_anchor.py` — PASS.
- `python3 -I /home/riels/.cache/pr181-fix-r2/check-oracle.py` — PASS, all 20 frozen v2 files independently reproduced in scratch.
- `node scripts/verify-audit-v1-node.cjs` — PASS, all 100 frozen v1 files identical.

These timings were measured locally with the specified resource wrapper, not on
a GitHub runner. The CI-mode run is comfortably inside the unchanged 15-minute
per-package timeout. Final logs are `required-tests-final.log`,
`audit-ci-scale-final.log`, `vet-final.log` and `lint-final.log` in the scratch
directory above. The earlier complete 4,097-record export is retained in
`required-tests-large-export.log` and `audit-ci-scale-large-export.log`.
