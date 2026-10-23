# ADR 0008 PR 10a design and size gate

Approved split: 10a-1 implements the source foundation below; 10a-2 completes
dependency evidence and the operator command. Base: main
`7aa71a3f20b7c7b1b7538aaaa5caf1fdf77dff3d`.

## Size assessment

Estimated changed lines, including tests and evidence:

| Area | Lines |
| --- | ---: |
| Snapshot reader, repository validation, paged scan and exact selection replay | 450–550 |
| Typed evidence associations, dependency closure and retry ledger joins | 650–800 |
| Canonical serialization, selection, manifest and atomic output | 450–550 |
| CLI flags, validation, read-only connection and sanitized errors | 150–200 |
| Golden, database, concurrency, retry, real-git and streaming tests | 1,400–1,700 |
| Scratch verifier, format specification and measured evidence | 500–600 |
| Total | 3,600–4,400 |

The combined estimate exceeded the original size gate. The user approved the
10a-1 / 10a-2 split below. These are original combined estimates; measured
10a-1 size and acceptance results belong in adr-0008-10a-1-evidence.md.

## Reader and selection (combined roadmap)

10a-1 uses a separate internal audit.Reader and a snapshot-local callback whose
entire lifetime is one explicit repeatable-read, read-only transaction. It leaves
JournalReader unchanged and does not call Journals:
it materializes all events and additionally reads warnings after commit.
LandingDecisionEvidence also starts its own transaction; extract transaction-local
helpers only where reuse preserves its existing behavior.

Validate installation and repository identity before output. Discover repository
queues through the existing identity index without a lifecycle filter. Reject
ambiguous tenancy rather than silently combining installations. Sort queue keys
by their canonical identity components using a documented bytewise comparator.
Read events in bounded `(queue_id, id)` pages through events_queue_watermark;
apply `[from, until)` to database recorded_at during the scan. Keep all selected
source events, including historical kinds without new evidence.

For 10a-2, use private disk spools for unbounded dependency bookkeeping.
10a-1 directly streams selection; it needs no intermediate spool.
Do not keep a repository-sized event slice or dependency map in memory. A
bounded external merge/dedup pass produces the final source sequence, ordered
by canonical queue identity and source event ID. Dependencies retain their own
source IDs and explicit roles; references may point forward. Include a source
once even when multiple decisions depend on it. Fixed-pass processing and indexed
identity lookups must avoid a full queue scan for each decision. Benchmark the
actual closure algorithm, not just the source page query.

10a-2 only: resolve explicit Stage A classification/source references, pinned eligibility
reads, accountability and receipt identities, authority evidence, landing checks,
and settlement boundaries. Validate each association against queue identity,
change/revision/head and batch/attempt as appropriate. Keep intended landed SHA
separate from confirmed settlement. Historical absence yields `not recorded`;
a required reference whose retained source cannot be found fails the export.

10a-2 only: for check evidence, join the admitted landing check snapshot to the retry ledger
for its batch/tested SHA and producer/run identity. Select the newest applicable
attempt at the decision boundary, retaining rerun generation and dispatch/observed
sources. A later retry after that boundary cannot rewrite the decision. Test a
stale successful attempt followed by a newer failed attempt explicitly. Never
substitute the first matching run or a current forge read.

## Serialization version 1

Normative encoding: compact UTF-8 JSON; recursively sort object keys by
UTF-8 bytes; preserve array order; no insignificant whitespace. Encode strings
with standard JSON escaping for quotes, backslashes and controls, without HTML
escaping; escape U+0085, U+2028 and U+2029 as lowercase Unicode escapes for line safety; emit other Unicode directly. Reject invalid Unicode. Represent finite
JSON numbers exactly as normalized decimal coefficients with no exponent, no
redundant leading/trailing zeroes and negative zero normalized to zero. Never
convert payload numbers through float64. Bound expansion and nesting and fail
explicitly when an input exceeds documented serialization limits. Canonicalize
JSONB values rather than claiming to preserve original JSON text.

Envelope timestamps use UTC with exactly six fractional digits for database
recorded_at. Preserve event-supplied timestamps inside canonical wire payloads.
The envelope carries kind, source event ID/type/recorded_at, canonical queue key,
wire payload, references and previous_sha256. Every record ends in one LF.
Digest each record excluding that LF; initialize previous_sha256 to 64 zeroes.
The manifest stores count and terminal digest; an empty export has the zero digest.
Golden vectors must independently fix full bytes and digests, including Unicode,
HTML characters, large integers, equivalent decimals, timestamps and empty output.

The selection file streams source identity, role (`scope` in 10a-1), canonical
payload hash and immutable envelope metadata. Hash its exact canonical bytes.
The manifest references that file and digest and records serialization version,
repository/installation, filter, per-queue watermarks and stream hashes,
record count and terminal digest. No export-time clock or added remote URLs.
Original wire fields remain intact even when they contain historical URLs.

Exact replay strictly decodes the manifest and selection, rejects unsupported
versions/unknown envelope fields and unsafe selection paths, fetches exactly the
listed sources in one read-only snapshot, and verifies payload and metadata
hashes. Recomputed output must match manifest count and terminal digest. A fresh
range export after a late commit is a new selection. Watermarks alone never
freeze a source set across transactions.

## Publication and verification

Build in a mode-0700 temporary sibling directory. Refuse an existing target,
write manifest last, and publish only after all files and hashes succeed. Use
no-replace publication semantics, including a target-creation race; plain rename
must not replace another completed export. Remove failed staging output. A
truncated stream cannot leave a valid published manifest.

The verifier must use scripts/adr0008_scratch_db.py's DSN guard and shared test
Postgres. Test with a restricted SELECT-only database role; assert transaction
read-only/repeatable-read settings and absence of migrations, locks and writes.
Use synchronization barriers for a transaction that obtains an event ID before
snapshot acquisition and commits afterward: exact replay stays identical while
a fresh export includes the newly visible event. Cover retired queues, half-open
boundaries, unknown old history. Cross-range dependencies and missing-evidence verdicts
are 10a-2 acceptance.

10a-2 only: use a real-git fixture journal to compare tested/landed SHAs and outside moves
with a bare remote graph. Record approximately 5,000-event and growth query
plans/timings plus bounded-memory evidence for the complete closure pipeline in 10a-2; 10a-1 measures source export/replay. Keep costly
cases outside -short. Run the requested existing-output and fixture comparisons,
main-versus-branch race-short package timings, and new-test race stress under
eight owned CPU burners, using the specified Go installation and shared flock.
Measured 10a-1 acceptance is recorded separately in its evidence document.

## Approved split

1. **10a-1: deterministic read-only source export foundation.** Single-snapshot
   repository scanner, canonical serializer and goldens, selection/manifest
   integrity, exact replay, atomic publication, read-only/concurrency/streaming
   tests. Internal API only; no advertised audit CLI until full evidence is ready.
2. **10a-2: complete evidence export and operator command.** Typed dependency
   closure, governing check/retry joins, historical/missing evidence distinctions,
   CLI, real-git and complete evidence acceptance. Both layers carry their own
   regression, stress and timing proofs. This completes the original row.

10a-1 is inert; 10a-2a is Hold because it changes shared production store
code. Both depend on Hold PR 8 and transitively 3a–4. Neither permits deployment before period B drains. PR 10b's
backup/restore and external anchoring acceptance remains out of scope. This
proposal changes no retention behavior, queue event, wire payload, schema,
index, forge method, worker behavior, formatter, fixture, CI or build setting.

## 10a-1 implementation contract

The internal `audit.Reader` owns a read-only pool and exposes `Export` and
`Replay`. It is not referenced by the daemon or command. Each operation opens
one explicit repeatable-read, read-only transaction. Repository names are exact
stored identities (no locale/case folding); an omitted installation must resolve
to exactly one installation. Queue discovery uses the queues identity index,
without consulting the live canonical inventory. Queue key components are
compared by UTF-8 bytes in Go. One identity-index-eligible query reads only IDs and
key components and watermarks for the repository, then sorts the inventory in memory.
One set query uses a lateral indexed top-one event lookup per queue, avoiding
per-queue SQL round trips and full event-history aggregation. Queue identities
use O(total queue-key bytes) uncapped memory; replay also holds maps for current
and frozen identities. Expected scale is thousands of queues per repository
(the growth probe covers 8,192 matching queues), with ordinary short base/lane
names. This is an inventory assumption, not constant-memory behavior; unusually
large inventories or keys require budgeting or a future spool design.
There is no queue keyset rescan or database collation dependency. Retained event pages
contain at most 32 rows and at most 4 MiB of cumulative raw payload bytes; replay
uses the same retained-page byte bound. One bounded lookahead payload may be
returned and reread on the next page; canonical copies remain bounded per record.
Oversized values return a rejection marker before client transfer. Each event
payload is converted to text once per query. The per-queue snapshot maximum ID is a scan bound, not
a reusable source selection. Time windows compare database `recorded_at`; both query bounds are rounded
up to the next microsecond when needed, preserving exact `[from, until)`
semantics for a database whose timestamp values are discrete microseconds.
Equal normalized bounds denote an empty interval; reversed bounds are rejected.

V1 files are `events.jsonl`, `selection.jsonl`, `watermarks.jsonl`,
and
`manifest.json`. All records are compact canonical UTF-8 JSON followed by LF.
Object keys use UTF-8 byte order. Arrays retain order. Strings escape quotes,
backslashes and controls (short escapes for b/f/n/r/t, lowercase u00xx otherwise);
U+0085, U+2028 and U+2029 use `\u0085`, `\u2028` and `\u2029`; HTML
characters and other Unicode are literal. Invalid Unicode,
duplicate keys, trailing JSON, depth beyond 64, raw/expanded records beyond
4 MiB and excessive decimal expansion fail explicitly. Number normalization
never uses a binary float. Database times are UTC with six fractional digits;
request bounds use this same UTC six-digit form after microsecond ceiling;
wire timestamps stay unchanged. Each event has `kind`, `source`, `payload`,
`role`, `references`, `previous_sha256`. The chain hashes each event without LF;
the initial and empty terminal hash is 64 zeroes. Whole-file hashes include LF.

Source metadata is ID, queue ID/key, type and database timestamp. Each selection
entry hashes canonical payload separately from canonical `{source, role,
references}` metadata. The manifest hashes all three streams and carries
version, resolved request, count and terminal hash. `manifest_sha256` hashes its
canonical representation with that field set to the empty string (without LF).
These unkeyed integrity hashes detect corruption; they are not authentication or
external anchoring. No export clock is recorded.

Replay strictly decodes metadata, verifies manifest/file/payload/metadata hashes
and canonical schema, validates every watermark against snapshot queue identity,
unique bytewise order, retained maximum and selected IDs (no unknown saved
queues or selected ID above its watermark), retrieves exactly the listed IDs in one new snapshot,
then rebuilds all files and requires the same manifest, count and terminal hash.
Only fixed safe relative filenames are accepted; symlinks and nonregular files
are rejected. Unknown source event kinds are opaque retained payloads. Replay
does not infer sources from watermarks or reapply a range query.

Files and any future spools live in a private 0700 staging sibling under the
output parent, with 0600 files. Files are flushed and synced; manifest is written
last. Linux renameat2(RENAME_NOREPLACE) atomically publishes the directory and
rejects a target-creation race. Unsupported platforms/filesystems fail closed.
The staging directory is synced before rename and the parent directory is synced
after rename, before success is returned. A completed export therefore persists
the directory entry across a crash on filesystems honoring fsync. This is local
publication durability; backup and external anchoring remain out of scope.
Unsupported atomic rename (including EINVAL) returns a distinct sanitized error.
Failed staging is removed; a parent-sync failure after rename returns failure
although the complete target may already be visible. Before retrying, preserve
the target and verify it by exact Replay to a distinct fresh path (which checks
DB sources and all hashes); inspect/retain the verified result. If verification
fails, investigate the storage failure and remove only that failed export target
before retrying. Never blindly retry against or overwrite the existing target.

V1 is frozen with PR 10a-2b-2. Any serialization shape change after merge
requires a version bump. The complete contract includes events.jsonl,
selection.jsonl, watermarks.jsonl, dependency_boundaries.jsonl and manifest.json.
See internal/audit/testdata/README.md and journal-v1.json for literal independent
complete-export vectors, including dependency roles, explicit absence, boundaries,
metadata hashes, chain links and manifest/file integrity. Canonical primitive
vectors remain unchanged. Payload timestamps retain their original spelling;
source and request bounds use UTC with exactly six fractional digits.

### 10a-2a round-1 historical boundary clarification

This layer is Hold because it changes the shared production landing-evidence
helper; deployment remains after period B. Authority/check requiredness is
conditional on the latest mode pin before each decision. GitHub history and
rollback produce explicit absence records without invented source IDs. Replay
recomputes export's closure from the scope roots and requires exactly that
selection; this single comparison replaced per-record re-resolution. Identities
are canonical JSON string tuples. Without new indexes, JSON-filtered retained lookups
remain owners × retained events (multiplied by checks); bounded memory is not a
claim of sublinear database work. See the 10a-2a evidence note for limits and plans.

## Governing evidence and final v1 contract

The approved sequence is in `adr-0008-10a-2b-size-gate.md`. Step 2 completes
the original 10a row and freezes the complete-export bytes.
Audit projects only config/pin events through `queue.Apply` in journal order
before each decision. An invalid or digest-ignored pin fails that export
closed, even if later history contains a valid replacement. Config changes
alone preserve the durable pin exactly as the queue fold does.

A governing github pin has `status: "inapplicable"` and retains its actual
source. `not_recorded` means the resolver found no applicable source. Reference
shape validation restricts `inapplicable` to the governing authority edge.
Under github authority `landing_checks_recorded` remains `not_recorded`, even
when an instrumented check event is retained; only the pin edge is inapplicable;
closure retains its source and replay re-resolves its status and identity
against retained history. Rehashing a status or source substitution cannot
legitimize it. Fully rehashed omissions still need the future 10b anchor.

Across check-observation events SQL compares run ID, normalized attempt
(`0` means `1`), then event ID. Run 5 attempt 1 followed by run 5 attempt 0
therefore selects the later event. Within the chosen event, Go normalization
validates the retained run and describes its reference; these are distinct
selection and validation responsibilities. Retry-generation selection stays
in SQL; the redundant `sameGeneration` helper and its helper-only test are gone.


The operator command uses audit.Open, Reader.Export and Reader.Replay; it never
runs migrations, takes worker locks, writes to Postgres or contacts a forge.
The snapshot-local governing-pin cursor retains one queue segment only and
resets on queue changes or backward decision traversal. It folds only new
config/pin prefixes through queue.Apply. Rejected pins still fail closed with
`audit: governing authority pin rejected by queue fold`, including after a
later valid replacement. No shared queue fold or wire behavior changes.
Classification: **Hold**; deploy only after period B drains.

## v1 frozen specification

This section is normative and supersedes every earlier serialization definition
in this document and the evidence notes. V1 freezes when PR 149 merges. Later
shape changes require a new version. Hashes establish local integrity, not
provenance or completeness; external manifest anchoring is separate (10b).

An export contains exactly `manifest.json` and four UTF-8 JSONL streams:
`events.jsonl`, `selection.jsonl`, `watermarks.jsonl`, and
`dependency_boundaries.jsonl`. Each record ends in LF, including the manifest.
Empty streams contain zero bytes. An existing queue with no selected events
still has a watermark. Range-mode CLI export rejects a nonexistent
repository/installation.

The event envelope has exactly `kind` (always `source`), `source`, `payload`,
`role`, `references`, `evidence_kinds`, and `previous_sha256`. Source has
`queue_id`, `id`, `type`, `recorded_at`, and `queue`; queue has `installation`,
`repository`, `base`, `lane`. The payload is the retained event JSON value.
Selection has exactly `source`, `role`, `references`, `evidence_kinds`,
`payload_sha256`, `metadata_sha256`. Metadata hash input is precisely the
canonical object `{source, role, references, evidence_kinds}`. Payload hashes
cover the canonical payload alone. Empty references and kinds are `[]`, never
null. Unknown historical event types have empty evidence kinds.

Roles are `scope` (in the half-open normalized request range) and `dependency`
(outside the range, transitively required); scope wins when both apply.
References have exactly `kind`, `relation`, `status`, `owner`, `expected_type`,
`identity`, `generation`, `run_id`, `attempt`, `source`. Owner has exactly
`queue_id`, `event_id`, `batch_id`, `attempt_id`, `change_id`, `revision`, `head`,
`tested_sha`. Owner identifies the referencing event, not the dependency.
Relations are `relied_on` and `excluded_later`; the latter retains the first
later observation without substituting it for the governing observation.
Statuses are `recorded`, `not_recorded`, `inapplicable`. Not-recorded has null
source and relied-on relation. Recorded has a retained source. Inapplicable is
reserved for a retained github-authority pin, with its source retained; the
corresponding decision's check edge is not_recorded, not inapplicable.
Numeric generation/run_id/attempt and owner revision/event_id/queue_id use 0
when not applicable; applicable source and owner database IDs are positive.
Nonapplicable owner string fields are empty strings.

For a recognized source type, `evidence_kinds` is the singleton containing its
mapped kind. The kind to expected-type mapping is exhaustive:

| Kind | Expected types |
| --- | --- |
| settlement | landing_decision_recorded, landing_started, landed, landing_abandoned |
| classification | change_revision_observed, change_classified |
| eligibility | approval_eligibility_observed |
| accountability | change_accountability_observed |
| receipt | approval_receipt_recorded, review_stance_observed |
| authority | policy_authority_applied |
| check | batch_started, test_ref_built, landing_checks_recorded, checks_observed, checks_completed, check_rerun_requested, check_rerun_dispatch_progress, check_rerun_dispatched, checks_failure_confirmed |

Identity is a nonempty structured JSON **array of strings**, never a JSON
string containing an array, and never a database event ID. Numeric components
are canonical decimal strings. Each edge uses the semantic selector below;
recorded and absent edges use the same selector. Owner and source provide the
queue, decision boundary, and actual retained event identity separately.

| Expected type | Identity tuple |
| --- | --- |
| landing_decision_recorded | [batch_id, attempt_id] (one edge per retained decision event for that attempt) |
| landing_started | [batch_id, attempt_id] when an attempt is known; [batch_id] for the preceding batch start selector from a settlement |
| landed, landing_abandoned, batch_started | [batch_id] |
| test_ref_built | [batch_id, tested_sha] |
| landing_checks_recorded | [batch_id, attempt_id] |
| change_revision_observed, change_accountability_observed | [change_id, revision] |
| change_classified | [change_id, revision, generation] |
| approval_eligibility_observed | [read_id] |
| approval_receipt_recorded, review_stance_observed | [source_channel, source_id] |
| policy_authority_applied | ["governing"] (latest accepted pin before owner) |
| checks_observed | [check_source, check_name], or [check_source, check_name, "later"] for the excluded boundary selector |
| check_rerun_requested, check_rerun_dispatch_progress, check_rerun_dispatched | [check_source, check_name] |
| checks_completed, checks_failure_confirmed | no emitted references in v1; source evidence-kind classification only |

Every non-null `reference.source`, including inapplicable and excluded-later
sources, is included recursively in the export. Closure is computed against the
retained snapshot, not just the request range; each source appears once.

| Owner type | Emitted expected types | Populated owner fields beyond queue_id/event_id |
| --- | --- | --- |
| landing_decision_recorded | policy_authority_applied; landing_checks_recorded; landing_started; landed; landing_abandoned; with approval: change_revision_observed, batch_started, approval_eligibility_observed, change_classified, change_accountability_observed, and one approval_receipt_recorded or review_stance_observed per relied source | batch_id, attempt_id, tested_sha; approval basis supplies change_id/revision/head |
| remedy landing_decision_recorded | decision edges above plus change_revision_observed for remedy change/revision/head | batch_id, attempt_id, tested_sha; remedy supplies change_id/revision/head |
| landing_started | landing_decision_recorded for every retained decision of this batch/attempt; landed; landing_abandoned | batch_id, attempt_id |
| landing_checks_recorded | batch_started, test_ref_built; per check: checks_observed, check_rerun_requested, check_rerun_dispatch_progress, check_rerun_dispatched, and later checks_observed | batch_id, attempt_id, tested_sha |
| landed, landing_abandoned | landing_started | batch_id; attempt_id copied from the greatest-ID preceding start of that batch |
| all other source types | none | no references, hence no owners |

All selectors are queue-local and bounded by the retained watermark. Revision and batch-start selectors require a unique matching ID strictly
before the owner (hence the greatest matching preceding ID); test-ref and
accountability selectors take the greatest matching ID strictly before the
owner. Eligibility, classification and relied receipt or
stance match their complete recorded basis/read/identity and precede the owner;
required evidence must resolve uniquely. Test-ref selection first chooses the
latest batch test-ref before the owner, then validates tested_sha. Decision
check/start references match batch and attempt; the checks edge is absent unless
OpenMerge authority applies. A start emits all matching retained decision events,
or one not_recorded decision edge if none exist. Settlement selectors take each
matching landed/abandoned event strictly after that attempt's start and strictly
before the next start for the same batch (or through the watermark if no next
start); optional single-source selectors must not resolve ambiguously.
Rerun requests select the greatest matching ID before the check owner, matching
batch, tested SHA, check source/name AND workflow_run_id when nonzero. Dispatch
and progress select before the owner in that request's generation; progress also
matches nonzero workflow_run_id. A missing request leaves generation zero
for dispatch/progress lookup. The later observation uses the lowest matching event ID
strictly after the owner, with the same batch/tested SHA/check source/name.

Watermarks have `queue_id`, `queue`, `id` (retained maximum or zero).
Dependency boundaries have `queue_id`, `queue`, `first`, `last`, `count` and
summarize only dependency records, one nonempty record per affected queue.
Events and selection share queue-key order (installation, repository, base,
lane, compared lexicographically by UTF-8 bytes), then numeric source ID.
Watermarks and boundaries use the same queue-key order, never queue-ID order.
References sort by the complete canonical reference bytes and exact duplicates
are removed. Arrays in retained payloads preserve their original order.
For governing check observations, order candidate runs by greatest run ID,
then greatest attempt with zero normalized to one, then greatest source event
ID across observations; the owner boundary excludes events at or after itself.
The first later observation is instead the lowest matching source ID after the
boundary and is retained as excluded_later. Retry request selection is scoped
to the check/workflow; dispatch and progress follow that request's generation.
Settlement edges belong to the interval from the matching batch/attempt start
until the next start for that batch. A settlement's preceding start selector
uses the greatest source ID before it. Governing authority folds only config
and pin events strictly before the owning decision. These selection orderings
are independent of the canonical byte comparator used to serialize references.

The manifest fields are exactly `manifest_sha256`, `version` (1), `request`,
`count`, `terminal_sha256`, and for each of the four stream names above without
`.jsonl`, the filename field and its `<name>_sha256` field. Request has exactly
`installation`, `repository`, `from`, `until`. File names are fixed safe relative
names. Stream hashes include every LF. Each chain link is SHA-256 of the previous
canonical event envelope without LF; the initial previous hash and empty terminal
hash are 64 zeroes. Manifest self-hash covers its canonical JSON with
`manifest_sha256` set to the empty string, without LF. Digests are lowercase hex.

All JSON object keys sort by UTF-8 bytes. Whitespace is absent outside strings.
Numbers use exact decimal arithmetic (including integers above 2^53), no binary
float rounding, no exponent, and no redundant fractional zeroes; negative zero
is 0. Strings must use `\b`, `\t`, `\n`, `\f`, `\r` for U+0008, U+0009,
U+000A, U+000C, U+000D respectively; all other U+0000–U+001F controls use
lowercase `\u00xx`. Quote and backslash use `\"` and `\\`. U+0085, U+2028,
and U+2029 use `\u0085`, `\u2028`, and `\u2029`. All remaining characters
are literal UTF-8, without HTML escaping or Unicode normalization. Invalid
UTF-8 is rejected. Source timestamps
are UTC with six fractional digits. Request bounds are ceiled to database
microseconds, UTC with six fractional digits, years 1 through 9999. Payload
strings, including timestamp spellings, remain unchanged. Requests select
`from <= recorded_at < until`; equal bounds are a valid empty selection.
An absent request bound is spelled `""`; the CLI always supplies both bounds
and therefore never emits the absent-bound shape.
