# Adaptive batch size backlog guard: evidence

This note records two pre-registered variants of a backlog guard on adaptive
fresh-batch sizing (ADR 0009/0015). Variant 1 applied at every window and did
not meet its bar. Variant 2 applies only when the admission window is 1.

## Variant 2: guard only at W = 1

Status: **meets the pre-registered bar** (all four criteria). The rule and the
bar below were fixed and committed before any run (commit "pre-register the
W=1 backlog guard variant and its bar") and were not changed after seeing
results.

### Rule

`queue.BacklogGuardSize`, applied in `State.batchCandidates` only when
`BatchTargetPassRate > 0`, `batch.backlog_guard` is not `false`, **and the
current admission window `State.Window(cfg)` is 1**:

```text
adaptive = AdaptiveBatchSize(max_size, failures, samples, target)  // unchanged
W        = State.Window(cfg)                        // current admission window
if W == 1:
    waiting = changes eligible for a fresh batch now   // same filter the planner uses
    size    = max(adaptive, min(max_size, waiting))    // ceil(waiting / 1)
else:
    size    = adaptive                                  // plain adaptive, unchanged
```

Every input is folded from the journal; no clock is read. At depth 1 W is
always 1, so the depth-1 behaviour equals variant 1's. At depth 4 the adaptive
window halves after a failure and can reach 1, so the guard can fire there too;
the lab reports how often (share of fresh batches started at W = 1).

### Pre-registered success bar

All deltas are the mean over seeds 1–10 of the seed-paired percentage
difference, as the efficiency lab computes `delta_percent`. "Guard" is the
`global-adaptive 0.8 + guard` arm (target 0.8, this rule).

- **(a) Depth 1.** In the `load-curve` matrix (beta-loaded at 300, 450, 600,
  700 and 800 PRs/day), against the fixed-size baseline: guard p50 latency Δ
  ≤ +10% at every load, and guard runner-minutes per landed change Δ ≤ +3% at
  every load.
- **(b) Depth 4.** In the `guard-depth4` matrix (adr0009-depth4,
  agent-burst-depth4, agent-burst-depth4-var, agent-burst-depth4-churn,
  team-slow-depth4, team-slow-depth4-churn), against plain adaptive
  (`global-adaptive 0.8`, guard off), on every workload: |Δ p50| ≤ 5%,
  |Δ p90| ≤ 5% and |Δ runner-minutes per landed| ≤ 2%.
- **(c)** `TestAdaptiveSizingPlannerHarness` (ADR 0009 first-day gate, adaptive
  p50 and p95 no worse than fixed) passes with the test unmodified.
- **(d)** Queues without `target_pass_rate` are byte-identical: planner goldens
  and sim invariant tests pass with no test data changes, configs without the
  key keep their digest, and every baseline (fixed-size) cell of the
  regenerated lab summary is unchanged.

### Results

Lab: `TestEfficiencyLab`, full suite, seeds 1–10. Deltas are the seed-paired
mean with the 95% Student-t half-width.

**(a) Depth 1: met.** `load-curve`, guard against the fixed-size baseline. In
the "W = 1 starts" column, every fresh batch at depth 1 starts at W = 1.
"Raised" is the share of fresh batches the guard made larger than the adaptive
size.

| PRs/day | fixed p50 (min) | adaptive 0.8 Δ p50 | guard Δ p50 (≤ +10%) | guard Δ min/landed (≤ +3%) | adaptive Δ min/landed | W = 1 starts | raised |
|---|---|---|---|---|---|---|---|
| 300 | 5.7 | +1.2% ±1.3 | +0.0% ±0.1 | −0.00% ±0.00 | +0.42% ±0.46 | 100% | 1.8% |
| 450 | 6.7 | +9.8% ±6.1 | +0.1% ±0.3 | +0.07% ±0.16 | +2.53% ±1.42 | 100% | 8.4% |
| 600 | 8.6 | +94.2% ±87.1 | +0.3% ±0.6 | +0.04% ±0.06 | +3.06% ±1.22 | 100% | 16.9% |
| 700 | 16.5 | +202.0% ±137.6 | +0.0% ±0.0 | +0.00% ±0.00 | +4.05% ±1.92 | 100% | 28.8% |
| 800 | 38.4 | +203.9% ±99.7 | +0.0% ±0.0 | +0.00% ±0.00 | +9.42% ±5.45 | 100% | 42.4% |

At depth 1 this rule is identical to variant 1, and these cells reproduce
variant 1's depth-1 results.

**(b) Depth 4: met.** `guard-depth4`, guard against plain adaptive
(`global-adaptive 0.8`, guard off). Bars: |Δ p50| ≤ 5%, |Δ p90| ≤ 5% and
|Δ min/landed| ≤ 2%.

| workload | adaptive p50 / p90 / min per landed | guard p50 / p90 / min per landed | Δ p50 | Δ p90 | Δ min/landed |
|---|---|---|---|---|---|
| adr0009-depth4 | 6.4 / 19.0 / 1.47 | 6.4 / 19.0 / 1.47 | +0.0% ±0.0 | +0.2% ±0.5 | −0.19% ±0.43 |
| agent-burst-depth4 | 10.7 / 40.2 / 1.57 | identical | 0 | 0 | 0 |
| agent-burst-depth4-var | 19.6 / 62.4 / 1.78 | identical | 0 | 0 | 0 |
| agent-burst-depth4-churn | 19.0 / 53.0 / 1.90 | identical | 0 | 0 | 0 |
| team-slow-depth4 | 35.0 / 35.0 / 29.54 | identical | 0 | 0 | 0 |
| team-slow-depth4-churn | 38.0 / 76.4 / 35.84 | identical | 0 | 0 | 0 |

**How often W reaches 1 at depth 4.** Fresh batch starts at W = 1, summed over
10 seeds:

| workload | plain adaptive | guard | raised by the guard |
|---|---|---|---|
| adr0009-depth4 | 1 of 3,113 (seed 10) | 2 of 3,098 (seed 10) | 2 |
| agent-burst-depth4 | 0 of 3,437 | 0 of 3,437 | 0 |
| agent-burst-depth4-var | 0 of 3,454 | 0 of 3,454 | 0 |
| agent-burst-depth4-churn | 0 of 5,020 | 0 of 5,020 | 0 |
| team-slow-depth4 | 0 of 5,641 | 0 of 5,641 | 0 |
| team-slow-depth4-churn | 0 of 6,215 | 0 of 6,215 | 0 |

The window halves on a failure (4 → 2 → 1) and grows by one on each landing,
so it reaches 1 only after two failures with no landing between them. In these
workloads that happened once, on one seed of the ADR 0009 profile. The guard
raised two batches there, and every other depth-4 run is identical to plain
adaptive. So at depth 4 the bar is met because the guard almost never fires.
These workloads do not show that it helps there. A workload with clustered
failures would exercise the depth-4 path more. The path is covered by
`TestBacklogGuardOnlyAtWindowOne` and the sim oracle.

**(c) ADR 0009 harness: met.** `TestAdaptiveSizingPlannerHarness` (seed 1,
`-short`) passes, and `internal/sim/adopter_test.go` is untouched. Pooled
results with and without cancellation:

| | fixed-8 | adaptive + guard (W = 1) |
|---|---|---|
| p50 (min) | 7.0 | 6.9 |
| p95 (min) | 45.9 | 31.4 |

These equal the pre-guard adaptive numbers.

**(d) Unchanged without a target: met.** Configs without a target take no
guard path. `om-go go test -short ./...` passes, and no planner golden
or sim test changed. The first full run caught two leaks of the new config key,
both in code carried over from variant 1, and both are fixed:

- The parsed-config golden in `internal/dependency` gained
  `"BacklogGuard":null`. The field is now `json:",omitempty"`, so that golden is
  byte-identical again without being edited.
- The candidate policy schema in `internal/changepolicy/testdata` mirrors the
  public schema by construction. It now carries the same `backlog_guard`
  property. The digest is unchanged without the key or with `true`
(`TestBacklogGuardValidationSchemaAndDigest`). In the regenerated
`docs/research/ci-efficiency-lab.json`, every baseline cell of every matrix is
byte-identical, as is every cell of `main`, `cancellation`, `score-noise` and
`fail-fast`. The summary changed in three ways:

- the `load-curve` guard arm now runs the production guard; the old sim-only
  arm is renamed `+ sim guard`;
- there is a new `guard-depth4` matrix;
- the `main` matrix's `global-adaptive` description now says the guard is off.

### Constraints checked

- No new event kinds or event fields. The window and the waiting count are
  folded from the journal, and no clock is read.
- The guard is skipped when `BatchTargetPassRate == 0`. `BatchBacklogGuardOff`
  is `omitempty`, so digests without the key are unchanged.
- The lab counters (`FreshBatches`, `FreshBatchesAtWindowOne`,
  `GuardRaisedBatches`) are recorded in the per-run lab metrics only, not in the
  summary.

### Reproduce

```sh
OPENMERGE_LAB_OUT=/some/dir om-go go test -count=1 ./internal/sim -run '^TestEfficiencyLab$'   # load-curve and guard-depth4; per-seed runs in efficiency-lab-runs.json
om-go go test -count=1 -short -v ./internal/sim -run '^TestAdaptiveSizingPlannerHarness$'
om-go go test -count=1 -v ./internal/queue -run 'BacklogGuard'
```

The guard-vs-adaptive deltas in (b) are seed-paired from
`efficiency-lab-runs.json` (the summary pairs every arm against the fixed
baseline).

## Variant 1: guard at every window (does not meet the bar)

The guard held depth-1 latency at fixed-sizing levels under load. At depth 4
it gave back much of adaptive sizing's benefit, and it failed the ADR 0009
first-day planner harness's p95 gate.

### Rule

`queue.BacklogGuardSize`, applied in `State.batchCandidates` only when
`BatchTargetPassRate > 0` and `batch.backlog_guard` is not `false`:

```text
adaptive = AdaptiveBatchSize(max_size, failures, samples, target)  // unchanged
waiting  = changes eligible for a fresh batch now   // same filter the planner uses
W        = State.Window(cfg)                        // current admission window
size     = max(adaptive, min(max_size, ceil(waiting / W)))
```

At depth 1 this is the lab's candidate rule, "never shrink a fresh batch below
what the waiting backlog would fill". At depth W, the backlog is spread over one
window of batches. Every input comes from the journal fold, and no clock is
read. The rule was chosen before any run and was not tuned afterwards.

### Load curve: depth 1 (beta-loaded, 10 seeds, paired Δ vs fixed)

| PRs/day | fixed p50 (min) | adaptive 0.8: Δ min / Δ p50 | + production guard | + sim-only guard |
|---|---|---|---|---|
| 300 | 5.7 | +0.4% / +1.2% | −0.0% / +0.0% | +0.5% / +1.3% |
| 450 | 6.7 | +2.5% / +9.8% ±6.1 | +0.1% / +0.1% | +1.7% / +7.1% ±7.1 |
| 600 | 8.6 | +3.1% / +94% ±87 | +0.0% / +0.3% | +1.2% / +8.8% ±8.5 |
| 700 | 16.5 | +4.0% / +202% ±138 | +0.0% / +0.0% | +0.5% / −10% ±17 |
| 800 | 38.4 | +9.4% / +204% ±100 | +0.0% / +0.0% | +0.0% / +3.0% ±14 |

At depth 1 the guard makes adaptive sizing behave like fixed sizing. This is
not a balance between the two: whenever more changes wait than the adaptive
size, the guard takes them all, up to `max_size`.

### Depth 4 (one day, 10 seeds; means; Δ p50 paired vs fixed)

| workload | fixed p50 / p90 / min per landed | adaptive 0.8 | + production guard | + sim-only guard |
|---|---|---|---|---|
| adr0009-depth4 (ADR 0009 profile, no bursts) | 7.7 / 29.4 / 1.47 | −15.8% ±10.0 · p90 19.0 · 1.47 | −15.1% ±10.5 · p90 22.4 · 1.48 | −11.4% ±8.4 · p90 26.6 · 1.49 |
| agent-burst-depth4 | 15.9 / 51.4 / 1.59 | −28.7% ±7.7 · p90 40.2 · 1.57 | −20.7% ±6.9 · p90 **52.6** · 1.59 | −18.1% ±6.4 · p90 50.8 · 1.59 |
| agent-burst-depth4-var | 26.2 / 90.8 / 1.79 | −24.2% ±3.5 · p90 62.4 · 1.78 | −12.9% ±8.1 · p90 88.6 · 1.82 | −10.6% ±5.0 · p90 91.4 · 1.82 |
| agent-burst-depth4-churn | 29.1 / 89.5 / 1.94 | −32.4% ±12.1 · p90 53.0 · 1.90 | −16.7% ±13.3 · p90 82.2 · 1.95 | −8.2% ±11.3 · p90 81.6 · 1.94 |
| team-slow-depth4 (±churn) | unchanged in every arm | | | |

The guard keeps 96% of the p50 benefit on the ADR 0009 profile but only 52–72%
on the burst workloads. Of the p90 benefit, it gives back a third on the ADR
0009 profile and 80–110% on the burst workloads, where its agent-burst p90 is
worse than fixed sizing. Runner minutes per landed change
move from slightly below fixed to slightly above it, within noise.

**ADR 0009 first-day planner harness** (`TestAdaptiveSizingPlannerHarness`,
seed 1, part of `-short`). This gate requires adaptive p50 and p95 to be no
worse than fixed. It now fails:

| | fixed-8 | adaptive (pre-guard) | adaptive + guard |
|---|---|---|---|
| p50 (min) | 7.0 | 6.9 | 7.0 |
| p95 (min) | 45.9 | 31.4 | **46.5** |

The guard was not tuned to pass this gate, and the gate was not changed.

### Why

The depth-1 model behind the guard is that serial throughput is roughly batch
size per CI duration, so shrinking cuts capacity. That model does not carry to
depth 4. A failure there invalidates the whole speculative suffix, so a smaller
batch also means a smaller chain blast radius. A backlog is also normal at depth
4 (agent bursts) and does not by itself mean that capacity is short. The
sim-only "full batch waiting" rule fails the same way, slightly worse.

A guard that applies only when the window is 1 would keep the depth-1 result
and leave depth 4 untouched by construction. It is a different rule and needs
its own pre-registered run. It was not tried here, so that the evaluation
stays clean.

### Constraints checked

- Byte-identical without a target: the guard is skipped when
  `BatchTargetPassRate == 0`. Fixed-size planner tests and goldens pass
  unchanged, and the main lab matrix pins its `global-adaptive` arm to guard off,
  so its published cells reproduce exactly.
- Digest: `BatchBacklogGuardOff` is `omitempty`. Configs without the key, or
  with `true`, keep their digest (`TestBacklogGuardValidationSchemaAndDigest`).
- No new event kinds or fields. No wall-clock reads.
- `-race -short` time: the new queue tests take about 10 ms, so the package
  delta is within machine-load noise (the baseline itself ranged 155–228 s).
  `internal/sim` went from 42.1 s to 43.0 s.

Variant 1's numbers came from its own branch (`feat/adaptive-batch-backlog-guard`,
head c1c5700). Its rule is no longer in the code.
