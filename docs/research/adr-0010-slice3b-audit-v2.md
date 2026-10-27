# ADR 0010 slice 3b: read-only audit export v2 (Inert)

Reviewed head: `f8fbe12`. Fix round 1 merges main (including PR #178)
without rewriting the reviewed history. Export reads raw immutable `events`
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
scratch grows with the journal index and required closure; working memory stays
bounded by one event and its codec-bounded dependency neighborhood.

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
- `TestProvenanceLinearExport`: 100/300/1,000/4,097 selected records; exactly one
  provenance SQL scan, no prefix queries, and only scope-page query growth.
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

## Fix-round validation recorded

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
