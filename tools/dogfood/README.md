# ADR 0009 dogfooding measurements

Run this separate program with `mise exec -- go run ./tools/dogfood`. It is not
included in the `openmerge` binary. Its measurement contract is the
[pre-registered plan](https://github.com/CanadaApollo6/openmerge/blob/0dbcf7f/docs/research/phase2-dogfood.md)
from PR #48. It does not change queue policy or operate the queue.

## Deployment invocation

These are commands for the operator to run. Development and tests use fixtures
and `OPENMERGE_TEST_DATABASE_URL`, never the deployment database or git cache.

```sh
export OM_DOGFOOD_DB="host=$HOME/.local/share/openmerge/run user=postgres dbname=openmerge sslmode=disable"
export OM_DOGFOOD_INSTALLATION='YOUR_INSTALLATION_ID'
# Use the exact repository spelling and lane recorded in this installation's journal.
export OM_DOGFOOD_REPO='CanadaApollo6/openmerge'
# Export GH_TOKEN with Actions read permission, or authenticate with gh auth login.
```

The connection string is required: there is no implicit deployment connection.
The only added internal API is `store.OpenJournalReader`, a restricted journal
reader that skips migrations and connection pings. Connections default to
read-only; the existing `Store.Journals` reader places **every query** in an
explicit `REPEATABLE READ, READ ONLY` transaction. No queue discovery, locks,
commands, schema changes, or snapshot writes run through this tool. Supply
`--lane` if the target has a nonempty lane; `--base` defaults to `main`.

First audit the full period, or use a wider audit covering both periods:

```sh
mise exec -- go run ./tools/dogfood audit \
  --database-url "$OM_DOGFOOD_DB" \
  --installation "$OM_DOGFOOD_INSTALLATION" --repository "$OM_DOGFOOD_REPO" \
  --since '2026-10-17T12:00:00Z' --until '2026-11-14T12:00:00Z' \
  --git-cache "$HOME/.local/share/openmerge/git-cache" --json > audit.json
```

The dates above illustrate the syntax; replace them with the actual completed
period. `--until` defaults to the invocation time. For a daily audit starting on
A's second day, set `--since` to the previous day's audit start or end (overlap
is safe). An audit of one day only covers that day; before comparing, run an
audit spanning each complete report period. The full journal is replayed to
recover tested batches and the previous queue head, including before `--since`.
Only landings in `[since, until)` cause git and Actions checks. Actions lookups
are cached by tested SHA within the invocation.

Alternatively pass `--git-repo /path/to/complete/clone-or-bare.git`. The two git
flags are mutually exclusive. `--git-cache` selects the existing
`<sha256(exact owner/repo)>.git` beneath the supplied cache root. It never opens
`gitops.Repo`, creates a repository, fetches, prunes, runs GC, or changes a ref.
Git reads disable optional locks, replacement objects and lazy fetching. A
complete clone must already contain tested commits, including fast-forward
markers and commits whose queue refs were deleted. Missing objects or missing
API evidence fail closed; the diagnostic names the event and check.

For each `landed` event, the audit checks:

- the landing is a valid transition under the actual `queue.Apply` fold;
- the new main head is the recorded tested commit; for fast-forward, it is the
  head pinned in the batch and landing attempt, and the tested marker has that
  head as its **only parent with an identical tree**;
- the new head strictly descends from the tested base, any intervening observed
  main head, and the previous queue landing, even if that landing predates
  `--since`;
- a `CI` workflow attempt on an `openmerge/queue/` branch concluded `success` on
  the tested commit by the landing's recorded time. A later success cannot
  justify a landing retroactively.

An audit exits **0** on success and **1** on any violation or inability to
complete verification. JSON output includes event IDs and individual violations;
operational errors go to stderr and do not produce a successful audit document.
This audits journaled queue pushes, rather than unrelated administrator pushes.

## Reports

`--from` is inclusive and `--to` exclusive. Each accepts an RFC3339 timestamp
(with fractional seconds if necessary), a `ConfigApplied` journal event ID
(`event:123`, or `123`), or the exact configuration commit (`commit:<full SHA>`,
or the full SHA alone). A commit must identify exactly one configuration event;
use its event ID if the same commit was applied again. Event IDs are database
journal IDs, not the fold's private revision counter. They preserve event order
when a policy event and an admission share a timestamp.

```sh
mise exec -- go run ./tools/dogfood report \
  --database-url "$OM_DOGFOOD_DB" \
  --installation "$OM_DOGFOOD_INSTALLATION" --repository "$OM_DOGFOOD_REPO" \
  --from event:123 --to event:456 --audit audit.json \
  --ci-config 'CI check+sim-exits, workflow <commit>, server change log reviewed' \
  --log-reviewed --json > A.json

mise exec -- go run ./tools/dogfood report \
  --database-url "$OM_DOGFOOD_DB" \
  --installation "$OM_DOGFOOD_INSTALLATION" --repository "$OM_DOGFOOD_REPO" \
  --from event:456 --to '2026-11-14T12:00:00Z' --audit audit.json \
  --ci-config 'CI check+sim-exits, workflow <commit>, server change log reviewed' \
  --log-reviewed --json > B.json

mise exec -- go run ./tools/dogfood compare A.json B.json
# Or compare --json A.json B.json
```

Replace event IDs and dates with the real boundaries. B should initially end at
`B start + A duration`, preserving fractional seconds. Omit `--json` for a human
readout of metrics, per-change timings/positions, admission queue lengths, W and
open-batch transitions, and halving evidence. JSON additionally retains distinct
run attempts and arrival counts by UTC calendar day.

`--ci-config` is an operator-supplied identity for the identical CI setup in both
periods, not each boundary's server revision. Use the log to record the boundary
server revisions and every intervening upgrade. `--log-reviewed` asserts that the
operator has reviewed that whole period and confirmed **no material CI/server
change, incorrect landing, hour-long wedge, or unexplained failure**. Do not set
it for an aborted or materially changed period: the plan restarts both periods.
The tool cannot reconstruct that classification or historical wedge evidence
from present-day workflow status. It never issues rollback commands.

Reports are still useful without these flags or `--audit`, but comparison then
reports inconclusive. An attached audit must cover the exact queue, the full
window, and **every** landed event in it, including excluded cohorts. Audit
violations outside the report window are discarded; violations inside it are
retained even for changes excluded from the latency cohort.

### Metric definitions

- **Admissions and cohorts:** replay every event through `queue.Apply` from the
  store's seed. An `enqueue_requested` counts only if it creates an admission.
  Rejected or duplicate requests do not reset the clock. A landed change uses
  its latest accepted admission and belongs to that admission's period. An A
  admission landing at/after the A→B boundary belongs to neither period. Traffic
  counts all accepted admissions, including re-enqueues and admissions that did
  not land; admission queue length includes the newly admitted change and all
  other entries, including entries in open batches.
- **Latency:** original admission `At` → landing journal `RecordedAt`, in
  seconds. The secondary clock starts at the first accepted enqueue of that PR,
  including pre-period history. Median and p95 use nearest rank, without
  interpolation. With no cohort landings, scalar percentiles and runs/merge are
  zero placeholders and the sample gate prevents a comparison from passing.
- **Engagement:** each cohort landing retains the batch's recorded chain
  position; the serial protocol is position 1. The floor counts changes, not
  batches. A later chain promotion does not erase the original position.
- **W and utilization:** the actual fold supplies W, bounds, halving evidence,
  and admitted open batches. Non-idle means at least one queued entry or open
  batch. Integrate event-to-event wall time, including the final interval up to
  `--to`; the numerator is time with more than one open batch. PostgreSQL records
  transaction start timestamps, which can be slightly out of event order after
  lock contention. Dwell time uses their high-water mark in journal order to
  avoid double-counting; latency always uses the original event timestamps.
- **Actions cost:** count distinct `(run_id, run_attempt)` for workflow name
  `CI` and branch prefix `openmerge/queue/`, including failures, cancelled runs,
  and invalidated batches. A workflow covers both required jobs and counts once
  per attempt. Attempt 1 belongs to its `created_at`; reruns belong to their
  attempt-specific `run_started_at`, obtained through the attempt endpoint.
  A rerun with no start timestamp fails the measurement rather than borrowing
  the first attempt's creation time. Divide by cohort landed changes.

The API adapter paginates `GET /repos/{owner}/{repo}/actions/runs`, filters
workflow/ref/time locally, and expands historical rerun attempts with
`GET /repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}`.
Reports intentionally list the whole repository history: an old run can be
rerun inside the measured period, so filtering by initial creation time would
miss its cost. It also avoids the documented 1,000-result cap on filtered
searches. Audit lookups use `head_sha` and reject that cap or incomplete pages
instead of silently accepting a partial history. See
[GitHub's workflow-run API contract](https://docs.github.com/en/rest/actions/workflow-runs).

### Comparison and exit status

`compare` makes no network or database requests. It returns:

- **0 / pass:** A median, p95 and runs/merge are each no worse than B; both have
  zero audited violations and at least 50 cohort landings; A ran at least 14
  days with at least 10 landings at position ≥2; the periods are adjacent, use
  depths 4 and 1, share the confirmed CI configuration and reviewed log, and
  arrivals/day are within a factor of two (exactly two is allowed).
- **1 / fail:** an audit violation (also the immediate abort trigger), or worse
  median, p95 or runs/merge after all comparability/sample gates are satisfied.
- **2 / inconclusive:** a missing sample, engagement, audit, operator review,
  duration, adjacency, depth, CI or traffic gate. A still missing the engagement
  floor at six weeks is inconclusive; landings after that deadline cannot rescue
  it. The default stays unchanged.

The baseline comparison requires equal durations. The decision rule permits
extending B for traffic: the tool permits a longer B only when its admissions
show that its *initial equal-duration prefix* failed the factor-of-two check.
The full extended B must now satisfy that check. No days are removed or spliced
out. Otherwise unequal periods remain inconclusive. Input/transport errors exit
1 and do not represent a completed comparison.

For the exact `compare` exit code in a script, build the standalone tool with
`mise exec -- go build -o /tmp/openmerge-dogfood-report ./tools/dogfood`; `go run` itself exits 1
when the child exits 2 and prints `exit status 2` to stderr.

## Tests

```sh
mise exec -- go test -race ./tools/dogfood
OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' \
  mise exec -- go test ./internal/store -run '^TestJournalReader' -count=1
```

Tool tests use encoded/decoded in-memory journals, a fake Actions interface,
local HTTP test servers for pagination, and temporary bare git repositories.
Store tests allocate isolated schemas in the explicitly configured test DB.
`make test` includes both packages; `make check` and `make test-sim-exits` retain
the existing CI gates. No test contacts GitHub or deployment services.
