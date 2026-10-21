# ADR 0008 PR 6 reporting and health evidence

Trial class: **HOLD**. May merge; deployment remains prohibited until ADR 0009
period B drains. No deployment, live DB/API access, systemd change, live ref
access, real GitHub call in code/tests, migration, wire event, YAML/schema change,
or Makefile/CI/test-flag change is part of this work. Health is for alerts, never
restarts. Runtime authority activation remains PR 9; organization-sandbox HTTP
acceptance remains PR 11.

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
