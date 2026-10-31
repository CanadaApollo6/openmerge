# Adaptive batch size backlog guard: evidence

Status: **does not meet the bar.** The guard holds depth-1 latency at
fixed-sizing levels under load. At depth 4 it gives back much of adaptive
sizing's benefit, and it fails the ADR 0009 first-day planner harness's p95
gate. It is not proposed for merge. This note records the result so the rule
is not retried unchanged.

## Rule

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

## Load curve: depth 1 (beta-loaded, 10 seeds, paired Δ vs fixed)

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

## Depth 4 (one day, 10 seeds; means; Δ p50 paired vs fixed)

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

## Why

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

## Constraints checked

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

## Reproduce

```sh
OPENMERGE_LAB_OUT=/some/dir om-go go test -count=1 ./internal/sim -run '^TestEfficiencyLab$'   # load-curve and guard-depth4 matrices
om-go go test -count=1 -short -v ./internal/sim -run '^TestAdaptiveSizingPlannerHarness$'
om-go go test -count=1 -v ./internal/queue -run 'BacklogGuard'
```

The full `TestEfficiencyLab` still compares against the committed summary from
#211, which does not contain the new arms. It fails until that summary is
regenerated, which is deliberately not done for a rule that is not proposed.
