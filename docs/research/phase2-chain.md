# Adaptive chain: planner-backed Phase 2 strict exit

ADR 0009 PR 5 runs the real planner and executor with W adapting between 1 and 4.
**All strict adopter and adaptive-sizing gates pass**, in supported and unsupported
cancellation modes. The workload pins and thresholds were not tuned. The default
`max_depth` remains 1 pending the separate two-week/50-change dogfooding gate.

Team and Slow use seeds 1–8, each with 30 days of uniform arrivals, 2% broken
changes, 1% flaky runs, batch ceiling 8, max wait 5m, 50 runners and a check timeout
of 1.5× CI. Team is 20/day with 30m CI; Slow is 5/day with 3h CI. The adaptive
workload uses the same seeds and horizon at 900/day, 2m CI and 5% broken changes,
paired seed-for-seed with fixed size 8. Only adaptive fresh batches set target 0.8.
All queues start cold; every workload drains after arrivals stop.

## Pooled gates and window equilibrium

Quantiles below pool individual merged changes, not seed quantiles. W equilibrium
is the seed mean of each run's time-weighted W **after the first 24 hours through
drain**, including idle time and settlement; it is measured, not an optimality
claim. The per-seed tables also show day-1 W. Three independent seeds run at a
time, each owning its forge, log, clock and RNG; scheduling cannot change virtual
time. The full adaptive gate is opt-in because the paired profile runs 32
27,000-change simulations; its first-day harness runs in `make check`.

| Profile / sizing | Cancellation | Merged | Runs | p50 min | p95 min | Runs/merge | Runner-min/merge | Equilibrium W | Tail % |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Team | supported | 4,716 | 4,690 | 35.000 | 35.000 | 0.99448685 | 29.74587169 | 3.948294 | 2.311281 |
| Team | unsupported | 4,716 | 4,690 | 35.000 | 35.000 | 0.99448685 | 29.83460560 | 3.948294 | 2.311281 |
| Slow CI | supported | 1,179 | 1,224 | 185.000 | 185.000 | 1.03816794 | 185.83018837 | 3.938186 | 2.120441 |
| Slow CI | unsupported | 1,179 | 1,224 | 185.000 | 185.000 | 1.03816794 | 186.87022901 | 3.938186 | 2.120441 |
| Adaptive workload, fixed 8 | supported | 205,055 | 153,786 | 7.000 | 35.571 | 0.74997440 | 1.49782754 | 3.552750 | 47.534564 |
| Adaptive workload, fixed 8 | unsupported | 205,055 | 153,786 | 7.000 | 35.571 | 0.74997440 | 1.49994879 | 3.552750 | 47.534564 |
| Adaptive workload, target 0.8 | supported | 205,053 | 153,251 | 6.328 | 23.089 | 0.74737263 | 1.48529673 | 3.588857 | 35.589335 |
| Adaptive workload, target 0.8 | unsupported | 205,053 | 153,251 | 6.328 | 23.089 | 0.74737263 | 1.49474526 | 3.588857 | 35.589335 |

Team passes p95 < 60m and runs/merge < 1.5; Slow passes p95 < 270m and
runs/merge < 1.5. Adaptive sizing passes runs/merge < 1.5 and median no worse
than its paired fixed-8 control **for every seed and pooled**, in both modes.
The additional p95-vs-fixed-8 regression assertion also passes for every seed and
pooled. These are merged-change latency measurements; every started attempt,
including reruns, cancelled and invalidated work, contributes to cost.

Against PR 4's fixed window, Team keeps 4,690 runs, 35m p50/p95 and the same
runner cost, but its >=60m tail changes **2.26887193% → 2.31128075%** (two
additional tail changes, +0.04240882 percentage points). Slow's pooled counts,
latencies, tail and runner costs are unchanged. This is an intended AIMD scheduling
delta, not evidence that every latency distribution stays identical at depth 4.
The PR 1 standalone capacity curve remains a separate model in
[phase2-capacity.md](phase2-capacity.md); it is not a planner trace oracle or a
high-volume strict SLA. The fixed-window PR 4 results are retained below.

## CI accounting

A wasted run is any started attempt other than the final successful attempt of a
landed batch. Cancellation confirmation can mean that the identified attempt had
already completed; requested/confirmed counts are not necessarily stopped work.
Unsupported attempts finish and consume service. Neither retired result path
can authorize a landing.

| Profile / sizing | Cancellation | Wasted runs | Requested | Confirmed | Unsupported |
|---|---|---:|---:|---:|---:|
| Team | supported | 299 | 159 | 159 | 0 |
| Team | unsupported | 299 | 159 | 0 | 159 |
| Slow CI | supported | 73 | 42 | 42 | 0 |
| Slow CI | unsupported | 73 | 42 | 0 | 42 |
| Adaptive workload, fixed 8 | supported | 96,684 | 59,464 | 59,464 | 0 |
| Adaptive workload, fixed 8 | unsupported | 96,684 | 59,464 | 0 | 59,464 |
| Adaptive workload, target 0.8 | supported | 77,790 | 47,194 | 47,194 | 0 |
| Adaptive workload, target 0.8 | unsupported | 77,790 | 47,194 | 0 | 47,194 |

## Per-seed and cold-start results

Both capability modes have identical merged/run counts, latency and wasted-run
counts in these profiles: 50 runners are not saturated. W is identical to the
precision shown. Cost pairs below are **supported / unsupported** runner-minutes
per merged change. Day 1 contains changes arriving in the first 24 hours, followed
through resolution; it has no pass criterion. Cold-start cost allocates every
started attempt in proportion to its batch members in that arrival cohort,
including service after day 1. It is not wall-clock CI consumption within day 1.

### Team

| Seed | Merged | Runs | p50 min | p95 min | W equilibrium | Wasted | Runs/merge | Runner-min/merge S / U |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 587 | 591 | 35.000 | 35.000 | 3.935910 | 38 | 1.006814 | 30.039685 / 30.204429 |
| 2 | 583 | 597 | 35.000 | 35.000 | 3.917051 | 61 | 1.024014 | 30.610482 / 30.720412 |
| 3 | 590 | 591 | 35.000 | 35.000 | 3.950370 | 42 | 1.001695 | 29.979469 / 30.050847 |
| 4 | 590 | 579 | 35.000 | 35.000 | 3.941817 | 28 | 0.981356 | 29.440678 / 29.440678 |
| 5 | 592 | 592 | 35.000 | 35.000 | 3.957008 | 33 | 1.000000 | 30.000000 / 30.000000 |
| 6 | 590 | 582 | 35.000 | 35.000 | 3.966597 | 34 | 0.986441 | 29.458997 / 29.593220 |
| 7 | 588 | 589 | 35.000 | 35.000 | 3.953760 | 47 | 1.001701 | 29.861401 / 30.051020 |
| 8 | 596 | 569 | 35.000 | 35.000 | 3.963836 | 16 | 0.954698 | 28.599204 / 28.640940 |

| Day-1 seed | Merged | p50 min | p95 min | Day-1 mean W | Runs/merge | Runner-min/merge S / U |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 14 | 35.000 | 35.000 | 3.971956 | 1.142857 | 34.285714 / 34.285714 |
| 2 | 20 | 35.000 | 35.000 | 3.883249 | 1.000000 | 30.000000 / 30.000000 |
| 3 | 20 | 35.000 | 35.000 | 4.000000 | 0.950000 | 28.500000 / 28.500000 |
| 4 | 16 | 35.000 | 35.000 | 4.000000 | 0.937500 | 28.125000 / 28.125000 |
| 5 | 22 | 35.000 | 35.000 | 4.000000 | 0.863636 | 25.909091 / 25.909091 |
| 6 | 16 | 35.000 | 35.000 | 3.539482 | 1.187500 | 35.625000 / 35.625000 |
| 7 | 20 | 35.000 | 35.000 | 4.000000 | 0.900000 | 27.000000 / 27.000000 |
| 8 | 19 | 35.000 | 35.000 | 4.000000 | 0.947368 | 28.421053 / 28.421053 |

### Slow CI

| Seed | Merged | Runs | p50 min | p95 min | W equilibrium | Wasted | Runs/merge | Runner-min/merge S / U |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 146 | 156 | 185.000 | 185.000 | 3.899487 | 13 | 1.068493 | 191.068917 / 192.328767 |
| 2 | 145 | 157 | 185.000 | 185.000 | 3.886833 | 16 | 1.082759 | 192.133662 / 194.896552 |
| 3 | 146 | 155 | 185.000 | 185.000 | 3.858281 | 11 | 1.061644 | 190.550466 / 191.095890 |
| 4 | 147 | 155 | 185.000 | 185.000 | 3.947578 | 11 | 1.054422 | 189.795918 / 189.795918 |
| 5 | 146 | 154 | 185.000 | 268.138 | 3.927803 | 16 | 1.054795 | 187.067332 / 189.863014 |
| 6 | 149 | 148 | 185.000 | 185.000 | 3.985507 | 3 | 0.993289 | 177.759400 / 178.791946 |
| 7 | 150 | 149 | 185.000 | 185.000 | 4.000000 | 2 | 0.993333 | 178.800000 / 178.800000 |
| 8 | 150 | 150 | 185.000 | 185.000 | 4.000000 | 1 | 1.000000 | 180.000000 / 180.000000 |

| Day-1 seed | Merged | p50 min | p95 min | Day-1 mean W | Runs/merge | Runner-min/merge S / U |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 3 | 185.000 | 185.000 | 4.000000 | 1.666667 | 300.000000 / 300.000000 |
| 2 | 4 | 221.317 | 376.879 | 3.583325 | 1.750000 | 288.158510 / 315.000000 |
| 3 | 3 | 185.000 | 185.000 | 4.000000 | 1.000000 | 180.000000 / 180.000000 |
| 4 | 7 | 185.000 | 185.000 | 4.000000 | 0.857143 | 154.285714 / 154.285714 |
| 5 | 4 | 185.000 | 185.000 | 4.000000 | 1.000000 | 180.000000 / 180.000000 |
| 6 | 2 | 185.000 | 185.000 | 4.000000 | 1.000000 | 180.000000 / 180.000000 |
| 7 | 4 | 185.000 | 185.000 | 4.000000 | 1.000000 | 180.000000 / 180.000000 |
| 8 | 4 | 185.000 | 185.000 | 4.000000 | 1.000000 | 180.000000 / 180.000000 |

### Adaptive workload, fixed 8

| Seed | Merged | Runs | p50 min | p95 min | W equilibrium | Wasted | Runs/merge | Runner-min/merge S / U |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 25,616 | 19,366 | 7.000 | 36.395 | 3.553710 | 12,225 | 0.756012 | 1.509375 / 1.512024 |
| 2 | 25,673 | 18,936 | 7.000 | 34.519 | 3.569122 | 11,806 | 0.737584 | 1.473093 / 1.475168 |
| 3 | 25,658 | 18,993 | 7.000 | 35.075 | 3.561579 | 11,853 | 0.740237 | 1.478178 / 1.480474 |
| 4 | 25,608 | 19,578 | 7.205 | 39.367 | 3.542069 | 12,417 | 0.764527 | 1.527691 / 1.529053 |
| 5 | 25,621 | 19,176 | 7.000 | 34.273 | 3.548856 | 12,055 | 0.748449 | 1.494725 / 1.496897 |
| 6 | 25,650 | 19,078 | 7.000 | 35.832 | 3.551611 | 11,931 | 0.743782 | 1.486017 / 1.487563 |
| 7 | 25,586 | 19,536 | 7.338 | 36.285 | 3.540894 | 12,426 | 0.763543 | 1.524387 / 1.527085 |
| 8 | 25,643 | 19,123 | 7.000 | 33.354 | 3.554159 | 11,971 | 0.745740 | 1.489306 / 1.491479 |

| Day-1 seed | Merged | p50 min | p95 min | Day-1 mean W | Runs/merge | Runner-min/merge S / U |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 865 | 7.468 | 31.041 | 3.492430 | 0.756069 | 1.507098 / 1.512139 |
| 2 | 876 | 9.415 | 37.357 | 3.502778 | 0.809817 | 1.617817 / 1.619635 |
| 3 | 822 | 7.000 | 49.779 | 3.617313 | 0.787561 | 1.575122 / 1.575122 |
| 4 | 835 | 7.000 | 27.507 | 3.579167 | 0.655689 | 1.307883 / 1.311377 |
| 5 | 905 | 7.000 | 25.618 | 3.627620 | 0.678867 | 1.354514 / 1.357735 |
| 6 | 845 | 7.000 | 24.524 | 3.594444 | 0.724970 | 1.447297 / 1.449941 |
| 7 | 892 | 7.000 | 22.016 | 3.650000 | 0.634529 | 1.265978 / 1.269058 |
| 8 | 847 | 7.030 | 28.012 | 3.555556 | 0.762397 | 1.521315 / 1.524793 |

### Adaptive workload, target 0.8

| Seed | Merged | Runs | p50 min | p95 min | W equilibrium | Wasted | Runs/merge | Runner-min/merge S / U |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 25,615 | 19,348 | 6.300 | 22.736 | 3.591823 | 9,862 | 0.755339 | 1.498953 / 1.510677 |
| 2 | 25,673 | 18,819 | 6.348 | 23.525 | 3.596067 | 9,581 | 0.733027 | 1.457485 / 1.466054 |
| 3 | 25,657 | 18,988 | 6.433 | 23.415 | 3.593781 | 9,647 | 0.740071 | 1.472047 / 1.480142 |
| 4 | 25,608 | 19,426 | 6.339 | 23.419 | 3.583878 | 9,857 | 0.758591 | 1.507496 / 1.517182 |
| 5 | 25,621 | 19,226 | 6.285 | 23.548 | 3.581985 | 9,764 | 0.750400 | 1.491152 / 1.500800 |
| 6 | 25,650 | 18,854 | 6.226 | 22.592 | 3.595226 | 9,546 | 0.735049 | 1.461468 / 1.470097 |
| 7 | 25,586 | 19,548 | 6.343 | 23.442 | 3.577885 | 9,879 | 0.764012 | 1.517893 / 1.528023 |
| 8 | 25,643 | 19,042 | 6.349 | 22.005 | 3.590214 | 9,654 | 0.742581 | 1.476049 / 1.485162 |

| Day-1 seed | Merged | p50 min | p95 min | Day-1 mean W | Runs/merge | Runner-min/merge S / U |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 865 | 6.298 | 22.223 | 3.531974 | 0.786127 | 1.555420 / 1.572254 |
| 2 | 876 | 6.332 | 29.640 | 3.544518 | 0.822298 | 1.633208 / 1.644597 |
| 3 | 822 | 6.019 | 21.572 | 3.620091 | 0.747932 | 1.494334 / 1.495864 |
| 4 | 835 | 5.641 | 19.028 | 3.618056 | 0.686826 | 1.367072 / 1.373653 |
| 5 | 905 | 6.561 | 18.518 | 3.604714 | 0.697238 | 1.390263 / 1.394475 |
| 6 | 845 | 6.288 | 19.508 | 3.607473 | 0.748817 | 1.491680 / 1.497633 |
| 7 | 892 | 6.163 | 21.305 | 3.590278 | 0.681614 | 1.355393 / 1.363229 |
| 8 | 847 | 6.346 | 19.887 | 3.613056 | 0.732782 | 1.458876 / 1.465565 |

## Round 1 fixes

Legacy upgrades read the policy at the recorded `ConfigCommit` when the digest
also changes. Reconciliation records that policy's fixed ceiling under its old
digest before applying the new bounds, so an old W=8 clamps to 4 instead of 1.
Unchanged digests initialize once and retain admitted work; serial upgrades keep
W=1. Historical read or digest failures stop the transition rather than invent W.
Only confirmed checks failures and timeouts of original batches halve W, as in
ADR 0009. Build conflicts measure neither CI health nor failure rate and change
neither W nor the outcome ring. The independent simulator oracle excludes them.
Failures at the floor preserve the last actual halving event. Empty estimates
render as `p̂: n/a (0 samples)`. Configured snapshots reject a null window;
unsupported versions are rejected before checking their version-specific fields.

Delayed duplicate landing actions may run after checks reopen or fail. The
executor safely rejects those replays with `ErrChecksNotSuccessful`; the timeline
accepts that rejection just as it accepts a stale action or a rejected lease,
after checking invariants. This tolerance does not authorize a landing.

## Explicit implementation decisions

**Retain conservative admission.** A separate eight-seed, one-day experiment
compares the retained barrier with an overlay removing both provisional-failure
admission guards and the failed-retry guard. The test still drives the real
planner and executor. It uses the pinned per-profile rates, CI, depth, runners
and timeout; it is not substituted for the 30-day exit. Results are pooled by
merged change. Both capability modes have identical runs, latency and waste:

| Workload | Admission | Runs | Wasted | p50 | p95 | Runs/merge | Runner-min/merge S / U |
|---|---|---:|---:|---|---|---:|---:|
| Team | either | 152 | 8 | 35m | 35m | 0.96815287 | 28.92003563 / 29.04458599 |
| Slow | either | 39 | 0 | 185m | 185m | 0.97500000 | 175.50000000 / 175.50000000 |
| Adaptive | conservative | 4,981 | 2,508 | 6m16.267608007s | 23m29.259078038s | 0.72736565 | 1.44667388 / 1.45473131 |
| Adaptive | relaxed | 5,016 | 2,543 | 6m16.671031247s | 23m30.999453236s | 0.73247664 | 1.45495702 / 1.46495327 |

Relaxation adds 35 wasted runs with no measured latency benefit. The retained
rule avoids fresh work on a known failing prefix and passes every full exit.
This finite experiment does not prove it is optimal under bursty arrivals,
interacting changes, scarce runners or variable CI durations.

**Keep snapshots as a codec.** Version 2 retains W/bounds/last halving, once-only
batch markers, the 200-outcome ring and retained cancellation confirmations.
Missing adaptive fields, invalid bounds/revisions/order and duplicate outcomes
are rejected. Full encoded bytes must be fixed points across decode/encode;
critical adaptive state and actual retained confirmations are compared explicitly.
The store still replays journals. Enabling compaction would also require a run
ledger retention policy and is out of scope.

## Compatibility and reproducibility

The clean baseline is `origin/main` **5e43d46dcfbbb1c1941e2019ddecc30144a75e9a**.
All 17 legacy exit JSON records match byte-for-byte (SHA-256 of newline-joined
records: `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`).
All 1,055 depth-1 retry event/action envelope and digest records also match
(`07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`), as do
all 73 existing wire fixtures. The default policy digest remains
`75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa`.
Unset target and default minimum depth add no digest fields. No new event/action
type is introduced; speculative `config_applied` adds optional bounds with a
separate additive golden fixture. API/CLI/PR-summary changes are additive.

Large simulations opt into exact first-parent file/PR-marker deltas and
parent/delta-derived synthetic commit IDs, retaining only 64 materialized trees.
Historical content remains reconstructible. Compact/full fake runs produce
identical planner outcomes, event types and virtual times on fixed and adaptive
controls; merge/squash/rebase snapshots are checked against reconstructed deltas
and changed-file content. Legacy workloads retain the original fake identity
format. Immutable PR-observation shards preserve replay and snapshot bytes while
avoiding full-history copying on every CI event. Initial full-run attempts were
interrupted to remove quadratic fake storage/hashing costs; the gate numbers
above come from the completed final run, not those attempts.

A backstop repetition exposed a racy immediate assertion: either server can own
the shared action claim when a peer cycle returns. That test now uses its existing
bounded durable-state wait. Production backstop timing is unchanged.

Round 1 reruns match `c3707e9` exactly: all **64** per-seed profile records
(including every day-1 metric and window measurement) and all **10** pooled or
cancellation tally lines have zero deltas. All original and additional gates
pass in both cancellation modes. The pinned exit workloads contain no build
conflicts, so the CI-only halving fix changes the injected-conflict cases rather
than their metrics. The admission experiment also reproduces its prior results.
The full adaptive command exits **0** in **1,164.400 seconds**, versus the review's
953.052 seconds (+211.348 seconds of wall time), with other validation running
concurrently. All numeric tables above remain current.

The final `make check` exits **0** (simulator race suite: 555.747s; server race
suite: 427.378s). The first attempt exited **2**: the new active-migration fixture
used an invalid batch ID, and the simulator package reached its default 10-minute
timeout in the existing cancellation-savings test. The corrected fixture passes
five race repetitions. The first broad backstop repetition exited **1** on an
idle-reconciliation deadline; its full five-repetition rerun exits **0**. Logs are
`/tmp/om-pr46-fix1{,-initial,-backstops,-backstops-rerun,-exits,-mutations,-compatibility,-admission,-regressions,-deltas}.log`.
The new server regression also fails as expected (exit **1**) with only the old
reconciliation implementation overlaid: 8→4 gives 1 and 8→12 gives 1. It passes
with the final implementation.

Commands (all final runs exit **0**, with no real GitHub calls from tests):

```sh
mise exec -- env OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' make check > /tmp/om-pr46-fix1.log 2>&1; echo exit=$?
mise exec -- python3 scripts/check-simulator-mutations.py
mise exec -- go test ./internal/sim -run '^TestExitAdopter' -sim-adaptive-full-exit -v -count=1 -timeout=60m
mise exec -- python3 scripts/check-phase2-admission.py
mise exec -- python3 scripts/check-phase2-compatibility.py /tmp/om-p2-pr5-baseline
OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' mise exec -- go test ./internal/serve -run 'Backstop|TestLegacyWindow' -race -count=5
mise exec -- go test ./internal/queue ./internal/status -run 'TestReviewPR46|TestAdaptive|TestSnapshotRejectsOld' -race -count=5
```

The mutation sweep detects **36/36** mutations, including missing increase or
decrease, repeated bisection decrease, admission beyond W, ignored sizing,
rejected equality, a shortened outcome ring and conflicts incorrectly halving W.
The compatibility script itself runs both 17-exit suites and the retry-envelope probe against the clean baseline.
`make check` includes formatting, lint, all default tests under the race detector
and a build. The explicit full-profile command supplies the expensive gate.

## Historical PR 4 fixed-window results

The following results are retained as the measured pre-adaptation baseline.
Reproduce this section on commit `5e43d46`; running its command on the PR 5
branch exercises the adaptive window instead.

### Fixed-window chain: planner-backed adopter results

The real planner and executor use fixed W = 4. Seeds 1–8 each admit 30 days of uniform arrivals, with the ADR 0009 pinned policy: batch size 8, five-minute max wait, 2% broken changes, 1% flaky runs, 50 runners, and timeout 1.5 × CI. The one-second observation/enqueue delay remains in the simulator. No thresholds were changed. This evidence validates depth 4; depths above 4 remain unvalidated.

Both cancellation modes pass through the real planner, durable cancellation
intent and executor's optional forge capability. Supported cancellation stops only
the identified attempts of permanently retired batches and releases their runner
capacity; unsupported cancellation lets those attempts finish. A durable
`observe_retired_checks` follow-up reads pending unsupported attempts until their
exact identities report terminal outcomes or its eight-attempt budget is exhausted.
Pending reads and cancellation faults use the same durable successor actions and
30s-to-30m exponential backoff in production and simulation; they do not occupy
the oldest claimable action during the wait. Historical observations cannot
authorize landing. Neither outcome can revive a retired batch. The adaptive-sizing exit test stays skipped for PR 5. The
PR 1 model includes AIMD and its configured cancellation behavior, so its numerical
predictions are a comparison, not an expected byte-identical trace.

### Pooled results

- Team, either mode: 4,716 merged, 4,690 runs, p50/p95 **35/35 minutes**, **0.99448685 runs/merge**, **2.26887193%** at or above 60 minutes, and **299 wasted runs**. Runner-minutes/merge: **29.74587169 supported / 29.83460560 unsupported**. Passes p95 < 60 minutes and runs/merge < 1.5.
- Slow CI, either mode: 1,179 merged, 1,224 runs, p50/p95 **185/185 minutes**, **1.03816794 runs/merge**, **2.12044105%** at or above 270 minutes, and **73 wasted runs**. Runner-minutes/merge: **185.83018837 supported / 186.87022901 unsupported**. Passes p95 < 270 minutes and runs/merge < 1.5.

Team requested cancellation for 159 identified attempts; Slow CI requested 42.
All are confirmed in supported mode and reported unsupported in unsupported mode.
The round-1 scheduling fix reproduces every pooled and per-seed adopter result in
both modes, including runner cost. Supported cancellations in these profiles
confirm immediately, so their dispatch time is unchanged. Delayed unsupported
follow-ups only read history; they do not affect CI service or queue gates. Of
Team's 159 requests, 26 stop running attempts and 133 arrive after completion,
saving 418.46911058 runner-minutes. Of Slow CI's 42 requests, 12 stop running
attempts and 30 arrive after completion, saving 1226.20790709 runner-minutes.
Neither profile cancels an attempt before it starts.
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

### Per-seed and cold-start results

Both modes have the same per-seed merged/run counts and latency results, listed once per seed. Day 1 means changes arriving in the first 24 hours, followed through resolution; it has no pass criterion. Cold-start cost is not calculated here.

#### Team

- Seed 1: merged 587, runs 591, p50 35m0s, p95 35m0s, runs/merge 1.006814, tail 1.1925%. Day 1: n=14, p50 35m0s, p95 35m0s, tail 0.0000%.
- Seed 2: merged 583, runs 597, p50 35m0s, p95 35m0s, runs/merge 1.024014, tail 3.2590%. Day 1: n=20, p50 35m0s, p95 35m0s, tail 5.0000%.
- Seed 3: merged 590, runs 591, p50 35m0s, p95 35m0s, runs/merge 1.001695, tail 3.0508%. Day 1: n=20, p50 35m0s, p95 35m0s, tail 0.0000%.
- Seed 4: merged 590, runs 579, p50 35m0s, p95 35m0s, runs/merge 0.981356, tail 1.3559%. Day 1: n=16, p50 35m0s, p95 35m0s, tail 0.0000%.
- Seed 5: merged 592, runs 592, p50 35m0s, p95 35m0s, runs/merge 1.000000, tail 2.7027%. Day 1: n=22, p50 35m0s, p95 35m0s, tail 0.0000%.
- Seed 6: merged 590, runs 582, p50 35m0s, p95 35m0s, runs/merge 0.986441, tail 1.6949%. Day 1: n=16, p50 35m0s, p95 35m0s, tail 0.0000%.
- Seed 7: merged 588, runs 589, p50 35m0s, p95 35m0s, runs/merge 1.001701, tail 3.7415%. Day 1: n=20, p50 35m0s, p95 35m0s, tail 0.0000%.
- Seed 8: merged 596, runs 569, p50 35m0s, p95 35m0s, runs/merge 0.954698, tail 1.1745%. Day 1: n=19, p50 35m0s, p95 35m0s, tail 0.0000%.

#### Slow

- Seed 1: merged 146, runs 156, p50 3h5m0s, p95 3h5m0s, runs/merge 1.068493, tail 2.7397%. Day 1: n=3, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.
- Seed 2: merged 145, runs 157, p50 3h5m0s, p95 3h5m0s, runs/merge 1.082759, tail 2.0690%. Day 1: n=4, p50 3h41m19.021219539s, p95 6h16m52.733046935s, tail 25.0000%.
- Seed 3: merged 146, runs 155, p50 3h5m0s, p95 3h5m0s, runs/merge 1.061644, tail 2.0548%. Day 1: n=3, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.
- Seed 4: merged 147, runs 155, p50 3h5m0s, p95 3h5m0s, runs/merge 1.054422, tail 3.4014%. Day 1: n=7, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.
- Seed 5: merged 146, runs 154, p50 3h5m0s, p95 4h28m8.29708477s, runs/merge 1.054795, tail 4.7945%. Day 1: n=4, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.
- Seed 6: merged 149, runs 148, p50 3h5m0s, p95 3h5m0s, runs/merge 0.993289, tail 0.0000%. Day 1: n=2, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.
- Seed 7: merged 150, runs 149, p50 3h5m0s, p95 3h5m0s, runs/merge 0.993333, tail 1.3333%. Day 1: n=4, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.
- Seed 8: merged 150, runs 150, p50 3h5m0s, p95 3h5m0s, runs/merge 1.000000, tail 0.6667%. Day 1: n=4, p50 3h5m0s, p95 3h5m0s, tail 0.0000%.

### Reproduce

```sh
mise exec -- go test ./internal/sim -run 'TestExitAdopter(Team|Slow)$' -v -count=1
```

The tests run both capability modes, assert that each actually exercises cancellation, and also run the pinned PR 1 capacity model for each seed and print pooled deltas, wasted runs and cancellation outcomes. Full commands and final exit statuses are recorded in the PR description.

### Depth-1 compatibility

All 17 JSON records from `TestExit500PRDay` and `TestExit2000PRDayBatch8` were byte-compared against clean `origin/main` commit `002f2f7` and match exactly (also unchanged from PR 3’s `7ffd878` comparison). SHA-256 of the newline-joined records in test order: `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`. Existing event/action golden files were not edited.
