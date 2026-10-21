# ADR 0008 PR 6 reporting and health evidence

Trial class: **HOLD**. May merge; deployment remains prohibited until ADR 0009
period B drains. No deployment, live DB/API access, systemd change, live ref
access, real GitHub call in code/tests, migration, wire event, YAML/schema change,
or Makefile/CI/test-flag change is part of this work. Health is for alerts, never
restarts. Runtime authority activation remains PR 9; organization-sandbox HTTP
acceptance remains PR 11.

## Round 3: deadline isolation, progress liveness, and #116 integration

Round 3 starts at `ad449c3337cf5088c089659067733f461a7807c5`. The requested
three-minute git-fetch polling observed #116 at the third poll (nine minutes):
`2b21c6ce6a47fcd292175c0eed98a39584262505`. This branch was rebased onto that main
commit; no #116 branch commits were merged. The same main commit is the widened
legacy baseline, checked out at `/home/riels/.cache/pr115-r3-main-2b21c6c`.
`round3/main-poll.log` records the poll receipts. Round 3 supersedes the earlier
rounds' current-model and dependency claims below; their receipts remain history.

The warning cache now separates a mutable fold index from its atomic complete
snapshot. Background DB acquisition, suffix paging and origin lookups operate on
a private index with no cache mutex held. A successful refresh swaps the complete
index under a brief lock, provided foreground work has not advanced beyond it.
Cancelled/failed refreshes discard private partial work. Foreground folds use
TryLock and return last complete counts with queue-local `ledger_unavailable` on
contention; older repeatable-read journals cannot see future snapshot counts.
Warnings fold after the shared journal transaction commits, so a warning deadline
cannot fail that transaction or its sibling queues.

`TestWarningRefreshNeverBlocksForegroundOrSibling` pauses real background SQL and
requires a multi-queue journal read to finish within its 50ms foreground deadline
while the refresh stays paused. It also exercises the atomic contention fallback and rejects future counts for an
older snapshot. `TestWarningRefreshDoesNotReplaceNewerForeground` pauses an older
refresh at COMMIT, advances the foreground journal to a second warning, then
verifies the refresh returns the newer complete counts without replacing its index.
`TestWarningFoldCancellationDoesNotFailJournals` cancels at the commit boundary
and verifies both journals survive with a queue-local warning error.
`TestWarningCacheCompletesAfterCancelledPage` now asserts private partial indices
remain unpublished and subsequent foreground/background reads recover normally.

The worker seeds progress on slot acquisition and advances `last_progress_at`
after every completed action (including completion persistence) and turn. Waiting
overdue remains next-start deadline plus grace: default 30s, range 1–60s. In-cycle
overdue is missing start/progress telemetry, progress older than
`ProgressStallLimit` (default 2m30s, range 1s–30m), or the absolute maximum cycle
duration (default 3h, range 2m–6h). `--health-progress-stall-limit` /
`OPENMERGE_HEALTH_PROGRESS_STALL_LIMIT` configures the stall threshold. Embedded
servers using default health settings derive it from action timeout plus grace,
clamped to the supported range. Degraded compares now against the later of last
success and in-cycle progress (queue process start before either exists), default
15m, range 1–60m. Progress never invents a reconciliation success. Waiting retries
continue aging from last success; failure counts remain informational.

`TestHealthLongProgressingCycle` holds health 200 for 70 simulated minutes under
both 15m and 60m success-age settings, then verifies stall detection, recovery
with new progress, and the absolute backstop. `TestHealthCompletedActionAndTurnPublishProgress`
executes a real claimed action and cycle using the fake forge. Missing cycle
start/progress fails closed; the actual-loop slot-starvation, journal-wake,
zero-delay first cycle, retry and recovery tests remain in the focused run.

Both scan and cache apply paths call the shared `validateOutsidePushOrigin`.
`EventRecord.DedupeKey` and `JournalEvent.DedupeKey` preserve pointers; all three
ledger/journal `COALESCE(dedupe_key, '')` projections were removed. SQL NULL stays
nil; NULL and explicit empty audit keys are orphan anomalies, never silently
ignored or accepted as acknowledged. The extended #116 orphan regression checks
scan, background cache and status journal cache before/after acknowledgement,
including an unaffected sibling. The dedupe-loading test distinguishes SQL NULL
from an explicitly empty key. Pin removal clears health's warning snapshot;
rollback refreshes historical warnings and acknowledgements consistently with
status, covered by `TestOutsideWarningsClearAfterPinRemoval` and
`TestOutsideWarningsRefreshAfterRollback`.

Implementation: `72bef61c9911ed719db3fb57fbdbc004f2ef4904`. Round 3 verification
receipts, exact commands, source hashes and implementation revision are recorded
in `round3/manifest.json`.

- `make check`: **PASS**, including format, zero lint issues, the full race-enabled
  short suite and build. `round3/make-check.log` retains every package result.
- Focused store/status/serve/CLI regressions: **PASS**; no integration skips.
- Widened legacy HTTP: **1,304 complete responses byte-identical** against main
  `2b21c6c` (#116), with no stripping. Full bytes/hashes: `round3/http-responses.json`.
- Legacy CLI: **six complete outputs byte-identical**, with no field or whitespace
  stripping, through Journals → Project → printStatus. The existing harness writes
  its receipt to `~/.cache/adr0008-6-legacy-cli-responses.json`; this was copied
  unchanged to `round3/cli-responses.json` after the passing run.
- Legacy reporting: **six fixtures PASS**, with byte-identical checks, unchanged
  preexisting serialized fields/order under the already permitted Stage A
  exclusions, and passing negative controls.
- Fresh query-count rerun: **PASS**. Legacy/cold/unchanged/new-event status each
  use **4 statements and 0 extra warning queries**. Background unchanged uses
  **3 statements**; two new audits use **5**, including two indexed origin probes.
  See `round3/query-counts.log`.

For reruns, prepend the manifest's wrapper and test DB environment to each command;
HTTP uses the explicit receipt argument, while CLI's fixed receipt path is copied
afterward. All Go work uses the required
shared flock, `GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`; DB integration uses isolated schemas
only on localhost:54329. The API contract, operating guide and ADR refinement note
now describe the final progress model.

## Round 2 revision and dependency scope

Round 2 starts at PR #115 head `a9be8bb` on a fresh local branch
`adr0008-6-round2` from `origin/adr0008/6-authority-status`. Implementation:
`4da72a1` plus `5a057ae05623dbc6df7360ff6ced5cd7f89d746f` (interrupted-page recovery).
The following evidence commit changes only these receipts. Remote destination:
`adr0008/6-authority-status`.

Main and the widened legacy baseline are `975b1b7762f58c128b1703f11fd5c4b6ff552c55`.
PR #116 has not landed as of this verification; no rebase onto #116 and no version
fallback change are included. The later landing branch must preserve #116's
pointer-aware scanners and `validateOutsidePushOrigin`, including validation of
acknowledged audits. Round 1 receipts remain in `adr-0008-6-artifacts/`; the
`round2/` directory supersedes their implementation/results claims.

## Ledger read cost and isolation (A, Muse 2/3)

`internal/store/outside_warning_cache.go` owns the per-queue index and mutex,
independent of workers. Its watermark advances through new events only. It keeps
accepted policy boundaries and outstanding event/origin IDs, rather than journal
payloads or all historical base moves. Delayed audits use the originating base
move's authority boundary, including after rollback. Acknowledgements delete
warnings without changing recent outside-push history. Public summaries retain
exact counts and at most 20 IDs in originating base-move order.

`Journals` already loads immutable history for status explanations. It now feeds
only the decoded suffix into the ledger API and uses binary searches for origins
and receipt sources; it never re-pages or re-decodes that history. Never-pinned
queues skip the ledger and allocate no warning index. Worker-owned CI check
rendering uses `ReportingJournals`, which omits warning I/O entirely. Health reads
atomic warning snapshots; the independent audit loop refreshes them from 256-row
suffix pages, with indexed source probes only for new audits/acknowledgements.
Authority registration seeds the cache once; legacy registration skips it.

A ledger error labels only that queue's warning section with
`error: "ledger_unavailable"`, preserving the last complete counts (possibly
stale), never partial data. Multi-queue status and registration continue; the
latter logs the failure. The snapshot gate keys on having had a pin, so rollback
and complete acknowledgement do not restore blocking worker reads.

`TestJournalWarningQueryCountsAndIncrementalWatermark` traces actual pgx SQL on a
1,024-event prefix:

- Legacy: 4 statements total (BEGIN, queue lookup, journal query, COMMIT), **0
  additional warning queries**; no ledger cache allocated.
- Authority cold, unchanged, and one new event: the same **4 statements / 0
  additional warning queries** each. The watermark advances only on new events.
- Background refresh unchanged: **3 statements** (BEGIN, suffix page, COMMIT).
- Background refresh with two new audits: **5 statements**: those same three
  plus two indexed origin probes. Work scales with the new suffix, not history.

`TestJournalWarningErrorIsolatesQueueAndReporting` covers a malformed association
beside a healthy queue and verifies worker reporting excludes warning work.
`TestWarningCacheDelayedAuditRollbackAndAcknowledgement` covers delayed origin
eligibility. `TestWarningCacheCompletesAfterCancelledPage` cancels a background
read after consuming a full page: partial counts stay unpublished, and the next
successful empty suffix completes the pending index before publishing it.
`TestOutsideWarningErrorPreservesRegistration` exercises both
registration paths and visible per-queue errors. Existing 300-warning/page-boundary,
restart, acknowledgement, GitHub omission and busy-worker audit-loop tests remain.

## Health model (B, C, Muse 4/6/7)

Waiting publishes the effective next cycle **start** deadline. Every immediate
wake, including a journal-change wake, advances it to now. Slot starvation is
overdue only after `deadline + OverdueGrace`. Acquiring a slot clears that deadline
and publishes `in_cycle` / `cycle_started_at`; in-cycle overdue requires exceeding
`cycle_started_at + MaxCycleDuration`. The next start deadline is published when
the turn finishes. Defaults and supported process ranges:

- Grace: 30s, range 1–60s.
- Maximum cycle duration: 3h, range 2m–6h, via
  `--health-max-cycle-duration` / `OPENMERGE_HEALTH_MAX_CYCLE_DURATION`. A hot turn
  can run 64 chained actions at the default 2m per-action timeout, plus other work.
- Maximum success age: 15m, range 1–60m. Degraded depends **only** on elapsed time
  since last successful reconciliation, or queue process start before first
  success. Failure counts remain informational; three failures 15s after success
  are healthy.

`TestHealthCycleRuntimeStartsAfterSlotAcquisition` runs the real loop with a
stalled fake forge: the first zero-delay cycle returns health 200, remains 200
beyond grace, and becomes overdue/503 beyond the configured runtime limit.
`TestHealthSlotStarvationAndImmediateJournalWake` tests both initial slot
starvation and a real journal wake interrupting the idle backstop. The existing
quota, validation, rules, webhook and error retry deadline tests still pass.

`TestHealthRecoveryLoopExplicitlyExcludesSettledWorker` sets `w.recovery` and
retains a stale landing log snapshot while durable state is settled. The actual
loop exits and explicitly clears the health watch, avoiding permanent 503 even
when best-effort log replay cannot advance. Pending obligations remain watched.
`TestAuthorityStatusAndHealthDoNotWaitForWorker` now covers authority, rollback
with a warning, and rollback with **zero** warnings while the worker mutex stays
held.

Future evidence ages are null with `clock_skew` and independent observed/verified
markers; CLI text exposes the skew. Health also marks future last-success times.
`TestAuthorityFutureEvidenceHasExplicitClockSkew` verifies JSON, text, independent
ages and clearing the marker when time catches up.

`docs/api.md` and the appended ADR implementation-refinement note enumerate the
final 503 contract: unavailable health provider/DB/deadline, busy registry,
queue/serialization/response limits, or a watched violated/overdue/degraded queue.
Readiness alone, warning availability and consecutive failures add no 503 reason.
Accepted Decision prose is unchanged.

## Verification and widened legacy compatibility (Muse 8)

All Go processes, formatting and Go-invoking probes run under:

```
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB
```

DB integration uses only isolated schemas on
`postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable`. No initdb
or live-service access was performed. Git integration uses disposable local
remotes and fake forge adapters.

- Focused store/status/serve/CLI health regressions: **PASS**; exact query-count
  evidence is in `round2/focused.log`.
- `make check`: **PASS** (format, zero lint issues, full race-enabled short suite, build).
- `scripts/check-legacy-reporting.py`: **PASS**, six check strings byte-identical;
  preexisting JSON fields/order identical after stripping only the already
  permitted Stage A `change`/`landing` additions; negative controls pass.
- `scripts/check-legacy-authority-http.py`: **PASS**, **1,304 entire HTTP
  responses** byte-identical, including status lines, headers and bodies, with
  **no field stripping**. Retains the original 56 Handler responses and adds
  1,248 DynamicHandler + real Server responses: six deterministic DB-backed
  fixtures × ready/waiting × token/no-token × 13 routes × GET/POST ×
  missing/valid credentials. Routes include queue lists/details, PR/event-limited
  detail, invalid/missing routes, brief (reviewer/operator), change, decisions,
  revision and discovery. Server supplies production readiness/policy methods;
  Journals uses the test DB.
- `scripts/check-legacy-authority-cli.py`: **PASS**, six entire CLI outputs
  byte-identical, without stripping whitespace/fields. Empty, idle, waiting,
  paused, active and outside-push fixtures now go through **Store.Journals →
  status.Project → printStatus**, rather than hand-built status DTOs.

Re-run each Python script with `/home/riels/.cache/pr115-r2-main-975b1b7` as its
baseline argument, inside the wrapper above; HTTP/CLI additionally require the
test DB environment. `round2/http-responses.json` retains both complete HTTP
responses and SHA-256 hashes; `round2/cli-responses.json` does the same for CLI
bytes. The manifest records commands, implementation, baseline and receipt
hashes. No comparison touches a running listener, live API or GitHub.
