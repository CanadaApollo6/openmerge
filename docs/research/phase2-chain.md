# Fixed-window chain: planner-backed adopter results

The real planner and executor use fixed W = 4. Seeds 1–8 each admit 30 days of uniform arrivals, with the ADR 0009 pinned policy: batch size 8, five-minute max wait, 2% broken changes, 1% flaky runs, 50 runners, and timeout 1.5 × CI. The one-second observation/enqueue delay remains in the simulator. No thresholds were changed. This evidence validates depth 4; depths above 4 remain unvalidated.

Both cancellation modes pass through the real planner, durable cancellation
intent and executor's optional forge capability. Supported cancellation stops only
the identified attempts of permanently retired batches and releases their runner
capacity; unsupported cancellation lets those attempts finish. A durable
`observe_retired_checks` follow-up reads pending unsupported attempts until their
exact identities report terminal outcomes, so their ledger results remain current
even if completion webhooks are dropped. Historical observations do not set queue
timers or authorize landing. Neither outcome can revive a retired batch. The adaptive-sizing exit test stays skipped for PR 5. The
PR 1 model includes AIMD and its configured cancellation behavior, so its numerical
predictions are a comparison, not an expected byte-identical trace.

## Pooled results

- Team, either mode: 4,716 merged, 4,690 runs, p50/p95 **35/35 minutes**, **0.99448685 runs/merge**, **2.26887193%** at or above 60 minutes, and **299 wasted runs**. Runner-minutes/merge: **29.74587169 supported / 29.83460560 unsupported**. Passes p95 < 60 minutes and runs/merge < 1.5.
- Slow CI, either mode: 1,179 merged, 1,224 runs, p50/p95 **185/185 minutes**, **1.03816794 runs/merge**, **2.12044105%** at or above 270 minutes, and **73 wasted runs**. Runner-minutes/merge: **185.83018837 supported / 186.87022901 unsupported**. Passes p95 < 270 minutes and runs/merge < 1.5.

Team requested cancellation for 159 identified attempts; Slow CI requested 42.
All are confirmed in supported mode and reported unsupported in unsupported mode.
The unsupported results, including runner cost, are identical to PR 3. Both modes
start the same number of runs and have the same latency: the 50-runner pool is
not saturated in these profiles. Cancellation saves service after invalidation,
which reduces runner-minutes without reducing the count of runs already started.

A wasted run is any started attempt other than the final successful attempt of a
landed batch. This deliberately broad cost measure includes failed diagnostic and
bisection attempts, superseded reruns, timed-out work, and invalidated or cancelled
attempts. A cancelled attempt still counts as started and wasted; a queued attempt
cancelled before it gets a runner counts as neither. Runner-minutes count service
actually consumed, including time up to cancellation.

Against the PR 1 model (both cancellation settings have the same predicted runs and latency):

- Team model: 4,692 runs, p95 35 minutes, 0.99491094 runs/merge, 2.33248516% tail. Planner difference: **−2 runs**, **0 minutes p95**, **−0.00042409 runs/merge**, **−0.06361323 percentage points** tail. Runner-minutes/merge: model 29.75001654 supported / 29.84732824 unsupported; planner delta −0.00414485 / −0.01272265.
- Slow model: 1,225 runs, p95 185 minutes, 1.03901612 runs/merge, 2.29007634% tail. Planner difference: **−1 run**, **0 minutes p95**, **−0.00084818 runs/merge**, **−0.16963528 percentage points** tail. Runner-minutes/merge: model 185.87542776 supported / 187.02290076 unsupported; planner delta −0.04523938 / −0.15267176.

The small cost/tail differences are measured, not reconciled into the capacity model. The model and planner have different window control, event/refresh/fence overhead, and no shared execution trace. These results validate the pinned adopter gates without establishing trace equivalence.

## Per-seed and cold-start results

Both modes have the same per-seed merged/run counts and latency results, listed once per seed. Day 1 means changes arriving in the first 24 hours, followed through resolution; it has no pass criterion. Cold-start cost is not calculated here.

### Team

- Seed 1: merged 587, runs 591, p50 35m0s, p95 35m0s, runs/merge 1.006814, tail 1.1925%. Day 1: n=14, p50 35m0s, p95 35m0s, tail 0.0000%.
- Seed 2: merged 583, runs 597, p50 35m0s, p95 35m0s, runs/merge 1.024014, tail 3.2590%. Day 1: n=20, p50 35m0s, p95 35m0s, tail 5.0000%.
- Seed 3: merged 590, runs 591, p50 35m0s, p95 35m0s, runs/merge 1.001695, tail 3.0508%. Day 1: n=20, p50 35m0s, p95 35m0s, tail 0.0000%.
- Seed 4: merged 590, runs 579, p50 35m0s, p95 35m0s, runs/merge 0.981356, tail 1.3559%. Day 1: n=16, p50 35m0s, p95 35m0s, tail 0.0000%.
- Seed 5: merged 592, runs 592, p50 35m0s, p95 35m0s, runs/merge 1.000000, tail 2.7027%. Day 1: n=22, p50 35m0s, p95 35m0s, tail 0.0000%.
- Seed 6: merged 590, runs 582, p50 35m0s, p95 35m0s, runs/merge 0.986441, tail 1.6949%. Day 1: n=16, p50 35m0s, p95 35m0s, tail 0.0000%.
- Seed 7: merged 588, runs 589, p50 35m0s, p95 35m0s, runs/merge 1.001701, tail 3.7415%. Day 1: n=20, p50 35m0s, p95 35m0s, tail 0.0000%.
- Seed 8: merged 596, runs 569, p50 35m0s, p95 35m0s, runs/merge 0.954698, tail 1.1745%. Day 1: n=19, p50 35m0s, p95 35m0s, tail 0.0000%.

### Slow

- Seed 1: merged 146, runs 156, p50 3h5m0s, p95 3h5m0s, runs/merge 1.068493, tail 2.7397%. Day 1: n=3, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.
- Seed 2: merged 145, runs 157, p50 3h5m0s, p95 3h5m0s, runs/merge 1.082759, tail 2.0690%. Day 1: n=4, p50 3h41m19.021219539s, p95 6h16m52.733046935s, tail 25.0000%.
- Seed 3: merged 146, runs 155, p50 3h5m0s, p95 3h5m0s, runs/merge 1.061644, tail 2.0548%. Day 1: n=3, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.
- Seed 4: merged 147, runs 155, p50 3h5m0s, p95 3h5m0s, runs/merge 1.054422, tail 3.4014%. Day 1: n=7, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.
- Seed 5: merged 146, runs 154, p50 3h5m0s, p95 4h28m8.29708477s, runs/merge 1.054795, tail 4.7945%. Day 1: n=4, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.
- Seed 6: merged 149, runs 148, p50 3h5m0s, p95 3h5m0s, runs/merge 0.993289, tail 0.0000%. Day 1: n=2, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.
- Seed 7: merged 150, runs 149, p50 3h5m0s, p95 3h5m0s, runs/merge 0.993333, tail 1.3333%. Day 1: n=4, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.
- Seed 8: merged 150, runs 150, p50 3h5m0s, p95 3h5m0s, runs/merge 1.000000, tail 0.6667%. Day 1: n=4, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.

## Reproduce

```sh
mise exec -- go test ./internal/sim -run 'TestExitAdopter(Team|Slow)$' -v -count=1
```

The tests run both capability modes, assert that each actually exercises cancellation, and also run the pinned PR 1 capacity model for each seed and print pooled deltas, wasted runs and cancellation outcomes. Full commands and final exit statuses are recorded in the PR description.

## Depth-1 compatibility

All 17 JSON records from `TestExit500PRDay` and `TestExit2000PRDayBatch8` were byte-compared against clean `origin/main` commit `002f2f7` and match exactly (also unchanged from PR 3’s `7ffd878` comparison). SHA-256 of the newline-joined records in test order: `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`. Existing event/action golden files were not edited.
