# ADR 0008 PR 6 reporting and health evidence

Trial class: **HOLD**. Merge is permitted; deployment is prohibited until ADR
0009 period B drains. No deployment, live DB access, live status API request,
real GitHub call in code/tests, live ref access, migration, wire event, public
YAML/schema change, or Makefile/CI/test-flag change is part of this work.

## Revision and dependency scope

Initial baseline: `d4e15c89fa9a1e68d8cf9afe55d0f3a25318ae91` (fetched
2026-10-20). Both #112 and #113 were OPEN at start. PR 4 reached main during
final verification, so the final implementation was rebased and integrated.
Final tested main baseline: `158afe96c43a6122edca8566b09ba0ebf2a1e4bd`.
Implementation commit: `5ecebebf9301f39df2ed14dae25e40ce9c8dc0d0`. The final evidence commit changes
only receipts/documentation. Branch: `adr0008/6-authority-status`.
This PR does not stack on #112 or #113.

`internal/status/authority.go: ProjectAuthority` directly consumes PR 4's
`WriteRestrictionStatus` durable state, last-observed and last-verified times.
It renders sticky violations through hidden reads without duplicating the
source's finding fold. Visibility uses retained applied evidence. Proposed verification remains excluded from applied rendering.
`QueueOutsideWarnings` and the worker's `publishHealth` mark PR 5 ledger seams.
The `outside_warnings` DTO and CLI/HTTP rendering are tested, but production
leaves the field absent until the actual ledger is available. No warning count
is inferred from the existing recent `outside_pushes` list. Runtime authority
activation remains PR 9 work. Real organization HTTP acceptance is PR 11 work.

## Automated acceptance

All Go processes, formatting, Python probes that invoke Go, and `make check`
run under `/home/riels/.cache/openmerge-heavy.lock`, with `GOFLAGS=-p=2` and
`GOMEMLIMIT=4GiB`. Integration tests use only
`postgres://postgres@localhost:54329/postgres?sslmode=disable`, with per-test
schemas and test lock scopes. No initdb was run.

**Final results:** `make check` exit 0 (format, zero lint issues, full race-enabled
short suite, build); focused tests exit 0; six legacy reporting fixtures, six
complete CLI outputs and 56 complete HTTP responses match. The build embeds
`5ecebeb-dirty` from the committed implementation (only evidence files were
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

For queues without applied OpenMerge authority, `authority` is omitted. Until
PR 5's real ledger is wired, `outside_warnings` is omitted too. Existing
`outside_pushes`, deployed check text and preexisting JSON ordering are retained.
The two new routes have their own response contracts and do not change existing
routes.

Execution receipts and SHA-256 manifest accompany this file in
`adr-0008-6-artifacts/`. The complete HTTP response receipt stores both sides
as base64 plus SHA-256 hashes. `health-query-plan.txt` retains one diagnostic
test-DB EXPLAIN/ANALYZE for SELECT 1; its sample timing is not a service SLO. Sandbox healthy/unverified/violation/drift/outside-warning HTTP
responses remain unavailable here and are explicitly deferred to PR 11.
