# ADR 0008 PR 6 reporting and health evidence

Trial class: **HOLD**. May merge; deployment remains prohibited until ADR 0009
period B drains. No deployment, live DB/API access, systemd change, live ref
access, real GitHub call in code/tests, migration, wire event, YAML/schema change,
or Makefile/CI/test-flag change is part of this work. Health is for alerts, never
restarts. Runtime authority activation remains PR 9; organization-sandbox HTTP
acceptance remains PR 11.

## Round 4: reconciliation progress, action budgets, and ledger agreement

Round 4 starts at `80660df` and rebases onto current main
`a4c4f4bcd1befa718908b3ad801150c34c049c7d` (#117, including #116). Only the
legacy harness receipt defaults conflicted during rebase; PR 6 retains its own
receipt locations and widened coverage. #117's shared `writeStatus`, queue
instructions and terminal-check refreshes remain intact. Reporting continues to
use `ReportingJournals`, so worker-owned check rendering never scans warnings.

Responses to review findings:

1. **False 503s during healthy reconciliation.** Publish process progress only
   after individual completed configuration/default-branch/ref reads, PR reads,
   comment reads, rules/permissions/protection reads, check-source reads, approval
   identity/review/diff/governing reads, observation writes and check-report writes.
   Existing post-action completion and post-turn publications remain. No timer
   or unrelated journal activity advances progress. `TestHealthRealReconciliationProgressAndHungRead`
   drives the actual loop/cycle with four sequential 60s PR reads at a 150s stall
   threshold and checks HTTP health throughout. A third hung read after two
   completed reads keeps its progress fixed and returns 503 after the stall limit.
   `TestHealthRealCyclePublishesEachReadAndReport` drives configuration, all three
   rule sources, four PR/comment/check reads and four report writes through a
   real cycle, requiring a new timestamp between operations and health 200
   throughout. `TestHealthLongProgressingCycle` now uses 70 completed PR reads,
   rather than test-generated progress ticks, for both 15m and 60m success ages.
   No serve test calls `publishProgress` directly.
2. **Stall/action-timeout compatibility.** Startup rejects a stall limit below
   action timeout + Git maintenance (30s, shared with the actual Git timeout) +
   completion write (10s, shared with the actual completion timeout) + configured
   overdue grace. CLI parsing and embedded startup call the same validator.
   The default stall limit is 4m; the default 2m action and 30s grace need 3m10s,
   leaving 50s margin. A 1h action timeout is rejected; longer embedded actions
   require an explicit compatible limit within the 30m ceiling. Boundary tests
   accept the exact minimum and reject one nanosecond less, check extension,
   reject incompatible explicit/default settings, and cover CLI flags/environment.
3. **Scan/cache agreement.** Both paths validate reserved association keys and
   acknowledgement SHA bindings in every authority mode. Differential cases
   inject wrong event types under audit/acknowledgement keys and a wrong-SHA
   acknowledgement for both GitHub- and OpenMerge-mode origins, then check scan,
   background refresh and journal fold with warm caches and after cold restart.
   An older background snapshot returned after a partially advanced foreground
   fold now carries `ledger_refreshing`; a complete newer foreground publication
   remains current and is returned without an error. The paused-COMMIT regression
   covers both completed and interrupted foreground folds and subsequent recovery.

Validation receipts and source hashes are retained under
`docs/research/adr-0008-6-artifacts/round4/`. All Go commands and Go-invoking
harnesses use the shared heavy-command flock, `GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`,
and isolated test schemas on localhost:54329. No live services, DBs or refs were
accessed; no real GitHub calls occur in code/tests. Main baseline checkout:
`/home/riels/.cache/pr115-r4-main-a4c4f4b`.

Implementation tested: `330542b4ab2b8e241384db29eacf4dbd5d5f878f`.
`round4/manifest.json` records commands, exit codes, complete HTTP/CLI response
bytes and hashes, source hashes and binary identity. Every source hash matches
that committed implementation. The build reports `330542b-dirty`,
`attestable: false`, because this evidence document was being updated; it is a
local test artifact and was not deployed.

- Focused store/status/serve/CLI regressions: **PASS** (`focused.log`).
- `make check`: **PASS** (format, zero lint issues, full race-enabled short suite,
  build; `make-check.log`). This includes #117 queue-instruction regressions.
- Widened legacy HTTP: **1,304 complete responses byte-identical**, with no
  stripping (`legacy-http.log`, `http-responses.json`).
- Legacy CLI: **6 complete outputs byte-identical**, with no whitespace/field
  stripping (`legacy-cli.log`, `cli-responses.json`).
- Legacy reporting: **6 fixtures pass**, retaining the existing permitted
  change/landing exclusions and comparator negative controls
  (`legacy-reporting.log`).
- Standalone query-count rerun: **PASS** (`query-counts.log`). Status legacy,
  authority cold, unchanged and one-new-event reads each use **4 statements,
  0 extra warning queries**. Background unchanged uses **3 statements**; two new
  audits use **5**, including two indexed origin probes.

A final fetch still observed main `a4c4f4b` after validation. Only evidence and
receipts changed after the passing implementation checks.

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

## Round 5 — successful page and sub-read progress

Round 5 starts from remote `2022a987e65ef6bcb50db733f0ca63988325f7e7`.
The stale worktree was clean; its rebased history was compared against the remote
and retained at local `backup/pr115-pre-round5-992d435` before reset. The tested
implementation is `dd1e4df978464dc6f19ffb50489d1da66ad65990`. The subsequent evidence
commit changes only this document and receipts. Main baseline is unchanged at
`a4c4f4bcd1befa718908b3ad801150c34c049c7d`.

The blocking false 503 is fixed in the shared GitHub transport, not in each
pagination loop. `forge.WithProgressHook` carries a worker-scoped callback from
`Server.cycle`. A successful HTTP response publishes once when its body reaches
EOF; an empty `http.NoBody` response is already complete. Successful conditional
revalidation also passes through this boundary. Headers, early Close, HTTP
failures, body read errors and hung requests/bodies cannot publish. This covers
ListOpenPullRequests, ListComments, PR changed-files/reviews and future driver
requests through the transport. Existing successful per-unit publications remain.

Optional finding #3 is addressed: configuration, PR/comments/checks/rules and
identity/native-review read-unit publication now requires success. Completed
actions and turns retain their independent completion publications, and failed
cycles still age against last success. Optional finding #4 is documented in both
the operating guide and PR body: an old explicit `150s` stall override is rejected
with standard action/grace settings; remove it for the 4m default or supply a
compatible budget.

Optional finding #2 is deferred. Cold-cache ReadRef clone/fetch and the one-time
fence initialization push still have no intermediate Git progress. The operating
guide and PR body explicitly document the startup/first-cycle overdue exposure.
There is no timer-based progress or weakening of hung-operation detection.

### Verification

All Go commands and Go-invoking scripts used
`flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB`
(6GiB for strict replay). DB tests used only isolated schemas on localhost:54329.
The full commands, exit codes, source hashes and receipt hashes are retained in
[the round 5 manifest](adr-0008-6-artifacts/round5/manifest.json). Round 4 receipts
remain historical; the new round 5 receipts rerun their checks on this implementation.

- `make check`: exit **0**, format, zero lint issues, complete race-enabled short
  suite and build. Source hashes in the manifest match the final implementation exactly.
- Focused race regressions: **PASS**, including the round-4 per-unit regression
  with success-only configuration probe expectations. The page counter is atomic;
  DB pools remain inside their synctest bubble and process-wide DNS initializes outside.
- [Focused regressions](adr-0008-6-artifacts/round5/focused.log): **PASS**, including
  real worker loop + real GitHub paginator/transport + health HTTP handler with
  fake HTTP and synctest. No GitHub calls or external network requests are made by these regressions.
  - (a) `TestHealthGitHubPagination/healthy`: **20 × 30s pages**, **10m** total,
    HTTP **200 at each 15s health check**, including after the 4m stall threshold.
  - (b) `/hung` and `/streaming`: two successful pages, then a request that never
    returns or a response body that never finishes. HTTP **503 at 4m1s after the
    second page**, with `LastProgressAt` exactly frozen at that completed page.
  - (c) `/failed` observes the real driver's failed page before the cycle completes
    and asserts progress still equals the previous successful page. Transport
    regressions separately reject HTTP 500, partial bodies, read errors and
    transport errors as progress.
  - (d) `TestHealthProgressHookOutsideCycle`, `TestProgressHookScope` and the
    transport `/no-hook` case prove no publication outside a cycle or without a
    hook in context.
- [Strict offline replay](adr-0008-6-artifacts/round5/replay.json): **PASS**, with
  mandatory synthetic self-tests and 2,159/2,159 matching action, strict state,
  snapshot-hash and boundary checkpoints on the existing 2,158-event export.
- [Simulator comparison](adr-0008-6-artifacts/round5/replay-sim-execution.json):
  **PASS**, required `make test-sim-exits` in baseline and candidate, with complete
  workload result blocks normalized only for harness timing/interleaving and
  compared byte-for-byte. The normalized results and both raw logs are retained.
- Refreshed legacy receipts: **1,304 complete HTTP responses**, **six complete CLI
  outputs**, and **six reporting fixtures** match main. HTTP/CLI compare all
  bytes with no stripping; reporting retains its preexisting permitted
  change/landing exclusions and negative controls.
- [Query counts](adr-0008-6-artifacts/round5/query-counts.log): **PASS**, status
  legacy/cold/unchanged/one-new-event each uses **four statements, zero extra
  warning queries**; background unchanged uses **three**, two new audits **five**.

Hold constraints remain intact: no migrations/indexes, fixtures, wire/schema,
Makefile/CI/Dockerfile/test-flag changes, deployment/merge, live DB/API/systemd or
live ref operations. PR 9 retains activation; PR 11 retains sandbox acceptance.

## Round 6 — validated 304 progress and successful Stage A reads

Started clean at fetched remote `f7653b958ddcddf624131f513235425359e6991e`.
The orchestrator corrected the round-5 review brief: a validated wire 304 served
from cache is a completed, successful GitHub round trip. It deliberately earns
progress once the cached response body reaches EOF. Excluding these revalidated
pages would restore the pagination false 503 in steady-state polling. A 304
without a usable cached representation remains an error and publishes nothing.
The transport comment and API/operating guides now state this rule explicitly.

The existing ListComments cache test now primes three pages without a hook,
observes three wire 304s on the second read and requires exactly three progress
notifications. The real worker/GitHub paginator/health regression adds twenty
revalidated pages at 30s each (10m total), with HTTP 200 at each 15s health check,
well beyond the 4m stall limit. The uncached-304 regression requires an error and
zero notifications. A custom RoundTripper's nil-body successful response publishes
exactly once and returns http.NoBody. The progress helper documents that nil
decode targets close bodies unread; paginated reads must decode into a target.

All three Stage A governing-file/diff publications now follow their error checks.
Race regressions inject failures separately into policy governing reads, revision
diffs and classification governing reads; each leaves LastProgressAt unchanged.

Verification uses the shared flock, GOFLAGS=-p=2 and GOMEMLIMIT=4GiB (6GiB for
strict replay). DB tests use isolated schemas only on localhost:54329. Receipts,
commands and tested source hashes are in [the round-6 manifest](adr-0008-6-artifacts/round6/manifest.json).

Tested implementation: `d6dbc1c9c15c48d9e8c404e9caea1c489be2e44d`; this evidence
update changes only documentation and receipts.

- `make check`: exit **0**; formatting, zero lint issues, full race-enabled short
  suite and build pass.
- Focused race regressions: **PASS**, including all cached/uncached 304,
  pagination health, nil-body and three Stage A failure cases described above.
- Strict offline replay: **PASS**, mandatory synthetic self-tests and all
  **2,159/2,159** action, strict state, snapshot-hash and boundary checkpoints.
  The initial attempt refused a dirty checkout (exit 2) before replay; after
  committing the implementation, the clean-checkout rerun exited **0**.
- Required `make test-sim-exits`: baseline and candidate both exit **0**;
  **146** complete normalized workload result blocks are byte-identical
  (only harness timing/interleaving excluded). Normalized SHA-256:
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- All **201** existing testdata files remain byte-identical to round-5 head.

Hold remains in place. No migrations/indexes, fixture, wire-v1, Makefile/CI/
Dockerfile/test-flag changes, live DB/API/systemd/ref operations, real GitHub API
calls from code/tests, deployment, queueing or merge were performed.
