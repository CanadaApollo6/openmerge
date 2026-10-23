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
