# Phase 2 dogfooding: `max_depth: 4` on this repository

ADR 0009 changes the default `max_depth` from 1 to 4 only after this project's
own queue has run `max_depth: 4` for two weeks and at least 50 changes, with no
incorrect landing, and with runs per change and time to merge no worse than an
adjacent depth-1 period of equal length under the same CI configuration
([ADR 0009](../adr/0009-speculative-batching.md#dogfooding-notes)).

## Plan

| Period | Setting | Starts | Ends |
|---|---|---|---|
| A | `speculation.max_depth: 4` (W adaptive, `min_depth: 1`) | at the journal's `ConfigApplied` for the depth-4 policy | after 14 days, at least 50 landed changes, **and** the engagement floor below |
| B | default depth 1 | at the journal's `ConfigApplied` for the depth-1 policy, pushed when A ends | after as many days as A **and** at least 50 landed changes |

**Engagement floor.** Our arrivals are bursty and often one change at a time, so
period A could run for weeks without ever having two batches open. A cannot end
until at least 10 of its landed changes were tested in a batch at chain position 2
or deeper (built on an unlanded batch). If A reaches 6 weeks without meeting the
floor, the result is recorded as inconclusive and the default does not change.

**Cohorts.** A change belongs to the period in which its counted admission (below)
was accepted. Changes admitted in A whose landing follows B's `ConfigApplied` are
excluded from both periods; the configuration change invalidates their batches
anyway.

This queue already exists, so W carries its depth-1 value into the new ceiling:
it starts at 1 and grows by one per landing (1 → 2 → 3 → 4) unless a confirmed
checks failure halves it. Only a new queue starts at `max_depth`.

Both periods use the same required checks (`check` and `sim-exits`). The
comparison that decides the default must consist of two adjacent periods that
each meet their gates under an identical CI configuration. Days are never
excluded piecemeal. The log records the server revision at each period boundary
and every upgrade or CI change in between. A change is **material**, and restarts
both periods, if it alters the CI workflow's jobs or runners, or changes how the
server plans batches, lands, or observes checks. Other upgrades are logged and do
not restart anything. The maintainer classifies each change when it is logged,
before looking at either period's metrics.

The live GitHub App has not been granted `actions: write`, so same-commit check
retries and CI cancellation report as unsupported during period A: confirmed
failures are not rerun, and invalidated runs complete. That is the documented
fallback; the runs that cancellation would have saved simply count against A, as
in the simulator's unsupported-cancellation gate. CI runs on GitHub-hosted
runners, so depth 4 has enough runner capacity for up to four concurrent batches.

## Metrics

Each metric uses a source that records the same thing at depth 1 and depth 4.

- **Time to merge:** per landed change, from the `At` of the accepted
  `enqueue_requested` admission that led to its landing (the latest one, if the
  change was re-enqueued) to the `RecordedAt` of its `landed` journal event. The
  time from its first enqueue is reported as a secondary figure, as the simulator
  does.
- **Runs per merged change:** the number of distinct GitHub Actions run attempts
  of the `CI` workflow (one per tested commit, covering both required jobs) on
  `openmerge/queue/**` refs during the period,
  counting each run once and each rerun attempt separately, divided by changes
  landed in the period. This comes from the Actions API
  (`GET /repos/{owner}/{repo}/actions/runs`, filtered by branch prefix and
  `run_attempt`), not from the status ledger: at depth 1 the ledger records only
  an aggregate result per batch, and the ledger attributes a speculative run to
  every change in its chain, so summing per-change ledgers would count shared runs
  several times. The count includes failed runs and runs of invalidated batches,
  which is the cost speculation adds.
- **Incorrect landings:** checked against git and GitHub, not only the journal:
  every commit added to `main` by the queue must equal the recorded tested commit
  (or, for fast-forward, the verified head beneath it); `main` must only move
  forward (each new head descends from the previous one); and the `CI` workflow
  must have succeeded on that tested commit. The target is zero. An audit script
  performs these checks daily from the second day of A, so the abort condition
  can actually fire.
- **Window behaviour (period A only):** W over time, halving events, the share
  of non-idle time with more than one batch open, and the chain position of each
  landed change, reconstructed by replaying the journal (status only exposes the
  current W and the last halving).
- **Traffic:** arrivals per day and queue length at each admission, per period,
  so differences in load are visible next to the result.

### Decision rule

Fixed before A starts. Percentiles use the nearest-rank method over the period's
landed changes. A passes if, against B: median time to merge is no worse, 95th
percentile time to merge is no worse, runs per merged change is no worse, and
there were no incorrect landings in either period. If arrivals per day differ by
more than a factor of two between the periods, the comparison is recorded as not
comparable and B is extended until the factor is within two, or both periods are
rerun.

The measurement and audit scripts must exist and be reviewed before A's second
day; they read the live journal read-only and the Actions API.

## Abort conditions

Period A ends, and `max_depth` returns to 1, on any incorrect landing, any queue
wedge lasting over an hour, or any failure the operator cannot explain from
status and logs. A wedge is a queue that records no journal event for an hour
while a change is queued and no required CI run for it is still in progress. An
aborted A is void: it is not converted into B, and the plan restarts from A after
the cause is fixed. The rollback must not depend on the queue making progress:

1. **Contain.** Stop the server (`systemctl --user stop openmerge`). A landing
   whose push was already accepted has landed; one that was not settles through
   the fence when the server next runs. After an incorrect landing, revert the
   offending commit on `main` directly as an administrator.
2. **Change the policy outside the queue.** Commit `.openmerge.yml` without the
   `speculation` block and push it directly to `main` as an administrator. Do not
   submit the rollback through the queue, which may be the thing that is stuck.
3. **Restart and verify.** Start the server, then confirm with
   `openmerge queue status` that the configuration change was applied and the
   queue reports `window: 1`. Configuration changes invalidate open speculative
   batches; their changes return to the queue in order.
4. **Record.** Add the incident to the log below and file an issue.

## Log

| Date | Event |
|---|---|
| 2026-10-17 | Plan adopted. Live server on a4b173f (ADR 0009 PR 5). Depth-1 history before this date is not comparable: CI changed with #46. |
