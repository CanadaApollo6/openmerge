# Phase 2 dogfooding: `max_depth: 4` on this repository

ADR 0009 changes the default `max_depth` from 1 to 4 only after this project's
own queue has run `max_depth: 4` for two weeks and at least 50 changes, with no
incorrect landing, and with runs per change and time to merge no worse than an
adjacent depth-1 period of equal length under the same CI configuration
([ADR 0009](../adr/0009-speculative-batching.md#dogfooding-notes)).

## Plan

| Period | Setting | Starts | Ends |
|---|---|---|---|
| A | `speculation.max_depth: 4` (W adaptive, `min_depth: 1`) | at the journal's `ConfigApplied` for the depth-4 policy | its admission window closes once it has lasted at least 14 days, at least 50 of its admitted changes have landed, **and** the engagement floor below is met; then the queue drains |
| B | default depth 1 | at the journal's `ConfigApplied` for the depth-1 policy, pushed once A has drained | its admission window closes after exactly as long as A's; then the queue drains |

**Drained boundaries.** Each period has an *admission window* (from its
`ConfigApplied` to the moment it closes) and a *drain*. During the drain no new
change is queued; it ends when every change admitted in the window has landed or
left the queue (`openmerge queue status --json` shows no entries, no active batch
and no chain) **and** no `CI` run on a queue ref of that period's batches is still
queued or in progress (runs of discarded batches keep running when cancellation
is unsupported). The next period's configuration is pushed only after the drain.
So every change and every landing belongs to exactly one period, no change
straddles a configuration switch, and none is cut off by a period's end. The
report logs each drain's duration and how many arrivals were held during it, and
reports B's latency with and without the changes held over from A's drain, as a
sensitivity figure.
The queue is idle at A's start. Changes that arrive during a drain wait,
unqueued, for the next period.

**Engagement floor.** Our arrivals are bursty and often one change at a time, so
period A could run for weeks without ever having two batches open. A's admission
window cannot close until at least 10 of its changes **landed from** a batch at
chain position 2 or deeper (the recorded position of the batch that landed them,
built on an unlanded batch and actually tested), and at least 3 landed from
position 3 or deeper, so the evidence for a default of 4 is not gathered entirely
at depth 2. If A reaches 6 weeks without meeting the floor, the pair is recorded
as inconclusive and the default does not change.

**Cohorts.** A period's cohort is the changes whose counted admission (below) was
accepted in its admission window. Drained boundaries make this the same set as
the changes that landed or left during the period.

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

ADR 0007 Stage A upgrades follow its
[trial deployment policy](../adr/0007-changes-not-prs.md#trial-deployment).
During this pair, only PRs 1–7, 10 and 12 (including their inert splits) are
eligible, each with the named differential evidence and a log entry. PRs 8, 9
and 11 and their splits stay off the live server until period B drains. Do not
add `approvals` to this queue until the pair closes or change the live App's
permissions mid-trial, including `members: read`. Merging behind an opt-in does
not itself make an upgrade inert; a change to batching, landing or check
observation restarts both periods.

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
- **Runs per merged change:** the number of GitHub Actions run attempts of the
  `CI` workflow (one per tested commit, covering both required jobs) on
  `openmerge/queue/**` refs testing batches the period started, divided by the
  cohort's landed changes. Every attempt counts once: list runs from
  `GET /repos/{owner}/{repo}/actions/runs` (the `branch` filter is exact-match,
  so the `openmerge/queue/` prefix is filtered client-side on `head_branch`), then
  enumerate attempts 1 through `run_attempt` of each run with
  `GET .../actions/runs/{run_id}/attempts/{n}`, deduplicate by
  `(run_id, attempt)`, and assign each attempt to the period whose journal
  started the batch that its queue ref (`openmerge/queue/<base>/<batch>`) names,
  whenever the attempt itself ran. Waiting for CI in the drain keeps that
  attribution and attempt start times consistent.
  The source is the same at both depths, unlike the status ledger, which records
  only an aggregate per batch at depth 1 and attributes a speculative run to every
  change in its chain. The count includes failed runs and runs of invalidated
  batches, which is the cost speculation adds.
- **Outcomes:** landed and dequeued counts per period, so a period that ejects
  more changes cannot hide them from latency, which only counts landed changes.
- **Incorrect landings:** checked against git and GitHub, not only the journal:
  every commit added to `main` by the queue must equal the recorded tested commit
  (or, for fast-forward, the verified head beneath it); `main` must only move
  forward (each new head descends from the previous one); and the `CI` workflow
  must have succeeded on that tested commit. The target is zero. An audit script
  performs these checks daily from the second day of A through the end of B's
  drain, so the abort condition can fire and both periods are covered.
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
there were no incorrect landings in either period. The comparative metrics are
computed after B's drain. The eligibility gates are checked when B's admission
window closes at A's length, never by extending B: if B then has
fewer than 50 landed changes, or arrivals per day differ by more than a factor of
two between the periods, the pair is recorded as inconclusive.

Every pair that is started is logged with its outcome: pass, fail, inconclusive
or aborted. A pair that fails the rule leaves the default at 1. A new pair may be
run only after a logged change to the system that addresses the failure or the
cause of inconclusiveness, never as a retrial of the same system, so repeated
attempts cannot produce a pass by chance.

The measurement and audit scripts must exist and be reviewed before A's second
day; they read the live journal read-only and the Actions API.

The [dogfood tool's invocation guide](../../tools/dogfood/README.md#deployment-invocation)
documents `audit --git-repo` and repeatable `--ci-workflow <path>` selection for
`audit` and `report`. This repository's trial keeps the default
`.github/workflows/ci.yml`; its outputs and attempt counting remain unchanged.
For repositories with several queue workflows, audit requires every selected
workflow to succeed before `LandingStarted`. Reports sum attempts across those
workflows, including reruns. Use the same selection for audits and both reports
and record it in `--ci-config`; `compare` remains offline.

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
   batches; their changes return to the queue in order. On restart the server
   first settles any outstanding landing, and only then applies configuration. If
   settlement cannot complete (for example the recorded landing is missing from
   `main`), the configuration is not applied: stop the server again and leave the
   incident contained until it is recovered by hand. Use ordinary forward pushes
   (a revert commit, not a reset or force-push) so an accepted landing stays in
   `main`'s history and settlement can succeed.
4. **Record.** Add the incident to the log below and file an issue.

## Log

| Date | Event |
|---|---|
| 2026-10-17 | Plan adopted. Live server on a4b173f (ADR 0009 PR 5). Depth-1 history before this date is not comparable: CI changed with #46. |
| 2026-10-17 | **Period A started** at 21:35:24Z: `ConfigApplied` journal event 1287 for configuration commit 6fa7d08 (`max_depth: 4`, `min_depth: 1`). Server revision a4b173f. W starts at 1. The live App still lacks `actions: write`, so retries and cancellation are unsupported. |
| 2026-10-17 | First depth-4 landing (#50, batch b27, position 1); W grew to 2. Audit since the start: 1 landing, 0 violations. |
| 2026-10-18 | **Inert deploy, non-material** at 05:13:04Z: live server a4b173f → 3f77a88 (17 PRs, #47–#60 and #63–#65; no held Stage A PR; no configuration or permission change — #48's depth-4 config was already applied at period A's start). Replay gates 2 and 4 PASS on the live journal (watermark event 2158; actions and structural state 2159/2159); complementary checks pass (outside-push isolation exits 1 vs a4b173f on PR 1's expected `Apply` hunks and 0 vs #53's parent, as explained in the record); window 4 retained. Full record, commands, per-PR verdicts and rollback: [deploys/2026-10-18-3f77a88.md](deploys/2026-10-18-3f77a88.md). |
| 2026-10-19 | Cumulative audit from A's start to 23:34:40Z: 32 landings checked, 0 violations (`audit --git-repo` on a full clone with `main` at 4e5a812). No separate audit was run for 2026-10-18; this one covers it. The same audit with `--git-cache` reported 64 violations, all `tested_commit`/`main_forward` failures on missing objects: the server's per-installation cache held no objects after its periodic GC. Those were missing evidence, not incorrect landings; the tool's README now says to audit from a full clone. Gate progress: about 2 of 14 days, 32 of 50 landings, engagement floor 3 of 10 landings from position ≥ 2 (#55 b35, #53 b37, #66 b42) and 0 of 3 from position ≥ 3. Live server still 3f77a88; nothing merged since #65 is deployed. |
