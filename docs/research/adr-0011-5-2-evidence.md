# ADR 0011 slice 5-2 evidence

Slice 5-2 completes slice 5 of
[ADR 0011](../adr/0011-cross-repository-dependent-changes.md#rollout-and-implementation-slices).
It adds the scoped audit export, distinct self-acknowledgement status and export,
the nonblocking multi-writer warning, the wait-age flag and additional
pagination. The class is Hold. Activation stays confined to the test seam. No
event kind or field is added. Inactive output and every existing golden are
unchanged. No real GitHub calls are made.

## Item-to-test map

| Item | Tests |
| --- | --- |
| Scoped export of decisions, compositions, invalidations and observations | `TestDependencyExportAudiences` (complete audience: every dependency record as a strict journal record with ID and time), `TestDependencyExportCompleteEvidenceIsStrict` |
| Scope to the reader's authorized sources | `TestDependencyExportAudiences`: no audience, one denied reader, a foreign-repository grant and a missing source binding all fall back to the redacted projection |
| ADR 0008 model and strict decoders | `TestDependencyExportStrictRoundTrip`: atomic publish, no overwrite, canonical bytes, digests, chain, positions, unknown and missing fields, private kinds, journal metadata on redacted records, false completeness claims |
| Versioning against older verifiers | `TestDependencyExportVersionBoundary` |
| Distinct self-acknowledgement status | `TestStatusSelfAcknowledgementIsDistinct`, `TestStatusIndependentAcknowledgementUnchanged`, metamorphic self-acknowledgement cases (`requireSelfAckView`) |
| Distinct self-acknowledgement export entry | `TestDependencyExportReflectAllowlist` (`self_acknowledgement` kind), `TestDependencyExportStrictRoundTrip` |
| Nonblocking multi-writer warning on the hold/status | `TestStatusSelfAcknowledgementIsDistinct` (one versus two writers, revocation), metamorphic cases, revert N5 |
| Wait-age flag | `TestDependencyWaitAgeAuthorizedOnly`, metamorphic status with the flag enabled, revert N1 |
| Additional pagination | `TestPageDependencyRecords`, `TestBoundDetails`, metamorphic cursor pages at limits 1, 2 and 100, revert N2b |
| No rendering-time fetches | Builders take only supplied journals, recorded audiences, source bindings and a caller clock. `TestDependencyPrivacyGoldensNoRenderingFetch` is unchanged |
| Unchanged precedence | `TestDependencyWaitAgeAuthorizedOnly` (code, text and blocking unchanged); existing precedence tests and goldens |
| Reflection over new view and export types | `TestDependencyExportReflectAllowlist`, `TestDependencyItemReflectAllowlist` |
| Read-only boundary | `TestNoProductionNewEventOrSnapshotWriters` passes without allowlist changes |

## Privacy design

**Allowlist projection.** Every unauthorized-audience value is built as a new
value from allowlisted public inputs.

- **Redacted export.** `redactedEntry` constructs four evidence types from
  consumer-repository facts:
  - policy: enabled, single-maintainer, governing commit, digest and writer count;
  - declaration hold: change, revision, held, unknown editor, editors and redeclaration;
  - independent acknowledgement;
  - self-acknowledgement.

  Any other kind returns nothing, including kinds added later. Positions are
  ordinals assigned after projection.
- **Self-acknowledgement.** `dependencyItem` adds the journal event reference
  only while the queue's journal positions are public.
- **Wait-age.** `waitAge` returns a flag only for a non-generic, blocking view.

**What a redacted export withholds, and why.** Private source state decides
whether and when these records exist:

- observations and diagnostics;
- compositions, decisions and invalidations;
- journal IDs and recording times.

The existence and timing of those records are the side channel, so they are
withheld entirely rather than replaced with placeholders. A complete export
requires every source declared in the queue's journal to be bound and fully
authorized for the recorded audience. Partial authorization degrades to the
redacted export, like 5-1's queue-wide redaction.

**Side channels considered:**

| Channel | Guard | Test |
| --- | --- | --- |
| Wait-age starting at private eligibility | Authorized, blocking views only; the generic view never has it | metamorphic status, N1 |
| Pagination totals and cursors | Pages and totals count export ordinals over the projected list | metamorphic pages, N2b, `TestPageDependencyRecords` |
| Export record counts and order | Private-dependent kinds withheld entirely; order is journal order of public records | metamorphic export bytes and private tail, N2, N6 |
| Warning presence | Derived from the policy writer count, only on a hold or completed self-acknowledgement; never in the generic view | metamorphic status, N5 |
| Timestamps | No times on redacted records; the verifier rejects journal metadata on them | metamorphic export with world-dependent delays, N3, N3b |
| Journal references | Self-acknowledgement `event_id` omitted while positions are withheld | metamorphic status, N4, `TestDependencyItemReflectAllowlist` |

**Generator.** `statusfixture.Metamorphic` gains a self-acknowledgement
dimension, and private reads now delay later records:

- **Opt-in.** The queue opts into single-maintainer self-acknowledgement with
  two writers, so the warning applies.
- **Self-acknowledgement.** After the public transition and the private-dependent
  prefix, observed change 7 records public history: an edge, its removal by
  author 11 (a loosening hold) and 11's self-acknowledgement.
- **Delays.** Every private read now delays later records by a world-dependent
  amount, in every case. Public records therefore differ across worlds in
  journal ID and recording time.

Existing 5-1 views stay byte-identical under these delays in status, CLI and
serve. That is independent confirmation that 5-1 exposes no timestamp channel.

The comparison rule is unchanged: worlds are compared only within confirmed
public facts (`merged:` and the target's public `left:` reasons). Unauthorized
readers must get byte-identical output for:

- the export manifest and records;
- every cursor page at limits 1, 2 and 100, including records, next cursors and totals;
- the wait-age-enabled API views, including PR 7's self-acknowledgement and warning.

A private-only tail must not change the export. Strict verification runs after
the comparison, so the differential reports a leak first.

- **Short:** three self-acknowledgement cases are appended, giving 18. The serve
  subset is still the first five.
- **Full:** self-acknowledgement variants are added for target 2, giving 896
  cases. 788 pass and 108 skip by name:

| Skip reason | 5-1 cases | 5-2 self-acknowledgement cases |
| --- | ---: | ---: |
| A private read discarded every batch | 60 | 20 |
| Private `checks-failed` exit | 8 | 3 |
| Sole world in a `merged:` group | 11 | 6 |

The 79 skips of the 672 5-1 cases are unchanged.

## Versioning

The scoped manifest is `format: openmerge.dependency.scoped-export`,
`version: 3`, over `dependency_records.jsonl`. Canonical ADR 0008 exports keep
v1/v2 and their bytes. The formats refuse each other explicitly:

- **Older verifier refuses scoped exports.** `Reader.Replay`'s strict manifest
  decoder rejects the scoped manifest's unknown fields (and version 3) before
  opening a database snapshot. Its `Selection` and `envelope` decoders reject
  scoped record lines. An older binary therefore fails with an integrity error
  rather than misreading a scoped export.
- **Scoped verifier refuses canonical exports.** `DecodeDependencyExport`
  rejects canonical v1/v2 manifests.
- **Complete-audience evidence** must strictly decode through the journal
  reader with its stated type and increasing journal IDs.

No new event kind exists. The self-acknowledgement event kind is reused, so
`authority_downgrade.go` is unchanged. Kind classification lives in the read-only
`journalreader`. The status builder lives in the sanctioned `status/dependencies.go`
seam, so the writer guard needed no new allowlist entry.

## Revert table

Command: `python3 scripts/verify-adr0011-slice5-2.py`. It reintroduces each leak
alone through `go test -overlay` on an isolated archive of HEAD and runs only
the `-short` metamorphic tests. Every detection is an assertion failure, not a
build error, panic or timeout. The results are in
[adr-0011-5-2-mutations.json](adr-0011-5-2-mutations.json).

New leak classes:

| # | Leak | `-short` |
| --- | --- | --- |
| N1 | Wait-age in the generic view (starts at private eligibility) | CAUGHT: status, world 4 `/v1/queues` |
| N2 | Private-dependent records in the redacted export | CAUGHT: status, private tail changed the export |
| N2b | N2, comparing only cursor pages (verification and tail check off) | CAUGHT: status, world 4 `export:pages:1` |
| N3 | Recording times on redacted records | CAUGHT: status, strict verifier |
| N3b | N3 with strict verification bypassed | CAUGHT: status, self-ack case, world 4 `export:manifest` |
| N4 | Journal event reference on the unauthorized self-acknowledgement | CAUGHT: status, self-ack case, world 4 change view |
| N5 | Warning present only while the private wait blocks | CAUGHT: status, self-ack case, world 4 `/v1/queues` |
| N6 | Complete export without complete source authorization | CAUGHT: status, private tail changed the export |

The ten 5-1 round-5 rows, reconstructed on this head:

| # | Leak | `-short` |
| --- | --- | --- |
| 1 | `predecessor-build` gate | CAUGHT: status, world 7 change view |
| 2 | Check cache survives revocation | CAUGHT: serve, "kept authorized output after revocation" |
| 3 | Co-batched `dependency-changed` reason | CAUGHT: status, world 5 |
| 4 | `dequeue-pending` batch fields | CAUGHT: status, world 4 |
| 5 | Duplicate publication under the pin | CAUGHT: serve, "republished 6 unchanged checks" |
| 6 | Check bypass for `dequeue-pending` | CAUGHT: serve, world 4 forge payload |
| 7 | Batch-derived `Detail` | CAUGHT: status, world 4 |
| 8 | Landing-batch metadata | CAUGHT: status, world 4 `/v1/queues` |
| 9 | Unconfirmed landing (revert `9176c96b`) | CAUGHT: status, CLI and serve, world 4 |
| 10 | `landing-preflight` / `bisection` gates | CAUGHT: status, world 4 change view |

The 5-1 runner `scripts/verify-adr0011-slice5.py` follows two targets moved
here (`denied-reader-accepted`, `empty-declaration-hold`); both still fail by
assertion. Its `raw-check-prefix` and `one-bit-other-entry-gate` targets were
already absent on `e951a3b`, before this slice. They are left unchanged.

## Interpretations

- **Export audience.** "Scoped to the reader's authorized sources" is
  all-or-nothing per queue. A reader authorized for every declared source gets
  complete evidence; otherwise they get the redacted projection. Per-source
  partial evidence in a redacted queue would expose scheduling-dependent read
  counts and journal positions. This matches 5-1's accepted queue-wide
  degradation and should be revisited at slice 6.
- **Export surface.** The export is a library contract (`status.DependencyExport`,
  `audit.WriteDependencyExport`, `VerifyDependencyExport` and
  `PageDependencyRecords`). It adds no HTTP route, and the status API routes
  are untouched.
- **Self-acknowledgement visibility.** It is a consumer-repository fact. It is
  shown in non-generic views, including to unauthorized readers when the
  change has no remaining source edge. The generic view is unchanged.
- **Warning.** It appears on a hold or a completed self-acknowledgement in an
  opted-in queue with more than one recorded writer. The count of authenticated
  writers is taken before exclusions, as in `HoldStatus`.
- **Wait-age.** Per the ADR it is authorized status only. The clock starts at
  the first recorded dependency event that blocks the current revision, but no
  earlier than its enqueue time. It resets when admission is satisfied. It
  needs a caller-supplied threshold and clock (`ProjectDependenciesAt`); the
  zero option is byte-identical to 5-1. The serve activation seam does not set
  a threshold.
- **Pagination.** Authorized summaries are bounded to 8 lines with an omission
  count. Full evidence is in the paginated export. Existing `events` and
  `decisions` paging is unchanged.

## Timings

`go test -race -short -count=1`, each package alone, with both test database
variables exported to port 54329, the heavy lock, Go 1.27.3, `GOFLAGS=-p=2` and
`GOMEMLIMIT=4GiB`:

| Package | `e951a3b` | this head |
| --- | ---: | ---: |
| status | 58.550s | 67.193s |
| serve | 349.910s | 354.023s |

Full products, without `-race`: status 158.6s, CLI 68.1s and serve 639.6s.
Each runs 896 cases: 788 pass and 108 skip by name, with no failures.

A first status measurement was 71.1s. The cost was a second full replay for
the private-tail check. It now runs in one world per case under `-short` and
in every world in the full product, and a redundant self-acknowledgement case
was dropped. The first full serve run also exposed a test-harness shift:
inserted variants changed the index-parity authority pin of later cases, so an
all-merged depth-1 batch ran unpinned and published nothing. The 5-1 pin
assignment is restored, and each variant shares its base case's pin.

`golangci-lint run ./...` reports 0 issues. gofmt is clean; its one fix is a
separate style commit.
