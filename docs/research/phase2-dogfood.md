# Phase 2 dogfooding: `max_depth: 4` on this repository

ADR 0009 changes the default `max_depth` from 1 to 4 only after this project's
own queue has run `max_depth: 4` for two weeks and at least 50 changes, with no
incorrect landing, and with runs per change and time to merge no worse than an
adjacent depth-1 period of equal length under the same CI configuration
([ADR 0009](../adr/0009-speculative-batching.md#dogfooding-notes)).

## Plan

| Period | Setting | Starts | Ends |
|---|---|---|---|
| A | `speculation.max_depth: 4` (W adaptive, `min_depth: 1`) | when this configuration lands on `main` | after 14 days **and** at least 50 landed changes |
| B | default depth 1 | the day period A ends | after the same number of days as A |

Both periods use the same required checks (`check` and `sim-exits`) and the same
live deployment. If CI or the deployment changes materially during a period,
the log below records it, and the comparison either excludes the affected days
from both periods or restarts the affected period.

The live GitHub App has not been granted `actions: write`, so same-commit check
retries and CI cancellation report as unsupported during period A: confirmed
failures are not rerun, and invalidated runs complete. That is the documented
fallback, and the comparison accounts for it rather than assuming either feature.

## Metrics

Taken from the live event log and the status ledger, per landed change:

- **Time to merge:** from the change's `enqueue_requested` event to its landing.
  Reported as median and 95th percentile.
- **Runs per merged change:** CI runs started for batches containing the change,
  including reruns and runs of invalidated batches, divided by landed changes.
- **Incorrect landings:** any landing whose landed commit is not exactly the
  tested commit, any base regression, or any change landed without passing
  required checks on a commit containing it. The target is zero.
- **Window behaviour (period A only):** W over time, halving events, and how
  often more than one batch was open at once. Our arrival pattern is bursty and
  often one change at a time, so the share of time speculation was actually
  engaged is reported alongside the result.

## Abort conditions

Period A ends immediately, and `max_depth` returns to 1, on any incorrect
landing, any queue wedge lasting over an hour, or any failure the operator cannot
explain from status and logs. The incident is recorded below and an issue filed.

## Log

| Date | Event |
|---|---|
| 2026-10-17 | Plan adopted. Live server on a4b173f (ADR 0009 PR 5). Depth-1 history before this date is not comparable: CI changed with #46. |
