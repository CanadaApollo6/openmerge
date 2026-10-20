# ADR 0008 PR 6 reporting and health evidence

Trial class: **HOLD**. Merge is permitted; deployment is prohibited until ADR
0009 period B drains. No deployment, live DB access, live status API request,
real GitHub call in code/tests, live ref access, migration, wire event, public
YAML/schema change, or Makefile/CI/test-flag change is part of this work.

## Revision and dependency scope

Initial baseline: `d4e15c89fa9a1e68d8cf9afe55d0f3a25318ae91` (fetched
2026-10-20). Both #112 and #113 were OPEN at start. PR 4 reached main during
initial verification. PR 5 then landed as
`975b1b7762f58c128b1703f11fd5c4b6ff552c55`; this revision rebases PR 6 onto that
main commit and wires its production ledger. Implementation commit:
`e16a98c35da0e94e3efb68342c9fb33507bc6806`. The final evidence commit changes
only receipts/documentation. Remote branch: `adr0008/6-authority-status`.
The branch is checked out in another worktree, so implementation uses fresh local
branch `t3/wire-authority-warnings-pr115` and pushes directly to the remote PR
branch with a lease on old head `992d4351d0348071ddb488ed8820ea602bfecf4b`.
This PR does not stack on #112 or #113. Legacy byte comparisons retain the
original pre-PR-6 baseline `158afe96c43a6122edca8566b09ba0ebf2a1e4bd`.

`internal/status/authority.go: ProjectAuthority` directly consumes PR 4's
`WriteRestrictionStatus` durable state, last-observed and last-verified times.
It renders sticky violations through hidden reads without duplicating the
source's finding fold. Visibility uses retained applied evidence. Proposed verification remains excluded from applied rendering.
`internal/store/journal.go` reads PR 5's paged outstanding-warning ledger in
the same read-only repeatable-read transaction as queue history. It counts all
outstanding warnings and retains at most 20 IDs in originating BaseMoved order,
setting truncation independently of the exact count. Ledger pages retain at most
256 events. No warning eligibility or dedupe association is inferred from
`EventsAfter` or the recent `outside_pushes` projection. The summary's zero value
means no outstanding warnings; a failed read returns an error and discards any
partial summary. Empty ledgers omit `outside_warnings`, including unchanged
GitHub-mode queues. Authority-era warnings survive rollback until acknowledged.

Health restores warning summaries at worker registration and refreshes them in
the independent outside-push audit loop, without worker locks. Requests read
immutable atomic summaries and still perform exactly one `SELECT 1`. Acknowledged
warnings disappear from status immediately and from health on the next audit
pass; read failures preserve the last successful health summary. Ledger scanning
is O(N) in journal length, bounded in memory and by the read deadline, and never
runs on a health request. Warning presence does not affect health status.
The PR 6 ADR section requires outstanding warnings, not a new receipt-history
view; the existing bounded recent `outside_pushes` history remains byte-identical
when a warning is acknowledged.

Runtime authority activation remains PR 9 work. Real organization HTTP acceptance is PR 11 work.

## Automated acceptance

All Go processes, formatting, Python probes that invoke Go, and `make check`
run under `/home/riels/.cache/openmerge-heavy.lock`, with `GOFLAGS=-p=2` and
`GOMEMLIMIT=4GiB`. Integration tests use only
`postgres://postgres@localhost:54329/postgres?sslmode=disable`, with per-test
schemas and test lock scopes. No initdb was run.

**Final results:** `make check` exit 0 (format, zero lint issues, full race-enabled
short suite, build); focused tests exit 0; six legacy reporting fixtures, six
complete CLI outputs and 56 complete HTTP responses match. The build embeds
`e16a98c-dirty` from the committed implementation (only evidence files were
dirty while the build completed);
`execution.json` retains the binary SHA-256 and exact source commit.

The tests cover exact live routing before auth/key discovery; health auth and
method/query/body validation; closed test-DB health 503 with live 200; worker
mutex contention on authority status and health; zero queues; per-queue
isolation; pending retirement/recovery obligations; successful versus failed
and report-only turns; independent observed/verified ages and missing evidence;
hidden, violated, repaired and stale applied inputs, including sticky violations
through hidden reads; snapshot restart; warning
visibility without failure; cancellation; queue/output/detail bounds; actual
webhook backstop, rules/validation retry, quota and error deadlines; repeated
on-time failures becoming degraded; and restart losing process success times.

The first in-flight full check started before the rollback fix; its newly
extended regression ran against the earlier status build and blocked after
rollback. A subsequent formatting check caught one import indentation. Both
were superseded by the final full check on the committed implementation;
`superseded-make-check.log` and `superseded-format-check.log` retain those results.
Focused tests passed with the rollback fix before its formatting-only amend.

The ledger integration adds:

- `TestOutsideWarningsLedgerStatusHealthAndAcknowledgement`: status/list/health
  warning IDs and count, HTTP 200, restart, acknowledgement, unchanged history,
  and failed refresh preserving the previous snapshot.
- `TestOutsideWarningsBoundedAcrossPagesAndAuthorityRollback`: 300 outstanding
  warnings across ledger pages with exactly 20 retained IDs; acknowledge an entry
  older than recent history, retain 299 warnings across rollback, and match health.
- `TestGitHubOutsidePushHasNoAuthorityWarnings`: default and explicitly pinned
  GitHub queues retain outside-push history without authority warnings.
- `TestOutsideWarningsAuditLoopPublishesWhileWorkerBusy`: the actual independent
  audit loop publishes acknowledgement while the worker mutex stays held.
- Extended `TestAuthorityStatusAndHealthDoNotWaitForWorker`: nonempty ledger
  warnings remain visible in status/list/health with the worker mutex held,
  including after rollback to GitHub mode.

PR 5's existing acknowledgement, delayed-audit eligibility, snapshot and 5,000 /
50,000-row bounded-page tests also pass after the shared reader extraction.

`TestHealthRealGitUnavailableRemote` uses a fresh bare remote and cache from
`newCompositionFixture`, with deterministic source commit inputs (Fixture author,
source timestamp 1700000000) and a fake forge adapter. Production Git performs a
healthy reconciliation, then the test renames its local remote out of service.
Both a queue settled by the production real-git abandon path and a durable
pending landing retain last-success
and publish the actual five-second retry deadline. A failed remote never settles
the pending attempt. These local refs are test artifacts, not sandbox/GitHub
acceptance evidence.

Health request DB work is exactly `SELECT 1`, independent of queue/event count;
it acquires no queue/event locks and loads no journals. The HTTP deadline bounds
pool acquisition and statement execution to 2s. The registry uses TryRLock,
worker values are immutable atomic snapshots, queue count is capped at 256,
visibility details at 64 per queue with explicit omission counts, and complete
responses at 256 KiB. Oversized output fails before any partial response.

## Legacy compatibility

The required probe is:

```
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB python3 scripts/check-legacy-reporting.py /home/riels/.cache/adr0008-6-main
```

It compares six legacy check outputs and serialized existing status fields,
including field order, and checks its negative controls. It only permits the
preexisting Stage A `change`/`landing` additive fields; it does not strip the new
authority or warning fields.

The additional complete-response probe is:

```
flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB python3 scripts/check-legacy-authority-http.py /home/riels/.cache/adr0008-6-main
```

It compares **56 entire HTTP responses**, including status line, every header
and complete body, with no field stripping. Coverage includes list, repository,
queue, PR detail and events-limited detail, invalid query and missing route;
GET/POST; configured/unconfigured tokens; and valid/missing credentials. It
uses Go overlays and deterministic local fixtures, never a running listener.

A separate `scripts/check-legacy-authority-cli.py` overlay compares six complete
CLI outputs (empty, idle, waiting, paused, active and outside-push warning) against
the same baseline, without stripping fields or whitespace. It retains both sides
as base64 and their hashes in the artifact directory.

For queues without applied OpenMerge authority, `authority` is omitted. For
queues without outstanding authority-era warnings, `outside_warnings` is omitted.
Existing
`outside_pushes`, deployed check text and preexisting JSON ordering are retained.
The two new routes have their own response contracts and do not change existing
routes.

Execution receipts and SHA-256 manifest accompany this file in
`adr-0008-6-artifacts/`. The complete HTTP response receipt stores both sides
as base64 plus SHA-256 hashes. `health-query-plan.txt` retains one diagnostic
test-DB EXPLAIN/ANALYZE for SELECT 1; its sample timing is not a service SLO. Sandbox healthy/unverified/violation/drift/outside-warning HTTP
responses remain unavailable here and are explicitly deferred to PR 11.
