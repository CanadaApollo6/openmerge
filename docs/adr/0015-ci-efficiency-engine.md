# 0015. CI efficiency engine

- **Status:** Proposed
- **Date:** 2026-10-31

## Context

The owner's 2026-10-31 directive is to pursue any defensible optimization of
CI time and minutes. OpenMerge sees every queued diff, composition, tested tree,
result and duration: it can avoid work that isolated per-PR CI cannot see.
This is a portfolio proposal, not measured savings or implementation approval.
It builds on [batching and the sealed experiment](0009-speculative-batching.md),
[executor economics and trust](0013-ci-cost-executors.md),
[dependency privacy and cancellation](0011-cross-repository-dependent-changes.md),
and proposed [review cards and risk gates](https://github.com/CanadaApollo6/openmerge/pull/207)
(ADR 0014, read at PR head 90193811). The deterministic
[simulator](../../internal/sim/doc.go) runs the real planner and executor;
the efficiency lab being built in parallel extends its runner-minute accounting
and heterogeneous failure priors. Those additions are dependencies, not assumed
completed features.

## Decision

### Safety and evidence contract

T0 preserves existing guarantees exactly. T1 requires explicit per-check opt-in
and a stated hermeticity/input-completeness contract. T2 changes guarantees,
requires owner policy opt-in, and is restricted to two-way doors. Unknown inputs
fail closed to ordinary CI. No heuristic prediction is passing evidence.

All techniques inherit ADR 0014's governing facts, approval gates and ceilings:
one-way changes run alone, without speculative predecessors or successors;
broad/unknown radius stays conservative. No one-way door is ever T2 eligible.
Reclassification invalidates affected unstarted compositions; durable landing
attempts settle through existing fences. These rules apply to every entry below,
including cache hits, stack grouping and optimizer recommendations.

Every entry also inherits ADR 0011 audience noninterference. Partition caches,
priors, traces and reports by tenant/repository and authorization. No cross-repo
private signal may change a visible scheduling, cancellation, explanation or
risk decision in a way that leaks existence or content. Use local authorized
signals or a conservative signal-independent fallback, not merely redacted prose.
Test identical unauthorized outputs and observable decisions while varying
private edges and outcomes. Author/agent history is repo-local, access-controlled,
calibrated and advisory; it never grants trust or relaxes gates.

### Portfolio

Effects below are hypotheses; each measurement compares the same seeded workload
and records all work, including rejected changes, retries and discarded runs.

| Technique | Mechanism | Expected minutes / latency | Safety tier | Dependencies | Measurement |
|---|---|---|---|---|---|
| Exact tree dedupe | Reuse a trusted passing result on identical Git TREE OID; useful after invalidation, bisection subsets, depth-4 suffix recomputation and requeues | Avoid repeated jobs / remove their wait | T0 for exact existing execution identity; cross-commit tree equivalence is T1 | Tree-deterministic check declaration, evidence cache | Hit rate, avoided minutes, false reuse fixtures, p95 |
| Per-path memoization | Job declares input globs; hash canonical path/type/tree/blob OID inventory for exactly those paths plus toolchain identity | Skip unchanged jobs / shorten critical path | T1 | Complete declared input closure and trusted cache | Hits, shadow full-run disagreements, minutes and critical path |
| Bayesian culprit isolation | Order adaptive subset probes by posterior culprit probability rather than halves | Fewer expected diagnostic runs / faster isolation | T0 | Local priors, deterministic probe planner | Runs and minutes per confirmed isolation, calibration, worst case |
| Attribution without bisection | Intersect failed-test coverage or historical co-failure files with PR diffs; probe likely culprit then confirm remaining composition | Often one confirmation / faster recovery | T0 only with verification below | Bound reports and coverage map | Attribution precision, fallback rate, total recovery work |
| Risk-weighted batching | Use heterogeneous review-card facts and calibrated failure priors; batch low risk, isolate high risk | Amortize passes and avoid wasted suffixes / lower tail | T0 | ADR 0014 facts, lab priors | Minutes/landing and p95 by risk cohort |
| Adaptive speculation | Admit depth only when expected latency value exceeds incremental runner cost | Less waste / spend only for useful overlap | T0 | Duration model, budgets, repo value knob | Pareto frontier, prediction error, wasted minutes |
| Fail-fast ordering | CLI or GitHub Action orders diff-related, recently failing and flaky-history tests early; balance shards by historical duration | Earlier failed exit / minutes to first failure fall | T0 if suite and semantics unchanged | Test inventory, duration history, cancellation | First-failure time, full-suite parity, shard skew |
| Minimal flake reruns | Same tree passing and failing flags a possible flake; rerun failed jobs only | Avoid full workflow retry / shorter confirmation | T0 for existing bounded retry semantics | Accepted run identities, actions:write after period B | Retry minutes, flake classification, false eviction |
| Predictive pre-warming | Warm dependencies and compile trusted new base as soon as base advances; use Boat-style snapshots | Reduce setup / shorten startup | T0 | ADR 0013 executor, isolated snapshots and start budgets | Cold/warm setup, wasted warming, net billed cost |
| Cancel redundant PR CI | On enqueue, skip/cancel duplicate PR jobs when equivalent required queue evidence will replace them | Remove duplicate work / free runners | T0 with equivalent obligations | Workflow policy mapping, cancellation grants | Net saved minutes, missing-check and dequeue recovery tests |
| Stack-top testing | Test a cumulative stack top once for an atomic eligible composition | Amortize shared tests / fewer waits | T0 only for that exact composition | Pinned ancestry, stack/barrier semantics | Runs/stack, tree equality and landing invariants |
| Post-merge verified fast lane | PR evidence plus lightweight queue check, then verify and auto-revert | Less pre-merge CI / faster landing; recovery costs extra | T2 | Explicit policy, complete disjoint reverse closures, revert capability | Exposure duration, incidents, revert success and total cost |
| Cost/latency optimizer | Search batch size, depth and isolation policy with real-planner simulation | Minimum minutes meeting latency SLO | T0 | Efficiency lab, fixed policy constraints | Held-out Pareto curves and SLO violations |
| Critical-path admission | Schedule ready long jobs first within fair per-repo budgets; deduplicate concurrent identical cache misses | Less idle/duplicate work / shorter makespan | T0 | DAG and runner accounting, durable single-flight ownership | Utilization, starvation bounds, duplicate starts, p95 |

### Reuse is a new evidence contract

The exact-tree cache key is **tree OID + workflow file blob OID + job definition
digest + immutable runner image identity**, namespaced by repository/tenant and
check producer. The job digest binds matrix, commands, environment contract,
toolchain and resolved action/dependency identities. Mutable image tags are not
identities. Tree-deterministic means no undeclared network, clock, randomness,
secret, commit/history/ref or external-service inputs. Ordinary GitHub workflows
are not presumed deterministic. A declaration is an assumption (T1), not a
proof (T0); cross-SHA reuse must never be marketed as unconditionally exact.

Only queue-owned trusted executions under ADR 0013 may populate either cache;
PR checks, fork state and contributor attestations cannot. Record origin run,
attempt, producer, policy and key version, and a reuse attestation for the current
composition. Do not copy a forge success onto another SHA or bypass ADR 0009's
unique-marker provenance. Activation needs an explicit evidence adapter preserving
revocation, generation, required matrix membership and final landing guards.
Revocation, runner/check changes or missing provenance cause misses. Bound
retention and eviction; a miss always executes normally.

Per-path keys include glob/contract version, job/workflow/image identity and
lockfiles, generated inputs and configuration closure, alongside the requested
Git content addresses and toolchain. Canonically encode names, modes, symlinks,
absence, deletions, renames and submodule identities using Git readers. Undeclared
inputs invalidate the hermeticity claim. Shadow full runs audit that claim;
disagreement disables reuse and investigates affected evidence.

### Diagnostic and batching mathematics

For a single culprit with posterior probabilities q_i summing to one, a binary
probe tree with culprit leaf depths d_i has expected additional probes
E[R] = sum_i q_i d_i. Balanced halves target uniform q; probability-balanced
splits approach entropy H(q) = -sum_i q_i log2(q_i), with ideal binary coding
bounds H(q) <= E[R] < H(q)+1 when arbitrary partitions are legal. A singleton-first
probe can win for a dominant q_i. Count the original failure and final composition
confirmation separately; not every code tree corresponds to a legal composition.

For multiple faults use generalized binary splitting / Hwang-style adaptive group
testing as a candidate, not a claim of optimality under interactions. Evaluate
V(S) = min_G [c(G) + P(pass G)V(S|pass G) + P(fail G)V(S|fail G)]
over legal probes, with posterior updates, bounded search and deterministic ties.
The lab measures actual run costs c, multiple culprits, correlated failures,
flakes and interactions. Priors combine blast radius, failure-prone paths,
repo-local author/agent history and retry history; poor calibration falls back
to ordinary isolation. Preserve oldest-batch blame and singleton-confirmed
failure before eviction. A passing complement alone cannot prove a singleton
failed: interactions require fallback, not wrongful eviction.

Coverage attribution therefore proposes a suspect, tests it for confirmed
single-change failure, and tests the remaining exact composition before landing.
One confirmation run suffices only when the suspect already has admissible
singleton failure evidence. Historical co-failure is a hint, never causal proof.

For homogeneous independent defect probability p, simple two-stage group testing
has expected tests/item 1/k + 1-(1-p)^k, approximately 1/k+kp at small kp,
minimized near k=1/sqrt(p). This is starting guidance, not an optimum for recursive
isolation, speculation or expensive unequal-duration jobs. For heterogeneous
p_i, use batch pass probability product_i(1-p_i) and optimize expected total
minutes including conditional isolation and invalidated suffix work. Correlated
paths need joint scenarios, not blind products. Respect fairness/max wait,
one-way barriers, queue ceilings and ADR 0009 policy until separately activated.

Speculation admits a candidate when P(prefix succeeds) * latency_saved * v
exceeds expected incremental runner-minutes * c plus start/cache costs. Here v
is the repo's value per saved minute and c its cost per runner-minute; a hard
budget and latency SLO bound the knob. Estimate prefix survival jointly when
failures correlate. Never compare seconds directly with dollars.

### Cancellation, flakes and fast lanes

Early test failure may stop dependent speculative execution provisionally;
it cannot establish blame before ADR 0009 confirmation. Preserve retry evidence
and rebuild as needed if the failure proves flaky. ADR 0011 composition/suffix
invalidation and ADR 0009 capability/depth restrictions still govern cancellation.
Distinguish request, confirmed stop and unsupported; charge until termination,
reject late success and never cancel durable landing attempts. Executor deletion
is a separate capability from forge cancellation.

Ordering must run the same tests with identical isolation; order-dependent suites
use original order. Quarantine means tracked diagnosis and targeted retries,
not dropping required tests: waiving a check changes guarantees and needs a
separate T2 policy. Same-tree disagreement may also mean environmental drift.
Retry only identified failed jobs with the existing budget and deadlines.

PR cancellation needs queue-check substitution accepted by branch protection,
all distinct PR obligations retained, and resumption on dequeue. Stack-top
success covers only that cumulative composition: no arbitrary intermediate prefix
may land from top evidence. Use atomic composition landing, or test prefixes.
A one-way layer breaks the stack and forces the solo barrier.

The fast lane deliberately gives up strict pre-merge tested-composition assurance:
main may be broken until detection/revert, and PR CI may have tested an older
base. Limit it to computed two-way none/small changes with complete, disjoint
reverse-dependency closures, no one-way or unknown dependencies, and owner opt-in.
Pin policy and evidence, bound verification/exposure time, pause on failure and
revert through a fenced recovery path. Conflicting/failed reverts pause and alert;
auto-revert is mitigation, not proof of zero harm. ADR 0005 remains the governing
post-merge foundation. Never apply T2 silently to meet an SLO.

### Evaluation and rollout

First evaluate every algorithm in the efficiency lab on internal/sim. Preserve
real planner/executor invariants, deterministic seeds and replayable decisions;
extend metrics rather than substituting the standalone capacity approximation.
Synthetic workloads vary arrivals, risk heterogeneity, duration distributions,
runner caps, interactions, flakes, cancellations, cache validity and cold starts.
Recorded public traces may supplement them with documented license, missingness
and representativeness limits. Freeze train/tune and held-out seeds/traces.

Do not read or use OpenMerge's dogfood journal latency or runs-per-merge for
this work before ADR 0009 period B ends AND drains. The seal includes discarded
and still-running period-B queue builds, not a calendar date. No live queue,
runner, workflow, permission or policy change before recorded drain acceptance.
Code may land Hold/Inert in the meantime without activation.

Then pre-register partner cohorts, baseline, sample size, assignment, SLO,
required savings, stop rules and analyzer versions before observing pilot results.
Publish a savings report with total and per-landing runner-minutes, elapsed CI
and enqueue-to-land p50/p95, unlanded counts, wasted/cancelled/retry/setup minutes,
cache hits, diagnostic runs, correctness incidents and billed versus marginal
cost including starts/storage. Use paired seeds and uncertainty intervals;
report each technique's marginal effect and combined effect without double-counting
avoided work. Present the nondominated minutes/latency Pareto curve per repo;
select minimum cost satisfying the SLO, with conservative fallback on drift.

| Slice, ordered by expected impact/risk | Exit evidence | Deploy class |
|---|---|---|
| ADR and index | Docs checks | Inert |
| Lab, metrics and offline optimizer | Seed replay, held-out curves, invariants | Inert while offline |
| T0 batching/isolation/speculation and attribution | Fault/interaction tests, confirmation and barrier parity | Hold |
| T0 ordering, shard balancing, failed-job retries and duplicate PR cancellation | Equivalent obligations, capability/recovery tests | Hold |
| Cache evidence adapter and T1 path/tree opt-ins | Poison/revocation/input-closure tests and shadow agreement | Hold |
| Executor warming and single-flight scheduling | Tenant isolation, cleanup and net cost evidence | Hold |
| Stack composition reuse | Exact landing coverage and one-way barrier tests | Hold |
| T2 partner fast lane | Owner policy, bounded exposure and revert rehearsal | Hold |

Each implementation PR declares its actual class; Hold dependencies propagate.
Prioritize qualified T0 work by measured impact divided by risk, then opt-in T1,
then separately approved T2. New durable evidence requires reader-first codecs,
recovery fixtures and rollback/backup boundaries. No slice bypasses prior ADR gates.

### Open questions and recommended answers

1. **Can tree reuse be T0?** Only when equivalence is established by the existing
   execution contract; otherwise label it T1 with per-check hermeticity opt-in.
2. **How much history should priors use?** Start repo-local with shrinkage toward
   a conservative common prior, calibration monitoring and no identity-based gates.
3. **When is direct attribution enough?** Only with confirmed singleton evidence
   and passing remaining composition; otherwise retain adaptive isolation.
4. **Who chooses cost versus latency?** Repository policy owners set SLO, budget
   and value knob; show the Pareto curve and retain current policy by default.
5. **What unlocks T2?** Explicit owner acceptance of exposure, complete two-way
   facts/closures and a successful recovery rehearsal; keep it off by default.
6. **How to manage hermeticity drift?** Version contracts, audit with shadow runs,
   revoke on disagreement and fall back to full checks immediately.

## Consequences

OpenMerge can save work before buying cheaper execution, while keeping guesses
separate from authority. The portfolio adds cache provenance, telemetry quality,
calibration, privacy and recovery obligations. Savings are workload-dependent;
extra probes, shadow runs and warming can cost more than they save. This proposal
changes no live service and preserves the sealed experiment.

## Alternatives considered

- Cheaper runners alone reduce unit price but leave redundant work intact.
- Uniform batches and fixed depth are simple baselines but ignore heterogeneous
  risk and duration; retain them as fallbacks and experimental controls.
- Prediction-only eviction or pass inference saves tests by weakening guarantees;
  reject it for strict mode.
- Global private-history models could leak decisions across repositories; prefer
  local models until noninterference can be demonstrated.
- Universal post-merge verification loses strict assurance for every change;
  reserve it for an explicit, qualified T2 policy.
