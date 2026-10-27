# 0011. Cross-repository dependent changes

- **Status:** Proposed
- **Date:** 2026-10-27

## Context

An agent can change an API in one repository and its client in another in one
working session. Two independent queues cannot express that the client must
wait for the API, or explain which API revision its tests used. A shared label
can group the work for review, but does not establish a safe landing order.

[Entire's Trails](https://entire.com/features/trails) connect intent, review and
changes across repositories from idea to merge. OpenMerge should borrow that
connected view of work while remaining a merge queue for existing forges.
Grouping work does not itself establish an atomic submission guarantee.

The relevant prior art, checked on 2026-10-27, is:

- [Zuul cross-project dependencies](https://zuul-ci.org/docs/zuul/latest/gating.html#cross-project-dependencies)
  use `Depends-On: <change-url>` in commit messages or PR descriptions. Its
  dependent pipelines order changes across projects and freeze repository state
  for jobs. Connections can span different review systems. This is the closest
  model for eventual co-testing; adopting its spelling does not supply its CI
  orchestration. Zuul also has opt-in circular dependencies; we do not adopt them.
- [Gerrit topics and submit whole topic](https://gerrit-review.googlesource.com/Documentation/cross-repository-changes.html)
  group submissions within one Gerrit instance. Gerrit explicitly warns that
  multi-repository submission can be partial; topic names can collide and even
  involve changes a caller cannot see. A topic is useful context, not a portable
  transaction boundary.
- [Mergify's current Depends-On protection](https://docs.mergify.com/merge-protections/builtin/#depends-on)
  **does support ordered cross-repository dependencies**, when repositories have
  Mergify enabled and the same owner. It waits for merging, rejects unresolved
  or closed-unmerged dependencies, and leaves cycles blocked. Its documented
  dependency list in the queue UI covers queued same-repository stacks, with
  other dependencies still enforced. A blanket claim that Mergify lacks support
  would be wrong. These docs do not establish cross-repository speculative
  checkout or atomic landing guarantees.
- [GitHub's native merge queue](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)
  documents a queue for a selected branch in a repository, testing its queued
  PRs against that branch. It documents no cross-repository dependency or
  co-testing contract. We treat that as an unsupported native capability,
  rather than inferring a guarantee from two queues passing independently.

OpenMerge already has the necessary boundaries:
[ADR 0006](0006-multi-repository-serving.md) serves several repositories;
[ADR 0007](0007-changes-not-prs.md) gives each queue-owned change a durable ID,
revision, digests and evidence; [ADR 0004](0004-forge-driver-interface.md) keeps
forge operations behind drivers and fences each base-branch landing.
[ADR 0009](0009-speculative-batching.md) orders speculation within a queue,
not across repositories. [ADR 0008](0008-openmerge-policy-authority.md) keeps
policy authoritative per repository, and
[ADR 0002](0002-storage-and-state-model.md) requires replayable decisions.
None provides a transaction across independent git repositories.

**Related:** agent provenance is being drafted separately as ADR 0010. This ADR
does not design agent identity, session capture, attribution or provenance trust.
The CI provenance below identifies tested inputs, not who produced the code.

## Decision

### Scope and staged guarantees

**Ship ordered landing first, with explicit, revision-pinned dependency edges.**
A dependency adds a gate; it never grants trust, approval, enqueue permission,
priority, or a right to write another repository.

| Stage | Guarantee | Release boundary |
|---|---|---|
| A: ordered | B is not tested or started landing until its declared A revision has a confirmed OpenMerge landing on A's target base | Implementation sequence below, after dogfood period B drains |
| B: co-tested speculation | B's CI consumes an identified speculative composition of A; A then B land through separate fences | Deferred; follow-up ADR must close the CI and coordination gates below |
| Topic submission | No all-or-nothing cross-repository guarantee | Not offered |

Stage A covers distinct repositories in **one serving scope**: the same
configured forge driver/origin, forge installation identity, and OpenMerge
database. An App installation is not the same thing as a server process.
Several workers may cooperate through that database; merely being visible to
one server under ADR 0006 does not make separate installations mutually trusted.
This also respects ADR 0008's dedicated-database-per-installation authority
boundary. No cross-installation, cross-forge or cross-server federation ships.

Both endpoints must be served, explicitly opted in and use strict verification.
Post-merge queues need a separate decision on verification/green evidence
([ADR 0005](0005-verification-modes.md)); a successful push there is not strict
validation. Same-repository edges are rejected in this first slice; ADR 0009's
queue order and stacking remain separate. Retargeting a PR creates/resolves a
different queue-owned change, never silently moves a dependency edge.

### Declaration and canonical references

**Use one `Depends-On:` line per dependency in the change's authoritative intent.**
For today's PR-backed changes this is the PR body, not a comment, label or a
scan of every commit. A future direct submission uses its authenticated intent
under ADR 0007 Stage B. A Gerrit driver can source that intent from its change
message through the same normalized contract. No direct-submission API is added
by Stage A.

The canonical, versioned reference is an opaque URI:

```text
openmerge-change:v1/<forge-instance>/<installation>/<change-id>@<revision>
```

- `forge-instance` is one percent-encoded UTF-8 segment containing
  `<driver-kind>:<canonical-origin>`, for example
  `github%3Ahttps%3A%2F%2Fgithub.com`. The origin comes from configured driver
  identity, including a nondefault port when applicable, never from PR text.
- `installation` is the percent-encoded stable installation key already used
  by queue identity, not a database row ID or an OpenMerge process ID.
- `change-id` is ADR 0007's ID, presently `chg1_` plus 64 lowercase hex digits;
  `revision` is its positive decimal observation sequence, without leading zeros.
  A SHA alone is not a revision: H1, H2, H1 are three revisions.
- The ID resolves to exactly one stored canonical queue key, including its
  repository, base and lane. Resolve within the named serving scope, verify
  that binding, and reject ambiguity. Do not derive IDs from discovered case
  aliases, YAML queue names, mutable titles or PR numbers alone.
- Components use canonical percent encoding; reject malformed encodings,
  credentials, extra components, queries and fragments. There is no network
  dereference of this URI and no caller-selected OpenMerge endpoint.

For illustration only, `<id>` below stands for a full valid change ID:

```text
Depends-On: openmerge-change:v1/github%3Ahttps%3A%2F%2Fgithub.com/123/<id>@7
```

The CLI/API read view supplies a copyable canonical reference. A recognized
HTTPS forge change URL, such as `https://github.com/acme/api/pull/42`, is also
accepted as an input alias for author convenience. Resolve it through the
configured driver and served-repository registry, pin its observed change ID,
revision and head, and show that pin in the brief **before it can authorize
work**. The URL remains a display alias, never the stored identity. Ambiguous
installation/base/lane selection requires the canonical form. Bare `#42` and
`forge:owner/repo#42` are rejected: they omit instance and queue scope.

**An alias is bound once per declaration, not followed on every poll.** Its
binding survives restart and an unrelated body edit. If the target advances,
the dependency becomes stale; updating it requires a canonical reference to
the new revision. Removing and re-adding an alias in separately observed
declarations is a new declaration and visibly records a new binding. An edit
which OpenMerge never observes cannot be reconstructed as an intervening act.

Recognize exact, case-sensitive `Depends-On:` keys on standalone body lines,
with one non-whitespace reference value. Ignore lines inside fenced code,
indented code, block quotes and HTML comments. Normalize CRLF, sort and dedupe
resolved pins; reject malformed recognized lines rather than ignoring them.
Parser fixtures fix these boundaries. Initially bound each declaration to
32 direct edges and each transitive walk to 256 nodes, 512 edges and depth 16;
overflow is a visible blocking diagnostic, never a truncated permissive graph.

Record a domain-separated, versioned declaration digest over sorted canonical
pins and their pinned heads. Record a separate dependency-basis digest over
the resolved graph and decision inputs. For opted-in changes, extend the
ADR 0007 brief digest with the declaration digest; this is an explicit proposed
extension to that contract, leaving all non-opted-in digest bytes unchanged.
Declaration edits invalidate dependent compositions even at an unchanged head.
They also invalidate receipt approvals through the new brief/intent digest;
they do not manufacture a new code revision or silently turn native reviews
into intent-bound receipts. Recheck authorization under B's own review policy.

A shared topic label may later group a graph for navigation. It declares no
edge, enqueues nothing and carries no landing or approval authority. There is
no topic expansion that discovers additional executable work implicitly.

### Stage A: ordered landing

For `B depends on A@7`, A must be queued by someone authorized in A, pass A's
own gates, and land normally. OpenMerge waits for a **confirmed, settled
landing of A@7** and checks that its recorded landing commit remains reachable
from the freshly observed target base. With fast-forward, distinguish the
tested marker from the head that landed. A PR's closed/merged flag, a matching
head SHA, or somebody's assertion that the patch was cherry-picked is insufficient.

Only then may B enter a test composition under B's normal strict queue rules.
All declared dependencies must satisfy this rule. Unrelated eligible changes
can pass a waiting B without losing their relative order. Waiting does not
consume a batch slot, trigger bisection, or count as a defective change.
Unchanged dependency inputs cause zero extra builds or tests.

Stage A guarantees **landing order**, not integration correctness across repos.
B's CI may still build an old released client dependency, and A's landing may
break older clients before B lands. CI must fetch suitable committed inputs,
and authors must use expand/contract changes, compatibility shims or release
coordination where intermediate states matter. Neither queue order nor this
ADR deploys a service, publishes a package, or bounds that compatibility window.

Before B's durable landing start, re-read the relevant declarations, revisions,
source landing/base evidence, lifecycle and bilateral policy. Record these
observations; the pure planner consumes recorded facts, not forge calls.
Revalidate inside the landing-start transaction. Acquire graph queue locks in
canonical key order, including B, after any existing tenancy/global locks;
never acquire source locks ad hoc while already holding B's queue lock. Compare
the recorded queue watermarks/bases with the current fold and retry on drift.
No forge I/O occurs while this multi-queue lock set is held. A newly expanded
graph restarts validation with a new lock set rather than escaping the check.

Store B's dependency decision companion, normal policy decision records and
unchanged `LandingStarted` atomically. A crash cannot leave an authorized start
without its dependency evidence. The existing claim, checks generation,
policy digest and base/fence checks still apply to every member of the batch.
Every path, including depth 1, speculative suffixes, retries and bisection
groups, applies the same dependency gate.

Remote reads cannot be atomic with a later git push. A source base rewind or
body edit after the last successful read may go unseen before B starts.
Record the precise observation boundary; do not claim continuous enforcement
against forge administrators or break-glass writers. Once `LandingStarted` is
durable, settle that attempt through its existing fence even if a dependency
is then changed, removed or inaccessible. Never rewrite its historical decision.

### Failure, invalidation and recovery

Dependency state is orthogonal to a change's own trust, approvals and CI state.
Use these rules before a landing has started:

| Observation | Result and recovery |
|---|---|
| A is open, valid and not yet landed | B waits; A is neither auto-enqueued nor promoted |
| A was dequeued, rejected by policy, or failed CI | B waits for A's recovery; B is not blamed for A's failure |
| A closed without landing | Block B with `dependency-closed`; reopen that revision or edit the declaration |
| A is marked merged but has no OpenMerge landing evidence | Block with `dependency-unverified`; an outside-push acknowledgement is not approval or landing evidence |
| Unknown ID/revision, malformed or ambiguous alias | Block with `dependency-unresolved`; fix the reference |
| Timeout, rate limit, permission loss or unreadable source | Block with `dependency-unavailable`; retry with bounded backoff and normal polling |
| Unserved/removed repo, unsupported mode or another serving scope | Block with `dependency-out-of-scope`; serving/trust adoption or an explicit declaration edit is required |
| A's declared revision was superseded before landing | Block with `dependency-stale`; repin explicitly and revalidate |
| A's confirmed landing is absent from the current source base | Block with `dependency-not-on-base`; repair/reconcile source history or declare replacement work |
| Cycle, self-edge, conflicting pins for one change, or traversal limit | Block the affected graph with a diagnostic; edit declarations, never choose an arbitrary winner |

Detect cycles over resolved queue-owned change IDs, not URL spellings, and
report a deterministic cycle path. Inspect a proposed update against the
current graph under the same ordered lock discipline so two simultaneous
updates cannot each admit half a cycle. Incomplete discovery is unknown,
not proof of acyclicity. Work downstream of an invalid component waits;
unrelated components and queues continue.

A confirmed matching landing is a terminal node for dependency traversal:
its own historical dependency decision is evidence, not a request to follow
the change's latest declaration. Recheck that landing's presence and current
direct-edge consent, without allowing later source edits to replace its pins.

**A pushed dependency invalidates dependents, not just its own batch.** If A@7
has not landed and a push produces A@8, invalidate every unstarted composition
using A@7, including transitive consumers and affected ADR 0009 suffixes.
Preserve B's queue position in a dependency wait; A follows ADR 0007's existing
head-change dequeue rule. No automatic rebind to A@8 is permitted. If A@7
already has a confirmed landing still on its target base, a later revision
does not erase that historical satisfaction. A rewind which removes it does.

A dequeue of A similarly cascades **invalidation and waiting**, not new
dequeue commands for B and every other repo. Invalidated members return in
their original order. Cancel only discarded runs where the existing optional
capability and depth rules permit; depth-1 cancellation is not added here.
Late build/check results never revive a retired composition. Attempts already
landing settle first, then the remaining graph is reconciled to their actual
outcomes. No cascade can abandon an uncertain push or roll back a landed base.

The reverse-edge index is a rebuildable scheduling aid. Source events wake
known consumers, but missed wakes are repaired by polling and landing-start
revalidation. A restart rebuilds it before admitting dependency-bearing work;
absence from the cache is never evidence that there are no dependencies.
The existing per-installation budget reserves settlement ahead of graph reads;
cap traversal and back off failures without starving unrelated queues.

### Policy authority and cross-repository trust

**Each repository's governing default-branch configuration remains authoritative.**
B cannot import A's conditions, approvals, classifier labels, owners, authority
mode or bypass grants. A and B each pass their own exact-revision gates and
retain their own default-branch config commit and semantic digest as evidence.
No dependency declaration or topic can satisfy either repository's policy.

Stage A proposes a per-queue `dependencies` section with `mode: ordered`,
`allow_from` and `allow_to` lists of canonical repository URLs. Absence is off.
B's `allow_from` must name A; A's `allow_to` must name B. Both lists are resolved
against the served scope, never arbitrary HTTP hosts, and both queues must be
enabled. Empty lists allow no edges, and there are no wildcard or transitive
grants. Every traversed edge needs bilateral consent; changing either list
invalidates affected unstarted compositions. No keys are accepted before the
complete gate is active. Exact schema fixtures belong to implementation PR 1.

In `openmerge` authority mode these are policy files requiring the existing
governing-owner approval; the proposed change cannot weaken the policy judging
it. In forge authority mode existing governing-config controls remain in force.
Trust adoption is not inferred from common ownership or App visibility.
An untrusted fork in A still needs A's exact-head trust approval before any
test ref is built; B's trusted author cannot supply that grant by naming A.

Stage A does not inject A's unlanded code or credentials into B's CI. Stage B
would require B's explicit trust for executing each foreign revision with B's
CI privileges, as well as A's own trust and bilateral source access. Passing
A's tests, or being on an allowlist, is not that execution grant. Missing
capability or grant must fail closed, never fall back to building `main`.

Views and audit exports enforce their existing authorization boundaries.
An operator-wide API credential is not converted into per-user forge access.
B's public check must not disclose private A titles, bodies, checks or hidden
graph membership. Show only declaration text already visible to that audience
and a generic inaccessible-dependency reason where authorization is absent.
Keep full source IDs, observations and diagnostics in authorized operator views.

### Status and audit evidence

Reuse the `WaitingReason` projection introduced by the
[#158 work](https://github.com/CanadaApollo6/openmerge/pull/158), now present in
`internal/status/waiting.go`. Add presentation code `dependencies` with text
such as `Waiting for dependency acme/api#42 at revision 7`. `Details` identifies
the blocking pins and what clears them; `URL` links only to a safe, authorized
change view. Preserve terminal/settlement/paused precedence and expose this
gate before generic queue-position text. When B has its own higher-priority
gate, keep dependency details available in its full brief.

CLI, API and the existing OpenMerge check consume the same projection; add no
comment stream or second authoritative state machine. Bound and escape graph
summaries, state when more blockers exist, and keep full paginated evidence in
authorized reads. Status rendering never fetches mutable forge state.

Each dependency decision records:

- B's queue, change, revision, head, declaration and dependency-basis digests;
- every resolved edge, A's pinned revision/head and queue identity, alias binding
  source, observation event IDs, timestamps and queue watermarks;
- each repository's governing config commit/digest and bilateral trust result;
- A's confirmed attempt/settlement event, tested and landed commits, freshly
  observed base and ancestry result, or a typed blocking/unknown reason;
- B's batch/attempt and the atomic link to its own policy decisions and durable
  start, plus subsequent invalidations and settlements as separate history.

This is evidence of the facts OpenMerge observed, not proof of unbroken remote
protection or application compatibility. Audit export follows ADR 0008's source
selection and cross-range dependency rules, with explicit cross-repository
authorization. Missing old evidence is `not recorded`, never reconstructed from
today's PR state. Retention must preserve referenced source records; pruning
and compaction remain disabled. An export hash chain still needs an external
anchor to detect deliberate rewriting.

### Deferred Stage B: co-tested speculative landing

This ADR reserves the following contract and failure policy, **without enabling
Stage B or accepting its configuration**. A follow-up ADR must specify scheduling,
execution grants, manifest publication/receipts and acceptance evidence first.

For an initial two-repository chain A then B, create one immutable composition
manifest before CI. It lists the full dependency closure, canonical change pins,
per-repo base, queue ref, batch ID, tested and landing SHAs (including marker/head
distinction), policy digests, composition generation and manifest digest.
A queue ref is a fetch locator; the commit ID and manifest digest bind evidence.
All jobs for this composition consume the same pinned state.

OpenMerge does not run CI or set environment variables inside a forge push
workflow. A repository-owned bootstrap must obtain the authenticated manifest
for B's exact queue ref/tested SHA, verify its digest, and fetch A's advertised
queue ref with repository-scoped read credentials. It verifies fetched SHAs
and checks out A's **landing commit** for B's integration build; A's own CI
checks its tested commit. It must not fetch moving `main`, a contributor ref,
or assume that a gitlink/lockfile has already selected these inputs.

A possible bootstrap environment is `OPENMERGE_COMPOSITION_DIGEST` and
`OPENMERGE_COMPOSITION_FILE`; these are a proposed local adapter convention,
not variables current OpenMerge injects. Environment values alone
are not attestation. Required CI evidence must bind the manifest digest and
tested inputs to the exact accepted producer/run/attempt on B's queue SHA.
A provider unable to report verifiable consumption cannot enable Stage B.
Manifests contain no tokens; checkout access and grant revocation are separate.
The JSON/git contract is forge-neutral; authentication and CI receipt transport
are optional driver capabilities tested through ADR 0004's common contracts.

After both required CI/policy gates pass, land A and settle its fence. Recheck
B's policy and composition before its own durable start. A provisional bound
is **10 minutes from A's confirmed settlement to B's durable start**, with one
active cross-repository group per participating queue. Record the deadline once
in the journal; restart never renews it. This bounds permission to reuse a
co-tested result, not the time to finish an uncertain push or restore service.
Stage B's follow-up must validate this allowance and resource limits.

If B's start misses the deadline, policy/check evidence changes, or a component
base differs from the pinned composition's expected transition (A at its exact
settled landing SHA, B at its pinned predecessor), discard the unused
cross-repository result and return to ordered waiting/retesting against actual landed state.
Do not rewrite an existing speculative batch to substitute a new composition.
Within each repository ADR 0009's depth, prefix settlement, blame and suffix
invalidation still apply; cross-repo graph size is a separate bounded resource,
not multiplication of each queue's speculation window.

**If A lands and B fails, A stays landed.** Resolve any started B attempt through
B's fence; until settlement its outcome is unknown, not failed. Once settled,
record the actual landed subset and mark the group `partially-landed` if B did
not land. Invalidate downstream compositions, alert in authorized status, and
require an explicitly queued fix-forward or revert under the affected repo's
ordinary policy. No automatic force-push, inverse patch, or retry of a stale
landing. Revert is a new reviewed change and can itself fail. Topics cannot
eliminate the interval in which only A is visible; even this bounded-start plan
offers no bound on recovery and no distributed atomic commit.

### Journal, projections and rollback

**Use new event kinds; do not add fields to deployed event payloads.** Older
strict `DisallowUnknownFields` decoders reject even optional additions.
In particular leave `pr_observed`, `config_applied`, approval/brief records,
`landing_decision_recorded`, batch actions and `landing_started` byte-identical.
Dependency-specific evidence lives in side projections keyed by change/revision
and batch/attempt, not in a new meaning for an old event.

Stage A proposes additive wire-v1 kinds `dependency_policy_applied`,
`change_dependencies_declared`, `dependency_observed`,
`dependency_composition_recorded`, `dependency_compositions_invalidated` and
`dependency_decision_recorded`.
The policy companion pins the dependency policy digest alongside unchanged
`config_applied`; batches record their dependency basis in companions before
building. Each composition companion binds the exact batch/members, declaration
and policy digests and source observation IDs; a build or start with a missing
or stale companion is refused. Their folds invalidate outdated work without
extending existing payloads with fields an older decoder cannot read. Fixture
design may split a kind before its first release; never mutate one after deployment.

Declaration records include alias bindings, expected prior declaration basis
and complete normalized pins. Observations include the source revisions and
bases they read; a delayed success cannot clear a newer stale/unavailable
finding. Invalidation pins exact discarded composition IDs and cause. Decision
records pin the attempt and inputs atomically with the unchanged durable start.
Replays reconstruct both passing and waiting decisions without external reads.

No SQL migration in Stage A. Store new types in the existing journal and reuse
queue/event indexes, dedupe keys and bounded per-queue scans. Rebuild the reverse
graph from declared/observed facts and retain bounded active state in a newly
versioned snapshot codec, preserving old decoding and legacy cursor encoding
when no dependency facts exist. Do not activate general snapshot persistence,
retention or compaction. Measure scan cost on isolated historical/growth
fixtures before proposing indexes; adding one requires an amended Hold plan.

New kinds preserve **old bytes**, not old-binary readability of new history.
Drain all old workers/readers and verify a whole-database backup before first
emission or persistence of the new snapshot version. Removing dependency config
does not erase its compatibility boundary. Rollback uses a compatible binary,
or stops workers, retains later evidence, restores the tested pre-emission
backup and reconciles real remote refs and subsequent landings before resuming.
Never delete events, ignore unknown kinds, or blindly resume stale restored state.

### Rollout and implementation slices

This PR is **Inert: documentation only**. Acceptance of this ADR does not change
runtime behavior, public schema or the live installation. Stage A implementation
and activation deploy only **after ADR 0009 dogfood period B drains**, expected
mid-November 2026; the recorded drain, not a calendar date, is the gate. Follow
the [dogfood plan](../research/phase2-dogfood.md#plan): no queued entries, active
batches/chain or still-running queue-ref CI from that period, including discarded
runs. Do not change live App grants, trial CI or queue behavior during the pair.

PR numbers below are planned slices, not issue numbers. Inert components may
merge before the drain, but this ADR authorizes no feature binary deployment
during the pair. A binary containing Hold dependencies remains held even if
its last PR is docs or dormant code. Touching batching, landing or check
observation inherits Hold; dormant paths do not waive it.

| PR | Scope and exit tests | Class |
|---|---|---|
| 1 | Reference/parser, candidate policy schema and pure bounded graph evaluation; vectors for URI escaping, URL aliases, revision pins, duplicate/conflicting edges, cycles, limits, scope and bilateral grants; public loader still rejects keys | Inert |
| 2 | New journal kinds, side projections and snapshot codec; strict required/unknown-field tests, immutable fold, dedupe, stale observation/alias binding races, full replay versus snapshot-tail, old wire/cursor bytes, transactional start companions; no runtime emission | Inert |
| 3 | Scoped driver observations, graph reconciliation and reverse wakes; fake contracts plus HTTP fixtures for closure, merged flags, lost access, unserved refs, polling repair, partial reads and rate budgets; no production wiring | Inert |
| 4 | Ordered admission, batch/suffix invalidation and atomic landing gate; planner/simulator at depths 1 and 4, bisection/retry paths, concurrent cycle edits, lock-order races, source pushes/rewinds, declaration edits, crash/restart and both cancellation capabilities | Hold |
| 5 | Shared WaitingReason, CLI/API/check and audit evidence; golden waiting/unknown/redacted/paginated output, unchanged precedence, no rendering-time fetches, deterministic cross-repo export and authorization tests | Hold |
| 6 | Publish schema/loader and wire the complete gate together; mixed enabled/disabled repos, fail-closed first cycle, drain/backup/restore rehearsal, two-repo real-git and forge-sandbox acceptance, operating guide | Hold |

Every slice preserves byte-identical legacy wire fixtures and non-opted-in
digests. Differential replay compares next actions at **every revision**, and
seeded legacy simulations compare results at depths 1 and 4. Disabled queues
emit no dependency events, make no new dependency reads and retain legacy check
output. Until PR 6, candidate schemas and test wiring cannot accept unenforced
public config. Stage A exits only when all six slices and their evidence pass.

The real-git acceptance uses two bare repositories/caches: B waits with no CI,
A lands, B becomes testable and lands; a pushed A revision, source rewind,
lost permission and crash around B's durable start must produce the documented
wait or fenced outcome. A real forge sandbox verifies scoped URL resolution,
queue-ref CI and access boundaries without changing the live App. Retain binary
SHAs, configs/digests, commands/exits, source decision/start/settlement IDs and
actual tested/landed remote SHAs. Stage B requires a new release decision and
additional manifest-consumption and partial-landing acceptance, not PR 6's flag.

### Open questions for the owner

1. Confirm Stage A's same-installation, strict-only, cross-repository scope and
   bilateral `allow_from`/`allow_to` consent. Recommendation: keep it narrow;
   cross-forge federation needs authenticated remote evidence and tenant restore
   boundaries, not an arbitrary URL fetch.
2. Confirm the opaque canonical URI plus pinned forge-URL aliases, and the rule
   that source pushes require explicit repinning. Recommendation: accept these
   for safety; measure author friction before considering a tracking reference.
3. Confirm rejection of external/closed-unmerged/outside-queue dependencies and
   preservation of waiting dependents on dequeue. Recommendation: no manual
   “mark satisfied” override in the first slice; use ordinary corrective changes.
4. Are co-tested speculative groups worth owning CI bootstrap, foreign-code
   execution grants and partial-landing recovery? Recommendation: gather ordered
   wait/CI-cost data first. The 10-minute start allowance is provisional, and
   cannot promise compatibility or recovery time.
5. Confirm the initial graph limits and choose the first two-repo sandbox plus
   the owner of post-B drain/backup acceptance. Recommendation: keep the limits
   fixed for Stage A and record the actual drain evidence before deployment.

## Consequences

- Coordinated work becomes visible and ordered without making OpenMerge a forge
  or requiring PRs as permanent identity. Existing queues remain unchanged until
  explicit adoption; dependency-bearing waits avoid unnecessary CI.
- Authors must maintain pins and compatible intermediate states. Ordered landing
  costs serial CI latency and does not test an API/client pair together.
- Cross-queue reads, graph locks, retention and trust adoption add operational
  cost. Replayable observations explain why work waited or landed; unknowns stop
  only affected work rather than granting permission or stopping settlement.
- New journal history is a binary downgrade boundary even after disabling the
  feature. Stage B remains deferred; a topic, timeout or pair of successful
  tests cannot establish cross-repository atomicity.

## Alternatives considered

- **Shared topic as authority.** Convenient grouping, but mutable membership,
  naming collisions, hidden changes and no inherent order make it unsafe as
  consent to execute or land. Keep topics descriptive only.
- **Unpinned PR URLs following the latest head.** Familiar, but a push silently
  changes both the dependency and the work a reviewer thought was related.
  Accept URLs as pinned aliases and retain stable ADR 0007 identity.
- **Cross-repository speculative testing immediately.** It improves latency and
  integration coverage only when CI proves which foreign inputs it consumed.
  Stage A is useful without inventing that evidence or a distributed coordinator.
- **All-or-nothing topic landing or automatic rollback.** Independent repositories
  have independent push transactions and fences. Reverts are new work and do not
  undo side effects, builds or deployments.
- **Federate installations by reading remote merged flags.** Easy to prototype,
  but loses revision/policy/settlement evidence, trust isolation and durable
  recovery. Cross-system URLs in Zuul do not make OpenMerge federation free.
- **Add fields to existing events or a dependencies SQL table now.** New fields
  break strict older readers; a second authoritative store complicates replay
  and adds a migration. New event kinds and bounded projections fit ADR 0002.
