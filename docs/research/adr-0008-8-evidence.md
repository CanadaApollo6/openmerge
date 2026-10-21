# ADR 0008 PR 8: landing checks

Trial class: **Hold**. Shared landing-start/check-read wiring changes even though
emission is authority-only and activation remains PR 9. This builds on main
through `2b21c6c` (PR 5 plus operator follow-up hardening), without PR 6 or PR 7 activation.

`landing_checks_recorded` is a separate additive wire-v1 event. Its only registry
fixture is `internal/queue/testdata/wire/v1/event_landing_checks_recorded.json`.
The deployed `landing_decision_recorded` and every existing fixture remain
unchanged. New objects reject unknown, missing, duplicate, and case-folded keys.

The event binds batch/landing attempt, governing config commit, effective config
and policy digests, tested SHA, intended landed SHA, merge method, queue ref,
ordered pinned PR heads, and check observation time. `required_check_source`
identifies the effective `merge_when` expression and independent rule revisions.
Event-wide `expressions`, `inputs`, and `checks` tables retain each distinct
expression, typed input, and run exactly once. A member's `conditions.expression`
and `conditions.inputs` contain zero-based indexes into the shared tables.
`check_order` indexes `checks` in the exact evaluator-read order, preserving even
repeated identical observations. Members share that check read. All nested and
negative terms and pending/missing knowledge remain intact. Every evaluator run
retains name, original source, producer kind/identity, normalized observed result,
native check/status ID, workflow run ID, attempt, and branch provenance.
Unavailable identities, attempts, and branch provenance are explicitly `null`.

The complete compact wire-v1 envelope is capped at **1,048,576 bytes (1 MiB),
inclusive**, counting JSON escapes, keys, indexes, punctuation, and timestamp.
The builder charges each newly interned input before copying its borrowed lists;
validation independently counts encoded bytes before serialization under the
queue lock. Replay applies the same compact-size limit, with a coarse 2 MiB
raw-data pre-decode limit to accommodate JSONB's added whitespace. Each check
name, input value/list element, identity, and provenance string is at most
**4,096 bytes**; expressions are at most **65,536 bytes**. Enum/object-ID fields
retain their stricter semantic constraints. Counts remain at most 128 members,
128 expressions, 1,536 distinct attribute inputs, 10,000 check-read entries/runs,
and 10,000 elements per input list. Tables reject duplicate/unused entries and
out-of-range or unordered member input references.

An overflow returns typed `LandingChecksTooLargeError`, matching
`ErrLandingChecksTooLarge`, without stale-authorization wrapping or truncation.
No landing start or associated evidence commits. The worker persists an
operator-clearable block with `landing-checks-too-large`, reports that reason for
the batch, and fails the Land action. Reduce the checks/condition inputs before
an operator refresh; automatic retries cannot bypass the size limit. Authority
pin races that require evidence omitted by a legacy gate return
`LandingChecksRequiredError`, classified beside `LandingDecisionRequiredError`.
Legacy approval gates allocate no check-evidence tables or condition recordings.

GitHub's legacy check reader defaults unknown attempts to one for retry
compatibility. `AttemptInferred` marks that default without changing its retry
meaning; audit records `null`. An explicit attempt URL or job-attempt read clears
the inference. The event does not turn neutral/skipped/cancelled results into
success or treat negative/alternative expressions as a flat success-check list.

The executor shares one provenance-filtered and retry-selected CheckRuns read
with the authority gate. Under `checks.retry_failed`, this event records only
the newest-attempt set selected for that evaluation. Superseded attempts remain
in the retry ledger. PR 10a must join both sources by queue/batch/tested SHA and
provider run/attempt identity; this event alone is not a complete retry history.
There is no second forge read to reconstruct history.
The gate evaluates `checkSnapshot`/`policyValuesWithApprovals` using that read,
then `Store.StartLanding` validates the claim, batch membership, tested SHA,
config/policy bindings, and independent source revisions under the queue lock.
It inserts fresh eligibility, decisions, one checks event, and LandingStarted in
one transaction. A final-start failure rolls back every preceding insert. Lost
commit responses do not duplicate evidence; settlement uses the existing fence.
Fast-forward keeps the tested marker separate from the contributor head it lands.

Ordinary GitHub queues, including legacy approval-free queues, emit **zero new
events per landing**. After PR 9 activates authority, an admitted authority
attempt emits **one additional event per batch landing**, independent of member
count; rejected starts emit none. Four speculative batch positions each record
their own tested SHA and runs. Snapshot bytes are unchanged by this journal-only
event: ordinary legacy state remains **v6**; queues with authority state/history already
use **v7** from PR 1.
The fold neither advances planner revisions nor retains a new projection.

Retain all journal evidence, including these checks, decisions, eligibility,
and their dependencies, after authority rollback, settlement, or queue retirement.
No compaction, migration, or index is added. PR 10a should stream bounded queue/id
pages using `eventPage` and `events_queue_watermark` in a read-only repeatable-read
transaction, associating checks by batch/attempt. Missing historical checks mean
"not recorded". Do not add an attempt index or load whole histories for export.

Before first emission, drain all old workers on every host after period B,
retain the worker inventory, capture and test the pre-emission DB backup, and
retain its receipt. The main-before-PR-8 decoder rejects this event. After first
emission use a compatible binary, or restore the verified backup while workers
are stopped; reconcile subsequent real refs and recover their evidence before
resuming. Switching authority mode is not a binary downgrade. This PR neither
performs a live backup nor authorizes deployment.

Main contains a generated-remedy decision wire variant, but no runtime remedy
generator/start path. Its exemption payload is unchanged. The checks event has
no reviewer requirement; the store seam supports an authority start without
inventing eligibility or a review floor. PR 9 still owns complete authority
activation without approvals, and a future remedy producer must use the same
atomic seam with its existing exemption semantics.

Real GitHub organization acceptance is not executed here: real GitHub calls,
live DB/API access, and deployment operations are outside this task. PR 11 must
retain actual queue-ref check/producer/results and settlement IDs against base
history before claiming organization acceptance. Local HTTP and real-git fixtures
provide automated adapter/object evidence, including atomic start and read-back
after real-git settlement with the test DB configured.

Round 2 verification completed on 2026-10-21 UTC against implementation
`abf6a63458263809bf64754c0a84a343abad6fe6` and main
`2b21c6ce6a47fcd292175c0eed98a39584262505`. Main advanced to
`a4c4f4bcd1befa718908b3ad801150c34c049c7d` (PR 7, #117)
during validation. `git merge-tree --write-tree` against that main exits 0 with
no conflicts; the tested comparison baseline remains `2b21c6c`. The later
evidence commit changes only these retained reports/docs. [Summary and commands](adr-0008-8-artifacts/summary.json),
[hash manifest](adr-0008-8-artifacts/manifest.sha256),
[make check log](adr-0008-8-artifacts/make-check.log), and
[real-git event IDs/SHAs](adr-0008-8-artifacts/real-git-and-chain.log) are retained.
Full replay streams, raw reports, simulator normalization, and command logs remain
under `/home/riels/.cache/adr0008-8-r2-verification`; their paths/hashes are in the manifest.

- Wrapped `make check`: **exit 0**, zero lint issues, complete race suite and build.
  Binary revision `abf6a63`; binary SHA-256 is recorded in the summary/manifest.
- Strict offline live-journal replay: **PASS**, 2,158 events from one queue,
  2,159/2,159 identical actions, full structural states, snapshots, and boundary
  checkpoints. All 8,676 boundary probes agree; gates 2 and 4 pass, mandatory
  synthetic self-tests pass (22 tests), and no allowlisted differences are used.
- Depth-1/4 contracts: **90/90 identical** checkpoint records, including all
  snapshot/plan hashes and sizes. Approval fixtures contribute 13 checkpoints
  per depth; legacy-block fixtures contribute 32 per depth.
- Wire: all **103** deployed registry/variant fixtures unchanged (89 registry
  fixtures); exactly one additive fixture, updated to the shared-table shape.
  The previous decoder rejects it.
- Reporting: all **six** complete legacy status/check fixtures byte-identical.
- Simulator differential: baseline/candidate **exit 0**, all **146** normalized
  result blocks identical. SHA-256:
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Bounds/deduplication: the 128-member/1,000-check reproduction fails closed
  with the typed overflow before copying the large input. Exactly 1 MiB encodes,
  replays, and round-trips through JSONB; one byte over returns the overflow
  without stale wrapping or partial start. Distinct member review inputs share
  CI inputs, repeated check-read order survives, invalid references/duplicates
  fail strict validation, and individual string overflows fail closed.
- A legacy gate accepts values exceeding the new evidence caps without making
  evidence. An authority pin injected immediately before its commit requires
  checks with `LandingChecksRequiredError`; no start/evidence is recorded. The
  worker tests retain failed completion and verify the durable overflow block
  plus the batch's `landing-checks-too-large` status.
- Atomic rollback/concurrency/stale claim, idempotent retry, crash before/after
  push, retry/cancellation ledger, legacy approval-free starts, retention, and
  snapshot gates pass in the full suite. All four methods at depths 1/4 pass;
  four speculative positions each retain their own runs. Sixteen real-git
  method/depth/restart cases persist start before push and read back the same
  checks after settlement. Their source event IDs and remote SHAs are logged.

Expected differences: the one new registry fixture and, after PR 9 activation,
one checks event per admitted authority batch attempt. There are **no** planner,
landing-outcome, snapshot, reporting, or simulator differences in the legacy
comparisons. The check adapter's `AttemptInferred` metadata changes no existing
wire payload or retry meaning. No new SQL lookup or query plan is introduced.

The first round-2 size oracle exposed invalid UTF-8 normalization, so encoding
now rejects invalid UTF-8. The first two full checks found field-order and
trailing-newline mismatches in the new fixture only; `abf6a63` corrected its exact bytes, and the focused golden
test plus final complete `make check` and comparisons above pass. Round 1's retained evidence remains in Git history.
There are no SQL or deployment changes. Real GitHub organization acceptance and
runtime remedy generation remain at the boundaries described above.
