# ADR 0008 PR 10b design: retention and external anchoring

Implementation status: #149 merged. 10b-2's fixture acceptance is executed;
10b-1 is separate and PR 11 real-journal acceptance remains outstanding. Scope:
[ADR 0008, PR 10b](../adr/0008-openmerge-policy-authority.md#pr-10b-prove-retention-and-external-anchoring).
The merged v1 contract is frozen in `adr-0008-10a-design.md` and
`internal/audit/testdata/README.md`; implementation adds no new v1 bytes.

## Decision and limits

Retain all journal evidence. Add retention integration acceptance and a documented
external anchoring procedure using existing `audit export --manifest`; add no
CLI verb or flag. Retain the replay-printed v1 manifest SHA-256 independently;
exact replay recomputes that commitment and validates its sources. This meets
the ADR without new wire,
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
`events` and `queues` rows needed to reproduce evidence; these are the only
tables read by `internal/audit`. A snapshot, current config,
current forge read or derived warning summary cannot replace original sources.

Explicit coverage must include these families (the policy is not an allowlist):

| Family | Required retained evidence |
| --- | --- |
| Decisions and settlement (10b-1b covered) | `landing_decision_recorded`, `landing_started`, `landed`, `landing_abandoned`, including unsuccessful/remedy attempts |
| Approval | `approval_eligibility_observed`, `approval_receipt_recorded`, `review_stance_observed`, pinned read IDs and accepted/rejected historical inputs |
| Classification/accountability | `change_revision_observed`, `change_classified`, `change_accountability_observed`, exact change/revision/head and policy inputs |
| Authority/config | `config_applied`, `policy_authority_applied`, all authority observations, write-restriction verification, outside-push warnings and acknowledgements |
| Checks/retries (10b-1b covered) | `batch_started`, `test_ref_built`, `landing_checks_recorded`, `checks_observed`, `checks_completed`, `checks_failure_confirmed`, `check_rerun_requested`, `check_rerun_dispatch_progress`, `check_rerun_dispatched` |

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

## Lifecycle acceptance driver

Use a new command-package integration journey modeled on
`cmd/openmerge/stage_a_acceptance_test.go`, but construct the server with
`serve.NewRepositories` and drive `Server.Run` / `Server.RefreshBranches`.
Use fake forge inputs and private local Git history with the real store and
production authority runtime wiring (`productionAuthorityRuntime` and
`productionServeOptions`); provide valid scratch adoption/emission receipts.
Do not append lifecycle/config/pin events directly for the tested transitions.

- Land a changed `.openmerge.yml` selecting `policy_authority: github` in the
  fixture's governing Git history. Worker `configuration` in
  `internal/serve/reconcile.go` calls `prepareAuthorityLifecycle` and
  `activateAuthority` in `authority_activation.go`, then
  `Store.ApplyAuthorityConfigWithEmissionGate`. Wait for the persisted github
  pin/config pair. Return to openmerge through the same path with fresh valid
  verification/proposal inputs; retain the prior history.
- Remove the queue selector in a valid default-branch config, retain the branch,
  then call `Server.RefreshBranches`. `refreshRepository` / `refreshWorker`
  in `internal/serve/branches.go` reach `Server.lifecycle`, which appends the
  validated `queue.LifecycleChanged{State: "retired"}` through `Store.Append`.
  Assert that event and the unchanged earlier inventory; do not synthesize it.
- Cancel the Run context, wait for Run to return, close all audit readers and
  `Store.Close`, then reopen the store and construct a new `NewRepositories`
  server against the same DB. This exercises stored-queue discovery,
  `Store.Load` and worker reconstruction, rather than reusing in-memory state.

`authority_github_compatibility_test.go` tests status CLI projections, not a mode
switch; reuse its CLI assertions only after driving the transition above.
`internal/queue/lifecycle.go` validates lifecycle events, not retirement discovery.
`cmd/openmerge/admin.go` has no retirement/mode-switch command: use its
`runAdmin` acknowledgement path only for warning evidence, with scratch receipts.

## External anchor and verification

The primary anchor is exactly the `manifest SHA-256: <hex>` printed by successful
#149 export/replay: v1 `manifest_sha256`, canonical manifest with that field
empty, without LF. It binds all four stream hashes, request, count and terminal.
It is **not** `sha256sum manifest.json`. An optional separately named
`manifest_file_sha256` may record exact file bytes including LF, but is never a
substitute for the primary anchor. This deliberately narrows the ADR's
"manifest/terminal digest" option to manifest-required acceptance: a terminal
alone does not bind selection, watermarks, absence metadata or empty identity.

Outside OpenMerge retain all five original files, the printed/recomputed primary
anchor, count/terminal, repository/installation, normalized bounds, version,
binary hash, backup identity/hash and independent custody time. Record an adopted
maximum transaction/commit lag L and require `until <= custody_time - L`.
Establish L by bounded writers or a quiesce/drain barrier, not an assumed timeout;
if no defensible bound exists, record the window as provisional. This condition
reduces late-commit risk but cannot authenticate recorded_at or exclude backdating.
Do not retain credentials. Sign/store the receipt under separate custody.

At anchor creation on a compatible binary:

```sh
openmerge audit export --repo "$REPO" --installation "$INSTALLATION" \
  --from "$FROM" --until "$UNTIL" --out "$EVIDENCE/original"
openmerge audit export --repo "$REPO" --installation "$INSTALLATION" \
  --from "$FROM" --until "$UNTIL" --out "$EVIDENCE/fresh-crosscheck"
openmerge audit export --manifest "$PRIVATE/original/manifest.json" \
  --out "$EVIDENCE/initial-replay"
```

Before replay, copy original into a private directory with no untrusted writers:
reject symlinks/nonregular files and copy bytes (no shared hard links). Create
the parent as 0700 while copying; then grant only a dedicated reader group
traversal/read access (0750 parent, 0550 bundle directory, 0440 files). The
trusted custodian owns these paths; run replay as a different OS identity in
that reader group with no ownership/write or chmod authority over the copy; the trusted custodian must not modify it until
verification ends. Permissions alone do not restrain an owner or root; that
custody separation is a procedure precondition. Use this same read-only private
copy for every validation/comparison to close the manifest-open TOCTOU.

Freeze crosscheck into a second private read-only copy too. Require all three
commands exit 0; `cmp` all five private original/crosscheck/replay files (each
exit 0), with replay output kept under trusted exclusive control. Any difference is investigate-and-record, not a pass.
Capture stdout/stderr/exits separately. Retain initial replay's printed digest,
verify the independently stored bundle copy, and record its immutable object ID.
A checksum beside an editable bundle on the same host is not an external anchor.

Later verification:

1. Obtain the trusted receipt/bundle from independent custody; verify any
   signature against trusted signer identity. Admit primary digest text only as
   exactly one line matching `[0-9a-f]{64}` plus one LF; reject extra lines,
   malformed text and trailing content. Do not use permissive `sha256sum -c`.
2. Prepare the candidate as the read-only private copy described above. Run
   `openmerge audit export --manifest "$PRIVATE/candidate/manifest.json"
   --out "$NEW_REPLAY"` against the intended DB or isolated restored DB.
   Require exit 0; failure is not an anchor comparison pass.
3. Require exactly two LF-terminated stdout lines: the literal `--out` value,
   then `manifest SHA-256: [0-9a-f]{64}`. Compare those 64 hex bytes with the trusted
   digest. Require equality; never compare only an unvalidated input field.
   The offline tool below reports mismatch as exit 1 / `anchor mismatch`.
4. Compare all five private candidate/replay files with `cmp`, exit 0 each.
   Record actual digests, stdout/stderr and exits. Preserve originals on failure;
   never update the trusted value to make a mismatch disappear.

Optional file-hash receipts also require exactly one lowercase 64-hex digest
line; compute `sha256sum manifest.json` on the same private copy, extract the
first field and compare hex directly. No checksum-file parser or multi-line
`-c` gate is used. Receipt syntax errors fail closed before comparison.
Without the DB, offline archive checks are useful but DB replay is unexecuted;
standard 10b acceptance requires replay.

## Detection boundary

Unmodified hashes catch archive edits at the stream-file hash check, before any
chain reconstruction. Replay does not walk supplied previous_sha256 links: it
rebuilds canonical events from DB/selection, calculates the chain, then compares
the rebuilt manifest to the supplied one. The matrix separates those gates.

An attacker can rebuild links, selection, boundaries and every hash; replay may
accept a valid smaller selection because it replays listed roots. A matching DB
edit may also be necessary for payload changes. A recomputed manifest digest
then differs from the independent earlier anchor (assuming SHA-256 collision
resistance). Keeping the old manifest instead fails stream/rebuild checks.
This detects disagreement with a prior commitment, not completeness of history.
Omissions before the first anchor and later in-window additions remain open.
A late commit and a backdated insert cannot be distinguished by this procedure.

## Overlapping and extended windows

A new selection starts a zero-based chain; different roles, references,
boundaries and pre-window dependencies prevent prefix/terminal comparisons.
Fresh same-range scans can see late commits. For anchored A and later B:

1. Validate each bundle by replay; verify A against its external anchor.
   Replay A on the later DB into C: all five files must equal A. B cannot
   substitute for C's retention proof.
2. Join selection rows by `(source.queue_id, source.id)`. Shared identities must
   match queue key, type, recorded_at and payload_sha256. Compare source facts,
   not context-dependent roles/references; `context differs` notices still exit 0.
3. For the common half-open time interval, every A scope identity must appear
   in B. If B claims full coverage of A, require every A identity including
   closure dependencies. Missing required identities fail. Closure-only A
   identities need not occur in an ordinary partial overlap.
4. Every B source with recorded_at in A's anchored interval but absent from A
   is `in-window addition`: investigate-and-record, never pass, including when
   the receipt's lag bound was satisfied. Do not relabel it harmless late commit.
5. Anchor B separately only after discrepancies are resolved and documented;
   retain A and its receipt. Resolution does not rewrite A's commitment.

10b-2 implements the Python stdlib-only offline operator verifier,
`scripts/verify-audit-anchor.py`, distinct
from #149's scratch acceptance driver. Invocation contract:

```sh
python3 scripts/verify-audit-anchor.py --anchor "$TRUSTED/digest.txt" \
  --replay-stdout "$REPLAY_STDOUT" --replay-out "$NEW_REPLAY" --earlier "$PRIVATE/A" --later "$PRIVATE/B"
```

Inputs must be already replay-validated read-only private bundles; this tool
neither connects to DB nor replaces replay. It checks strict receipt/stdout
syntax, digest equality against A's successful replay stdout and A manifest
field (whose validity replay established), repository/installation/version identity,
then the join/range rules above. Add `--require-full-coverage` for rule 3's full
claim. Reject duplicate identities/JSON keys and malformed source/digest/time
fields; use exact integer IDs and UTC timestamps, not floating point.
Use a temporary SQLite database (stdlib sqlite3) for a disk-backed join, bounded
JSONL reads and deterministic identity-sorted findings; clean up on all exits.
Do not infer closure requiredness for partial overlaps beyond these rules.
Exit 0: `anchor and overlap match` (including `context differs` notices);
exit 1: `anchor mismatch`, `missing source`,
`source mismatch` or `in-window addition`; exit 2: malformed/usage/unreadable input.
Emit all discrepancies with identity and reason, never payloads or credentials.
No later bundle is required for a primary-anchor-only check; omit `--later`.

## Acceptance evidence and remaining work

Executed fixture results are recorded in the 10b-1/10b-2 evidence files linked
below. The table states required proof; those files identify executed coverage
and remaining work. PR 11 real-journal acceptance and OS write denial remain open.
All database work uses scratch databases only on shared test Postgres
`localhost:54329`. Reuse `scripts/adr0008_scratch_db.py` before any connection or
subprocess, reject PG environment/routing overrides, allocate unique
`om_9c2c_<32 hex>` names, and guard each source/restore/tamper DSN separately.
Reuse ownership/cleanup patterns from `scripts/check-adr0008-9c-2c-restore.py`
and its `adr0008_scratch_db.py` guard; do not reuse its synthetic event injection
for lifecycle transitions. Track owned databases and cluster-global roles with
unique `om_10b_<32 hex>` role names; record successful creations immediately,
revoke owned grants/drop owned roles in finally cleanup after closing pools,
and assert no owned roles/databases remain, including failed runs. Never read the live DB,
production units, `openmerge/*` refs or `127.0.0.1:8081`.

| ADR requirement | Required proof and retained evidence |
| --- | --- |
| Export twice, same selection | Quiesce a seeded source; range export A, fresh same-range export B, then A-manifest replay C. All commands exit 0; all five files byte-identical, including empty and multiple/retired-queue cases. Save binary commit, selectors, per-file SHA-256, count/terminal and exact commands. Replay additionally covers frozen selection after late commits. |
| Isolated backup → restore | Stop scratch writers before A and `pg_dump -Fc`; save dump SHA-256, PostgreSQL/tool versions and source identity. Restore with `pg_restore --exit-on-error` into a distinct guarded owned DB. Dump/restore exit 0; use the identical binary to fresh-export and replay A there, all five files equal A. Preserve queue/event IDs, keys, recorded_at and complete inventory. No migration on the audit connection. |
| Switch to github and retire queue | Drive the lifecycle journey specified above; compare original inventory after each step. Replay pre-switch A unchanged and fresh-export old/new ranges. Cover return to openmerge, cross-range dependencies, original github history and active-config removal. Save transition event IDs, read-only discovery and byte comparisons. |
| Edit/reorder/drop/truncate | Execute every matrix row below on separate bundle copies and, where needed, separate cloned scratch DBs. Preserve originals and record raw stderr/exits, expected result and candidate/trusted hashes. |
| Real-git source read-back | Reuse #149 `internal/audit/freeze_acceptance_test.go:TestAuditBareRemote` graph/read-back assertions. The 10b delta applies them to post-transition and isolated restored-DB exports, matching tested/intended/confirmed/outside SHAs and parents to the same private bare remote. Save `git cat-file` / `git rev-list` output; no production refs or forge writes. |
| Repeat on PR 11 journal | Defer actual organization authority switch, real App observations, break-glass, drift, rollback and the sandbox journal export to PR 11. Repeat export/replay, isolated restore, lifecycle and tamper/anchor evidence there. Fixture success does not complete Part 3 without that real export. |

Additional acceptance cases:

| Requirement | Expected result |
| --- | --- |
| No retention config | Loader rejects top-level and queue `retention:`, `pruning:` and `prune_after:` keys; schema validation rejects the same corpus. Extend `cmd/openmerge/schema_test.go` to assert generated schema exposes no retention/pruning property and does not allow unknown keys there (therefore arbitrary pruning-key spellings fail too). No schema artifact edits in this design PR. |
| Exact operator role | Export/replay exit 0 with CONNECT, schema USAGE, SELECT only on events/queues; no inherited writer privileges, CREATE, TEMP or public write. Denied INSERT/UPDATE/DELETE/CREATE/TEMP probes fail. Independently omit events or queues SELECT: each export exits 1 with generic diagnostic below. Capture read-only/repeatable-read settings and owned-role cleanup. |
| Overlap missing source | Delete an independent A scope row from B's common interval, fully rehash into a replay-valid subset; offline join exits 1 / `missing source`. Test partial and full-coverage modes, including a replay-valid B that omits an entire owner/dependency component, which fails the full-coverage claim. |
| Overlap added source | Commit an additional scratch event with recorded_at inside A's interval after A's snapshot; fresh B/replay exit 0, offline join exits 1 / `in-window addition`. Repeat with deliberate backdating; verdict is identical. |
| Strict receipt/private copy | Extra receipt lines, malformed hex or duplicate stdout anchor lines exit 2; mismatched valid digest exits 1. Attempt writes as replay identity: denied. All comparisons use the same frozen copy. |

Tamper recipe notation (executed coverage/deviations: see 10b-2 evidence): choose
three independent, valid `base_moved` scope records E1/E2/E3 in one queue, IDs
1/2/3, no references, data.sha equal to 40 lowercase `a`, `b`, `c` bytes
respectively; use 40 lowercase `d` bytes for the replacement SHA. Use a separate
linked-check fixture for dependency cases. Number IDs per owned fixture, not globally.
Serialize edits with v1 canonical JSON plus LF. `H` means update each affected
whole-file SHA-256 (including LF), then recompute manifest_sha256 with its field
empty/no LF. `R` means additionally regenerate previous_sha256 from zero and
terminal/count. All unmentioned bytes/files remain unchanged. Choose mutations
that change bytes; save before/after files and hex digests, not just descriptions.

`G` below is exact CLI stderr
`audit: connection, source, integrity or publication failure` (exit 1).
`M` is the offline primary comparison `anchor mismatch` (exit 1), reached only
after successful replay. Do not claim anchor success when replay failed.

| Exact mutation | Replay result and first catching check | Anchor result |
| --- | --- | --- |
| None | 0, prints manifest SHA-256 | 0 |
| Replace E1 payload data.sha string with another valid SHA; keep hashes | 1/G, events file hash | replay failed |
| Swap complete E1/E2 lines; keep hashes | 1/G, events file hash | replay failed |
| Remove complete E2 line; keep hashes | 1/G, events file hash | replay failed |
| Remove E3 line, or remove final LF and final `}` of E3; keep hashes | 1/G, events file hash | replay failed |
| Replace E2.previous_sha256 with 64 zeroes; H only, leave terminal old | 1/G, rebuilt events hash/manifest equality, not a supplied-link walker | replay failed |
| Remove E2, relink E3 to hash(E1); leave original manifest unchanged | 1/G, events file hash | replay failed |
| Swap E1/E2 in events AND selection; R+H | 1/G, selection source-order validation | replay failed |
| Remove a referenced checks_observed event and its selection row, leave owning landing_checks_recorded reference intact; R+H | 1/G, exact closure comparison against retained DB | replay failed |
| Remove that required checks_observed row from a cloned DB only | 1/G, retained source fetch | replay failed |
| Remove independent E2 from events/selection; R+H, retain valid watermarks | 0, valid smaller scope/closure, demonstrates omission gap | 1/M |
| Remove E3 from events/selection; R+H | 0, valid suffix omission | 1/M |
| Change E1 payload data.sha in events and cloned DB, update its selection payload_sha256; R+H | 0, valid wire/source and empty closure | 1/M |
| Lower watermark as described below; H only | expected 0, same terminal, changed manifest | 1/M |
| Combine --manifest with --repo | 2, `audit: invalid selectors, bounds or output; see audit --help` | not run |

The H-only bad-link case intentionally leaves terminal/count unchanged: the DB
rebuild produces the original chain while the altered stream hash differs.
Also exercise an edited payload with H but no relinking: rebuilding unchanged DB
sources fails manifest equality even if supplied file hashes are now consistent.

Executed terminal-only demonstrator: select independent opaque `base_moved`
IDs 1/2/3; retain ID 4 outside the request interval. Change only watermark `id`
4 → 3 and H the watermark stream/manifest. Replay exits 0 with identical events
and terminal; the changed manifest fails the earlier anchor (exit 1/M).
[10b-2 evidence](adr-0008-10b-2-evidence.md) records hashes and execution.
Deviations: this extends the proposed 2 → 1 fixture; `--replay-out` is required
for exact stdout-path validation; tamper tests call Replay directly with separate
CLI stdout acceptance. OS replay-identity write-denial acceptance remains open.
These results do not justify terminal-only custody.

## Operator guidance to land with implementation

The runbook landed in #153 in `docs/operating.md`; record executed
commands/hashes in [10b-1](adr-0008-10b-1-evidence.md) and
[10b-2](adr-0008-10b-2-evidence.md). Mirror #149: the
audit binary is **Hold for deployment until period B drains**, including when
following these operator instructions. Documentation is not a dependency waiver.

Use a separate operator database login: CONNECT, schema USAGE and SELECT only on
`events` and `queues`; revoke database CREATE/TEMP from PUBLIC as well as the
role, and revoke PUBLIC schema writes in the owned scratch DB, with no inherited
grants. Never change shared-cluster defaults or another database for this test. No DDL, writes, migration role,
service or forge credentials. Test under exactly this role as specified above.
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

This companion updates the implemented design and its evidence references.
Implementation is Inert by its own behavior, but depends on 10a and transitively
Hold PR 8 and 3a–4 (including the shared-store
10a-2a change). Do not deploy its binary before dogfood period B drains. Merging
docs or testing held code in isolation is not deployment permission.

The size split was taken: 10b-1 retention/lifecycle/restore/role/config proofs
are in #152; 10b-2's offline verifier, tamper acceptance and runbook are in #153,
with design alignment in this docs-only companion. Each implementation slice
carries its own evidence; neither alone completes 10b or Part 3. PR 10b-1b
extends #152 with production-path abandonment, exhausted checks and retry
history, including pre-window dependencies and unsuccessful attempts; see
[the retention evidence](adr-0008-10b-1-evidence.md). Unknown kinds have no
production emitter and are not claimed by this lifecycle fixture.

## Questions for the maintainer

1. Who owns independent anchor storage/signing and recovery verification, and
   what custody cadence, lag bound, retention duration and access separation
   will be adopted?
