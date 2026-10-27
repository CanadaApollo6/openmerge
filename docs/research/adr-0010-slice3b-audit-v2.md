# ADR 0010 slice 3b: read-only audit export v2 (Inert)

Base: main, `515ba818376f1815db3f33db642c034fe2ffbd6e`.
This implementation is independent of PR #178 and `internal/journalreader`.
Only the audit export adapter imports slice 3a's strict provenance codec/fold.
Live journal replay, snapshot loading and all writers remain unchanged.

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

The export-local adapter streams the provenance prefix through slice 3a's pure
fold. This validates generation fences, complete assembled inventory/declaration
digests, member record digests, exact chunk bindings, latest-generation use,
mapping completeness and tested/landed separation. External decision targets
also match batch, attempt and tested/landed result; layer targets must decode as
known evidence in this queue. Replay recomputes exact closure, so rehashing a
bundle cannot hide missing or extraneous dependencies.

This adapter deliberately uses the existing queue index and SELECTs, with no
migration. Prefix queries scan earlier queue history for each selected new record, and
validation reuses the immutable fold (including its copy costs and bounded
record/byte state). Oversized prefixes fail closed. Closure's on-disk worklist
and source pagination remain bounded. This is an inert correctness slice, not
a new scalable runtime replay reader.

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
- `TestNoRuntimeProvenanceJournalImports`: permits exactly the export adapter;
  live readers and writers cannot import the codec.
- Existing `TestCompleteExportV1`, anchor tamper/overlap, retention, strict
  manifest and publication tests continue to apply unchanged, except the
  unsupported-version negative case now uses version 3.

The 10b verifier keeps the same receipt syntax and replay prerequisite for
both versions. It compares the version as part of bundle identity and refuses
unsupported versions. V1 receipts and existing v1 bundles remain valid.

## Validation recorded

All Go commands used the requested heavy-work lock, Go 1.27.3, `GOFLAGS=-p=2`
and `GOMEMLIMIT=4GiB`. Database tests used only localhost:54329 with both test
URL variables set.

- `go test ./internal/audit/... ./cmd/openmerge/... ./internal/provenance/journal/...`:
  pass (audit 140.518s, CLI 96.553s, journal 4.425s).
- `go vet ./...`: pass.
- `golangci-lint run`: pass, zero issues.
- `gofmt` on all touched Go files and `git diff --check`: pass.
- `python3 scripts/derive-audit-v2.py`: all 20 v2 reference files reproduced.
- `node scripts/verify-audit-v1-node.cjs`: 100 frozen v1 files identical.
- Main's original Python anchor verifier, extracted at the pinned base,
  refuses the v2 reference bundle with exit 2.
- Main's original Go binary, built from `git archive` into scratch, refuses
  the same v2 manifest with exit 1 and publishes no output. This checks the
  actual old reader, without modifying or entering the project's main checkout.

Scratch command logs and the original main-generated bundle were retained at
`/home/riels/.cache/adr0010-slice3b/`.
