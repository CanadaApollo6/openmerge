# ADR 0009 dogfooding measurements

Run this separate program with `mise exec -- go run ./tools/dogfood`. It is not
included in the `openmerge` binary. Its measurement contract is the
[pre-registered plan](https://github.com/CanadaApollo6/openmerge/blob/1dbf8a7/docs/research/phase2-dogfood.md)
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
read-only, with `statement_timeout` and `idle_in_transaction_session_timeout`
set to the read deadline and `lock_timeout` capped at five seconds. The default
journal deadline is **two minutes**, overridable with `--journal-timeout 30s`
(minimum 1ms). A bounded context covers connection acquisition and the entire
snapshot read, including all queries and decoding. The existing `Store.Journals`
reader places **every query** in an explicit `REPEATABLE READ, READ ONLY` transaction. No queue discovery, locks,
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
period. `--until` defaults to the invocation time. Run the audit **daily from
A's second day through the end of B's drain**, including both drains. Keep `--since` at A's start for overlapping cumulative
audits, or retain each daily result and run a final full-period audit before
comparison. A one-day audit only covers that day. The final audit must extend
past B's drain end (the audit upper bound is exclusive). The full journal is
replayed to recover tested batches and the previous queue head, including before
`--since`.
Only landings in `[since, until)` cause per-landing git and API checks. Actions lookups
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
- the new head strictly descends from the base the batch was tested on and the
  previous queue landing, even if that landing predates `--since`. A later
  administrator push may descend from the queue landing and is allowed;
- independently read `GET /repos/{owner}/{repo}/branches/{base}` (normally
  `main`) once, then `GET .../compare/{landed}...{tip}` for each landing. Only
  `ahead` or `identical` proves the landing is still in current main's history.
  A rewind that removes a landing is a violation. Missing branch/comparison
  evidence fails closed; no fetch or cache-ref mutation is needed;
- an attempt of **`.github/workflows/ci.yml`** on an `openmerge/queue/` branch
  concluded `success` on the tested commit **strictly before `LandingStarted`**.
  Use the earlier of that event's `At` and journal `RecordedAt`; when replaying
  a seeded in-flight attempt, require its recorded `StartedAt`. The durable
  event precedes the push, so this is a conservative deadline even if it rejects
  a success between starting the attempt and pushing. Settlement's `Landed`
  timestamp can be delayed and never extends the deadline. Missing deadline
  evidence fails closed.

An audit exits **0** on success and **1** on any violation or inability to
complete verification. JSON output includes event IDs and individual violations;
operational errors go to stderr and do not produce a successful audit document.
Audit JSON version **2** records the independently read `base_tip`. Regenerate
version-1 audits: comparison and attachment reject them. This audits journaled
queue pushes; administrator pushes are allowed while preserving those landings.

## Boundaries and reports

Each period has an admission window `[ConfigApplied, close)` and a drain. At A's
start the queue must be idle. Record its exact `ConfigApplied` event ID and server
revision. Keep the change/incident log throughout both windows and drains.

At **each** boundary the operator must:

1. Record the admission-window close time and **stop queueing new changes**.
   A can close only after ≥14 days, ≥50 cohort landings, ≥10 cohort changes
   landed from tested batches at position ≥2, and ≥3 from position ≥3. If both
   engagement thresholds are not reached by six weeks, record inconclusive.
   B closes at exactly `B start + A admission-window length`; never extend B.
2. Wait for the drain. `openmerge queue status --json` must show **no entries,
   no active batch and no chain**, and no `CI` run may remain queued or in
   progress on any queue ref of that period's batches. Include discarded
   batches: cancellation is unsupported and their runs can keep running.
3. Run the report with that fixed close time; check `drain_complete`, its
   computed end/duration, outcomes and warnings. Continue daily audits.
4. **Only after the drain**, push the next configuration directly as prescribed
   by the plan, and record its `ConfigApplied` event/server revision. Arrivals
   held during A's drain can now be admitted in B. After B, run the final audit
   through B's drain, regenerate both reports from the final journal/API
   history, and compare them. A later rerun on an old batch remains charged to
   its owning period and may reveal an invalid premature configuration switch.

`--from` identifies the period's starting `ConfigApplied`: an event ID
(`event:123` or `123`), exact config commit (`commit:<full SHA>` or full SHA), or
an RFC3339 timestamp identifying exactly one config event. Prefer event IDs:
these are database journal IDs and preserve order at identical timestamps.
`--to` is the **operator-supplied, exclusive admission close**, normally an
RFC3339 timestamp with fractional seconds as necessary. It also accepts a
journal/config boundary, but is not the next period's start when a drain is
needed. Neither the journal nor Actions listing is cut off at this close.

```sh
mise exec -- go run ./tools/dogfood report \
  --database-url "$OM_DOGFOOD_DB" \
  --installation "$OM_DOGFOOD_INSTALLATION" --repository "$OM_DOGFOOD_REPO" \
  --from event:123 --to '2026-10-31T12:00:00Z' --audit audit.json \
  --ci-config 'CI check+sim-exits, workflow <commit>' --log-reviewed --json > A.json

mise exec -- go run ./tools/dogfood report \
  --database-url "$OM_DOGFOOD_DB" \
  --installation "$OM_DOGFOOD_INSTALLATION" --repository "$OM_DOGFOOD_REPO" \
  --from event:456 --to '2026-11-14T15:00:00Z' --audit audit.json \
  --ci-config 'CI check+sim-exits, workflow <commit>' --log-reviewed --json > B.json

mise exec -- go run ./tools/dogfood compare A.json B.json
# Or compare --json A.json B.json
```

Replace all dates/IDs: the examples assume A starts October 17 at 12:00 and B
starts October 31 at 15:00, after a three-hour A drain. Both windows last exactly
14 days. JSON report version **2** replaces the old time-sliced contract;
regenerate old reports. Human output includes per-change timings/positions,
outcomes, admission queue lengths, W/open-batch transitions, halvings and drain
warnings. JSON also retains attempts, started batch IDs, individual dequeues and
held arrivals. An incomplete drain emits a provisional report and exits **1**;
`compare` cannot pass it. With an incomplete drain, duration is elapsed time
since close and `drain_end` remains unset. Boundary warnings make comparison
inconclusive even if the queue eventually drains.

`--ci-config` identifies the identical CI setup across both periods, not their
server revisions. The operator log must record revisions and every upgrade,
and classify material changes before reviewing metrics. `--log-reviewed`
asserts no material CI/server change, incorrect landing, hour-long wedge or
unexplained failure over the window **and drain**. Missing confirmation or audit
makes comparison inconclusive. Use `--aborted-reason '...'` to record an abort
from the operator log; the tool cannot reconstruct historical wedges or
unexplained failures and never issues rollback commands.

An attached audit must cover the exact queue and **all** landing events from the
start through the computed drain end, including landings outside the cohort.
The audit upper bound is exclusive, so use a time strictly after drain end.
Violations within that interval are retained; unrelated later violations are
excluded when attaching a wider audit. Incorrect landings immediately give an
aborted pair, regardless of sample size.

### Metric definitions

- **Cohort and outcomes:** replay from the journal seed through `queue.Apply`.
  An `enqueue_requested` counts only if it creates an accepted admission inside
  the window. Rejected/duplicate requests do not reset the clock. Follow each
  counted admission until it lands or leaves the queue, including in the drain;
  report landed, dequeued and pending counts. Re-enqueues are distinct counted
  admissions. The gate retains the landed count **at admission close**, so drain
  landings cannot rescue a short sample. Queue length includes all entries.
- **Latency:** latest counted admission's original `At` → landing journal
  `RecordedAt`; the secondary figure uses the PR's first accepted enqueue.
  Median/p95 are nearest rank, without interpolation. Latency includes only
  cohort landings. Zero scalar values with a zero count are undefined
  placeholders, never evidence of fast merges.
- **Engagement:** use the recorded position of the batch that **actually landed**
  each change, with recorded successful checks on that batch's tested commit.
  A batch that was merely started, or a position-2 test discarded before the
  change later lands at position 1, contributes nothing to the floor. Promotion
  preserves a landing batch's original position. Require both ≥10 at position
  ≥2 and ≥3 at position ≥3 **before A closes**, and by the six-week deadline.
- **Actions cost:** list the entire repository's runs with
  `GET /repos/{owner}/{repo}/actions/runs`, filter workflow path
  `.github/workflows/ci.yml` and the period's **exact batch refs before expanding
  attempts**; never use the API's exact `branch` filter for a prefix. Display
  names are not identity. Fetch **every** attempt 1..`run_attempt` through
  `GET .../actions/runs/{run_id}/attempts/{n}` and deduplicate `(run_id, attempt)`.
  Attribute by the exact queue ref `openmerge/queue/<base>/<batch>` to the period
  whose replay accepted that batch's start, including starts during its drain.
  The single `nextBatch` counter survives `ConfigApplied`; batch IDs do not
  repeat between periods. Include failed, cancelled and discarded-batch runs,
  and reruns after close or an apparent drain, regardless of attempt time.
  Divide **all** these attempts by the cohort's eventual landed changes.
- **Drain:** compute the first post-close idle queue state with all cohort
  admissions settled, and wait for every attempt on the period's batch refs to
  complete. The end is the later of queue settlement and those terminal times.
  Actions exposes `updated_at`, which is used conservatively as the completion
  timestamp; this may overestimate drain duration. Any nonterminal attempt,
  unsettled cohort or nonempty queue prevents completion. Applying the next
  config before this end is a boundary violation. No time filter discards a
  late attempt to make a boundary look valid.
- **Held arrivals (first-eligibility lower bound):** count distinct PRs whose **first journal observation
  with `pr_observed eligible=true`** is in `[close, drain_end)` and whose first
  accepted admission is at/after drain end, or has not happened yet. Record PR,
  first eligibility and later admission time (zero if still waiting). Exclude
  previously eligible or previously admitted PRs and duplicates. This is a
  first-eligibility lower bound, not GitHub creation time or a complete count of
  demand: a PR never observed by the server cannot be counted. `compare` matches
  A's held PRs to B's cohort landings and reports B median/p95 and sample size
  both with and without those holdovers. Regenerating A after B captures later
  admissions. Returning PRs that were eligible or admitted before the drain are
  excluded from this lower bound: the **without-holdover sensitivity can still
  include returning holdovers**. Both reports must be regenerated after both
  drain ends before comparison.
- **W and utilization:** actual fold supplies W, bounds, halvings and batches.
  Integrate non-idle (entries or batches) and multiple-batch time over the
  admission window. Dwell time uses the journal timestamp high-water mark to
  avoid double-counting transaction timestamp reordering; latency retains the
  original event timestamps. Arrivals/day divides accepted admissions by the
  fixed admission-window duration, with UTC daily counts alongside it.

The API adapter paginates unfiltered report listings, avoiding the 1,000-result
cap on filtered searches. Audit lookups use `head_sha` and fail closed on that
cap or incomplete pages, missing objects or missing attempt metadata. See
[GitHub's workflow-run API contract](https://docs.github.com/en/rest/actions/workflow-runs).

### Comparison and exit status

`compare` is offline. It requires both reports to have been generated at or
after **both** drain ends, recomputes percentiles, arrivals/day and UTC daily
counts, runs/merge, at-close landings and engagement from the evidence arrays,
and rejects mismatching summary fields. Every cohort landed-event ID must be
covered by the attached audit's `checked_event_ids`. It returns exactly one
pair outcome, with reasons:

- **0 / pass:** A median, p95 and runs/merge are each no worse than B after
  **B's drain**; zero incorrect landings in both periods; all gates below pass.
- **1 / fail:** an inferior comparative metric after all eligibility gates pass.
- **2 / inconclusive:** either period has fewer than 50 landed at window close,
  A is shorter than 14 days, B's window is not exactly A's length, arrivals/day
  differ by more than 2× (exactly 2× is allowed), or engagement is insufficient.
  A reaching six weeks without **both** thresholds is inconclusive; later
  landings cannot rescue it. Also covers missing/incomplete drain, audit, log,
  depth, adjacency or CI evidence. **Never extend B** or remove/splice days.
- **3 / aborted:** any audited incorrect landing or `--aborted-reason` from the
  operator log (including an hour-long wedge or unexplained failure).

Periods must be adjacent at the next recorded `ConfigApplied`, after A's drain,
with max depths 4 then 1 and identical confirmed CI. Boundary mistakes and
material changes cannot produce a pass. Fail, inconclusive and aborted leave
the default unchanged; rerun only after logging a change addressing the cause.
Input/transport failures exit 1 without claiming a completed comparison.

For exact exit codes build the standalone tool with
`mise exec -- go build -o /tmp/openmerge-dogfood-report ./tools/dogfood`;
`go run` itself returns 1 when the child exits 2 or 3.

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
