# 0013. Reduce CI cost with pluggable CI executors

- **Status:** Proposed
- **Date:** 2026-10-28

## Context

The owner's product direction, recorded on 2026-10-28, is an open-source,
agent-optimized merge queue, review and change-management system informed by
Scion, Entire, Mergify and Graphite. Individual private repositories are in
scope: founders with many agent-authored PRs should scale their queues while
saving CI minutes and dollars. Lower compute prices alone do not establish
lower cost per landed PR.

[ADR 0009](0009-speculative-batching.md) supplies batching, bisection and
speculation. Its dogfood latency and runs-per-merge observations remain sealed
until period B ends; this ADR publishes none of them.
[ADR 0010](0010-agent-provenance.md) treats provenance as declarations that can
only add requirements. [ADR 0011](0011-cross-repository-dependent-changes.md)
adds dependency invalidation and reserves forge-neutral growth, including
GitLab and Forgejo. Executor results must preserve both contracts.

[ADR 0012](0012-hosted-service-architecture.md) is Accepted: per-installation Go
engines and Postgres cells, a Cloudflare control plane, one US region initially,
and Bun tooling. Running tests in an external sandbox must not run repository
code inside an engine cell or move landing authority into the control plane.
Its licensing question remains open. The owner's newer open-source description
leans toward fully OSS; the final licensing decision belongs to the owner.

This proposal and its index entry are **docs only, Inert**. They authorize no
runner switch, configuration change, App grant, deployment or activation. The
[operating guide](../operating.md) and earlier ADR release gates still apply.
Implementation and experiments start only after the owner records the ADR 0009
period-B drain, expected mid-November 2026; a date alone is insufficient.

## Decision

### Apply the savings stack in order of leverage

This is an engineering priority order, not measured savings for every workload.

| Lever | Already present | Missing work |
|---|---|---|
| Fewer CI runs per merge | ADR 0009 batching, bisection, adaptive sizing and speculative chains | Setup guidance and cost-per-landing reporting that includes failures, retries and discarded work; no new dogfood claims |
| Queue-only full CI | Queue test refs and required forge-check evaluation | Document light PR suites versus full queue suites, configure triggers and policy, and define explicit check-name mapping; no automatic substitution today |
| Fast cancellation of invalidated speculative builds | ADR 0009 optional cancellation, durable intents and run attribution; ADR 0011 slice 4b specifies composition/suffix invalidation | Complete and qualify slice 4b integration under Hold, provider cancellation support and measured stop delay; preserve depth rules |
| Warm starts / snapshot reuse per base SHA | Git/build state and journal projection snapshots exist, but are not CI machine snapshots | Tenant-isolated CI snapshot cache, lifecycle, invalidation and cost accounting |
| Cheaper compute | Forge-reported checks already allow external runners | Provider setup guidance, comparable measurements and, later, an opt-in API executor |

Queue-only full CI needs distinct stable names, for example `pr-light` and
`queue-full`. PR admission may require the former; queue landing must require
the latter on the exact tested commit, with every required matrix member
accounted for. Document each workflow/job producer, trigger and policy name.
Do not rename a light job to satisfy a full-suite requirement, count a skipped
suite as success, or leave a PR-only required check waiting forever on a queue
ref. In forge authority mode, reconcile required-check protection with these
triggers; in OpenMerge authority mode, map requirements into `merge_when` under
ADR 0008's protection import rules. Producer ambiguity remains a blocker, not a
reason to relax checks. This ADR defines the intended contract, not new config
keys accepted by today's binary.

### Separate three execution paths

**(a) Forge-reported checks remain the default and current trust root.**
OpenMerge reads CI outcomes from the forge using its existing exact-commit and
trust gates. It does not execute the tests. Keep this path supported even if
an installation never buys compute from an OpenMerge partner.

**(b) Route runners through `runs-on`, without OpenMerge code changes.**
Owners can install a provider integration and select Boat's `boat` label or an
appropriate `blacksmith-*` label. Actions still reports the results. Optional
future setup guidance can route only queue refs to the cheaper runner:

```yaml
# Illustrative job field; apply only after the period-B gate and qualification.
runs-on: ${{ startsWith(github.ref, 'refs/heads/openmerge/') && 'boat' || 'ubuntu-latest' }}
```

Keep the workflow's push trigger on `openmerge/queue/**`, as the operating guide
requires, and retain its PR trigger for the light suite. The expression selects
a runner; it neither triggers CI nor implements the light/full suite split.
For Blacksmith, substitute the actual provisioned label from its
[setup guide](https://docs.blacksmith.sh/introduction/quickstart.md).
Qualify permissions and personal-account installation support separately;
Blacksmith's organization registration mechanism is not proof that an individual
private repository can use it. A branch prefix is not a security boundary:
apply existing exact-head trust gates and never expose privileged secrets merely
because a ref starts with `openmerge/`.

**(c) Introduce a forge-neutral API executor only after the trust decision.**
Boat is the first candidate, not a required dependency. An executor creates a
sandbox from an approved snapshot, acquires the exact tested tree, runs all
owner-approved required commands, collects terminal results and deletes the
sandbox on cancellation. Commands execute only in the sandbox. The Go engine
owns scheduling and result validation; no provider, dashboard or webhook can
request landing directly. This matters for GitLab/Forgejo and for environments
without native CI, independently of GitHub runner routing.

The conceptual executor interface supports start, observe and cancel with
capability discovery. Bind each operation to installation/repository, queue,
batch/composition generation, tested SHA, base SHA, policy/command digest,
matrix member and immutable attempt ID. Return provider/sandbox identity,
terminal outcome and evidence references. Unknown, partial, timed-out or
unattributed results block. Idempotent start recovery must discover an existing
sandbox before creating another; persist intent before external effects.
Webhooks are scheduling hints; reconcile with fresh API reads and polling.
No provider-specific types enter the planner or forge driver contracts.

### Require an explicit trust expansion for API execution

Recommend **App-published forge checks plus signed journal attestations**, with
an explicit owner opt-in. Path (c) stays disabled until the owner accepts this
model and its implementation passes adversarial qualification. It is a proposed
extension to ADR 0008's “CI stays where it is” boundary, not a claim that the
current engine already supports self-attested CI.

On a forge with native checks, (c) must publish its result through the
installation's App and then consume the fresh forge observation for the exact
SHA. Where protection actually enforces required checks, require the executor's
stable check names and pin their App producer where supported. Qualify that
producer binding before activation: ADR 0008 already identifies App-pinned and
ambiguous-name imports as weaker or unsupported. Do not silently treat a
same-name status from another producer as equivalent.

In ADR 0008 OpenMerge-authority mode the App bypasses applicable write
restrictions and OpenMerge enforces CI policy. A ruleset requiring an App-written
check therefore does **not** independently constrain a compromised App/engine.
The atomic base-and-fence update prevents stale/concurrent landing and preserves
attempt identity; it does not prove tests ran. Preserve fresh policy/protection,
trust, approval and dependency checks and final push guards in either mode.
No executor result can bypass those gates or revive a retired composition.

Threats and limits of the proposed model:

- A contributor controls test code, can try to replace commands, poison caches,
  exfiltrate secrets or replay a successful result for another SHA. Resolve the
  command/policy contract from owner-approved configuration; isolate untrusted
  code and credentials, bind all inputs and attempts, and reject stale results.
  Neither declared provenance nor an `openmerge/*` ref grants trust.
- A compromised provider, sandbox, API credential or result collector may forge
  an exit result. Keep App write credentials and attestation signing keys out of
  sandboxes, use scoped provider keys, and bind collection to the recorded job.
  Lifecycle webhooks, even HMAC-authenticated, are not test-success evidence.
- An App credential holder can forge App checks. An engine/signing-service
  compromise can create matching checks and signed false attestations. Signing
  establishes attribution and tamper evidence, not independent execution proof.
  Separate signing custody from repository jobs and control-plane deployment
  roles, restrict key access and record key rotation. Owners must accept the
  remaining engine/provider trust or stay with independent CI through (a)/(b).
- A database/export custodian can omit or rewrite locally self-consistent audit
  material. Use ADR 0008's independently retained signed manifest anchors and
  overlapping export verification; a locally rehashed bundle proves neither
  completeness nor truthful execution. An administrator who can change forge
  protection or use break-glass remains outside the ordinary landing guarantee;
  retain authority-drift and outside-push evidence.

Before landing, persist a canonical, versioned signed result attestation binding
all operation identities above, snapshot/image and command digests, required
matrix inventory, provider result identity, timestamps, exit status, bounded
log/artifact digests and signer key ID. Record failed/cancelled attempts too,
without secrets or unbounded logs. Export the attestations and their decision
closure through a versioned extension of the ADR 0008 audit export; do not
mutate frozen formats or substitute a signature for recorded source evidence.
Verify missing members, wrong tenant/SHA/policy, duplicate/replayed completion,
key revocation, collector compromise, restart and late success after cancel.

For a forge without native CI, a separately approved adapter may consume the
signed result directly with the same evidence and landing guards. Restricting
(c) to such forges is an alternative, not an excuse to enable it implicitly.

### Provider facts as of 2026-10-28

Prices below are USD list rates, not measured bills or performance equivalence.
Sources were fetched on this date; unconfirmed points are called out below.

| Option | Per minute | Source / qualification |
|---|---|---|
| GitHub-hosted Linux x64 2-core | $0.006 | [Runner pricing](https://docs.github.com/en/billing/reference/actions-runner-pricing) |
| GitHub-hosted Linux 4-core | $0.012 | [Runner pricing](https://docs.github.com/en/billing/reference/actions-runner-pricing); Team/Enterprise Cloud organizations only |
| Blacksmith Ubuntu x64 | $0.004 | [Pricing](https://www.blacksmith.sh/pricing); base listed option, not every size |
| Boat small (2 vCPU / 4 GB) | $0.0003 | [Pricing](https://docs.boat.dev/pricing.md); $0.018/hour divided by 60 |
| Boat default (4 vCPU / 8 GB) | $0.0006 | [Pricing](https://docs.boat.dev/pricing.md); $0.036/hour divided by 60 |
| Boat large (8 vCPU / 16 GB) | $0.0012 | [Pricing](https://docs.boat.dev/pricing.md); $0.072/hour divided by 60 |

Blacksmith lists 3,000 free minutes/month and Docker layer caching at
$0.50/GB/month on its [pricing page](https://www.blacksmith.sh/pricing).
Its [runner docs](https://docs.blacksmith.sh/blacksmith-runners/overview.md)
list `us-east`, `us-west`, `eu-west` and `eu-central`.
Its [App permissions](https://docs.blacksmith.sh/blacksmith-administration/github-app.md)
describe GitHub organization self-hosted runner provisioning/registration.
These facts do not establish personal-account eligibility.

Boat's [pricing](https://docs.boat.dev/pricing.md) lists $20/$100/$500/$2000
monthly plans with the corresponding dollars included as VM time, rather than
an additional fee on top of that included compute. This is a subscription
commitment, not cash refundable credit: included time expires each billing
period. Organizations pay per seat. The $20 plan permits 12 starts/minute,
60/hour and 200/day. Bursting reaches up to 3x limits, charging $0.03, $0.05 or
$0.07 per excess start by tier. Create, fork and resume count as starts.
The [CI page](https://docs.boat.dev/ci.md) also describes start-rate retry
behavior; qualify how CI integration and paid bursting interact before costing
an experiment. Do not assume every throttled job buys a burst automatically.

Boat's [CI docs](https://docs.boat.dev/ci.md) support `runs-on: boat`, reject
public-repository jobs, provide no Windows runners and cap CI jobs at six hours.
Its [compliance docs](https://docs.boat.dev/compliance.md) say SOC 2 is in
progress and machines run in Germany, Finland and France. **EU-only compute
does not mean EU-only storage:** those docs explicitly allow snapshot copies
outside the EU. The [REST API](https://docs.boat.dev/api/v1.md) base is
`https://boat.dev/api/v1`, with [scoped API keys](https://docs.boat.dev/api-keys.md).
Outbound [HMAC-signed webhooks](https://docs.boat.dev/webhooks.md) cover
`sandbox.ready`, `sandbox.hydrated`, `sandbox.error`, `sandbox.archived`,
`sandbox.degraded` and `sandbox.recovered`; none attests command success.

**Unconfirmed:** whether Boat CI jobs register as GitHub self-hosted runners.
Supporting `runs-on` does not answer that implementation question. Also
unconfirmed is the current disposition of GitHub's announced per-minute
self-hosted runner fee. [Current GitHub billing docs](https://docs.github.com/en/actions/concepts/billing-and-usage)
say self-hosted usage is free. Use that documented baseline, identify any future
platform fee separately, and reverify before the experiment or selling compute.

### Risks, economics and experiment

For depth four and a matrix of M jobs, initial speculation can demand **4M
starts**, before retries, bisection, invalidation rebuilds or other repositories.
At M=3 that consumes the $20 plan's 12-start minute allowance. At M=4 it needs
16 starts, exceeding that allowance even though only 16 sandboxes are involved.
Without bursting, pace admission across windows; 60/hour allows only three
complete 16-start waves (48 starts), and 200/day only twelve (192), ignoring all
other usage. Matrix concurrency limits help but do not remove hour/day budgets.
Use an installation-wide start budget, backpressure and an explicit burst-spend
cap; more speculation can otherwise increase both latency and cost.

Illustrative arithmetic, not a benchmark: a two-minute Boat default job costs
2 × $0.0006 = $0.0012 in VM time. A $0.03–$0.07 excess-start charge alone is
25–58.3 times that compute cost. Count it, cancelled time, retries, snapshot
storage, plan utilization and any remaining Actions orchestration cost in cost
per landed PR. Compare matched resources and actual durations, not rate ratios.

Boat's EU machines create a cross-region path from ADR 0012's US cells. Measure
checkout, hydration, artifact transfer and cancellation round trips; do not
promise a latency improvement. Require owner consent to code/secrets leaving
the engine region and qualify snapshot storage location and deletion. No
US-only or EU-only residency promise follows from this proposal.

Keep provider credentials, snapshot formats and pricing outside the planner;
retain (a) and allow migration to another executor without rewriting policy.
Snapshot reuse must be keyed by tenant/repository, base SHA, trusted image,
toolchain, dependency/lockfile and command-policy digests. Never reuse an
untrusted PR's writable state as a trusted base or across tenants. A hit is
setup reuse, not permission to reuse a prior passing result on a different SHA.

Public repositories already have free standard GitHub-hosted CI, while larger
runners are charged; [GitHub billing](https://docs.github.com/en/actions/concepts/billing-and-usage)
distinguishes these cases. Individual private repos are the savings target.
Boat's public-repo restriction means the requested experiment on OpenMerge's
own CI is blocked for the Boat arm: GitHub repository metadata checked on 2026-10-28
identifies [OpenMerge](https://github.com/CanadaApollo6/openmerge) as public.
Recheck eligibility before starting. Record that blocked arm; only an explicitly approved private
mirror with equivalent workload may substitute, clearly reporting the limit on
comparability. Do not silently omit the arm or change repository visibility.

After period B ends **and drains**, pre-register a three-arm comparison on
OpenMerge's own CI: GitHub-hosted, Boat through `runs-on`, and Blacksmith.
Resolve public/personal-account eligibility before assigning work. Fix the
suite, resource class, matrix, depth/batching policy, retry policy, time windows,
sample size, assignment method and stopping rules before seeing outcomes.
Separate cold and warm cache strata; run equivalent workloads with balanced
assignment, preserving all failed and cancelled attempts. Define metrics first:

- **Cost per landed PR:** all attributable CI charges in an arm, including
  speculative waste, cancellations, retries, burst starts, storage and allocated
  plan commitment, divided by landed PRs. Report zero-landed windows separately,
  billed versus marginal costs, credits/free tiers and public free baseline.
- **Queue latency:** enqueue-to-confirmed-landing median and p95, plus waiting
  for runner/start budget; report blocked or unlanded work and sample counts.
- **Flake rate:** failures followed by success on the same SHA and unchanged
  suite/environment under the pre-registered rerun rule, over eligible attempts;
  separate infrastructure/start failures and retain the raw classification.

Pre-register acceptable latency/flake regressions and required cost improvement
with the owner; do not invent thresholds after the comparison. No runner changes
before period B, because they would contaminate ADR 0009's latency comparison.

### Staged slices and deployment classes

Inert means no changed live behavior, credentials, policy or writers; Hold means
no deployment/activation until prerequisites and owner approval are recorded.
Every implementation slice waits for the period-B drain and earlier ADR gates.

| Slice | Scope and exit evidence | Deploy class |
|---|---|---|
| 0 | This Proposed ADR and index; no activation | Inert |
| 1 | Docs/setup guidance for (b), queue-only full-suite/check-name mapping, eligibility and optional queue-ref expression; applying any runner/config switch is a separate qualified rollout | Inert for docs; Hold for adoption |
| 2 | Forge-neutral executor interface, typed identities/capabilities and fake-executor recovery/replay fixtures; no production execution | Inert |
| 3 | Boat API executor behind the owner-approved trust decision; exact-SHA/matrix/tenant validation, App check publication, signed evidence/export, budget and secret isolation qualification | Hold |
| 4 | Snapshot cache per trusted base SHA and environment digests; prove tenant isolation, poison rejection, eviction, misses and truthful billing | Hold |
| 5 | Cancel-by-delete using durable recorded targets; prove idempotency, restart recovery, confirmed deletion, late-result rejection and no deletion of landing attempts | Hold |

Cancel-by-delete extends the executor capability, not the meaning of a forge
cancel POST. Reuse ADR 0011 slice 4b's invalidated composition/suffix identities
and ADR 0009's depth restrictions. A delete request is not confirmation. Observe
termination, report unsupported/failed cleanup and keep retry budgets bounded;
never turn cleanup failure into permission to land. The Boat slice requires
bounded sandbox lifetime/cleanup before activation; slice 5 adds prompt
invalidation cancellation, not the first protection against leaked machines.

### Open questions for the owner

1. **Which trust model for (c)?** Recommend App-published checks plus signed
   attestations and independent audit anchors, explicitly accepting that
   engine/provider compromise can falsify execution. Keep (c) off until that
   acceptance and qualification; retain independent forge CI as the default.
2. **Should hosted OpenMerge bundle or resell compute?** Recommend bring-your-own
   provider account initially, with transparent cost reporting and spending caps.
   Defer resale/bundling until measured bills, support obligations and pricing
   are approved; no hidden subsidy or compute commitment in the alpha.
3. **Default executor for new installs?** Recommend (a), existing forge CI, with
   opt-in (b) guidance. Do not silently switch providers or require an
   organization account for individual private repositories. API execution is
   a later explicit opt-in, including on forges without native CI.
4. **Accept EU-only Boat compute alongside US hosting?** Recommend per-installation
   opt-in after latency and data-location review, including non-EU snapshot
   copies; otherwise retain the current executor. Do not make Boat the hosted
   default or promise a residency boundary the provider does not supply.
5. **Licensing direction?** Recommend fully OSS in line with the owner's updated
   product description. Leave license selection and any change to ADR 0012's
   unresolved licensing position to a separate explicit owner decision.

## Consequences

The first savings improvements preserve forge CI and require no executor
implementation. Later API execution can be forge-neutral, but expands the
trusted computing base and adds signing, audit, tenant isolation, start-budget
and cleanup obligations. A low per-minute price is not a savings guarantee.
This ADR neither accepts its open questions nor changes any live policy.

## Alternatives considered

- **Only buy cheaper runners.** Useful through (b), but leaves avoidable runs,
  speculative waste and setup time untouched; it is the last savings lever.
- **Only publish App checks for (c).** Preserves the forge check interface but
  supplies no independent execution proof when OpenMerge writes the checks;
  inadequate without explicit trust acceptance and durable audit evidence.
- **Only sign results in the journal.** Useful for forge-neutral audit, but
  skips enforceable forge checks where available and cannot make a compromised
  signer truthful. Combine both and document their limits.
- **Limit API execution to forges without native CI.** Reduces the initial
  migration surface but excludes owners who deliberately accept sandbox CI on
  GitHub. Keep it available as a stricter owner choice, not the general design.
- **Make Boat the default or resell compute immediately.** Eligibility,
  start-rate economics, data location and trust are unresolved. Measure and
  qualify before making a product-wide commitment.
