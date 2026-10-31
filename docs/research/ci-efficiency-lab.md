# CI efficiency lab: strategy comparison

Status: evidence for [ADR 0015](../adr/0015-ci-efficiency-engine.md). Deploy class: Inert (simulator and tests only). Revised after review round 1 on #211: cancellation is now measured against a no-cancellation arm; variable-duration and mid-flight invalidation workloads are added; adaptive sizing is a load curve; and culprit-first is shown as a score-quality curve.

## What this is

`internal/sim` drives the real planner (`internal/queue`) and executor against the
fake forge on a virtual clock. `Config.Lab` (nil by default) adds:

- **A runner-minute ledger.** Every CI run is charged for its duration: first attempts, bisection runs, same-SHA reruns, builds on a speculative predecessor, and cancelled runs (charged up to cancellation). A run *contributed* when it is the final, uncancelled run of a tested SHA that landed; everything else is waste, split into failed, invalidated, cancelled and superseded minutes.
  - **Cancelled minutes are residual waste**: the time charged to runs *before* they were cancelled (`CancelledResidualMinutes`). They are not the minutes cancellation recovered. Recovered minutes are measured only as the seed-paired difference against the `no-cancel` arm, which runs the same CI without cancellation support.
- **Workload realism** that never touches the legacy random streams:
  - beta or mixture per-change failure probabilities;
  - a strategy-visible risk score: the truth with Gaussian noise in logit space, standing in for [ADR 0014](https://github.com/CanadaApollo6/openmerge/pull/207) facts (PR #207, not yet merged). Noise has its own stream, so a noise sweep keeps identical truth, arrivals and baseline runs (`TestScoreNoiseKeepsTruth`);
  - lognormal CI durations. Each run's duration is derived from (seed, tested tree, attempt), so an arm that skips runs, like tree dedupe, sees the same durations for the same trees as the baseline (`TestLabDurationsArePairedByTree`);
  - fail-fast for failing runs;
  - bursty agent arrivals;
  - **mid-flight invalidation** (`Lab.Invalidation`, sim-only). A share of changes is re-pushed (head changed: dequeue and re-enqueue), and a share is dequeued by request. Each event fires after a uniform or exponential delay, timed from when the change first enters a started batch. The legacy `RepushRate` fires within a second of arrival, before any CI runs, so it never exercised cancellation;
  - exact tree identity: a SHA-256 of the test commit's file tree.
- **Sim-only strategies:**
  - exact tree dedupe in the simulated CI;
  - risk-weighted batch sizing, as a per-pass `BatchMaxSize` override passed to the real planner;
  - a **backlog guard**: a candidate rule that keeps `BatchMaxSize`, disabling both risk sizing and the production `BatchTargetPassRate` for that pass, whenever a full batch of trusted changes is waiting.
- **An isolation model** (`Isolate`) for strategy (c). Bayesian culprit-first changes the reducer's bisection rule (`State.failBatch`). Changing that rule would mean touching production code, so the model replays every failed batch from the baseline runs instead. At depth one without flakes, the halving model reproduces the planner's bisection run count exactly on every calibrated seed (see the calibration tables). At depth four it does not: it predicts 1208 runs where the planner ran 3045 for agent-burst-depth4. Depth-four isolation rows are therefore labelled **uncalibrated** and are kept out of the headline.

**No production hook was added.** Planner, executor, store and serve code is unchanged. The overrides live only in `simulation.plannerConfig`, and only when a lab strategy sets them. With a lab config and no strategy, every outcome, draw, timing and metric is byte-identical to the plain run (`TestLabInstrumentationIsTransparent`).

## Reproduce

```sh
# Full suite: 6 matrices, 10 seeds, about 2 minutes on 16 cores. Checks the committed summary.
om-go go test -count=1 ./internal/sim -run '^TestEfficiencyLab$'
# Keep efficiency-lab.json (summary), efficiency-lab-runs.json (every per-seed run) and efficiency-lab.md:
OPENMERGE_LAB_OUT=/some/dir om-go go test -count=1 ./internal/sim -run '^TestEfficiencyLab$'
# Rewrite the committed summary after an intentional change:
OPENMERGE_LAB_UPDATE=1 om-go go test -count=1 ./internal/sim -run '^TestEfficiencyLab$'
# Smoke (part of -short): 1 × 3 × 2, run twice for determinism.
om-go go test -short ./internal/sim -run '^TestEfficiencyLab$'
```

`ci-efficiency-lab.json` is the summary, not the raw runs. Every cell carries the mean and 95% half-width for each metric, plus the isolation, calibration and cancellation tables, at 4 significant digits (107 KB). The raw per-seed runs (2.4 MB) are fully reproducible from the command above. The non-short `TestEfficiencyLab` regenerates the summary and fails if it differs from the committed file; it tolerates only last-digit rounding differences from platform floating point. Two independent runs on amd64 produced byte-identical output:

- `ci-efficiency-lab.json`: sha256 `d94a1771…3068ff`
- `efficiency-lab.md`, the generated tables below before their headings were shifted one level: sha256 `545339a3…bec86`

## Parameter sources (ADR 0009 seal respected)

No parameter comes from the OpenMerge or Smartfire databases, or from OpenMerge dogfood latency or runs-per-merge. Workloads reuse:

- the simulator defaults (`DefaultConfig`, doc.go);
- the exit-test policy (`TestExit500PRDay`, batch 8 / 30 s);
- the [ADR 0009](../adr/0009-speculative-batching.md) capacity profiles (`capacity.AdaptiveWorkload`, `capacity.Team`, `Profile.Config`).

Distributions are synthetic, with means matched to those defaults (5% or 2%). These are assumptions, not measurements:

- the fail-fast fractions (30/60/90%);
- the score noise (σ = 0.5 logit by default);
- the duration spread (σ 0.3 and 0.6);
- the invalidation rates (10% and 25% re-pushed, 2% dequeued) and their delays.

## Corrected findings

Δ is seed-paired against the matrix's baseline (mean ± 95% CI over 10 seeds). "Minutes" means runner-minutes per landed change.

| # | Finding | Evidence (Δ minutes; Δ p50 where it matters) | Confidence |
|---|---|---|---|
| 1 | **Fail-fast is the largest lever, roughly linear in how early failing runs stop.** | At depth 4: −44% at 30%, −26% at 60%, −6% at 90%. At depth 1: −4 to −6% at 30%; loaded depth-1 p50 −69% / −61% / −29%. | High that it scales with the failed-minute share. The fraction is a property of real suites and is unmeasured. |
| 2 | **Cancellation's value scales with mid-flight invalidation and timeouts, not with depth alone.** | Fixed CI, no invalidation: 0.2% ±0.1. Duration σ 0.3 / 0.6: 0.9% / 2.4%. Adding 10% / 25% re-pushes: 12–15% / 24–27%. A 1.5× timeout under σ 0.6: 17%. With 3 runners (contended) it also cuts p50 by 18% ±9. Slow team CI with churn: 7.7%. | High within the model. Real invalidation rates are unmeasured. |
| 3 | **Exact tree dedupe saves about 20% at depth 4, and is free without flakes.** | −19 to −20% at every depth-4 burst workload; −3.4% (p50 −54%) in loaded depth 1; about −1% elsewhere. Flakes are the hazard: 3% flakes raise wrongful dequeues from 9.3 to 11.5 per run. | High; the outcome identity is tested. Production needs a flake-aware rule for reusing failures. |
| 4 | **Adaptive sizing at a 0.8 target is neutral at light load and harmful above about 60–70% load at depth 1. A backlog guard removes the harm.** | Global 0.8 p50 by load: 300/day +1%; 450 +10% ±6; 600 +94% ±87; 700 +202% ±138; 800 +204% ±100. Target 0.6 stays within noise at every load. With the guard, p50 stays within +9% at every load and minutes are +0–1.7%. At depth 4 it helps: −24 to −32% p50. | High for the direction; the size of the large deltas is noisy. |
| 5 | **Risk-weighted sizing helps only with speculation, and does not need precise scores there.** | Depth 4: −8 to −10% minutes and −45 to −54% p50, unchanged up to noise 1; at noise 2, minutes +2% but p50 still −47%. At depth 1 it is never better than fixed: +0–2% minutes at the default noise, up to +15% at noise 2 (beta-loaded). | Medium. The depth-4 p50 gain is mostly from smaller batches, which global adaptive also gets (−29%). |
| 6 | **Culprit-first isolation is worth about 12 points beyond dedupe on large failed batches, degrades gracefully with score noise, and is capped well below an outcome oracle.** | Calibrated loaded depth 1, Δ runs per failed batch with dedupe: −38% at noise 0, −36% at 0.5, −34% at 1, −29% at 2, against −24% for halving with dedupe. An outcome oracle reaches −58%. On small batches (default-day, mixture-day) it is no better than halving with dedupe. | Medium: depth-1 model only. Depth-4 rows are uncalibrated. |
| 7 | **Same-SHA failed-check retries are 23% of depth-4 minutes** | 0.36 of 1.59 minutes per landed change (agent-burst-depth4). | High; it is in the ledger directly. |
| 8 | **Strategies are not additive.** | Mixture-day: combined −2.2% versus fail-fast alone −4.6%, because risk sizing's extra small batches eat the gain. At depth 4: combined −40% versus fail-fast −44%. | High. |

Headline: runner-minutes per landed change (main matrix):

| workload | no-cancel (cost of no cancellation) | (b) tree-dedupe | (d) global-adaptive 0.8 | (d) risk-sized | (e) fail-fast 30% | combined b+d+e |
|---|---|---|---|---|---|---|
| default-day | n/a (no cancellation) | −1.0% ±0.5 | +0.5% ±0.3 | +0.1% ±0.2 | −4.4% ±0.7 | −4.7% ±0.8 |
| mixture-day | n/a | −1.0% ±0.3 | +0.7% ±0.7 | +1.5% ±0.5 | −4.6% ±0.7 | −2.2% ±0.8 |
| beta-loaded (depth 1, ~80% load) | n/a | −3.4% ±1.4 | **+9.4% ±5.5** | +0.9% ±1.2 | −6.1% ±2.2 | −4.2% ±2.0 |
| flaky-mixture-day (3% flakes) | n/a | −1.1% ±1.0 | +2.0% ±1.8 | +1.6% ±1.1 | −5.7% ±1.2 | −2.9% ±1.4 |
| agent-burst-depth4 (fixed CI) | +0.2% ±0.1 | **−20.3% ±0.7** | −1.0% ±1.9 | −9.4% ±2.7 | **−44.3% ±1.7** | −39.8% ±2.9 |
| agent-burst-depth4-var (σ 0.6) | +2.5% ±0.5 | −19.0% ±1.0 | −0.6% ±2.2 | −9.7% ±3.0 | −43.9% ±2.1 | −39.8% ±3.3 |
| agent-burst-depth4-churn (σ 0.6, 10% re-push, 2% dequeue) | **+18.1% ±1.9** | −19.8% ±2.3 | −1.7% ±4.6 | −8.4% ±3.3 | −44.0% ±2.6 | −37.6% ±4.0 |
| team-slow-depth4 (fixed 30-min CI) | +0.3% ±0.2 | −0.2% ±0.2 | 0.0% | +0.3% ±0.2 | −3.5% ±0.6 | −3.3% ±0.6 |
| team-slow-depth4-churn | +8.4% ±0.8 | −0.2% ±0.2 | 0.0% | +1.1% ±1.4 | −3.5% ±1.5 | −3.1% ±1.8 |

p50 enqueue-to-land latency, where it moves:

| workload | tree-dedupe | global-adaptive 0.8 | risk-sized | fail-fast 30% | combined |
|---|---|---|---|---|---|
| beta-loaded | −53.6% ±14.7 | **+204% ±100** | +29% ±44 | −68.6% ±17.0 | −58.2% ±20.3 |
| agent-burst-depth4 | −1.0% ±3.1 | −28.7% ±7.7 | −44.9% ±13.0 | −49.2% ±13.9 | −57.8% ±9.4 |
| agent-burst-depth4-var | −9.3% ±5.6 | −24.2% ±3.5 | −53.8% ±6.8 | −66.9% ±7.5 | −70.8% ±7.3 |
| agent-burst-depth4-churn | −12.8% ±11.7 | −32.4% ±12.1 | −52.7% ±5.8 | −70.7% ±4.8 | −74.1% ±4.8 |
| other five workloads | \|Δ\| ≤ 0.5% | \|Δ\| ≤ 2.7% | \|Δ\| ≤ 3.2% | \|Δ\| ≤ 1.9% | \|Δ\| ≤ 1.5% |

Strategy (c), isolation model, **calibrated (depth-one) workloads only**: CI runs per failed batch, Δ versus production halving.

| workload | halving + dedupe | culprit-first (noise 0.5) | culprit-first + dedupe | culprit-first + dedupe, outcome oracle ("perfect scores") |
|---|---|---|---|---|
| default-day | −27.3% ±5.9 | −6.8% ±5.9 | −30.1% ±8.4 | −17.1% ±10.0 |
| mixture-day | −25.1% ±5.2 | −12.0% ±4.4 | −22.1% ±6.8 | −21.5% ±6.9 |
| beta-loaded | −23.9% ±2.9 | −21.6% ±5.0 | −36.5% ±3.4 | −58.3% ±2.4 |

The depth-four rows are in the full tables, marked uncalibrated.

### 1. Fail-fast

If failing runs stop early, the saving is proportional to (1 − fraction) × the failed-minute share. Depth four has the larger share: 64% of baseline minutes are waste there, most of it failed runs on speculative predecessors and their retries.

| workload | fail at 30% | 60% | 90% |
|---|---|---|---|
| mixture-day | −4.6% ±0.7 | −2.4% ±0.3 | −0.5% ±0.2 |
| beta-loaded | −6.1% ±2.2 (p50 −69%) | −4.5% ±1.6 (p50 −61%) | −1.6% ±0.6 (p50 −29%) |
| agent-burst-depth4-var | −43.9% ±2.1 (p50 −67%) | −26.1% ±1.8 (p50 −53%) | −6.1% ±1.1 (p50 −15%) |

The fraction is not something OpenMerge controls; it is how early a real suite reports its first failure. It must be measured against real suites, for example by ordering a suite's fast or historically failing tests first, before anyone relies on these numbers.

### 2. Cancellation is a function of duration variance and invalidation rate

The first draft said "cancellation saves almost nothing". That was an artifact of fixed CI durations with no mid-flight invalidation. In that model:

- a speculative successor finishes almost exactly when its predecessor does;
- it contains the culprit, so it fails at the same instant instead of being cancelled;
- nothing else ever invalidates a running build.

Measured as no-cancel − baseline (agent-burst-depth4, seed-paired):

| CI duration | no invalidation | 10% re-pushed + 2% dequeued | 25% re-pushed + 2% dequeued |
|---|---|---|---|
| fixed | 0.2% ±0.1 | 11.8% ±1.3 | 23.7% ±1.6 |
| lognormal σ 0.3 | 0.9% ±0.2 | 13.1% ±1.6 | 25.0% ±1.5 |
| lognormal σ 0.6 | 2.4% ±0.5 | 15.3% ±1.4 | 27.3% ±2.0 |

Two more rows:

- **σ 0.6 with the fixed-CI timeout (1.5 × median): 17.0% ±0.9 without any invalidation.** About a quarter of runs exceed the timeout, and cancellation stops them; without it they run to the clamp. This probably explains the reviewer's 14% at σ 0.6: that counterfactual kept the 1.5 × timeout, and this lab's variance workloads use 3 ×.
- **3 runners instead of 50: 18.2% ±3.3 of minutes, and p50 −17.7% ±8.9.** With 50 runners, cancellation never changes latency in this model (Δ p50 is exactly zero), because no run ever waits for a runner. Once runners are contended, freed runners shorten the queue.

So cancellation is worth implementing to the extent that real queues see mid-flight head changes, dequeue requests, timeouts and runner contention. None of these has been measured for OpenMerge, because the ADR 0009 seal applies.

### 3. Tree dedupe

Most hits are the bisection right half: once the left half lands, the right half's tree is exactly the failed batch's tree. Dedupe changes no outcome without flakes (`TestTreeDedupePreservesOutcomes`, plus every sim invariant on every lab run). With flakes, reusing a flaky failure dequeues innocent changes: wrongful dequeues go from 9.3 to 11.5 per run at 3% flakes. Production dedupe of *failures* needs a confirmed failure, such as one after a retry, or a flake-aware rule. Dedupe of *successes* carries no such hazard.

### 4. Adaptive sizing is a load curve, and needs a utilisation guard

Production adaptive sizing (`batch.target_pass_rate`) is **opt-in**. [docs/operating.md](../operating.md) says to leave the target unset to keep fixed sizing, and ADR 0009 validates it only at depth four. This lab does not argue for changing that default. It shows when the opt-in hurts.

Beta-loaded (depth 1, 4-minute lognormal CI, Beta(0.4, 7.6) risk). The workload's own estimate puts 800/day at about 80% of serial batch-8 capacity:

| PRs/day | global 0.8: Δ min / Δ p50 | global 0.6 | global 0.8 + backlog guard | risk-sized 0.8 | risk-sized + guard |
|---|---|---|---|---|---|
| 300 | +0.4% / +1.2% | −0.0% / +0.0% | +0.5% / +1.3% | +1.2% / +2.8% | +1.1% / +2.5% |
| 450 | +2.5% / +9.8% ±6.1 | +0.2% / +0.5% | +1.7% / +7.1% ±7.1 | +1.9% / +3.3% | +1.7% / +2.5% |
| 600 | +3.1% / **+94% ±87** | +0.0% / −1.3% | +1.2% / +8.8% ±8.5 | +2.3% / +25% ±18 | +2.0% / +11.5% ±5.8 |
| 700 | +4.0% / **+202% ±138** | +0.2% / +3.8% | +0.5% / −10% ±17 | +1.2% / +50% ±83 | +0.7% / +5.9% ±23 |
| 800 | +9.4% / **+204% ±100** | +0.3% / +13.6% ±16 | +0.0% / +3.0% ±14 | +0.9% / +29% ±44 | −0.4% / −4.6% ±24 |

This load curve has two causes:

- **Mechanism.** At a 0.8 target and a 5% change failure rate, the size falls to (1 − p̂)^k ≥ 0.8, so k ≈ 4: fresh batches are half the configured maximum. At depth 1, serial throughput is roughly batch size per CI duration, so halving the batch halves capacity. Below about 60% utilisation that is invisible. Above it, capacity falls below the arrival rate and the queue keeps growing for the rest of the day, which is why p50 triples.
- **Target sensitivity.** A 0.6 target barely shrinks batches at a 5% rate (k = 9, capped at 8), so it stays within noise at every load.

The **backlog guard** fixes the regression in this model. It keeps the configured maximum whenever a full batch is waiting, which is a queue-observable sign of high utilisation, and leaves small-batch behaviour unchanged when the queue is short. **Candidate rule for ADR 0015:** never shrink a fresh batch below the size the waiting backlog would fill. Equivalently, cap adaptive shrinking by utilisation. The guard is sim-only here; a production version needs its own ADR slice and must be checked against the depth-4 profile, where shrinking currently helps. The sim-only guard is the `+ sim guard` arm in the regenerated summary. The production guard, which applies only at admission window 1, is evaluated in [the backlog guard evidence](adaptive-backlog-guard.md).

### 5–6. Score quality

The score-noise sweep holds truth, arrivals and the baseline runs fixed and varies only the scores. That makes it a clean value-of-information curve:

- **Risk-sized at depth 4** keeps −6 to −10% minutes up to noise 1 and loses the minute saving at noise 2 (+2.1% ±3.4). Its −45% p50 survives every noise level, which says the p50 gain comes from smaller batches, not from accurate scores.
- **Culprit-first** (calibrated, beta-loaded, Δ runs per failed batch, without / with dedupe):

  | noise | without dedupe | with dedupe |
  |---|---|---|
  | 0 | −25% | −38% |
  | 0.25 | −23% | −37% |
  | 0.5 | −22% | −36% |
  | 1 | −16% | −34% |
  | 2 | −4% | −29% |

  Even noise-free probabilities stop at −38%. Only an oracle that knows the outcome reaches −58%, because a 25%-risk change still usually passes. Better risk facts ([ADR 0014](https://github.com/CanadaApollo6/openmerge/pull/207)) can move culprit-first from about −29% to about −38%, not to −58%.

## What to build next

Ranked by expected value per unit of production risk. Each item would need its own ADR 0015 slice.

1. **Fail-fast plumbing: make an early failure signal actionable.** This has the largest modelled effect at depth 4 (−26 to −44% of minutes at 60–30%). It needs the queue to react to the first failing check, rather than waiting for the suite, and CI providers to expose that signal. *Caveat:* the fraction is unmeasured. Measure the real time-to-first-failure on adopter suites before committing.
2. **Exact tree dedupe of successful results, and of confirmed failures.** It saves about 20% at depth 4 and changes no outcome without flakes. *Caveat:* reusing an unconfirmed failure is unsafe under flakes; reuse failures only after a retry confirms them.
3. **A backlog/utilisation guard on adaptive sizing.** It is cheap and removes a +200% p50 regression at high depth-1 load in the opt-in feature. *Caveat:* it is sim-only and must be validated against the ADR 0009 depth-4 profile.
4. **Cancellation on mid-flight invalidation and timeouts.** It saves 12–27% when 10–25% of changes are re-pushed mid-flight, 17% with tight timeouts, and also latency under runner contention. It saves nothing without invalidation. *Caveat:* real invalidation rates are unknown and should be measured from forge events before ranking this above item 3.
5. **Flake-aware retries.** Same-SHA retries are 23% of depth-4 minutes. The retry policy cannot tell a flake from a real failure, so this is the next strategy to model.
6. **Culprit-first isolation.** It gives 12 points beyond dedupe on large failed batches and degrades gracefully with noise. *Caveat:* it is depth-one model evidence only, it needs a reducer change, and its value is bounded by score calibration. It is worth revisiting once ADR 0014 facts exist.
7. **Risk-weighted sizing.** Do not ship it at depth 1. At depth 4 most of its benefit comes from batch size, which global adaptive already provides.

## Limitations

- **Synthetic workloads.** Flakes are independent per run, and CI is deterministic per tree except for flakes. There are no per-path or partial-test effects, so per-path memoization is not evaluated.
- **Invalidation model.** Re-push and dequeue-request rates and delays are synthetic. Each change is invalidated at most once, timed from its first started batch. There are no base-branch pushes during flight beyond the existing `ExternalPushRate` (zero in these workloads), and no conflict-driven invalidation.
- **Cancellation.** Cancellation is instantaneous and frees the runner at once; real providers take seconds to minutes. With 50 runners no run ever waits for capacity, so latency effects appear only in the 3-runner row.
- **Durations.** Lognormal draws are clamped to [¼, 3] × median. Variance workloads use a 3 × median timeout so no run times out, except in the explicit 1.5 × row. A run's duration depends on its tree and attempt only, not on runner load.
- **Dedupe scope.** Dedupe reuses only completed results; it does not join an in-flight run on the same tree.
- **Risk sizing and the guard.** The sizing pass approximates the planner's candidate list: all unbatched trusted entries in queue order. That is exact for these workloads, which have no dependency gates or approvals. The guard's threshold, a full batch waiting, was chosen, not tuned.
- **Isolation model.** It is serial at depth one and excludes queueing delay, so its innocent delay is isolation time, not end-to-end latency. Its halving calibration does not apply at depth four (0/10 seeds match), so depth-four rows are uncalibrated.
- **Cost model.** Runner cost is minutes only. [ADR 0013](../adr/0013-ci-cost-executors.md) per-start charges are not priced, but runs per landed change are reported.
- **Throughput.** Throughput is arrival-limited in the underloaded workloads. Only beta-loaded and the load curve show throughput differences.

## Full tables

Seeds: 10 ([1 2 3 4 5 6 7 8 9 10]). Values are means with 95% Student-t half-widths across seeds; Δ is the seed-paired change versus each matrix's baseline.

### Matrix: main

Main comparison: every strategy on every workload.

#### default-day

sim DefaultConfig (doc.go): 500 PRs/24h, 5% global broken, fixed 2-min CI; batch 8 / 30s wait from TestExit500PRDay

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1.70 ±0.04 | +0.0% ±0.0 | 0.852 ±0.020 | 0.15 ±0.02 | 9.0 ±1.1 | 2.5 ±0.0 | +0.0% ±0.0 | 3.8 ±0.1 | +0.0% ±0.0 | 476 ±3 | 0.0 ±0.0 | 0.0 ±0.0 |
| no-cancel | 1.70 ±0.04 | +0.0% ±0.0 | 0.852 ±0.020 | 0.15 ±0.02 | 9.0 ±1.1 | 2.5 ±0.0 | +0.0% ±0.0 | 3.8 ±0.1 | +0.0% ±0.0 | 476 ±3 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 1.69 ±0.04 | -1.0% ±0.5 | 0.844 ±0.018 | 0.12 ±0.01 | 7.3 ±0.8 | 2.5 ±0.0 | +0.0% ±0.0 | 3.8 ±0.1 | -0.9% ±0.5 | 476 ±3 | 7.2 ±2.8 | 0.0 ±0.0 |
| global-adaptive | 1.71 ±0.04 | +0.5% ±0.3 | 0.856 ±0.020 | 0.15 ±0.02 | 8.9 ±1.1 | 2.5 ±0.0 | +0.0% ±0.0 | 3.9 ±0.1 | +1.4% ±1.2 | 476 ±3 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 1.71 ±0.04 | +0.1% ±0.2 | 0.853 ±0.021 | 0.15 ±0.02 | 9.0 ±1.1 | 2.5 ±0.0 | +0.0% ±0.0 | 3.9 ±0.1 | +0.3% ±0.6 | 476 ±3 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 1.63 ±0.03 | -4.4% ±0.7 | 0.867 ±0.020 | 0.04 ±0.01 | 2.7 ±0.3 | 2.5 ±0.0 | +0.0% ±0.0 | 3.7 ±0.0 | -3.6% ±1.0 | 476 ±3 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 1.62 ±0.03 | -4.7% ±0.8 | 0.855 ±0.017 | 0.04 ±0.00 | 2.3 ±0.2 | 2.5 ±0.0 | +0.0% ±0.0 | 3.7 ±0.0 | -3.7% ±1.2 | 476 ±3 | 6.3 ±2.5 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs. Cancelled is residual waste charged before cancellation.

| strategy | failed | invalidated | cancelled (residual) | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 0.15 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.10 ±0.03 |
| no-cancel | 0.15 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.10 ±0.03 |
| tree-dedupe | 0.12 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.07 ±0.02 |
| global-adaptive | 0.15 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.10 ±0.03 |
| risk-sized | 0.15 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.11 ±0.03 |
| fail-fast | 0.04 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.06 ±0.02 |
| combined | 0.04 ±0.00 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.05 ±0.02 |

#### mixture-day

default-day arrivals; synthetic risk mixture 85%@1.5% + 15%@25% (mean 5.0%, matched to DefaultConfig), logit score noise 0.5; lognormal CI median 2m sigma 0.3

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1.75 ±0.03 | +0.0% ±0.0 | 0.837 ±0.017 | 0.17 ±0.03 | 9.5 ±1.4 | 2.9 ±0.0 | +0.0% ±0.0 | 4.9 ±0.3 | +0.0% ±0.0 | 477 ±4 | 0.0 ±0.0 | 0.0 ±0.0 |
| no-cancel | 1.75 ±0.03 | +0.0% ±0.0 | 0.837 ±0.017 | 0.17 ±0.03 | 9.5 ±1.4 | 2.9 ±0.0 | +0.0% ±0.0 | 4.9 ±0.3 | +0.0% ±0.0 | 477 ±4 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 1.74 ±0.03 | -1.0% ±0.3 | 0.829 ±0.015 | 0.13 ±0.02 | 7.6 ±1.1 | 2.9 ±0.0 | -0.5% ±0.3 | 4.7 ±0.2 | -4.8% ±2.6 | 477 ±4 | 7.0 ±2.4 | 0.0 ±0.0 |
| global-adaptive | 1.77 ±0.04 | +0.7% ±0.7 | 0.842 ±0.022 | 0.17 ±0.03 | 9.4 ±1.3 | 2.9 ±0.0 | +0.5% ±0.6 | 5.0 ±0.3 | +1.4% ±1.6 | 477 ±4 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 1.78 ±0.03 | +1.5% ±0.5 | 0.849 ±0.017 | 0.13 ±0.02 | 7.2 ±1.3 | 3.0 ±0.1 | +2.5% ±0.8 | 5.0 ±0.2 | +1.5% ±3.6 | 477 ±4 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 1.67 ±0.02 | -4.6% ±0.7 | 0.852 ±0.020 | 0.05 ±0.01 | 2.9 ±0.5 | 2.8 ±0.0 | -1.9% ±1.0 | 4.3 ±0.1 | -11.4% ±4.5 | 477 ±4 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 1.71 ±0.02 | -2.2% ±0.8 | 0.857 ±0.017 | 0.03 ±0.01 | 2.0 ±0.3 | 2.9 ±0.1 | +0.6% ±0.8 | 4.6 ±0.1 | -5.7% ±5.3 | 477 ±4 | 2.4 ±1.0 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs. Cancelled is residual waste charged before cancellation.

| strategy | failed | invalidated | cancelled (residual) | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 0.17 ±0.03 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.13 ±0.03 |
| no-cancel | 0.17 ±0.03 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.13 ±0.03 |
| tree-dedupe | 0.13 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.09 ±0.02 |
| global-adaptive | 0.17 ±0.03 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.12 ±0.03 |
| risk-sized | 0.13 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.05 ±0.02 |
| fail-fast | 0.05 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.07 ±0.02 |
| combined | 0.03 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.03 ±0.01 |

#### beta-loaded

synthetic: 800 PRs/24h, Beta(0.4, 7.6) risk (mean 5%), score noise 0.5, lognormal CI median 4m sigma 0.3 (about 80% of serial batch-8 capacity)

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1.94 ±0.06 | +0.0% ±0.0 | 0.460 ±0.014 | 0.71 ±0.13 | 36.6 ±5.4 | 38.4 ±18.4 | +0.0% ±0.0 | 88.3 ±30.6 | +0.0% ±0.0 | 733 ±20 | 0.0 ±0.0 | 0.0 ±0.0 |
| no-cancel | 1.94 ±0.06 | +0.0% ±0.0 | 0.460 ±0.014 | 0.71 ±0.13 | 36.6 ±5.4 | 38.4 ±18.4 | +0.0% ±0.0 | 88.3 ±30.6 | +0.0% ±0.0 | 733 ±20 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 1.87 ±0.03 | -3.4% ±1.4 | 0.444 ±0.008 | 0.46 ±0.08 | 24.3 ±3.8 | 13.9 ±4.2 | -53.6% ±14.7 | 51.3 ±15.6 | -39.5% ±7.5 | 749 ±10 | 38.1 ±5.2 | 0.0 ±0.0 |
| global-adaptive | 2.12 ±0.16 | +9.4% ±5.5 | 0.504 ±0.036 | 0.60 ±0.07 | 28.0 ±1.7 | 131.3 ±83.2 | +203.9% ±99.7 | 201.7 ±99.9 | +110.4% ±55.2 | 678 ±46 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 1.95 ±0.07 | +0.9% ±1.2 | 0.464 ±0.014 | 0.45 ±0.07 | 23.0 ±2.8 | 41.8 ±17.2 | +29.4% ±43.7 | 77.3 ±22.6 | -2.8% ±31.7 | 731 ±22 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 1.81 ±0.02 | -6.1% ±2.2 | 0.532 ±0.015 | 0.17 ±0.02 | 9.6 ±1.2 | 7.3 ±0.3 | -68.6% ±17.0 | 18.9 ±2.4 | -75.0% ±6.7 | 757 ±6 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 1.85 ±0.02 | -4.2% ±2.0 | 0.498 ±0.012 | 0.10 ±0.01 | 5.4 ±0.7 | 10.3 ±1.6 | -58.2% ±20.3 | 24.4 ±4.4 | -66.9% ±11.0 | 754 ±7 | 15.9 ±3.5 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs. Cancelled is residual waste charged before cancellation.

| strategy | failed | invalidated | cancelled (residual) | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 0.71 ±0.13 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 1.00 ±0.19 |
| no-cancel | 0.71 ±0.13 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 1.00 ±0.19 |
| tree-dedupe | 0.46 ±0.08 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.69 ±0.12 |
| global-adaptive | 0.60 ±0.07 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.77 ±0.09 |
| risk-sized | 0.45 ±0.07 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.49 ±0.08 |
| fail-fast | 0.17 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.48 ±0.06 |
| combined | 0.10 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.25 ±0.05 |

#### flaky-mixture-day

mixture-day with 3% flakes (synthetic stress; capacity profiles use 1%)

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1.79 ±0.03 | +0.0% ±0.0 | 0.864 ±0.015 | 0.22 ±0.02 | 12.4 ±1.3 | 2.9 ±0.0 | +0.0% ±0.0 | 5.1 ±0.4 | +0.0% ±0.0 | 468 ±4 | 0.0 ±0.0 | 9.3 ±2.4 |
| no-cancel | 1.79 ±0.03 | +0.0% ±0.0 | 0.864 ±0.015 | 0.22 ±0.02 | 12.4 ±1.3 | 2.9 ±0.0 | +0.0% ±0.0 | 5.1 ±0.4 | +0.0% ±0.0 | 468 ±4 | 0.0 ±0.0 | 9.3 ±2.4 |
| tree-dedupe | 1.77 ±0.03 | -1.1% ±1.0 | 0.851 ±0.013 | 0.19 ±0.02 | 10.5 ±1.1 | 2.9 ±0.0 | +0.0% ±1.9 | 4.9 ±0.2 | -4.7% ±7.0 | 466 ±4 | 9.6 ±3.0 | 11.5 ±2.1 |
| global-adaptive | 1.83 ±0.05 | +2.0% ±1.8 | 0.875 ±0.022 | 0.21 ±0.02 | 11.7 ±0.8 | 2.9 ±0.1 | +2.7% ±2.8 | 5.4 ±0.6 | +4.4% ±6.7 | 468 ±4 | 0.0 ±0.0 | 9.7 ±2.4 |
| risk-sized | 1.82 ±0.03 | +1.6% ±1.1 | 0.871 ±0.015 | 0.18 ±0.02 | 10.1 ±1.1 | 2.9 ±0.0 | +3.2% ±2.3 | 5.2 ±0.3 | +1.0% ±7.8 | 466 ±4 | 0.0 ±0.0 | 10.9 ±2.3 |
| fail-fast | 1.69 ±0.04 | -5.7% ±1.2 | 0.886 ±0.016 | 0.07 ±0.01 | 3.9 ±0.5 | 2.8 ±0.0 | -1.2% ±2.2 | 4.4 ±0.1 | -13.3% ±4.4 | 468 ±4 | 0.0 ±0.0 | 9.5 ±2.3 |
| combined | 1.74 ±0.02 | -2.9% ±1.4 | 0.888 ±0.010 | 0.05 ±0.01 | 3.0 ±0.3 | 2.9 ±0.0 | +1.5% ±2.1 | 4.6 ±0.1 | -9.4% ±7.1 | 464 ±4 | 5.4 ±1.2 | 12.6 ±2.2 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs. Cancelled is residual waste charged before cancellation.

| strategy | failed | invalidated | cancelled (residual) | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 0.22 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.15 ±0.03 |
| no-cancel | 0.22 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.15 ±0.03 |
| tree-dedupe | 0.19 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.10 ±0.02 |
| global-adaptive | 0.21 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.14 ±0.03 |
| risk-sized | 0.18 ±0.02 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.06 ±0.02 |
| fail-fast | 0.07 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.11 ±0.03 |
| combined | 0.05 ±0.01 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.00 ±0.00 | 0.00 ±0.00 | 0.04 ±0.01 |

#### agent-burst-depth4

capacity.AdaptiveWorkload (900/day, 2-min CI, depth 4, 5%) and Profile.Config (batch 8, 5m wait, 50 runners, 1% flakes, timeout 1.5x CI) for one day; Beta(0.5, 9.5) risk (mean 5%), noise 0.5; 60% of PRs in bursts of 25 within 5m (synthetic agent bursts); fixed CI duration

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1.59 ±0.10 | +0.0% ±0.0 | 0.796 ±0.050 | 1.02 ±0.10 | 64.1 ±2.1 | 15.9 ±5.0 | +0.0% ±0.0 | 51.4 ±11.1 | +0.0% ±0.0 | 846 ±10 | 0.0 ±0.0 | 0.0 ±0.0 |
| no-cancel | 1.59 ±0.10 | +0.2% ±0.1 | 0.796 ±0.050 | 1.03 ±0.10 | 64.2 ±2.1 | 15.9 ±5.0 | +0.0% ±0.0 | 51.4 ±11.1 | +0.0% ±0.0 | 846 ±10 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 1.27 ±0.07 | -20.3% ±0.7 | 0.634 ±0.036 | 0.70 ±0.07 | 55.0 ±2.3 | 15.7 ±5.0 | -1.0% ±3.1 | 51.4 ±11.0 | +0.2% ±0.9 | 846 ±10 | 125.6 ±10.4 | 0.0 ±0.0 |
| global-adaptive | 1.57 ±0.09 | -1.0% ±1.9 | 0.788 ±0.047 | 0.83 ±0.06 | 52.5 ±1.3 | 10.7 ±2.3 | -28.7% ±7.7 | 40.2 ±8.0 | -20.8% ±6.5 | 848 ±9 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 1.44 ±0.07 | -9.4% ±2.7 | 0.721 ±0.036 | 0.66 ±0.06 | 45.4 ±2.4 | 7.8 ±1.2 | -44.9% ±13.0 | 28.6 ±6.1 | -43.4% ±7.1 | 850 ±8 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 0.88 ±0.04 | -44.3% ±1.7 | 0.758 ±0.045 | 0.27 ±0.02 | 30.4 ±1.5 | 6.9 ±0.2 | -49.2% ±14.0 | 17.3 ±2.8 | -65.2% ±4.0 | 854 ±5 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 0.95 ±0.03 | -39.8% ±2.9 | 0.647 ±0.032 | 0.14 ±0.02 | 15.1 ±1.3 | 5.9 ±0.5 | -57.7% ±9.4 | 13.0 ±2.3 | -74.1% ±2.5 | 855 ±5 | 49.7 ±5.0 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs. Cancelled is residual waste charged before cancellation.

| strategy | failed | invalidated | cancelled (residual) | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 1.02 ±0.10 | 0.00 ±0.00 | 0.007 ±0.002 | 0.00 ±0.00 | 0.36 ±0.03 | 0.73 ±0.07 | 0.98 ±0.09 |
| no-cancel | 1.03 ±0.10 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.36 ±0.03 | 0.74 ±0.07 | 0.98 ±0.09 |
| tree-dedupe | 0.69 ±0.07 | 0.00 ±0.00 | 0.007 ±0.002 | 0.00 ±0.00 | 0.36 ±0.03 | 0.41 ±0.04 | 0.65 ±0.06 |
| global-adaptive | 0.82 ±0.06 | 0.00 ±0.00 | 0.008 ±0.002 | 0.00 ±0.00 | 0.29 ±0.02 | 0.78 ±0.07 | 0.67 ±0.04 |
| risk-sized | 0.65 ±0.06 | 0.00 ±0.00 | 0.008 ±0.003 | 0.00 ±0.00 | 0.22 ±0.02 | 0.71 ±0.04 | 0.41 ±0.05 |
| fail-fast | 0.27 ±0.02 | 0.00 ±0.00 | 0.001 ±0.000 | 0.00 ±0.00 | 0.11 ±0.01 | 0.24 ±0.01 | 0.42 ±0.04 |
| combined | 0.14 ±0.02 | 0.00 ±0.00 | 0.002 ±0.001 | 0.00 ±0.00 | 0.07 ±0.01 | 0.35 ±0.02 | 0.16 ±0.02 |

#### agent-burst-depth4-var

agent-burst-depth4 with lognormal CI (median 2m, sigma 0.6, synthetic) and timeout 3x median

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1.79 ±0.12 | +0.0% ±0.0 | 0.801 ±0.057 | 1.18 ±0.12 | 65.3 ±2.4 | 26.2 ±8.1 | +0.0% ±0.0 | 90.8 ±24.4 | +0.0% ±0.0 | 842 ±14 | 0.0 ±0.0 | 0.0 ±0.0 |
| no-cancel | 1.84 ±0.13 | +2.5% ±0.5 | 0.801 ±0.057 | 1.22 ±0.13 | 66.1 ±2.4 | 26.2 ±8.1 | +0.0% ±0.0 | 90.8 ±24.4 | +0.0% ±0.0 | 842 ±14 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 1.45 ±0.09 | -19.0% ±1.0 | 0.637 ±0.041 | 0.82 ±0.09 | 56.5 ±2.7 | 23.7 ±7.6 | -9.3% ±5.6 | 82.4 ±21.8 | -8.7% ±3.4 | 844 ±13 | 127.8 ±12.2 | 0.0 ±0.0 |
| global-adaptive | 1.78 ±0.10 | -0.6% ±2.2 | 0.791 ±0.044 | 0.94 ±0.07 | 52.8 ±2.0 | 19.6 ±5.7 | -24.2% ±3.5 | 62.4 ±11.0 | -27.2% ±11.2 | 845 ±12 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 1.61 ±0.09 | -9.7% ±3.0 | 0.724 ±0.038 | 0.75 ±0.09 | 45.9 ±3.5 | 11.5 ±3.0 | -53.8% ±6.8 | 44.0 ±11.3 | -48.7% ±10.2 | 848 ±10 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 1.00 ±0.04 | -43.9% ±2.1 | 0.766 ±0.049 | 0.31 ±0.03 | 30.8 ±2.0 | 7.7 ±0.6 | -66.9% ±7.5 | 23.6 ±3.7 | -71.4% ±6.3 | 852 ±6 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 1.07 ±0.04 | -39.8% ±3.3 | 0.650 ±0.029 | 0.16 ±0.02 | 15.3 ±1.3 | 6.6 ±0.3 | -70.8% ±7.3 | 17.3 ±2.9 | -79.0% ±5.3 | 854 ±5 | 50.6 ±4.8 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs. Cancelled is residual waste charged before cancellation.

| strategy | failed | invalidated | cancelled (residual) | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 1.10 ±0.12 | 0.00 ±0.00 | 0.077 ±0.014 | 0.00 ±0.00 | 0.42 ±0.05 | 0.87 ±0.08 | 1.11 ±0.11 |
| no-cancel | 1.22 ±0.13 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.42 ±0.05 | 0.91 ±0.08 | 1.14 ±0.11 |
| tree-dedupe | 0.80 ±0.08 | 0.00 ±0.00 | 0.028 ±0.009 | 0.00 ±0.00 | 0.42 ±0.05 | 0.51 ±0.05 | 0.76 ±0.08 |
| global-adaptive | 0.87 ±0.07 | 0.00 ±0.00 | 0.074 ±0.008 | 0.00 ±0.00 | 0.34 ±0.03 | 0.94 ±0.09 | 0.76 ±0.05 |
| risk-sized | 0.68 ±0.09 | 0.00 ±0.00 | 0.065 ±0.007 | 0.00 ±0.00 | 0.26 ±0.03 | 0.84 ±0.06 | 0.47 ±0.07 |
| fail-fast | 0.29 ±0.03 | 0.00 ±0.00 | 0.017 ±0.004 | 0.00 ±0.00 | 0.13 ±0.01 | 0.29 ±0.02 | 0.48 ±0.05 |
| combined | 0.16 ±0.02 | 0.00 ±0.00 | 0.008 ±0.002 | 0.00 ±0.00 | 0.09 ±0.01 | 0.42 ±0.03 | 0.19 ±0.02 |

#### agent-burst-depth4-churn

agent-burst-depth4-var plus synthetic mid-flight invalidation: 10% of changes re-pushed and 2% dequeued by request, uniform 0-2m after first entering a started batch

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 1.94 ±0.14 | +0.0% ±0.0 | 0.977 ±0.062 | 1.32 ±0.14 | 67.5 ±2.3 | 29.1 ±7.3 | +0.0% ±0.0 | 89.5 ±29.2 | +0.0% ±0.0 | 819 ±18 | 0.0 ±0.0 | 0.0 ±0.0 |
| no-cancel | 2.29 ±0.14 | +18.1% ±1.9 | 0.977 ±0.062 | 1.66 ±0.14 | 72.5 ±1.6 | 29.1 ±7.3 | +0.0% ±0.0 | 89.5 ±29.2 | +0.0% ±0.0 | 819 ±18 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 1.55 ±0.10 | -19.8% ±2.3 | 0.803 ±0.052 | 0.93 ±0.09 | 59.5 ±2.3 | 25.4 ±7.2 | -12.8% ±11.7 | 76.5 ±18.3 | -10.8% ±10.5 | 822 ±15 | 124.8 ±12.6 | 0.0 ±0.0 |
| global-adaptive | 1.90 ±0.13 | -1.7% ±4.6 | 0.984 ±0.067 | 1.06 ±0.08 | 55.5 ±1.3 | 19.0 ±4.5 | -32.4% ±12.1 | 53.0 ±9.9 | -34.7% ±15.0 | 828 ±10 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 1.77 ±0.11 | -8.4% ±3.3 | 0.932 ±0.052 | 0.87 ±0.10 | 49.0 ±2.7 | 13.6 ±3.8 | -52.7% ±5.8 | 45.8 ±8.5 | -43.9% ±12.0 | 833 ±9 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 1.08 ±0.04 | -44.0% ±2.6 | 0.927 ±0.057 | 0.39 ±0.03 | 36.1 ±1.8 | 7.9 ±0.6 | -70.7% ±4.8 | 24.6 ±3.5 | -69.0% ±7.3 | 836 ±6 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 1.20 ±0.04 | -37.6% ±4.0 | 0.833 ±0.037 | 0.27 ±0.02 | 22.8 ±1.1 | 6.9 ±0.3 | -74.1% ±4.8 | 18.7 ±3.3 | -76.0% ±7.1 | 838 ±6 | 49.2 ±7.5 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs. Cancelled is residual waste charged before cancellation.

| strategy | failed | invalidated | cancelled (residual) | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 1.13 ±0.13 | 0.00 ±0.00 | 0.182 ±0.017 | 0.00 ±0.00 | 0.42 ±0.05 | 0.95 ±0.09 | 1.16 ±0.13 |
| no-cancel | 1.54 ±0.15 | 0.13 ±0.03 | 0.000 ±0.000 | 0.00 ±0.00 | 0.43 ±0.05 | 1.23 ±0.10 | 1.19 ±0.13 |
| tree-dedupe | 0.80 ±0.09 | 0.00 ±0.00 | 0.132 ±0.014 | 0.00 ±0.00 | 0.42 ±0.04 | 0.57 ±0.06 | 0.77 ±0.08 |
| global-adaptive | 0.87 ±0.07 | 0.01 ±0.00 | 0.185 ±0.014 | 0.00 ±0.00 | 0.33 ±0.03 | 1.05 ±0.11 | 0.75 ±0.05 |
| risk-sized | 0.68 ±0.09 | 0.01 ±0.00 | 0.181 ±0.018 | 0.00 ±0.00 | 0.26 ±0.03 | 0.97 ±0.08 | 0.48 ±0.07 |
| fail-fast | 0.30 ±0.03 | 0.00 ±0.00 | 0.085 ±0.005 | 0.00 ±0.00 | 0.13 ±0.01 | 0.32 ±0.02 | 0.50 ±0.05 |
| combined | 0.17 ±0.02 | 0.01 ±0.00 | 0.100 ±0.009 | 0.00 ±0.00 | 0.08 ±0.01 | 0.50 ±0.03 | 0.19 ±0.02 |

#### team-slow-depth4

capacity.Team (20/day for 30 days, 30-min CI, depth 4, 2%) and Profile.Config; synthetic mixture 90%@0.5% + 10%@15.5% (mean 2%), noise 0.5; fixed CI duration

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 29.54 ±0.35 | +0.0% ±0.0 | 0.987 ±0.012 | 1.64 ±0.28 | 5.5 ±0.9 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| no-cancel | 29.62 ±0.35 | +0.3% ±0.2 | 0.987 ±0.012 | 1.72 ±0.31 | 5.8 ±1.0 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 29.47 ±0.30 | -0.2% ±0.2 | 0.985 ±0.010 | 1.57 ±0.24 | 5.3 ±0.8 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 1.4 ±1.1 | 0.0 ±0.0 |
| global-adaptive | 29.54 ±0.35 | +0.0% ±0.0 | 0.987 ±0.012 | 1.64 ±0.28 | 5.5 ±0.9 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 29.62 ±0.37 | +0.3% ±0.2 | 0.990 ±0.013 | 1.62 ±0.31 | 5.4 ±1.0 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 28.51 ±0.20 | -3.5% ±0.6 | 0.985 ±0.012 | 0.44 ±0.08 | 1.5 ±0.3 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 28.57 ±0.19 | -3.3% ±0.6 | 0.985 ±0.010 | 0.41 ±0.06 | 1.4 ±0.2 | 35.0 ±0.0 | +0.0% ±0.0 | 35.0 ±0.0 | +0.0% ±0.0 | 20 ±0 | 1.1 ±1.2 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs. Cancelled is residual waste charged before cancellation.

| strategy | failed | invalidated | cancelled (residual) | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 1.53 ±0.25 | 0.00 ±0.00 | 0.104 ±0.057 | 0.00 ±0.00 | 0.81 ±0.14 | 8.79 ±0.43 | 0.26 ±0.18 |
| no-cancel | 1.72 ±0.31 | 0.00 ±0.00 | 0.000 ±0.000 | 0.00 ±0.00 | 0.81 ±0.14 | 8.87 ±0.46 | 0.26 ±0.18 |
| tree-dedupe | 1.46 ±0.21 | 0.00 ±0.00 | 0.104 ±0.057 | 0.00 ±0.00 | 0.81 ±0.14 | 8.72 ±0.41 | 0.19 ±0.12 |
| global-adaptive | 1.53 ±0.25 | 0.00 ±0.00 | 0.104 ±0.057 | 0.00 ±0.00 | 0.81 ±0.14 | 8.79 ±0.43 | 0.26 ±0.18 |
| risk-sized | 1.51 ±0.27 | 0.00 ±0.00 | 0.106 ±0.064 | 0.00 ±0.00 | 0.81 ±0.16 | 8.91 ±0.44 | 0.19 ±0.20 |
| fail-fast | 0.43 ±0.08 | 0.00 ±0.00 | 0.011 ±0.008 | 0.00 ±0.00 | 0.47 ±0.10 | 8.33 ±0.40 | 0.13 ±0.09 |
| combined | 0.40 ±0.07 | 0.00 ±0.00 | 0.011 ±0.008 | 0.00 ±0.00 | 0.46 ±0.09 | 8.42 ±0.40 | 0.08 ±0.08 |

#### team-slow-depth4-churn

team-slow-depth4 with lognormal CI (sigma 0.6, timeout 3x median) plus 10% re-pushed and 2% dequeued, uniform 0-30m after first entering a started batch (synthetic)

| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| baseline | 35.84 ±0.58 | +0.0% ±0.0 | 1.100 ±0.019 | 3.52 ±0.49 | 9.8 ±1.4 | 38.0 ±0.8 | +0.0% ±0.0 | 76.4 ±1.6 | +0.0% ±0.0 | 19 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| no-cancel | 38.83 ±0.60 | +8.4% ±0.8 | 1.100 ±0.019 | 6.51 ±0.71 | 16.7 ±1.8 | 38.0 ±0.8 | +0.0% ±0.0 | 76.4 ±1.6 | +0.0% ±0.0 | 19 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| tree-dedupe | 35.78 ±0.59 | -0.2% ±0.2 | 1.098 ±0.018 | 3.44 ±0.45 | 9.6 ±1.3 | 38.0 ±0.8 | -0.0% ±0.0 | 76.5 ±1.7 | +0.1% ±0.2 | 19 ±0 | 1.1 ±0.9 | 0.0 ±0.0 |
| global-adaptive | 35.84 ±0.58 | +0.0% ±0.0 | 1.100 ±0.019 | 3.52 ±0.49 | 9.8 ±1.4 | 38.0 ±0.8 | +0.0% ±0.0 | 76.4 ±1.6 | +0.0% ±0.0 | 19 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| risk-sized | 36.24 ±0.48 | +1.1% ±1.4 | 1.103 ±0.019 | 3.44 ±0.44 | 9.5 ±1.2 | 38.4 ±0.7 | +1.1% ±2.0 | 77.8 ±2.3 | +1.9% ±3.1 | 19 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| fail-fast | 34.57 ±0.50 | -3.5% ±1.5 | 1.100 ±0.017 | 2.04 ±0.22 | 5.9 ±0.6 | 37.6 ±0.8 | -1.1% ±1.4 | 74.0 ±2.0 | -3.2% ±2.8 | 19 ±0 | 0.0 ±0.0 | 0.0 ±0.0 |
| combined | 34.73 ±0.48 | -3.1% ±1.8 | 1.102 ±0.018 | 2.03 ±0.21 | 5.9 ±0.7 | 37.8 ±0.6 | -0.4% ±1.9 | 74.2 ±2.5 | -2.9% ±3.5 | 19 ±0 | 1.0 ±1.0 | 0.0 ±0.0 |

Runner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs. Cancelled is residual waste charged before cancellation.

| strategy | failed | invalidated | cancelled (residual) | superseded | same-SHA reruns | on speculative predecessor | bisection |
|---|---|---|---|---|---|---|---|
| baseline | 1.88 ±0.34 | 0.05 ±0.04 | 1.585 ±0.175 | 0.00 ±0.00 | 0.93 ±0.14 | 11.85 ±0.30 | 0.24 ±0.19 |
| no-cancel | 2.25 ±0.44 | 4.26 ±0.35 | 0.000 ±0.000 | 0.00 ±0.00 | 0.94 ±0.14 | 13.03 ±0.35 | 0.25 ±0.20 |
| tree-dedupe | 1.83 ±0.30 | 0.05 ±0.04 | 1.561 ±0.172 | 0.00 ±0.00 | 0.94 ±0.15 | 11.76 ±0.32 | 0.17 ±0.12 |
| global-adaptive | 1.88 ±0.34 | 0.05 ±0.04 | 1.585 ±0.175 | 0.00 ±0.00 | 0.93 ±0.14 | 11.85 ±0.30 | 0.24 ±0.19 |
| risk-sized | 1.78 ±0.30 | 0.05 ±0.03 | 1.615 ±0.174 | 0.00 ±0.00 | 0.92 ±0.17 | 12.29 ±0.43 | 0.17 ±0.20 |
| fail-fast | 0.52 ±0.07 | 0.05 ±0.03 | 1.469 ±0.153 | 0.00 ±0.00 | 0.50 ±0.05 | 11.21 ±0.43 | 0.15 ±0.10 |
| combined | 0.51 ±0.08 | 0.06 ±0.03 | 1.470 ±0.146 | 0.00 ±0.00 | 0.52 ±0.07 | 11.37 ±0.39 | 0.08 ±0.08 |

#### Cancellation saving (no-cancel − baseline, seed-paired)

Residual is the time the baseline still charged to runs it cancelled; it is waste, not saving.

| workload | cancellation | saved runner-min/landed | saving % | residual cancelled min/landed | Δ p50 vs no-cancel |
|---|---|---|---|---|---|
| default-day | not supported | – | – | – | – |
| mixture-day | not supported | – | – | – | – |
| beta-loaded | not supported | – | – | – | – |
| flaky-mixture-day | not supported | – | – | – | – |
| agent-burst-depth4 | supported | 0.003 ±0.001 | +0.2% ±0.1 | 0.007 ±0.002 | +0.0% ±0.0 |
| agent-burst-depth4-var | supported | 0.044 ±0.009 | +2.4% ±0.5 | 0.077 ±0.014 | +0.0% ±0.0 |
| agent-burst-depth4-churn | supported | 0.348 ±0.025 | +15.3% ±1.4 | 0.182 ±0.017 | +0.0% ±0.0 |
| team-slow-depth4 | supported | 0.079 ±0.055 | +0.3% ±0.2 | 0.104 ±0.057 | +0.0% ±0.0 |
| team-slow-depth4-churn | supported | 2.993 ±0.260 | +7.7% ±0.6 | 1.585 ±0.175 | +0.0% ±0.0 |

#### Culprit isolation model (strategy c)

Replays every failed fresh batch from the baseline runs (deterministic CI, no flakes). Runs and minutes exclude the batch's own failing run; innocent delay is the mean time from the batch failure until an innocent change's landing run completes, serial at depth one. Rows for workloads where calibration does not apply (depth four) are uncalibrated.

| workload | calibrated | policy | failed batches | runs/failed batch | Δ runs | runner-min/failed batch | innocent delay (min) | Δ delay | false culprits |
|---|---|---|---|---|---|---|---|---|---|
| default-day | yes | halving (production) | 95 | 2.53 ±0.34 | – | 5.1 ±0.7 | 4.0 ±0.7 | – | 0 |
| default-day | yes | halving + tree dedupe | 95 | 1.84 ±0.28 | -27.3% ±5.9 | 3.7 ±0.6 | 3.6 ±0.6 | -7.6% ±4.5 | 0 |
| default-day | yes | culprit-first (noisy scores) | 95 | 2.34 ±0.29 | -6.8% ±5.9 | 4.7 ±0.6 | 3.8 ±0.7 | -4.5% ±6.5 | 0 |
| default-day | yes | culprit-first + tree dedupe | 95 | 1.76 ±0.30 | -30.1% ±8.4 | 3.5 ±0.6 | 3.6 ±0.7 | -8.9% ±7.4 | 0 |
| default-day | yes | culprit-first + dedupe, perfect scores | 95 | 2.04 ±0.09 | -17.1% ±10.0 | 4.1 ±0.2 | 4.1 ±0.2 | +7.5% ±17.9 | 0 |
| default-day | yes | halving + fail-fast 30% | 95 | 2.53 ±0.34 | +0.0% ±0.0 | 3.3 ±0.4 | 2.9 ±0.4 | -26.1% ±4.3 | 0 |
| default-day | yes | culprit-first + dedupe + fail-fast 30% | 95 | 1.76 ±0.30 | -30.1% ±8.4 | 2.7 ±0.3 | 2.7 ±0.4 | -30.5% ±5.3 | 0 |
| mixture-day | yes | halving (production) | 110 | 2.65 ±0.24 | – | 5.3 ±0.5 | 4.5 ±1.0 | – | 0 |
| mixture-day | yes | halving + tree dedupe | 110 | 1.99 ±0.29 | -25.1% ±5.2 | 4.0 ±0.6 | 4.1 ±1.1 | -8.3% ±4.2 | 0 |
| mixture-day | yes | culprit-first (noisy scores) | 110 | 2.32 ±0.17 | -12.0% ±4.4 | 4.6 ±0.3 | 4.6 ±0.9 | +3.6% ±10.0 | 0 |
| mixture-day | yes | culprit-first + tree dedupe | 110 | 2.05 ±0.20 | -22.1% ±6.8 | 4.1 ±0.4 | 4.3 ±0.8 | -1.2% ±11.0 | 0 |
| mixture-day | yes | culprit-first + dedupe, perfect scores | 110 | 2.05 ±0.08 | -21.5% ±6.9 | 4.1 ±0.2 | 4.2 ±0.4 | -1.8% ±13.2 | 0 |
| mixture-day | yes | halving + fail-fast 30% | 110 | 2.65 ±0.24 | +0.0% ±0.0 | 3.4 ±0.3 | 3.2 ±0.5 | -28.5% ±3.5 | 0 |
| mixture-day | yes | culprit-first + dedupe + fail-fast 30% | 110 | 2.05 ±0.20 | -22.1% ±6.8 | 2.8 ±0.2 | 2.9 ±0.4 | -32.7% ±6.9 | 0 |
| beta-loaded | yes | halving (production) | 324 | 5.49 ±0.43 | – | 22.0 ±1.7 | 15.7 ±1.7 | – | 0 |
| beta-loaded | yes | halving + tree dedupe | 324 | 4.19 ±0.41 | -23.9% ±2.9 | 16.7 ±1.6 | 13.1 ±1.6 | -16.4% ±1.7 | 0 |
| beta-loaded | yes | culprit-first (noisy scores) | 324 | 4.28 ±0.30 | -21.6% ±5.0 | 17.1 ±1.2 | 13.9 ±1.0 | -10.3% ±7.3 | 0 |
| beta-loaded | yes | culprit-first + tree dedupe | 324 | 3.47 ±0.22 | -36.5% ±3.4 | 13.9 ±0.9 | 11.7 ±0.8 | -24.2% ±5.3 | 0 |
| beta-loaded | yes | culprit-first + dedupe, perfect scores | 324 | 2.27 ±0.08 | -58.3% ±2.4 | 9.1 ±0.3 | 9.1 ±0.2 | -40.8% ±6.6 | 0 |
| beta-loaded | yes | halving + fail-fast 30% | 324 | 5.49 ±0.43 | +0.0% ±0.0 | 13.9 ±1.0 | 10.2 ±0.9 | -34.3% ±1.8 | 0 |
| beta-loaded | yes | culprit-first + dedupe + fail-fast 30% | 324 | 3.47 ±0.22 | -36.5% ±3.4 | 9.8 ±0.6 | 8.3 ±0.5 | -46.3% ±4.9 | 0 |
| flaky-mixture-day | **no** | halving (production) | 139 | 2.48 ±0.22 | – | 5.0 ±0.4 | 4.0 ±0.6 | – | 0 |
| flaky-mixture-day | **no** | halving + tree dedupe | 139 | 1.80 ±0.19 | -27.6% ±4.3 | 3.6 ±0.4 | 3.7 ±0.8 | -8.4% ±5.8 | 30 |
| flaky-mixture-day | **no** | culprit-first (noisy scores) | 139 | 2.29 ±0.15 | -7.4% ±4.9 | 4.6 ±0.3 | 4.1 ±0.6 | +2.7% ±6.7 | 0 |
| flaky-mixture-day | **no** | culprit-first + tree dedupe | 139 | 1.87 ±0.10 | -24.2% ±5.9 | 3.7 ±0.2 | 3.9 ±0.6 | -2.9% ±7.5 | 30 |
| flaky-mixture-day | **no** | culprit-first + dedupe, perfect scores | 139 | 1.86 ±0.07 | -24.2% ±6.7 | 3.7 ±0.1 | 3.8 ±0.3 | -2.8% ±9.1 | 30 |
| flaky-mixture-day | **no** | halving + fail-fast 30% | 139 | 2.48 ±0.22 | +0.0% ±0.0 | 3.5 ±0.3 | 3.1 ±0.4 | -22.0% ±3.3 | 0 |
| flaky-mixture-day | **no** | culprit-first + dedupe + fail-fast 30% | 139 | 1.87 ±0.10 | -24.2% ±5.9 | 2.7 ±0.1 | 2.8 ±0.3 | -29.2% ±4.7 | 30 |
| agent-burst-depth4 | **no** | halving (production) | 234 | 5.18 ±0.38 | – | 10.4 ±0.8 | 7.8 ±0.5 | – | 0 |
| agent-burst-depth4 | **no** | halving + tree dedupe | 234 | 3.94 ±0.31 | -24.1% ±1.8 | 7.9 ±0.6 | 6.6 ±0.5 | -15.2% ±1.3 | 4 |
| agent-burst-depth4 | **no** | culprit-first (noisy scores) | 234 | 4.11 ±0.40 | -20.7% ±5.2 | 8.2 ±0.8 | 6.9 ±0.6 | -11.1% ±7.0 | 0 |
| agent-burst-depth4 | **no** | culprit-first + tree dedupe | 234 | 3.39 ±0.30 | -34.5% ±4.2 | 6.8 ±0.6 | 5.9 ±0.5 | -23.8% ±5.6 | 4 |
| agent-burst-depth4 | **no** | culprit-first + dedupe, perfect scores | 234 | 2.25 ±0.13 | -56.3% ±2.0 | 4.5 ±0.3 | 4.5 ±0.3 | -41.3% ±3.9 | 4 |
| agent-burst-depth4 | **no** | halving + fail-fast 30% | 234 | 5.18 ±0.38 | +0.0% ±0.0 | 6.6 ±0.4 | 5.1 ±0.2 | -34.5% ±1.3 | 0 |
| agent-burst-depth4 | **no** | culprit-first + dedupe + fail-fast 30% | 234 | 3.39 ±0.30 | -34.5% ±4.2 | 4.8 ±0.4 | 4.1 ±0.3 | -46.3% ±4.4 | 4 |
| agent-burst-depth4-var | **no** | halving (production) | 199 | 5.16 ±0.40 | – | 10.3 ±0.8 | 7.6 ±0.4 | – | 0 |
| agent-burst-depth4-var | **no** | halving + tree dedupe | 199 | 3.96 ±0.34 | -23.3% ±1.9 | 7.9 ±0.7 | 6.5 ±0.4 | -14.7% ±1.4 | 9 |
| agent-burst-depth4-var | **no** | culprit-first (noisy scores) | 199 | 4.25 ±0.33 | -17.3% ±4.8 | 8.5 ±0.7 | 6.9 ±0.4 | -8.3% ±6.2 | 0 |
| agent-burst-depth4-var | **no** | culprit-first + tree dedupe | 199 | 3.47 ±0.29 | -32.4% ±4.8 | 6.9 ±0.6 | 5.9 ±0.4 | -21.6% ±6.4 | 9 |
| agent-burst-depth4-var | **no** | culprit-first + dedupe, perfect scores | 199 | 2.25 ±0.13 | -56.1% ±3.2 | 4.5 ±0.3 | 4.5 ±0.3 | -40.8% ±4.3 | 9 |
| agent-burst-depth4-var | **no** | halving + fail-fast 30% | 199 | 5.16 ±0.40 | +0.0% ±0.0 | 6.6 ±0.5 | 5.0 ±0.2 | -34.0% ±1.0 | 0 |
| agent-burst-depth4-var | **no** | culprit-first + dedupe + fail-fast 30% | 199 | 3.47 ±0.29 | -32.4% ±4.8 | 5.0 ±0.4 | 4.3 ±0.3 | -43.5% ±5.1 | 9 |
| agent-burst-depth4-churn | **no** | halving (production) | 230 | 5.32 ±0.22 | – | 10.6 ±0.4 | 7.6 ±0.3 | – | 0 |
| agent-burst-depth4-churn | **no** | halving + tree dedupe | 230 | 4.13 ±0.18 | -22.5% ±0.9 | 8.3 ±0.4 | 6.5 ±0.3 | -14.3% ±1.3 | 5 |
| agent-burst-depth4-churn | **no** | culprit-first (noisy scores) | 230 | 4.34 ±0.36 | -18.4% ±5.8 | 8.7 ±0.7 | 7.0 ±0.5 | -7.5% ±5.2 | 0 |
| agent-burst-depth4-churn | **no** | culprit-first + tree dedupe | 230 | 3.56 ±0.25 | -33.1% ±3.8 | 7.1 ±0.5 | 6.0 ±0.3 | -21.6% ±3.1 | 5 |
| agent-burst-depth4-churn | **no** | culprit-first + dedupe, perfect scores | 230 | 2.27 ±0.10 | -57.4% ±1.4 | 4.5 ±0.2 | 4.5 ±0.2 | -40.8% ±2.1 | 5 |
| agent-burst-depth4-churn | **no** | halving + fail-fast 30% | 230 | 5.32 ±0.22 | +0.0% ±0.0 | 6.8 ±0.3 | 5.0 ±0.1 | -34.2% ±0.8 | 0 |
| agent-burst-depth4-churn | **no** | culprit-first + dedupe + fail-fast 30% | 230 | 3.56 ±0.25 | -33.1% ±3.8 | 5.1 ±0.4 | 4.2 ±0.2 | -44.1% ±2.9 | 5 |
| team-slow-depth4 | **no** | halving (production) | 14 | 2.00 ±0.00 | – | 60.0 ±0.0 | 50.6 ±8.5 | – | 0 |
| team-slow-depth4 | **no** | halving + tree dedupe | 14 | 1.58 ±0.39 | -20.8% ±19.4 | 47.5 ±11.6 | 47.5 ±11.6 | -7.3% ±11.4 | 2 |
| team-slow-depth4 | **no** | culprit-first (noisy scores) | 14 | 2.25 ±0.59 | +12.5% ±29.6 | 67.5 ±17.7 | 56.9 ±8.3 | +15.6% ±22.2 | 0 |
| team-slow-depth4 | **no** | culprit-first + tree dedupe | 14 | 1.88 ±0.49 | -6.2% ±24.4 | 56.2 ±14.6 | 52.5 ±9.5 | +6.8% ±25.5 | 2 |
| team-slow-depth4 | **no** | culprit-first + dedupe, perfect scores | 14 | 1.81 ±0.31 | -9.4% ±15.6 | 54.4 ±9.3 | 54.4 ±9.3 | +9.9% ±23.4 | 2 |
| team-slow-depth4 | **no** | halving + fail-fast 30% | 14 | 2.00 ±0.00 | +0.0% ±0.0 | 42.9 ±6.5 | 38.4 ±3.3 | -21.9% ±13.0 | 0 |
| team-slow-depth4 | **no** | culprit-first + dedupe + fail-fast 30% | 14 | 1.88 ±0.49 | -6.2% ±24.4 | 40.5 ±10.0 | 38.1 ±4.7 | -22.8% ±13.1 | 2 |
| team-slow-depth4-churn | **no** | halving (production) | 13 | 2.00 ±0.00 | – | 60.0 ±0.0 | 49.6 ±13.7 | – | 0 |
| team-slow-depth4-churn | **no** | halving + tree dedupe | 13 | 1.62 ±0.46 | -18.8% ±23.1 | 48.8 ±13.9 | 48.8 ±13.9 | -1.7% ±4.3 | 1 |
| team-slow-depth4-churn | **no** | culprit-first (noisy scores) | 13 | 2.00 ±0.00 | +0.0% ±0.0 | 60.0 ±0.0 | 50.8 ±12.6 | +14.2% ±58.5 | 0 |
| team-slow-depth4-churn | **no** | culprit-first + tree dedupe | 13 | 1.67 ±0.43 | -16.7% ±21.4 | 50.0 ±12.9 | 50.0 ±12.9 | +12.5% ±59.2 | 1 |
| team-slow-depth4-churn | **no** | culprit-first + dedupe, perfect scores | 13 | 1.92 ±0.21 | -4.2% ±10.7 | 57.5 ±6.4 | 57.5 ±6.4 | +25.0% ±46.8 | 1 |
| team-slow-depth4-churn | **no** | halving + fail-fast 30% | 13 | 2.00 ±0.00 | +0.0% ±0.0 | 40.8 ±4.5 | 37.0 ±5.1 | -22.2% ±15.7 | 0 |
| team-slow-depth4-churn | **no** | culprit-first + dedupe + fail-fast 30% | 13 | 1.67 ±0.43 | -16.7% ±21.4 | 36.0 ±3.9 | 36.0 ±3.9 | -20.8% ±32.6 | 1 |

#### Isolation model calibration (halving model vs planner bisection runs, baseline)

The model claims exactness only at depth one; at depth four, bisection interacts with failed-check retries and chain invalidation.

| workload | applies | seeds matching | model runs | planner runs |
|---|---|---|---|---|
| default-day | yes | 10/10 | 248 | 248 |
| mixture-day | yes | 10/10 | 290 | 290 |
| beta-loaded | yes | 10/10 | 1800 | 1800 |
| flaky-mixture-day | n/a | 10/10 | 346 | 346 |
| agent-burst-depth4 | n/a | 0/10 | 1208 | 3045 |
| agent-burst-depth4-var | n/a | 0/10 | 1018 | 3110 |
| agent-burst-depth4-churn | n/a | 0/10 | 1218 | 3084 |
| team-slow-depth4 | n/a | 2/10 | 28 | 37 |
| team-slow-depth4-churn | n/a | 4/10 | 26 | 30 |

### Matrix: cancellation

Cancellation saving as a function of CI duration variance and mid-flight invalidation rate (agent-burst-depth4).

| workload | baseline runner-min/landed | baseline p50 (min) | baseline landed/day | no-cancel Δ min | no-cancel Δ p50 |
|---|---|---|---|---|---|
| burst σ=0.0 repush=0% | 1.59 ±0.10 | 15.9 ±5.0 | 846 ±10 | +0.2% ±0.1 | +0.0% ±0.0 |
| burst σ=0.0 repush=10% | 1.72 ±0.13 | 16.9 ±4.5 | 828 ±10 | +13.4% ±1.7 | +0.0% ±0.0 |
| burst σ=0.0 repush=25% | 1.79 ±0.13 | 16.5 ±4.4 | 829 ±10 | +31.2% ±2.8 | +0.0% ±0.0 |
| burst σ=0.3 repush=0% | 1.65 ±0.12 | 19.8 ±6.1 | 845 ±11 | +0.9% ±0.2 | +0.0% ±0.0 |
| burst σ=0.3 repush=10% | 1.78 ±0.14 | 21.0 ±6.3 | 825 ±13 | +15.1% ±2.1 | +0.0% ±0.0 |
| burst σ=0.3 repush=25% | 1.83 ±0.14 | 20.5 ±5.8 | 829 ±9 | +33.4% ±2.7 | +0.0% ±0.0 |
| burst σ=0.6 repush=0% | 1.79 ±0.12 | 26.2 ±8.1 | 842 ±14 | +2.5% ±0.5 | +0.0% ±0.0 |
| burst σ=0.6 repush=10% | 1.94 ±0.14 | 29.1 ±7.3 | 819 ±18 | +18.1% ±1.9 | +0.0% ±0.0 |
| burst σ=0.6 repush=25% | 1.99 ±0.15 | 29.7 ±9.0 | 824 ±15 | +37.8% ±3.7 | +0.0% ±0.0 |
| burst σ=0.6 repush=0% timeout 1.5× | 2.45 ±0.21 | 35.4 ±12.5 | 779 ±21 | +20.5% ±1.4 | +0.0% ±0.0 |
| burst σ=0.6 repush=10% runners=3 | 1.92 ±0.14 | 29.3 ±7.3 | 819 ±19 | +22.6% ±5.2 | +24.0% ±13.3 |

#### Cancellation saving (no-cancel − baseline, seed-paired)

Residual is the time the baseline still charged to runs it cancelled; it is waste, not saving.

| workload | cancellation | saved runner-min/landed | saving % | residual cancelled min/landed | Δ p50 vs no-cancel |
|---|---|---|---|---|---|
| burst σ=0.0 repush=0% | supported | 0.003 ±0.001 | +0.2% ±0.1 | 0.007 ±0.002 | +0.0% ±0.0 |
| burst σ=0.0 repush=10% | supported | 0.229 ±0.028 | +11.8% ±1.3 | 0.129 ±0.008 | +0.0% ±0.0 |
| burst σ=0.0 repush=25% | supported | 0.554 ±0.033 | +23.7% ±1.6 | 0.211 ±0.009 | +0.0% ±0.0 |
| burst σ=0.3 repush=0% | supported | 0.015 ±0.004 | +0.9% ±0.2 | 0.034 ±0.008 | +0.0% ±0.0 |
| burst σ=0.3 repush=10% | supported | 0.268 ±0.041 | +13.1% ±1.6 | 0.144 ±0.013 | +0.0% ±0.0 |
| burst σ=0.3 repush=25% | supported | 0.608 ±0.039 | +25.0% ±1.5 | 0.224 ±0.014 | +0.0% ±0.0 |
| burst σ=0.6 repush=0% | supported | 0.044 ±0.009 | +2.4% ±0.5 | 0.077 ±0.014 | +0.0% ±0.0 |
| burst σ=0.6 repush=10% | supported | 0.348 ±0.025 | +15.3% ±1.4 | 0.182 ±0.017 | +0.0% ±0.0 |
| burst σ=0.6 repush=25% | supported | 0.745 ±0.051 | +27.3% ±2.0 | 0.253 ±0.020 | +0.0% ±0.0 |
| burst σ=0.6 repush=0% timeout 1.5× | supported | 0.505 ±0.066 | +17.0% ±0.9 | 1.114 ±0.122 | +0.0% ±0.0 |
| burst σ=0.6 repush=10% runners=3 | supported | 0.427 ±0.080 | +18.2% ±3.3 | 0.161 ±0.016 | -17.7% ±8.9 |

### Matrix: load-curve

Adaptive batch sizing against load (beta-loaded, depth one). The production backlog guard floors each fresh batch started at window 1 at min(max, waiting); the sim-only guard keeps the configured maximum whenever a full batch is waiting.

| workload | baseline runner-min/landed | baseline p50 (min) | baseline landed/day | global-adaptive 0.8 Δ min | global-adaptive 0.8 Δ p50 | global-adaptive 0.8 + guard Δ min | global-adaptive 0.8 + guard Δ p50 | global-adaptive 0.8 + sim guard Δ min | global-adaptive 0.8 + sim guard Δ p50 | global-adaptive 0.6 Δ min | global-adaptive 0.6 Δ p50 | risk-sized Δ min | risk-sized Δ p50 | risk-sized + sim guard Δ min | risk-sized + sim guard Δ p50 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| beta-loaded 300/day | 3.37 ±0.10 | 5.7 ±0.2 | 287 ±4 | +0.4% ±0.5 | +1.2% ±1.3 | -0.0% ±0.0 | +0.0% ±0.1 | +0.5% ±0.5 | +1.3% ±1.3 | -0.0% ±0.1 | +0.0% ±0.2 | +1.2% ±0.8 | +2.8% ±1.5 | +1.1% ±0.9 | +2.5% ±1.5 |
| beta-loaded 450/day | 2.80 ±0.06 | 6.7 ±0.3 | 428 ±4 | +2.5% ±1.4 | +9.8% ±6.1 | +0.1% ±0.2 | +0.1% ±0.3 | +1.7% ±1.2 | +7.1% ±7.1 | +0.2% ±0.3 | +0.5% ±0.6 | +1.9% ±1.2 | +3.3% ±4.4 | +1.7% ±1.1 | +2.5% ±4.2 |
| beta-loaded 600/day | 2.34 ±0.05 | 8.6 ±1.1 | 565 ±5 | +3.1% ±1.2 | +94.2% ±87.1 | +0.0% ±0.1 | +0.3% ±0.6 | +1.2% ±0.6 | +8.8% ±8.5 | +0.0% ±0.4 | -1.3% ±4.3 | +2.3% ±0.6 | +24.9% ±17.7 | +2.0% ±0.4 | +11.5% ±5.8 |
| beta-loaded 700/day | 2.12 ±0.06 | 16.5 ±7.4 | 649 ±11 | +4.0% ±1.9 | +202.0% ±137.6 | +0.0% ±0.0 | +0.0% ±0.0 | +0.5% ±0.9 | -10.2% ±16.8 | +0.2% ±0.3 | +3.8% ±5.5 | +1.2% ±1.6 | +49.5% ±82.9 | +0.7% ±1.0 | +5.9% ±22.7 |
| beta-loaded 800/day | 1.94 ±0.06 | 38.4 ±18.4 | 733 ±20 | +9.4% ±5.5 | +203.9% ±99.7 | +0.0% ±0.0 | +0.0% ±0.0 | +0.0% ±0.7 | +3.0% ±13.6 | +0.3% ±0.6 | +13.6% ±16.1 | +0.9% ±1.2 | +29.4% ±43.7 | -0.4% ±1.0 | -4.6% ±24.2 |

### Matrix: guard-depth4

The backlog guard against the depth-four profiles, where adaptive sizing helps: the ADR 0009 adaptive profile and the main depth-four workloads. The production guard fires only while failures have halved the window to 1.

| workload | baseline runner-min/landed | baseline p50 (min) | baseline landed/day | global-adaptive 0.8 Δ min | global-adaptive 0.8 Δ p50 | global-adaptive 0.8 + guard Δ min | global-adaptive 0.8 + guard Δ p50 | global-adaptive 0.8 + sim guard Δ min | global-adaptive 0.8 + sim guard Δ p50 |
|---|---|---|---|---|---|---|---|---|---|
| adr0009-depth4 | 1.47 ±0.12 | 7.7 ±0.9 | 849 ±7 | +0.2% ±2.5 | -15.8% ±10.0 | +0.1% ±2.6 | -15.8% ±10.0 | +1.0% ±1.6 | -11.4% ±8.4 |
| agent-burst-depth4 | 1.59 ±0.10 | 15.9 ±5.0 | 846 ±10 | -1.0% ±1.9 | -28.7% ±7.7 | -1.0% ±1.9 | -28.7% ±7.7 | -0.1% ±1.8 | -18.1% ±6.4 |
| agent-burst-depth4-var | 1.79 ±0.12 | 26.2 ±8.1 | 842 ±14 | -0.6% ±2.2 | -24.2% ±3.5 | -0.6% ±2.2 | -24.2% ±3.5 | +1.7% ±2.5 | -10.6% ±5.0 |
| agent-burst-depth4-churn | 1.94 ±0.14 | 29.1 ±7.3 | 819 ±18 | -1.7% ±4.6 | -32.4% ±12.1 | -1.7% ±4.6 | -32.4% ±12.1 | -0.3% ±2.0 | -8.2% ±11.3 |
| team-slow-depth4 | 29.54 ±0.35 | 35.0 ±0.0 | 20 ±0 | +0.0% ±0.0 | +0.0% ±0.0 | +0.0% ±0.0 | +0.0% ±0.0 | +0.0% ±0.0 | +0.0% ±0.0 |
| team-slow-depth4-churn | 35.84 ±0.58 | 38.0 ±0.8 | 19 ±0 | +0.0% ±0.0 | +0.0% ±0.0 | +0.0% ±0.0 | +0.0% ±0.0 | +0.0% ±0.0 | +0.0% ±0.0 |

### Matrix: score-noise

Value of score quality: the risk score is the true probability with this much logit noise. Truth, arrivals and baseline runs are identical across noise levels.

| workload | baseline runner-min/landed | baseline p50 (min) | baseline landed/day | risk-sized Δ min | risk-sized Δ p50 |
|---|---|---|---|---|---|
| mixture-day noise=0 | 1.75 ±0.03 | 2.9 ±0.0 | 477 ±4 | +1.6% ±0.6 | +2.5% ±1.0 |
| mixture-day noise=0.25 | 1.75 ±0.03 | 2.9 ±0.0 | 477 ±4 | +1.5% ±0.6 | +2.4% ±1.2 |
| mixture-day noise=0.5 | 1.75 ±0.03 | 2.9 ±0.0 | 477 ±4 | +1.5% ±0.5 | +2.5% ±0.8 |
| mixture-day noise=1 | 1.75 ±0.03 | 2.9 ±0.0 | 477 ±4 | +1.7% ±0.8 | +2.4% ±1.1 |
| mixture-day noise=2 | 1.75 ±0.03 | 2.9 ±0.0 | 477 ±4 | +3.9% ±0.7 | +5.5% ±1.8 |
| beta-loaded noise=0 | 1.94 ±0.06 | 38.4 ±18.4 | 733 ±20 | -0.9% ±1.4 | +10.2% ±38.6 |
| beta-loaded noise=0.25 | 1.94 ±0.06 | 38.4 ±18.4 | 733 ±20 | -0.6% ±1.4 | +2.7% ±28.0 |
| beta-loaded noise=0.5 | 1.94 ±0.06 | 38.4 ±18.4 | 733 ±20 | +0.9% ±1.2 | +29.4% ±43.7 |
| beta-loaded noise=1 | 1.94 ±0.06 | 38.4 ±18.4 | 733 ±20 | +4.1% ±1.4 | +101.4% ±97.3 |
| beta-loaded noise=2 | 1.94 ±0.06 | 38.4 ±18.4 | 733 ±20 | +15.0% ±1.9 | +324.6% ±157.0 |
| agent-burst-depth4 noise=0 | 1.59 ±0.10 | 15.9 ±5.0 | 846 ±10 | -9.6% ±3.0 | -45.6% ±13.4 |
| agent-burst-depth4 noise=0.25 | 1.59 ±0.10 | 15.9 ±5.0 | 846 ±10 | -10.0% ±3.2 | -46.0% ±14.2 |
| agent-burst-depth4 noise=0.5 | 1.59 ±0.10 | 15.9 ±5.0 | 846 ±10 | -9.4% ±2.7 | -44.9% ±13.0 |
| agent-burst-depth4 noise=1 | 1.59 ±0.10 | 15.9 ±5.0 | 846 ±10 | -6.3% ±2.4 | -46.3% ±13.1 |
| agent-burst-depth4 noise=2 | 1.59 ±0.10 | 15.9 ±5.0 | 846 ±10 | +2.1% ±3.4 | -46.8% ±14.4 |

#### Culprit isolation model: Δ runs per failed batch versus production halving

Workloads marked uncalibrated are depth four, where the halving model does not reproduce the planner.

| workload | calibrated | failed batches | halving + tree dedupe | culprit-first (noisy scores) | culprit-first + tree dedupe | culprit-first + dedupe, perfect scores | halving + fail-fast 30% | culprit-first + dedupe + fail-fast 30% |
|---|---|---|---|---|---|---|---|---|
| mixture-day noise=0 | yes | 110 | -25.1% ±5.2 | -12.0% ±4.0 | -24.5% ±5.2 | -21.5% ±6.9 | +0.0% ±0.0 | -24.5% ±5.2 |
| mixture-day noise=0.25 | yes | 110 | -25.1% ±5.2 | -11.8% ±4.0 | -21.9% ±6.9 | -21.5% ±6.9 | +0.0% ±0.0 | -21.9% ±6.9 |
| mixture-day noise=0.5 | yes | 110 | -25.1% ±5.2 | -12.0% ±4.4 | -22.1% ±6.8 | -21.5% ±6.9 | +0.0% ±0.0 | -22.1% ±6.8 |
| mixture-day noise=1 | yes | 110 | -25.1% ±5.2 | -11.3% ±4.0 | -22.0% ±6.4 | -21.5% ±6.9 | +0.0% ±0.0 | -22.0% ±6.4 |
| mixture-day noise=2 | yes | 110 | -25.1% ±5.2 | -3.5% ±4.9 | -19.1% ±6.4 | -21.5% ±6.9 | +0.0% ±0.0 | -19.1% ±6.4 |
| beta-loaded noise=0 | yes | 324 | -23.9% ±2.9 | -25.1% ±4.8 | -37.7% ±2.5 | -58.3% ±2.4 | +0.0% ±0.0 | -37.7% ±2.5 |
| beta-loaded noise=0.25 | yes | 324 | -23.9% ±2.9 | -23.4% ±5.2 | -37.4% ±3.1 | -58.3% ±2.4 | +0.0% ±0.0 | -37.4% ±3.1 |
| beta-loaded noise=0.5 | yes | 324 | -23.9% ±2.9 | -21.6% ±5.0 | -36.5% ±3.4 | -58.3% ±2.4 | +0.0% ±0.0 | -36.5% ±3.4 |
| beta-loaded noise=1 | yes | 324 | -23.9% ±2.9 | -15.7% ±5.3 | -34.2% ±3.5 | -58.3% ±2.4 | +0.0% ±0.0 | -34.2% ±3.5 |
| beta-loaded noise=2 | yes | 324 | -23.9% ±2.9 | -3.7% ±5.2 | -29.1% ±2.5 | -58.3% ±2.4 | +0.0% ±0.0 | -29.1% ±2.5 |
| agent-burst-depth4 noise=0 | **no** | 234 | -24.1% ±1.8 | -20.9% ±4.9 | -33.9% ±3.5 | -56.3% ±2.0 | +0.0% ±0.0 | -33.9% ±3.5 |
| agent-burst-depth4 noise=0.25 | **no** | 234 | -24.1% ±1.8 | -20.8% ±5.1 | -34.2% ±3.9 | -56.3% ±2.0 | +0.0% ±0.0 | -34.2% ±3.9 |
| agent-burst-depth4 noise=0.5 | **no** | 234 | -24.1% ±1.8 | -20.7% ±5.2 | -34.5% ±4.2 | -56.3% ±2.0 | +0.0% ±0.0 | -34.5% ±4.2 |
| agent-burst-depth4 noise=1 | **no** | 234 | -24.1% ±1.8 | -17.0% ±4.7 | -33.7% ±3.3 | -56.3% ±2.0 | +0.0% ±0.0 | -33.7% ±3.3 |
| agent-burst-depth4 noise=2 | **no** | 234 | -24.1% ±1.8 | -7.3% ±5.5 | -30.6% ±3.1 | -56.3% ±2.0 | +0.0% ±0.0 | -30.6% ±3.1 |

### Matrix: fail-fast

Fail-fast saving against the point at which a failing run stops.

| workload | baseline runner-min/landed | baseline p50 (min) | baseline landed/day | fail-fast 30% Δ min | fail-fast 30% Δ p50 | fail-fast 60% Δ min | fail-fast 60% Δ p50 | fail-fast 90% Δ min | fail-fast 90% Δ p50 |
|---|---|---|---|---|---|---|---|---|---|
| mixture-day | 1.75 ±0.03 | 2.9 ±0.0 | 477 ±4 | -4.6% ±0.7 | -1.9% ±1.0 | -2.4% ±0.3 | -1.2% ±0.7 | -0.5% ±0.2 | +0.0% ±0.4 |
| beta-loaded | 1.94 ±0.06 | 38.4 ±18.4 | 733 ±20 | -6.1% ±2.2 | -68.6% ±17.0 | -4.5% ±1.6 | -61.4% ±17.3 | -1.6% ±0.6 | -29.1% ±13.8 |
| agent-burst-depth4-var | 1.79 ±0.12 | 26.2 ±8.1 | 842 ±14 | -43.9% ±2.1 | -66.9% ±7.5 | -26.1% ±1.8 | -52.7% ±4.8 | -6.1% ±1.1 | -15.1% ±5.1 |
