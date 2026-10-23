# ADR 0008 PR 10b design: retention and external anchoring

Proposal; acceptance below is planned, not executed. Normative scope:
[ADR 0008, PR 10b](../adr/0008-openmerge-policy-authority.md#pr-10b-prove-retention-and-external-anchoring).
Base inspected: `2d6792c`; audit v1 and CLI remain contingent on
[PR #149](https://github.com/CanadaApollo6/openmerge/pull/149), inspected at
`b6221f48712ed7b948f157142ed1f9b4f5a6365d`.
Its `cmd/openmerge/audit.go`, `internal/audit/testdata/README.md`,
`docs/research/adr-0008-10a-2b-2-evidence.md` and
`scripts/verify-adr0008-10a-2b-2.py` define the pending command/format contract.
Reconfirm the merged contract before implementation; this proposal freezes no
new v1 bytes and edits none of #149's files.

## Decision and limits

Retain all journal evidence. Add retention integration acceptance and a documented
external anchoring procedure using existing `audit export --manifest`; add no
CLI verb or flag. Standard SHA-256 tools bind an independently stored manifest,
while exact replay validates its sources. This meets the ADR without new wire,
configuration, forge contracts, migrations or indexes. No retention YAML is
accepted by the schema or loader. No daemon retention behavior changes.

An unkeyed chain proves internal consistency, not authenticity or completeness.
A privileged writer can replace a journal and rehash an export. An earlier anchor
outside that writer's control detects disagreement with the earlier commitment;
it cannot prove that evidence omitted before the first anchor ever existed.
Neither the database nor this command is tamper-proof. Retention duration,
anchoring cadence, custody and recovery objectives are organizational decisions,
not compliance guarantees supplied by OpenMerge.

## Retention invariant

Preserve every journal row, including unknown historical kinds, original JSONB
value, source ID, queue identity/key, type and database recorded_at. JSONB does
not preserve original input text; compare canonical payloads and metadata.
Keep queue identity rows discoverable after retirement, plus all retained
lookup/ledger data needed to reproduce evidence. A snapshot, current config,
current forge read or derived warning summary cannot replace original sources.

Explicit coverage must include these families (the policy is not an allowlist):

| Family | Required retained evidence |
| --- | --- |
| Decisions and settlement | `landing_decision_recorded`, `landing_started`, `landed`, `landing_abandoned`, including unsuccessful/remedy attempts |
| Approval | `approval_eligibility_observed`, `approval_receipt_recorded`, `review_stance_observed`, pinned read IDs and accepted/rejected historical inputs |
| Classification/accountability | `change_revision_observed`, `change_classified`, `change_accountability_observed`, exact change/revision/head and policy inputs |
| Authority/config | `config_applied`, `policy_authority_applied`, all authority observations, write-restriction verification, outside-push warnings and acknowledgements |
| Checks/retries | `batch_started`, `test_ref_built`, `landing_checks_recorded`, `checks_observed`, `checks_completed`, `checks_failure_confirmed`, `check_rerun_requested`, `check_rerun_dispatch_progress`, `check_rerun_dispatched` |

For each selected decision retain the recursive dependency closure resolved by
10a: governing mode/config history, classification, eligibility and receipt
sources, accountability, admitted checks and applicable producer/run/attempt,
retry generation/dispatch history, starts and settlement boundaries. Preserve
excluded-later sources and explicit `not_recorded` / `inapplicable` boundaries;
absence is not a fabricated source ID. Dependencies outside `[from, until)`
remain retained and exportable. Keep evidence for superseded heads, failed runs,
rejected pins and abandoned attempts as well as successful landings.

Exercise openmerge → github → openmerge, restart between transitions, and queue
retirement/removal from active config. No transition deletes or rewrites history
or reinterprets an old decision under the new mode. GitHub-governed decisions
keep their actual governing pin and historical absence semantics. Retired queues
remain reachable by stored repository/installation identity, without workers.
Backup/restore preserves IDs, timestamps, payloads, queue keys and dependencies;
restore must not replay writes to manufacture replacement evidence.

`internal/store/evidence_retention_test.go` is a source-level SQL sentinel for
pruning and payload mutation, not a database enforcement mechanism. Keep it and
its recognition tests. Extend proof with integration comparisons of the complete
pre-transition row inventory and canonical hashes after each lifecycle operation,
not just checks that a decision still exists. Verify dependency closure and exact
export bytes too. A negative scratch-only case removing a required dependency
must make replay fail; preserve the original database for subsequent cases.
Dynamic SQL, external clients and DBA edits remain outside the sentinel's reach.

Future configurable pruning is deferred. It must preserve all transitive
sources for every retained audit window and ship compaction integration tests;
10b does not introduce a pruning path or weaken the existing guard.

## External anchor and verification

Use a full-manifest anchor as the default. Outside OpenMerge retain:

- The complete original export bundle: `manifest.json`, `events.jsonl`,
  `selection.jsonl`, `watermarks.jsonl`, `dependency_boundaries.jsonl`.
- SHA-256 of the exact manifest file bytes, including its final LF, separately
  named `manifest_file_sha256`; the printed v1 `manifest_sha256`; terminal digest
  and count. These two manifest hashes are different algorithms/inputs.
- Repository/installation, normalized bounds, serialization version, binary
  commit/hash, backup identity/hash and independently recorded custody time.
  Do not put database credentials in the receipt.

V1 `manifest_sha256` hashes canonical manifest JSON with that field empty,
without LF. It is not `sha256sum manifest.json`. The file checksum chosen here
also commits the embedded v1 digest and all stream hashes. Verification must
recompute hashes, not trust a digest copied from an untrusted manifest field.
The terminal chain hashes each canonical event without LF, includes the previous
digest, starts at 64 zeroes and is zero for an empty export. File hashes include LF.
A terminal-only receipt commits event bytes/order, but not all selection,
watermark or absence-boundary metadata, and an empty terminal commits no identity.
Therefore terminal-only custody is insufficient for full 10b acceptance; retain
its context and the manifest commitment as well.

Proposed runbook commands, using #149's CLI, on a compatible binary:

```sh
openmerge audit export --repo "$REPO" --installation "$INSTALLATION" \
  --from "$FROM" --until "$UNTIL" --out "$EVIDENCE/original"
(cd "$EVIDENCE/original" && sha256sum manifest.json) > "$EVIDENCE/manifest.sha256"
openmerge audit export --manifest "$EVIDENCE/original/manifest.json" \
  --out "$EVIDENCE/initial-replay"
```

Require both exports to exit 0 and compare all five files byte-for-byte using
`cmp` (exit 0 each). Capture each command's exit independently; do not allow a
pipeline's final success to conceal an earlier failure. Copy the bundle,
checksum file and receipt to separately controlled storage, verify the copy,
and record its immutable version/object identity. The checksum stored next to
an editable bundle on the same host is not an external anchor.

To verify a later supplied bundle against the original commitment:

1. Obtain the checksum/receipt and original bundle from independent custody.
   Verify a detached signature if used, including the trusted signer identity.
   Refuse a receipt supplied solely by the party supplying the candidate export.
2. Run `(cd "$CANDIDATE" && sha256sum -c "$TRUSTED/manifest.sha256")`.
   The checksum entry names only `manifest.json`; validate that fixed name when
   admitting a receipt. Exit 0 is a match; exit 1 with `manifest.json: FAILED`
   is an anchor mismatch. Missing/unreadable files also fail; never treat an
   inability to check as a pass.
3. Run `openmerge audit export --manifest "$CANDIDATE/manifest.json"
   --out "$NEW_REPLAY"` against the intended source or isolated restored DB.
   Require exit 0. Replay validates canonical schema, all stream hashes,
   dependencies, exact source selection and database payload/metadata.
4. Compare all five candidate/replay files using `cmp`; require exit 0 each.
   Record actual hashes, stdout/stderr and exits. A checksum pass alone checks
   the manifest commitment, not the files it references; all steps are required.

Run these checks against untrusted copies; never replace the trusted anchor
with newly calculated values on mismatch. If the DB is unavailable, file hashes
and an independent v1 validator can check archive consistency, but mark database
replay unexecuted. The standard 10b acceptance requires DB replay.

## Why the external comparison closes the gap

Without recomputation, editing/reordering/dropping/truncating records changes
file hashes and/or the next record's previous digest or terminal/count.
Replay must fail. For a last-record truncation the unchanged manifest count and
terminal/file hashes provide the check even without a successor record.

An attacker can instead rebuild event links, selection metadata, boundaries,
stream hashes and the manifest. Replay may then accept a semantically valid
smaller selection: it replays listed roots, not an independently known complete
historical inventory. A fully rehashed edit may require matching scratch DB
mutation too; otherwise source validation already rejects it. Reordering can
also violate canonical source ordering regardless of rehashing. Do not claim
that every altered/rehashed input passes replay.

For any successful rewrite of committed bytes, the candidate manifest file hash
now differs from the externally retained value (assuming SHA-256 collision
resistance). Keeping the old manifest instead makes its stream hashes fail.
Thus either the anchor comparison or replay rejects. A full rehash cannot change
the independent earlier commitment. An alteration only to non-event metadata may
leave terminal_sha256 unchanged, which is why the manifest anchor is primary.

## Overlapping and extended windows

Each export starts a new zero-based chain. Larger/different selections can change
roles, references, dependency boundaries and record order, including dependencies
before an earlier range. Their terminal and manifest hashes need not match.
Do not compare a later export's terminal with an earlier terminal or assume the
earlier export is a byte prefix. Fresh same-range scans can see late commits too.

For original anchored export A and later overlapping/extended export B:

1. Verify A against its external anchor and replay A's saved manifest against
   the later database into C. All five C files must equal A exactly. This proves
   retention of A's frozen selection; B alone cannot stand in for C.
2. Validate B independently. For every source identity `(queue_id, event_id)`
   shared by A and B require identical queue key, type, recorded_at and canonical
   payload hash. Missing an A source that is selected by B's scope/closure is a
   failure. Closure-only sources in A need not be selected by every overlap B.
3. If B claims to contain all A evidence, require every A identity in B, not just
   the intersection. Roles/references and absence boundaries may differ with
   selection/watermark context; A's exact replay, not naive envelope comparison,
   preserves their earlier meaning. Record all contextual differences.
4. Anchor B separately and retain A. A new anchor does not supersede evidence
   required to verify an earlier commitment.

Failure to reproduce A after restore is a retention/recovery failure even if B
is internally consistent. Neither overlap comparison nor A replay proves that
new unanchored events were never omitted from B.

## Acceptance evidence plan

One evidence section per ADR Tests/real evidence requirement; no pass until run.
All database work uses scratch databases only on shared test Postgres
`localhost:54329`. Reuse `scripts/adr0008_scratch_db.py` before any connection or
subprocess, reject PG environment/routing overrides, allocate unique
`om_9c2c_<32 hex>` names, and guard each source/restore/tamper DSN separately.
Track ownership and clean up only owned databases. Never read the live DB,
production units, `openmerge/*` refs or `127.0.0.1:8081`.

| ADR requirement | Planned proof and retained evidence |
| --- | --- |
| Export twice, same selection | Quiesce a seeded source; range export A, fresh same-range export B, then A-manifest replay C. All commands exit 0; all five files byte-identical, including empty and multiple/retired-queue cases. Save binary commit, selectors, per-file SHA-256, count/terminal and exact commands. Replay additionally covers frozen selection after late commits. |
| Isolated backup → restore | Stop scratch writers before A and `pg_dump -Fc`; save dump SHA-256, PostgreSQL/tool versions and source identity. Restore with `pg_restore --exit-on-error` into a distinct guarded owned DB. Dump/restore exit 0; use the identical binary to fresh-export and replay A there, all five files equal A. Preserve queue/event IDs, keys, recorded_at and complete inventory. No migration on the audit connection. |
| Switch to github and retire queue | Drive real supported mode transitions and retirement in the scratch fixture, with process/pool restart; compare original inventory after each step. Replay pre-switch A unchanged and fresh-export old/new ranges. Cover return to openmerge, cross-range dependencies, original github history and active-config removal. Save transition event IDs, read-only discovery and byte comparisons. |
| Edit/reorder/drop/truncate | Execute every matrix row below on separate bundle copies and, where needed, separate cloned scratch DBs. Preserve originals and record raw stderr/exits, expected result and candidate/trusted hashes. |
| Real-git source read-back | Use a private local bare remote and working clone with ordinary fixture refs. Read back commit objects/parents using `git cat-file` and `git rev-list`; match source head, tested SHA, intended landing, confirmed landing and outside movement to exported evidence. Distinguish intended from confirmed. Retain graph/output and hashes; no forge writes or production refs. |
| Repeat on PR 11 journal | Defer actual organization authority switch, real App observations, break-glass, drift, rollback and the sandbox journal export to PR 11. Repeat export/replay, isolated restore, lifecycle and tamper/anchor evidence there. Fixture success does not complete Part 3 without that real export. |

Tamper matrix: `Replay` below means #149's `audit export --manifest`; each
mutation must be meaningful (at least two records for reorder, nonempty for drop).

| Input | Replay exit / diagnostic | External verification |
| --- | --- | --- |
| Unchanged archive | 0 / printed manifest SHA-256 | checksum 0; all `cmp` 0 |
| Edit payload, keep hashes | 1 / generic integrity failure | checksum may remain 0; replay rejects |
| Swap records, keep hashes | 1 / generic integrity failure | checksum may remain 0; replay rejects |
| Drop interior record, keep hashes | 1 / generic integrity failure | checksum may remain 0; replay rejects |
| Truncate last record or partial final line | 1 / generic integrity failure | checksum may remain 0; replay rejects |
| Rehash omission of an independent scope root and its now-unneeded closure | 0 if remaining selection is valid / success | manifest checksum 1 / FAILED |
| Rehash edited source and matching cloned DB, maintaining valid wire/closure | 0 if semantically valid / success | manifest checksum 1 / FAILED |
| Rehash reorder or truncate/drop with invalid order/dependencies | 1 / generic integrity failure | changed manifest checksum 1 as well |
| Rehash valid suffix omission (no remaining required references) | 0 / success | manifest checksum 1 / FAILED |
| Wrong selector/manifest flag combination | 2 / invalid selectors, bounds or output | do not proceed |
| Required dependency missing in restored/cloned DB | 1 / generic source/integrity failure | anchor may match; replay rejects |

Generic CLI text in inspected #149 is
`audit: connection, source, integrity or publication failure`.
Record it exactly in execution evidence; checksum diagnostics are from the
checksum tool, not a new OpenMerge exit code. Pin rejection has its own diagnostic.
Archive edits must not be confused with valid database evidence: demonstrate the
known omission gap with a genuine exit-0 rehashed case, not just an expected pass.

## Operator guidance to land with implementation

Use a separate operator database login: CONNECT, schema USAGE and SELECT on
required audit tables only; no DDL, writes, migration role, service credentials
or forge credentials. Test denied writes and read-only/repeatable-read snapshots.
Backup/restore uses a separately authorized scratch recovery role, not that login.
The audit command must remain migration-free and make no forge calls.

Keep a tested compatible binary, configuration, DB backup and required Git
objects together with version/hash receipts. Retain originals before upgrades
and first new-event emission. A mode rollback does not make older binaries safe:
respect v6/v7 snapshot and unknown journal-type downgrade limits. Restore in
isolation first; reconcile remote history and post-backup landings before service
recovery. A restored journal older than an anchor must fail its replay rather
than silently blessing lost evidence.

Long repeatable-read exports retain old row versions and can delay vacuum,
increase disk use and contend for I/O. Budget snapshot duration, database and
spool space, inventory memory and statement/command timeouts; measure on scratch
scale fixtures. Use bounded windows where appropriate, retain dependency closure,
and replay from an isolated restored backup for expensive historical work.
Timeout is a failed export, not permission to use partial output.

Store anchors/bundles on separately administered versioned or write-once storage,
with access separation from DB/service administrators. A detached organization
signature (for example GPG or an approved signing service) can authenticate the
receipt; preserve signer verification material, rotation/revocation context and
custody records. Encryption protects confidentiality; neither encryption nor an
untrusted signature proves history complete. Document who verifies, retrieves
and restores the archive and adopted cadence; do not assert certification.

## Size and deployment gate

This design PR changes only this new document. No code, tests, scripts, fixtures,
CI, build, migrations or indexes. Implementation is Inert by its own behavior,
but depends on 10a and transitively Hold PR 8 and 3a–4 (including the shared-store
10a-2a change). Do not deploy its binary before dogfood period B drains. Merging
docs or testing held code in isolation is not deployment permission.

Estimate implementation at 650–950 changed lines including evidence. If the
reviewable diff exceeds about 800 lines, propose two slices before implementation:
10b-1 retention/lifecycle/restore proofs (350–500); 10b-2 external anchor tamper
acceptance and operator guidance/evidence (300–450). Each carries its own
acceptance evidence; no partial slice claims 10b or Part 3 complete. Final paths
must avoid concurrent #149 edits until it merges.

## Questions for the maintainer

1. Approve the documented full-manifest checksum procedure without a new CLI,
   and requiring manifest custody even when a terminal digest is also retained?
2. Who owns independent anchor storage/signing and recovery verification, and
   what cadence, retention duration and access separation will be adopted?
3. Approve the >800-line implementation split above, with size rechecked after
   #149 merges and its final v1/CLI contract is confirmed?
4. Which lifecycle fixture/driver should exercise supported retirement and
   mode transitions without bypassing production persistence paths?
5. Confirm PR 11 owns the executed real sandbox journal repeat and the named
   operator schedules; fixture evidence alone leaves Part 3 incomplete.
