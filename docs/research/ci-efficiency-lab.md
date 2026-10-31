# CI efficiency lab: first strategy comparison

Status: evidence for ADR 0015 (draft). Deploy class: Inert (simulator and tests only).

## What this is

`internal/sim` drives the real planner (`internal/queue`) and executor against the
fake forge on a virtual clock. `Config.Lab` (nil by default) adds:

- **A runner-minute ledger.** Every CI run is charged for its duration: first attempts, bisection runs, same-SHA reruns, builds on a speculative predecessor, and cancelled runs (charged up to cancellation). A run *contributed* when it is the final, uncancelled run of a tested SHA that landed; everything else is waste, split into failed, invalidated, cancelled and superseded minutes.
- **Workload realism** on its own PCG stream, so every strategy sees the same changes for a seed:
  - beta or mixture per-change failure probabilities;
  - a strategy-visible risk score, the truth with Gaussian noise in logit space, standing in for ADR 0014 facts;
  - lognormal CI durations;
  - fail-fast for failing runs;
  - bursty agent arrivals;
  - exact tree identity: a SHA-256 of the test commit's file tree.
- **Sim-only strategies:**
  - exact tree dedupe in the simulated CI;
  - risk-weighted batch sizing, as a per-pass `BatchMaxSize` override passed to the real planner.
- **An isolation model** (`Isolate`) for strategy (c). Bayesian culprit-first changes the reducer's bisection rule (`State.failBatch`). Changing that rule would mean touching production code, so the model replays every failed batch from the baseline runs instead. At depth one without flakes, the halving model reproduces the planner's bisection run count exactly on all 30 calibrated seeds, and on all 10 flaky seeds (see the calibration table).

**No production hook was added.** Planner, executor, store and serve code is unchanged. The risk-sizing override exists only in `simulation.plannerConfig`, and only when `Lab.Strategy.RiskBatchTarget > 0`. With a lab config and no strategy, every outcome, draw, timing and metric is byte-identical to the plain run (`TestLabInstrumentationIsTransparent`).

## Reproduce

```sh
OPENMERGE_LAB_OUT=/some/dir om-go go test ./internal/sim -run '^TestEfficiencyLab$'   # full: 6 × 6 × 10, ~25 s
om-go go test -short ./internal/sim -run '^TestEfficiencyLab$'                         # smoke: 1 × 3 × 2 + determinism
```

The output is deterministic per seed. Two independent full runs produced identical files:

- `ci-efficiency-lab.json`: sha256 `65a8f6fd…20f9a2`
- the tables below: sha256 `2021ecee…de22c`

## Parameter sources (ADR 0009 seal respected)

No parameter comes from the OpenMerge or Smartfire databases, or from OpenMerge dogfood latency or runs-per-merge. Workloads reuse:

- the simulator defaults (`DefaultConfig`, doc.go);
- the exit-test policy (`TestExit500PRDay`, batch 8 / 30 s);
- the ADR 0009 capacity profiles (`capacity.AdaptiveWorkload`, `capacity.Team`, `Profile.Config`).

Distributions are synthetic, with means matched to those defaults (5% or 2%). The fail-fast fraction (30%) and the score noise (σ = 0.5 logit) are assumptions, not measurements.

## Headline (seed-paired Δ versus baseline, mean ± 95% CI over 10 seeds)

Runner-minutes per landed change:

| workload | (b) tree-dedupe | (d) global-adaptive | (d) risk-sized | (e) fail-fast | combined b+d+e |
|---|---|---|---|---|---|
| default-day | −1.0% ±0.5 | +0.5% ±0.3 | +0.1% ±0.2 | −4.4% ±0.7 | −4.7% ±0.8 |
| mixture-day | −0.8% ±0.6 | +0.9% ±0.9 | +1.5% ±0.9 | −4.7% ±1.1 | −2.6% ±0.9 |
| beta-loaded (depth 1, ~80% load) | −5.1% ±2.8 | **+10.4% ±3.9** | +2.2% ±2.3 | −7.2% ±3.0 | −5.4% ±3.5 |
| flaky-mixture-day (3% flakes) | −0.3% ±0.6 | +2.4% ±1.8 | +1.9% ±1.4 | −5.8% ±1.1 | −4.1% ±1.2 |
| agent-burst-depth4 | **−20.7% ±1.4** | −1.2% ±2.7 | −8.4% ±4.1 | **−44.4% ±1.9** | −40.2% ±3.4 |
| team-slow-depth4 | −0.2% ±0.1 | 0.0% | +0.2% ±0.2 | −3.9% ±0.8 | −3.7% ±0.8 |

p50 enqueue-to-land latency:

| workload | (b) tree-dedupe | (d) global-adaptive | (d) risk-sized | (e) fail-fast | combined |
|---|---|---|---|---|---|
| beta-loaded | −66.5% ±12.6 | **+181% ±141** | +13% ±29 | −78.7% ±8.0 | −70.8% ±9.8 |
| agent-burst-depth4 | −0.3% ±1.1 | −27.4% ±10.1 | −45.2% ±12.8 | −52.5% ±13.9 | −60.4% ±9.9 |
| other four workloads | \|Δ\| ≤ 1.0% | \|Δ\| ≤ 1.3% | \|Δ\| ≤ 1.6% | \|Δ\| ≤ 2.2% | \|Δ\| ≤ 2.2% |

Strategy (c), from the isolation model: CI runs per failed batch, Δ versus production halving.

| workload | halving + dedupe | culprit-first (noisy) | culprit-first + dedupe | culprit-first + dedupe, perfect scores |
|---|---|---|---|---|
| default-day | −27.3% ±5.9 | −6.8% ±5.9 | −30.1% ±8.4 | −17.1% ±10.0 |
| mixture-day | −26.1% ±5.3 | −12.5% ±8.3 | −23.1% ±7.5 | −22.8% ±7.7 |
| beta-loaded | −23.6% ±2.1 | −22.3% ±6.2 | −36.6% ±3.6 | −60.4% ±1.6 |
| agent-burst-depth4 | −23.2% ±3.0 | −21.7% ±5.7 | −34.9% ±3.8 | −56.8% ±3.4 |

## Findings, including what does not help

1. **Fail-fast CI is the largest single lever when its assumption holds.** If a failing run stops at 30% of its duration:
   - runner-minutes fall by 4–7% at depth one;
   - they fall by 44% in the burst depth-four workload, where 65% of baseline minutes are waste;
   - loaded depth-one latency falls by 78%.

   The saving scales with (1 − fraction) × the failed-minute share, so it must be measured against real suites before anyone relies on it.
2. **Queue cancellation recovers almost nothing here.** It recovers 0.009 minutes per landed change at depth four. A speculative dependent of a failing batch contains the culprit, so it fails at the same moment instead of being cancelled. Earlier failure signals (fail-fast) are what shorten those runs.
3. **Same-SHA failed-check retries are 23% of depth-four minutes** (0.37 of 1.63 per landed change). The retry policy cannot tell a flake from a real failure. This makes *minimal flake reruns* (ADR 0015) the next candidate to evaluate.
4. **Exact tree dedupe is free without flakes.** It changes no outcome (`TestTreeDedupePreservesOutcomes`, plus all invariants on every lab run).
   - Most hits are the bisection right half: after the left half lands, the right half's tree is exactly the failed batch's tree.
   - It saves 21% of minutes at depth four and 5% of minutes in loaded depth one, where it also cuts p50 latency by 66%. Elsewhere it saves about 1%.
   - **Hazard:** reusing a flaky failure dequeues innocent changes. Wrongful dequeues rise from 9.4 to 11.9 per run at 3% flakes, and the isolation model shows the same effect. Production dedupe of failures needs a confirmed failure, e.g. after a retry, or a flake-aware rule.
5. **Global adaptive sizing (production `BatchTargetPassRate`) hurts under depth-one load.**
   - In beta-loaded it costs +10% runner-minutes and +181% p50 latency: shrinking batches lowers throughput once the queue is busy.
   - At depth four it improves latency by 27% at equal cost.
   - It is neutral on light loads.
6. **Risk-weighted sizing helps only with speculation.** At depth four it gives −8% minutes and −45% p50. At depth one it is neutral to slightly worse (+0–2% minutes). It does not beat fixed batch-8 in the loaded depth-one case (+2.2% ±2.3, not significant). It does beat global adaptive there.
7. **Bayesian culprit-first is worth roughly 12 percentage points beyond dedupe, but only for large failed batches.** In beta-loaded and the burst workload it gives −35 to −37% runs against −23 to −24% for halving with dedupe.
   - On small batches it is no better than halving with dedupe.
   - It slightly delays innocent changes, which land only after the suspect's run.
   - With perfect scores the reduction reaches 57–60%. The value is in score quality, i.e. in ADR 0014 facts. Shipping it needs a reducer change under its own ADR slice.
8. **Combining strategies is not additive.** Risk-sizing's extra small batches partly cancel fail-fast's gain at depth one (mixture-day: −2.6% combined vs −4.7% for fail-fast alone).

## Limitations

- **Synthetic workloads.** Flakes are independent per run, and CI is deterministic per tree except for flakes. There are no per-path or partial-test effects, so per-path memoization is not evaluated.
- **Dedupe scope.** Dedupe reuses only completed results; it does not join an in-flight run on the same tree.
- **Risk sizing.** The sizing pass approximates the planner's candidate list: all unbatched trusted entries in queue order. That is exact for these workloads, which have no dependency gates or approvals.
- **Isolation model.** The model is serial at depth one and excludes queueing delay, so its innocent delay is isolation time, not end-to-end latency. Its halving calibration does not apply at depth four, where bisection interacts with retries and chain invalidation (0/10 seeds match).
- **Cost model.** Runner cost is minutes only. ADR 0013 per-start charges are not priced, but runs per landed change are reported.
- **Throughput.** Throughput is arrival-limited in the underloaded workloads. Only beta-loaded shows throughput differences: 658 to 752 landed per day.

## Full tables

Seeds: 10 ([1 2 3 4 5 6 7 8 9 10]). Values are means with 95% Student-t half-widths across seeds; Δ is the seed-paired change versus baseline.

### default-day

sim DefaultConfig (doc.go): 500 PRs/24h, 5% global broken, fixed 2-min CI; batch 8 / 30s wait from TestExit500PRDay

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1.70 ±0.04 | +0.0% ±0.0 | 0.852 ±0.020 | 0.15 ±0.02 | 9.0 ±1.1 | 2.5 ±0.0 | +0.0% ±0.0 | 3.8 ±0.1 | +0.0% ±0.0 | 476 ±3 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 1.69 ±0.04 | -1.0% ±0.5 | 0.844 ±0.018 | 0.12 ±0.01 | 7.3 ±0.8 | 2.5 ±0.0 | +0.0% ±0.0 | 3.8 ±0.1 | -0.9% ±0.5 | 476 ±3 | 7.2 ±2.8 | 0.0 ±0.0 |
| global-adaptive | 1.71 ±0.04 | +0.5% ±0.3 | 0.856 ±0.020 | 0.15 ±0.02 | 8.9 ±1.1 | 2.5 ±0.0 | +0.0% ±0.0 | 3.9 ±0.1 | +1.4% ±1.2 | 476 ±3 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 1.71 ±0.04 | +0.1% ±0.2 | 0.853 ±0.021 | 0.15 ±0.02 | 9.0 ±1.1 | 2.5 ±0.0 | +0.0% ±0.0 | 3.9 ±0.1 | +0.3% ±0.6 | 476 ±3 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 1.63 ±0.03 | -4.4% ±0.7 | 0.867 ±0.020 | 0.04 ±0.01 | 2.7 ±0.3 | 2.5 ±0.0 | +0.0% ±0.0 | 3.7 ±0.0 | -3.6% ±1.0 | 476 ±3 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 1.62 ±0.03 | -4.7% ±0.8 | 0.855 ±0.017 | 0.04 ±0.00 | 2.3 ±0.2 | 2.5 ±0.0 | +0.0% ±0.0 | 3.7 ±0.0 | -3.7% ±1.2 | 476 ±3 | 6.3 ±2.5 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs.

| strategy | failed | invalidated | cancelled | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 0.15 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.10 ±0.03 |
| tree-dedupe | 0.12 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.07 ±0.02 |
| global-adaptive | 0.15 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.10 ±0.03 |
| risk-sized | 0.15 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.11 ±0.03 |
| fail-fast | 0.04 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.06 ±0.02 |
| combined | 0.04 ±0.00 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.05 ±0.02 |

### mixture-day

default-day arrivals; synthetic risk mixture 85%@1.5% + 15%@25% (mean 5.0%, matched to DefaultConfig), logit score noise 0.5; lognormal CI median 2m sigma 0.3

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1.75 ±0.03 | +0.0% ±0.0 | 0.842 ±0.011 | 0.18 ±0.04 | 10.3 ±1.9 | 2.8 ±0.0 | +0.0% ±0.0 | 4.8 ±0.2 | +0.0% ±0.0 | 475 ±5 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 1.74 ±0.03 | -0.8% ±0.6 | 0.835 ±0.010 | 0.14 ±0.02 | 7.9 ±1.3 | 2.8 ±0.0 | -0.7% ±1.1 | 4.5 ±0.1 | -4.7% ±2.8 | 475 ±5 | 7.4 ±2.5 | 0.0 ±0.0 |
| global-adaptive | 1.76 ±0.03 | +0.9% ±0.9 | 0.850 ±0.010 | 0.17 ±0.03 | 9.6 ±1.5 | 2.9 ±0.1 | +1.3% ±0.9 | 4.9 ±0.3 | +1.9% ±5.2 | 475 ±5 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 1.77 ±0.04 | +1.5% ±0.9 | 0.855 ±0.016 | 0.14 ±0.02 | 7.6 ±1.1 | 2.9 ±0.0 | +1.6% ±1.2 | 4.7 ±0.2 | -1.2% ±5.5 | 475 ±5 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 1.67 ±0.03 | -4.7% ±1.1 | 0.860 ±0.014 | 0.05 ±0.00 | 3.0 ±0.3 | 2.8 ±0.0 | -1.6% ±1.8 | 4.3 ±0.1 | -9.1% ±4.0 | 475 ±5 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 1.70 ±0.03 | -2.6% ±0.9 | 0.861 ±0.017 | 0.04 ±0.01 | 2.1 ±0.3 | 2.8 ±0.0 | -0.7% ±1.1 | 4.5 ±0.1 | -5.4% ±5.0 | 475 ±5 | 2.8 ±1.0 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs.

| strategy | failed | invalidated | cancelled | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 0.18 ±0.04 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.13 ±0.02 |
| tree-dedupe | 0.14 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.09 ±0.02 |
| global-adaptive | 0.17 ±0.03 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.12 ±0.02 |
| risk-sized | 0.14 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.05 ±0.01 |
| fail-fast | 0.05 ±0.00 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.07 ±0.01 |
| combined | 0.04 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.03 ±0.00 |

### beta-loaded

synthetic: 800 PRs/24h, Beta(0.4, 7.6) risk (mean 5%), score noise 0.5, lognormal CI median 4m sigma 0.3 (about 80% of serial batch-8 capacity)

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1.98 ±0.09 | +0.0% ±0.0 | 0.471 ±0.019 | 0.79 ±0.10 | 39.7 ±3.4 | 44.2 ±13.8 | +0.0% ±0.0 | 109.0 ±34.6 | +0.0% ±0.0 | 718 ±28 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 1.87 ±0.04 | -5.1% ±2.8 | 0.446 ±0.008 | 0.46 ±0.05 | 24.8 ±2.5 | 12.4 ±3.1 | -66.5% ±12.6 | 43.8 ±7.1 | -55.0% ±11.2 | 746 ±11 | 47.1 ±5.9 | 0.0 ±0.0 |
| global-adaptive | 2.18 ±0.12 | +10.4% ±3.9 | 0.519 ±0.027 | 0.65 ±0.05 | 29.8 ±1.2 | 118.8 ±57.4 | +181.4% ±140.9 | 221.8 ±84.8 | +102.2% ±58.4 | 658 ±34 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 2.02 ±0.09 | +2.2% ±2.3 | 0.481 ±0.019 | 0.49 ±0.06 | 24.3 ±2.2 | 47.8 ±17.7 | +13.4% ±28.5 | 117.9 ±48.0 | +8.6% ±30.7 | 707 ±29 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 1.83 ±0.03 | -7.2% ±3.0 | 0.542 ±0.015 | 0.19 ±0.02 | 10.5 ±0.8 | 7.6 ±0.4 | -78.7% ±8.0 | 21.2 ±3.8 | -77.6% ±7.3 | 752 ±6 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 1.87 ±0.02 | -5.4% ±3.5 | 0.500 ±0.009 | 0.10 ±0.01 | 5.4 ±0.6 | 11.0 ±2.4 | -70.8% ±9.8 | 34.1 ±13.4 | -65.7% ±14.0 | 750 ±7 | 21.7 ±4.7 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs.

| strategy | failed | invalidated | cancelled | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 0.79 ±0.10 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 1.12 ±0.15 |
| tree-dedupe | 0.46 ±0.05 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.72 ±0.09 |
| global-adaptive | 0.65 ±0.05 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.84 ±0.05 |
| risk-sized | 0.49 ±0.06 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.53 ±0.10 |
| fail-fast | 0.19 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.53 ±0.06 |
| combined | 0.10 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.27 ±0.05 |

### flaky-mixture-day

mixture-day with 3% flakes (synthetic stress; capacity profiles use 1%)

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1.79 ±0.04 | +0.0% ±0.0 | 0.865 ±0.014 | 0.23 ±0.03 | 13.0 ±1.6 | 2.9 ±0.0 | +0.0% ±0.0 | 5.0 ±0.2 | +0.0% ±0.0 | 466 ±5 | 0.0 ±0.0 | 9.4 ±1.9 |
| tree-dedupe | 1.79 ±0.04 | -0.3% ±0.6 | 0.862 ±0.013 | 0.20 ±0.03 | 10.9 ±1.4 | 2.9 ±0.1 | -1.0% ±0.9 | 4.7 ±0.2 | -6.3% ±2.5 | 463 ±6 | 10.4 ±2.9 | 11.9 ±2.5 |
| global-adaptive | 1.84 ±0.05 | +2.4% ±1.8 | 0.885 ±0.017 | 0.22 ±0.03 | 12.1 ±1.1 | 2.9 ±0.1 | +1.1% ±0.9 | 5.3 ±0.4 | +6.1% ±7.5 | 465 ±5 | 0.0 ±0.0 | 9.7 ±2.3 |
| risk-sized | 1.83 ±0.04 | +1.9% ±1.4 | 0.881 ±0.017 | 0.20 ±0.02 | 10.7 ±1.2 | 2.9 ±0.0 | +1.0% ±1.0 | 4.9 ±0.2 | -2.2% ±6.0 | 465 ±6 | 0.0 ±0.0 | 10.7 ±2.4 |
| fail-fast | 1.69 ±0.03 | -5.8% ±1.1 | 0.890 ±0.015 | 0.07 ±0.01 | 4.0 ±0.4 | 2.8 ±0.0 | -2.2% ±1.2 | 4.4 ±0.1 | -11.8% ±5.0 | 466 ±5 | 0.0 ±0.0 | 9.0 ±2.1 |
| combined | 1.72 ±0.03 | -4.1% ±1.2 | 0.890 ±0.019 | 0.05 ±0.01 | 3.1 ±0.4 | 2.8 ±0.0 | -2.2% ±1.3 | 4.5 ±0.1 | -9.9% ±5.5 | 463 ±5 | 4.5 ±1.4 | 12.2 ±2.3 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs.

| strategy | failed | invalidated | cancelled | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 0.23 ±0.03 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.15 ±0.02 |
| tree-dedupe | 0.20 ±0.03 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.11 ±0.02 |
| global-adaptive | 0.22 ±0.03 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.13 ±0.02 |
| risk-sized | 0.20 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.06 ±0.02 |
| fail-fast | 0.07 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.10 ±0.02 |
| combined | 0.05 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.03 ±0.01 |

### agent-burst-depth4

capacity.AdaptiveWorkload (900/day, 2-min CI, depth 4, 5%) and Profile.Config (batch 8, 5m wait, 50 runners, 1% flakes, timeout 1.5x CI) for one day; Beta(0.5, 9.5) risk (mean 5%), noise 0.5; 60% of PRs in bursts of 25 within 5m (synthetic agent bursts)

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1.63 ±0.14 | +0.0% ±0.0 | 0.816 ±0.069 | 1.06 ±0.13 | 64.9 ±2.4 | 16.2 ±3.5 | +0.0% ±0.0 | 61.7 ±13.4 | +0.0% ±0.0 | 850 ±6 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 1.29 ±0.09 | -20.7% ±1.4 | 0.646 ±0.045 | 0.72 ±0.08 | 55.8 ±2.4 | 16.2 ±3.6 | -0.3% ±1.1 | 61.3 ±12.7 | -0.2% ±2.0 | 850 ±6 | 132.3 ±18.4 | 0.0 ±0.0 |
| global-adaptive | 1.60 ±0.12 | -1.2% ±2.7 | 0.804 ±0.059 | 0.83 ±0.07 | 51.5 ±1.5 | 11.5 ±2.5 | -27.4% ±10.1 | 39.1 ±5.6 | -33.7% ±9.5 | 851 ±3 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 1.48 ±0.09 | -8.4% ±4.1 | 0.744 ±0.046 | 0.70 ±0.08 | 46.8 ±2.5 | 8.4 ±2.0 | -45.2% ±12.8 | 31.8 ±5.9 | -45.5% ±11.1 | 852 ±3 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 0.90 ±0.05 | -44.4% ±1.9 | 0.784 ±0.058 | 0.28 ±0.03 | 31.4 ±1.8 | 6.9 ±0.2 | -52.5% ±13.9 | 18.8 ±2.6 | -67.7% ±5.9 | 854 ±5 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 0.96 ±0.03 | -40.2% ±3.4 | 0.663 ±0.035 | 0.15 ±0.02 | 15.8 ±1.3 | 5.8 ±0.5 | -60.4% ±9.9 | 13.2 ±1.7 | -77.1% ±4.4 | 854 ±5 | 53.1 ±7.7 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs.

| strategy | failed | invalidated | cancelled | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 1.05 ±0.13 | 0.00 ±0.00 | 0.009 ±0.002 | 0.00 ±0.00 | 0.37 ±0.04 | 0.76 ±0.10 | 1.01 ±0.12 |
| tree-dedupe | 0.71 ±0.08 | 0.00 ±0.00 | 0.009 ±0.002 | 0.00 ±0.00 | 0.37 ±0.04 | 0.42 ±0.05 | 0.67 ±0.07 |
| global-adaptive | 0.82 ±0.08 | 0.00 ±0.00 | 0.008 ±0.002 | 0.00 ±0.00 | 0.30 ±0.02 | 0.81 ±0.09 | 0.65 ±0.05 |
| risk-sized | 0.69 ±0.08 | 0.00 ±0.00 | 0.009 ±0.003 | 0.00 ±0.00 | 0.24 ±0.03 | 0.73 ±0.06 | 0.45 ±0.07 |
| fail-fast | 0.28 ±0.03 | 0.00 ±0.00 | 0.001 ±0.000 | 0.00 ±0.00 | 0.11 ±0.01 | 0.25 ±0.02 | 0.44 ±0.05 |
| combined | 0.15 ±0.02 | 0.00 ±0.00 | 0.002 ±0.000 | 0.00 ±0.00 | 0.08 ±0.01 | 0.35 ±0.02 | 0.17 ±0.03 |

### team-slow-depth4

capacity.Team (20/day for 30 days, 30-min CI, depth 4, 2%) and Profile.Config; synthetic mixture 90%@0.5% + 10%@15.5% (mean 2%), noise 0.5

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 29.71 ±0.46 | +0.0% ±0.0 | 0.992 ±0.015 | 1.74 ±0.40 | 5.8 ±1.3 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 29.65 ±0.44 | -0.2% ±0.1 | 0.990 ±0.015 | 1.67 ±0.37 | 5.6 ±1.2 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 1.2 ±0.8 | 0.0 ±0.0 |
| global-adaptive | 29.71 ±0.46 | +0.0% ±0.0 | 0.992 ±0.015 | 1.74 ±0.40 | 5.8 ±1.3 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 29.76 ±0.45 | +0.2% ±0.2 | 0.994 ±0.015 | 1.70 ±0.40 | 5.7 ±1.3 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 28.55 ±0.24 | -3.9% ±0.8 | 0.989 ±0.015 | 0.47 ±0.11 | 1.7 ±0.4 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 28.60 ±0.23 | -3.7% ±0.8 | 0.990 ±0.014 | 0.45 ±0.10 | 1.6 ±0.3 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 1.0 ±0.8 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs.

| strategy | failed | invalidated | cancelled | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 1.70 ±0.39 | 0.00 ±0.00 | 0.039 ±0.023 | 0.00 ±0.00 | 0.90 ±0.20 | 8.91 ±0.46 | 0.25 ±0.17 |
| tree-dedupe | 1.64 ±0.36 | 0.00 ±0.00 | 0.039 ±0.023 | 0.00 ±0.00 | 0.90 ±0.20 | 8.85 ±0.46 | 0.19 ±0.13 |
| global-adaptive | 1.70 ±0.39 | 0.00 ±0.00 | 0.039 ±0.023 | 0.00 ±0.00 | 0.90 ±0.20 | 8.91 ±0.46 | 0.25 ±0.17 |
| risk-sized | 1.67 ±0.39 | 0.00 ±0.00 | 0.039 ±0.023 | 0.00 ±0.00 | 0.88 ±0.20 | 9.01 ±0.45 | 0.21 ±0.16 |
| fail-fast | 0.46 ±0.11 | 0.00 ±0.00 | 0.010 ±0.005 | 0.00 ±0.00 | 0.48 ±0.10 | 8.36 ±0.37 | 0.12 ±0.08 |
| combined | 0.44 ±0.10 | 0.00 ±0.00 | 0.010 ±0.005 | 0.00 ±0.00 | 0.48 ±0.11 | 8.42 ±0.37 | 0.08 ±0.06 |

### Culprit isolation model (strategy c)

Replays every failed fresh batch from the baseline runs (deterministic CI, no flakes). Runs and minutes exclude the batch's own failing run; innocent delay is the mean time from the batch failure until an innocent change's landing run completes, serial at depth one.

| workload | policy | failed batches | runs/failed batch | Δ runs | runner-min/failed batch | innocent delay (min) | Δ delay | false culprits |
|---|---|---|---|---|---|---|---|---|
| default-day | halving (production) | 95 | 2.53 ±0.34 | – | 5.1 ±0.7 | 4.0 ±0.7 | – | 0 |
| default-day | halving + tree dedupe | 95 | 1.84 ±0.28 | -27.3% ±5.9 | 3.7 ±0.6 | 3.6 ±0.6 | -7.6% ±4.5 | 0 |
| default-day | culprit-first (noisy scores) | 95 | 2.34 ±0.29 | -6.8% ±5.9 | 4.7 ±0.6 | 3.8 ±0.7 | -4.5% ±6.5 | 0 |
| default-day | culprit-first + tree dedupe | 95 | 1.76 ±0.30 | -30.1% ±8.4 | 3.5 ±0.6 | 3.6 ±0.7 | -8.9% ±7.4 | 0 |
| default-day | culprit-first + dedupe, perfect scores | 95 | 2.04 ±0.09 | -17.1% ±10.0 | 4.1 ±0.2 | 4.1 ±0.2 | +7.5% ±17.9 | 0 |
| default-day | halving + fail-fast 30% | 95 | 2.53 ±0.34 | +0.0% ±0.0 | 3.3 ±0.4 | 2.9 ±0.4 | -26.1% ±4.3 | 0 |
| default-day | culprit-first + dedupe + fail-fast 30% | 95 | 1.76 ±0.30 | -30.1% ±8.4 | 2.7 ±0.3 | 2.7 ±0.4 | -30.5% ±5.3 | 0 |
| mixture-day | halving (production) | 112 | 2.65 ±0.23 | – | 5.3 ±0.5 | 4.1 ±0.6 | – | 0 |
| mixture-day | halving + tree dedupe | 112 | 1.96 ±0.22 | -26.1% ±5.3 | 3.9 ±0.4 | 3.7 ±0.6 | -9.7% ±4.2 | 0 |
| mixture-day | culprit-first (noisy scores) | 112 | 2.30 ±0.17 | -12.5% ±8.3 | 4.6 ±0.3 | 4.2 ±0.4 | +3.1% ±11.5 | 0 |
| mixture-day | culprit-first + tree dedupe | 112 | 2.02 ±0.18 | -23.1% ±7.5 | 4.0 ±0.4 | 4.0 ±0.4 | -0.6% ±12.4 | 0 |
| mixture-day | culprit-first + dedupe, perfect scores | 112 | 2.02 ±0.03 | -22.8% ±7.7 | 4.0 ±0.1 | 4.0 ±0.1 | +1.7% ±17.6 | 0 |
| mixture-day | halving + fail-fast 30% | 112 | 2.65 ±0.23 | +0.0% ±0.0 | 3.4 ±0.3 | 3.0 ±0.3 | -26.5% ±4.5 | 0 |
| mixture-day | culprit-first + dedupe + fail-fast 30% | 112 | 2.02 ±0.18 | -23.1% ±7.5 | 2.8 ±0.2 | 2.8 ±0.2 | -31.3% ±8.5 | 0 |
| beta-loaded | halving (production) | 345 | 5.85 ±0.32 | – | 23.4 ±1.3 | 15.5 ±0.9 | – | 0 |
| beta-loaded | halving + tree dedupe | 345 | 4.47 ±0.31 | -23.6% ±2.1 | 17.9 ±1.2 | 12.9 ±0.9 | -16.5% ±1.8 | 0 |
| beta-loaded | culprit-first (noisy scores) | 345 | 4.56 ±0.50 | -22.3% ±6.2 | 18.2 ±2.0 | 14.1 ±1.4 | -8.8% ±7.5 | 0 |
| beta-loaded | culprit-first + tree dedupe | 345 | 3.71 ±0.33 | -36.6% ±3.6 | 14.9 ±1.3 | 12.0 ±0.9 | -22.5% ±4.4 | 0 |
| beta-loaded | culprit-first + dedupe, perfect scores | 345 | 2.31 ±0.13 | -60.4% ±1.6 | 9.3 ±0.5 | 9.2 ±0.5 | -40.3% ±3.3 | 0 |
| beta-loaded | halving + fail-fast 30% | 345 | 5.85 ±0.32 | +0.0% ±0.0 | 14.8 ±0.7 | 10.1 ±0.4 | -34.3% ±1.0 | 0 |
| beta-loaded | culprit-first + dedupe + fail-fast 30% | 345 | 3.71 ±0.33 | -36.6% ±3.6 | 10.4 ±1.0 | 8.4 ±0.7 | -45.8% ±3.9 | 0 |
| flaky-mixture-day | halving (production) | 135 | 2.56 ±0.20 | – | 5.1 ±0.4 | 4.0 ±0.4 | – | 0 |
| flaky-mixture-day | halving + tree dedupe | 135 | 1.84 ±0.16 | -28.2% ±3.7 | 3.7 ±0.3 | 3.6 ±0.4 | -10.1% ±3.6 | 24 |
| flaky-mixture-day | culprit-first (noisy scores) | 135 | 2.23 ±0.14 | -12.1% ±6.8 | 4.5 ±0.3 | 4.0 ±0.4 | -0.3% ±7.5 | 0 |
| flaky-mixture-day | culprit-first + tree dedupe | 135 | 1.86 ±0.16 | -27.0% ±5.9 | 3.7 ±0.3 | 3.9 ±0.5 | -3.0% ±8.5 | 24 |
| flaky-mixture-day | culprit-first + dedupe, perfect scores | 135 | 1.88 ±0.09 | -25.7% ±7.5 | 3.8 ±0.2 | 3.8 ±0.2 | -1.8% ±12.8 | 24 |
| flaky-mixture-day | halving + fail-fast 30% | 135 | 2.56 ±0.20 | +0.0% ±0.0 | 3.5 ±0.3 | 3.1 ±0.3 | -23.1% ±2.3 | 0 |
| flaky-mixture-day | culprit-first + dedupe + fail-fast 30% | 135 | 1.86 ±0.16 | -27.0% ±5.9 | 2.7 ±0.2 | 2.7 ±0.3 | -31.6% ±3.8 | 24 |
| agent-burst-depth4 | halving (production) | 243 | 5.31 ±0.38 | – | 10.6 ±0.8 | 7.8 ±0.5 | – | 0 |
| agent-burst-depth4 | halving + tree dedupe | 243 | 4.07 ±0.26 | -23.2% ±3.0 | 8.1 ±0.5 | 6.6 ±0.4 | -15.1% ±2.7 | 9 |
| agent-burst-depth4 | culprit-first (noisy scores) | 243 | 4.16 ±0.42 | -21.7% ±5.7 | 8.3 ±0.8 | 6.8 ±0.6 | -12.9% ±6.6 | 0 |
| agent-burst-depth4 | culprit-first + tree dedupe | 243 | 3.45 ±0.27 | -34.9% ±3.8 | 6.9 ±0.5 | 5.9 ±0.4 | -24.1% ±4.4 | 9 |
| agent-burst-depth4 | culprit-first + dedupe, perfect scores | 243 | 2.27 ±0.12 | -56.8% ±3.4 | 4.5 ±0.2 | 4.5 ±0.3 | -41.3% ±3.6 | 9 |
| agent-burst-depth4 | halving + fail-fast 30% | 243 | 5.31 ±0.38 | +0.0% ±0.0 | 6.8 ±0.5 | 5.1 ±0.2 | -34.4% ±0.9 | 0 |
| agent-burst-depth4 | culprit-first + dedupe + fail-fast 30% | 243 | 3.45 ±0.27 | -34.9% ±3.8 | 4.9 ±0.3 | 4.1 ±0.3 | -47.1% ±3.0 | 9 |
| team-slow-depth4 | halving (production) | 10 | 2.17 ±0.43 | – | 65.0 ±12.9 | 53.8 ±26.5 | – | 0 |
| team-slow-depth4 | halving + tree dedupe | 10 | 1.75 ±0.80 | -20.8% ±25.8 | 52.5 ±23.9 | 53.8 ±26.5 | +0.0% ±0.0 | 0 |
| team-slow-depth4 | culprit-first (noisy scores) | 10 | 2.00 ±0.00 | -5.6% ±14.3 | 60.0 ±0.0 | 50.0 ±12.9 | +14.4% ±71.7 | 0 |
| team-slow-depth4 | culprit-first + tree dedupe | 10 | 1.67 ±0.43 | -22.2% ±20.5 | 50.0 ±12.9 | 50.0 ±12.9 | +14.4% ±71.7 | 0 |
| team-slow-depth4 | culprit-first + dedupe, perfect scores | 10 | 2.00 ±0.00 | -5.6% ±14.3 | 60.0 ±0.0 | 60.0 ±0.0 | +32.5% ±59.9 | 0 |
| team-slow-depth4 | halving + fail-fast 30% | 10 | 2.17 ±0.43 | +0.0% ±0.0 | 42.2 ±8.4 | 38.9 ±12.0 | -21.8% ±18.5 | 0 |
| team-slow-depth4 | culprit-first + dedupe + fail-fast 30% | 10 | 1.67 ±0.43 | -22.2% ±20.5 | 36.0 ±3.9 | 36.0 ±3.9 | -19.3% ±42.0 | 0 |

### Isolation model calibration (halving model vs planner bisection runs, baseline)

The model claims exactness only at depth one; at depth four, bisection interacts with failed-check retries and chain invalidation.

| workload | applies | seeds matching | model runs | planner runs |
|---|---|---|---|---|
| default-day | yes | 10/10 | 248 | 248 |
| mixture-day | yes | 10/10 | 296 | 296 |
| beta-loaded | yes | 10/10 | 2024 | 2024 |
| flaky-mixture-day | n/a | 10/10 | 344 | 344 |
| agent-burst-depth4 | n/a | 0/10 | 1292 | 3140 |
| team-slow-depth4 | n/a | 5/10 | 22 | 35 |
