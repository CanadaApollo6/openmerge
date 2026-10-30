# 0011. Cross-repository dependent changes

- **Status:** Accepted
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

**Related:** agent provenance is accepted separately in ADR 0010. This ADR
does not design agent identity, session capture, attribution or provenance trust.
The CI provenance below identifies tested inputs, not who produced the code.

## Decision

### Scope and staged guarantees

**Ship ordered landing first, with revision-less change references by default.**
An optional `@revision` pin requires that exact revision to land. Stage B requires
revision pins because its CI consumes unlanded code. This default is an owner
decision, not an open question.

A dependency adds a gate; it never grants trust, approval, enqueue permission,
priority, or a right to write another repository.

| Stage | Guarantee | Release boundary |
|---|---|---|
| A: ordered | B is not tested or started landing until A has a confirmed OpenMerge landing on its target base, matching the revision if pinned | Implementation sequence below, after dogfood period B drains |
| B: co-tested speculation | B's CI consumes an identified speculative composition of A; A then B land through separate fences | Deferred until ordered landing has real usage; follow-up ADR must close the CI and coordination gates below |
| Topic submission | No all-or-nothing cross-repository guarantee | Not offered |

Stage A covers distinct repositories in **one serving scope**: the same
configured forge driver/origin, forge installation identity, and OpenMerge
journal namespace within one database. On GitHub, both repositories must also
belong to the same organisation. An App installation is not the same
thing as a server process. Several workers may cooperate through that database; merely being visible to
one server under ADR 0006 does not make separate installations mutually trusted.
This also respects ADR 0008's dedicated-database-per-installation authority
boundary. No cross-installation, cross-forge or cross-server federation ships.

Both endpoints must be served, explicitly opted in and use strict verification.
External dependencies in repositories OpenMerge does not serve are rejected.
There is no manual "mark satisfied" override.
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
openmerge-change:v1/<forge-instance>/<installation>/<journal-namespace>/<change-id>[@<revision>]
```

- `forge-instance` is one percent-encoded UTF-8 segment containing
  `<driver-kind>:<canonical-origin>`, for example
  `github%3Ahttps%3A%2F%2Fgithub.com`. The origin comes from configured driver
  identity, including a nondefault port when applicable, never from PR text.
- `installation` is the percent-encoded stable installation key already used
  by queue identity, not a database row ID or an OpenMerge process ID.
- `journal-namespace` is `omj1_` plus a domain-separated SHA-256 digest of the
  ADR 0008 receipt database identity and a journal incarnation token. The
  [existing identity](../operating.md#receipt-database-binding) binds the
  cluster `system_identifier`, database and schema names/OIDs, and journal
  genesis. The token is a random 256-bit value recorded once per incarnation
  by explicit dependency initialization under the database events lock, shared
  by workers. Use versioned JSON framing for the namespace digest inputs.
  Missing, unreadable or mismatched identity fails closed. Initialization and
  opted-in startup verify this binding; legacy startup gains no identity read.
- `change-id` is ADR 0007's ID, presently `chg1_` plus 64 lowercase hex digits.
  Omit `@revision` to accept any OpenMerge-landed revision of that change.
  An optional `revision` is its positive decimal observation sequence, without
  leading zeros. A SHA alone is not a revision: H1, H2, H1 are three revisions.
- The ID resolves to exactly one stored canonical queue key, including its
  repository, base and lane. Resolve within the named serving scope, verify
  that binding, and reject a foreign journal namespace before looking up an ID.
  Never reinterpret it in the local journal or fetch remote evidence. Reject
  ambiguity. Do not derive IDs from discovered case aliases, YAML queue names,
  mutable titles or PR numbers alone.
- Components use canonical percent encoding; reject malformed encodings,
  credentials, extra components, queries and fragments. There is no network
  dereference of this URI and no caller-selected OpenMerge endpoint.

A normal restart preserves the namespace. Every restore, physical clone,
database/schema recreation or journal reinitialization requires stopped and
drained workers, a new incarnation token and remote-ref reconciliation before
resuming dependency work. Old references, including restored declarations, are
foreign and require explicit redeclaration and fresh authorization; never remap
them automatically. Non-rewinding failover may retain the namespace only when
it continues the same exclusive journal. ADR 0008's identity alone cannot detect
a physical clone or history fork preserving genesis, so operators must fence
copies and rotate the token before either copy serves divergent history. This
is a recovery requirement, not automatic clone detection.

For illustration, `<journal>` and `<id>` stand for full valid values. The first
line is the recommended Stage A form; the second opts into an exact pin:

```text
Depends-On: openmerge-change:v1/github%3Ahttps%3A%2F%2Fgithub.com/123/<journal>/<id>
Depends-On: openmerge-change:v1/github%3Ahttps%3A%2F%2Fgithub.com/123/<journal>/<id>@7
```

The CLI/API read view supplies a copyable canonical reference. A recognized
HTTPS forge change URL, such as `https://github.com/acme/api/pull/42`, is also
accepted as an input alias for author convenience. Resolve it through the
configured driver and served-repository registry, bind its journal namespace
and change ID, and show the binding in the brief before it can authorize work.
A URL alias is revision-less in Stage A; use a canonical `@revision` for a pin.
The URL remains a display alias, never the stored identity. Ambiguous
installation/base/lane selection requires the canonical form. Bare `#42` and
`forge:owner/repo#42` are rejected: they omit instance and queue scope.

**An alias is bound once per declaration, not followed on every poll.** Its
change binding survives restart and an unrelated body edit. Source pushes do
not edit that binding or require B to repin a revision-less edge. An explicit
pin becomes stale if superseded before landing; changing it edits the declaration.
Removing and re-adding an alias in separately observed declarations is a new
declaration and visibly records a new binding. Declaration observations are
poll-bounded: coalesce semantic edits within a poll interval into one comparison
with the last recorded declaration, bounding declaration journal growth to one
observation per PR per poll. An edit which OpenMerge never observes cannot be
reconstructed as an intervening act. Coalescing retains the union of all
identified editors across the window, not just the final editor. Identify an
editor from the authenticated webhook sender, or the forge's edit history where
the driver exposes it. A polled PR body alone carries no editor identity; fail
closed when neither source establishes it.

Recognize exact, case-sensitive `Depends-On:` keys on standalone body lines,
with one non-whitespace reference value. Ignore lines inside fenced code,
indented code, block quotes and HTML comments. Normalize CRLF, sort and dedupe
resolved edges; reject malformed recognized lines rather than ignoring them.
Parser fixtures fix these boundaries. Initially bound each declaration to
32 direct edges and each transitive walk to 256 nodes, 512 edges and depth 16;
overflow is a visible blocking diagnostic, never a truncated permissive graph.

Record a domain-separated, versioned declaration digest over sorted canonical
edges, their revision-less or pinned mode, and pinned heads where applicable.
At binding, record a revision-less edge's observed source revision and intent
digest as a non-digest baseline, outside the declaration and dependency-basis
digests; the baseline stays fixed on source pushes. A revision-less edge's
observed source head also stays outside the declaration digest. B's brief and
dependency decision show when the landed revision differs: "landed revision N
differs from the revision observed at declaration", with both revisions and
intent digests. This is informational, not a new gate.
Use an exact pin when semantic continuity matters, for example if A may be split
and the eventual revision might omit the part B needs.
Record a separate dependency-basis digest over resolved evidence and decision inputs,
including the revision that actually landed. For opted-in changes, extend the
ADR 0007 brief digest with the declaration digest, leaving non-opted-in bytes
unchanged. Declaration edits retire B's unstarted compositions and invalidate
intent-bound receipts even at an unchanged head; they create no code revision.

A loosening declaration edit (removing, weakening or retargeting an edge,
including changing an exact pin) sets a durable `dependency-declaration-changed`
hold. Tightening edits (adding an edge or adding a pin) need no acknowledgement,
but cannot clear an existing hold. Mixed edits that loosen any edge set the hold.
An eligible B writer must explicitly acknowledge the current declaration: exclude
the union of all identified editors across the coalesced windows contributing
to the unacknowledged hold and, under
[ADR 0007](0007-changes-not-prs.md#policy-approvals-stage-a), B's author, submitter
and every accountable human. If any loosening edit's actor is unknown or
unauthenticated, accept no acknowledgement until an authenticated redeclaration
or fresh authenticated declaration edit establishes the current declaration's
actor. This restores attribution, not eligibility: retain the identified-editor
exclusions and the hold until an eligible writer acknowledges. Bind the fresh
action to the current digest and declaration event ID; re-read the writer's
identity-bound permission and exclusions.
Replaying an old action or restoring an earlier digest cannot clear the hold.
Native head-bound approvals alone never clear it or carry a removed edge.
B's ordinary review policy still applies after acknowledgement; old receipts
remain invalid. Unrelated body edits that preserve the declaration do not set
this hold. A future `@openmerge repin` convenience must use this authorization path.

**Single-maintainer self-acknowledgement requires explicit repository opt-in.**
The governing, owner-approved policy in B's repository may opt B's queue into
single-maintainer self-acknowledgement with `single_maintainer: true` inside its
existing per-queue `dependencies` section, alongside `mode`, `allow_from` and
`allow_to` below. Only the setting for B's queue in B's repository governs B's
acknowledgement; A's setting has no effect. Read the
opt-in from the default-branch governing config at acknowledgement time, never
from B's head or a proposed change. Omitted or false keeps the strict rule above;
never infer it from writer count, common ownership, or lack of an eligible
independent acknowledger.

Only in an opted-in repository may an otherwise-excluded B writer acknowledge a
loosening edit, including their own. They must still be authenticated, identified,
and freshly authorized to write B. The unknown/unauthenticated-editor rule above
still blocks every acknowledgement until attribution is restored. Bind a
self-acknowledgement to the current declaration event/digest, governing policy
commit/digest, writer grant, and the editor/author/submitter/accountable-human
exclusions it overrides. Record it as a distinct
`dependency_declaration_self_acknowledged` event, visible as self-acknowledgement
in authorized status and audit export. It clears only the declaration hold;
B's ordinary approvals, owner review, trust, CI, and dependency gates still apply.
In particular it grants no self-approval exception under ADR 0007.

Enabling the opt-in is itself a policy change. In `openmerge` authority mode,
ADR 0008 requires governing-owner approval under the existing policy. Otherwise
it goes through normal governing-policy review. Include the setting in the
affected queue's dependency policy digest; toggling it retires that queue's
unstarted compositions and any dependent compositions whose basis is invalidated,
without needlessly retiring unrelated queues. A proposed edit cannot authorize
itself.
If recorded permission observations show more than one eligible repository
writer, warn that the single-maintainer opt-in is enabled in a multi-writer
repository, displaying the warning on B's declaration hold/status itself, not
only in repository diagnostics. Count authenticated identities with write
permission before the change-specific exclusions. The warning is nonblocking:
self-acknowledgement remains permitted and distinctly audited even when an
independent eligible writer is available. It neither revokes nor enables the
explicit opt-in.

Without this opt-in, if no eligible writer exists, the hold blocks indefinitely;
this is intentional and fail-closed. Recovery requires adding an eligible writer
or a replacement change. A replacement PR is a new source alias with a fresh
declaration, not a continuation of the hold; where approval policy applies, it still requires
independent approval.

Key the hold and expected prior declaration basis by B's stable forge source
alias (forge instance, installation, stable forge repository ID and PR number),
across all its queue-owned changes, rather than by change ID. Use the repository
ID, not its name, so the key survives repository rename and transfer. Retargeting
B to another enrolled base must carry that history and hold into the new change,
never initialize an empty prior declaration. The hold survives final-edge
removal, disabling dependency policy, dequeue and re-request. Auto-admission from a held request
and re-queue commands never clear it; only the explicit acknowledgement does.
[#163](https://github.com/CanadaApollo6/openmerge/pull/163) landed on main as
`1c78bcc`; its held-request admission in `internal/queue/held_requests.go` is
separate from this declaration hold. Preserve its exact-head admission rules as
specified in the [operating guide](../operating.md#queue-requests-with-pending-entry-conditions).

A shared topic label may later group a graph for navigation. It declares no
edge, enqueues nothing and carries no landing or approval authority. There is
no topic expansion that discovers additional executable work implicitly.

### Stage A: ordered landing

For the default `B depends on A`, A must be queued by someone authorized in A,
pass A's own gates, and have a confirmed, settled OpenMerge landing. Record the
landed revision and head as evidence. For `B depends on A@7`, only A@7 satisfies
the edge. In either case, check that the recorded landing commit remains
reachable from the freshly observed target base. With fast-forward, distinguish
the tested marker from the head that landed. A PR's closed/merged flag, a matching
head SHA, or somebody's assertion that the patch was cherry-picked is insufficient.

Only then may B enter a test composition under B's normal strict queue rules.
All declared dependencies must satisfy this rule. Unrelated eligible changes
can pass a waiting B without losing their relative order. Waiting does not
consume a batch slot, trigger bisection, or count as a defective change.
Suspend B's conflict-grace and `max_wait` clocks during dependency waits; resume
with their remaining eligible time. Compute batch `max_wait` only from eligible
candidates. A wait never dequeues B merely because either clock would expire.
Expose a non-actionable "dependency wait older than N" flag in authorized
status for operator review; it grants no bypass and changes no queue order.
Unchanged dependency inputs cause zero extra builds or tests.

Stage A guarantees **landing order**, not integration correctness across repos.
B's CI may still build an old released client dependency, and A's landing may
break older clients before B lands. CI must fetch suitable committed inputs,
and authors must use expand/contract changes, compatibility shims or release
coordination where intermediate states matter. Neither queue order nor this
ADR deploys a service, publishes a package, or bounds that compatibility window.

Before B's durable landing start, re-read the relevant declarations, revisions,
source landing/base evidence, lifecycle and bilateral policy, including B's
governing single-maintainer opt-in. If it was revoked after self-acknowledgement
and before landing, that self-acknowledgement no longer clears the declaration
hold: B needs an eligible independent acknowledgement under the current policy.
This keeps policy current and fails closed. Record these observations; the pure
planner consumes recorded facts, not forge calls.
Revalidate inside the landing-start transaction. Only at landing-start, acquire
queue locks over B plus its directly landed sources, in canonical key order
after existing tenancy/global locks. For a batch, use the union of those sets.
These locks protect landing, not declaration edits or cycle detection. Never
acquire source locks ad hoc while already holding B's queue lock. Compare the
recorded queue watermarks/bases with the current fold and retry on drift.
No forge I/O occurs while these locks are held. A changed direct-source set
restarts validation with a new lock set.

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
Use these rules before a landing has started. Typed reasons below are visible
only to audiences authorized to read A; all other audiences receive the same
generic dependency reason defined under cross-repository trust.

| Observation | Result and recovery |
|---|---|
| A is open, valid and not yet landed | B waits; A is neither auto-enqueued nor promoted |
| A was dequeued, rejected by policy, or failed CI | B waits for A's recovery; B is not blamed for A's failure |
| A was retargeted away, orphaning the bound old change ID | Block with `dependency-source-retargeted`; explicitly redeclare against the new change, never report ordinary dependency waiting |
| A closed without landing | Block B with `dependency-closed`; reopen A or edit the declaration, retaining any explicit pin |
| A is marked merged but has no OpenMerge landing evidence | Block with `dependency-unverified`; an outside-push acknowledgement is not approval or landing evidence |
| Unknown ID/revision, malformed or ambiguous alias | Block with `dependency-unresolved`; fix the reference |
| Timeout, rate limit, permission loss or unreadable source | Block with `dependency-unavailable`; retry with bounded backoff and normal polling |
| Foreign journal namespace, unserved/removed repo, unsupported mode or another serving scope | Block with `dependency-out-of-scope`; serving/trust adoption or an explicit declaration edit is required |
| A's explicitly pinned revision was superseded before landing | Block with `dependency-stale`; repin explicitly and revalidate |
| A's confirmed landing is absent from the current source base | Block with `dependency-not-on-base`; repair/reconcile source history or declare replacement work |
| Declaration loosened, including removal of the final edge | Block with `dependency-declaration-changed`; require explicit eligible non-editor B writer acknowledgement with ADR 0007 exclusions, or audited self-acknowledgement under the explicit single-maintainer opt-in; unresolved edit attribution blocks both |
| Cycle, self-edge, conflicting modes/pins for one change, or traversal limit | Block the affected graph with a diagnostic; edit declarations, never choose an arbitrary winner |

Cycle detection is a derived projection and diagnostic over journal-scoped
change IDs, not a serialized declaration-admission protocol. Concurrent edits
may form a cycle; reconciliation reports a deterministic path and keeps its
unlanded members waiting. No member can land while a direct source is unlanded.
Incomplete discovery is unknown, not proof of acyclicity. Work downstream of an
invalid component waits; unrelated components and queues continue.

A confirmed matching landing is a terminal node for dependency traversal:
its own historical dependency decision is evidence, not a request to follow
the change's latest declaration. Recheck that landing's presence and current
direct-edge consent, without allowing later source edits to replace its pins.

In Stage A, a push from unlanded A@7 to A@8 leaves a revision-less edge waiting
for any qualifying landing. An explicit A@7 edge waits with `dependency-stale`
until the declaration is edited; no automatic repin is permitted. B has no
composition consuming unlanded A@7 to invalidate. A follows ADR 0007's existing
head-change dequeue rule. A later revision does not erase an already confirmed
landing still on its target base.

A source-base rewind removing the satisfying landing retires B's unstarted
compositions and affected ADR 0009 suffixes, and returns B to dependency waiting.
A dequeue of an unlanded A keeps B waiting without cascading dequeue commands.
Declaration or policy edits also retire compositions whose dependency basis is
no longer valid. Preserve original queue order. Cancel only discarded runs where
the existing optional capability and depth rules permit; depth-1 cancellation
is not added here. Stage B additionally requires transitive invalidation when
an unlanded revision consumed by speculative CI changes, as reserved below.
Late build/check results never revive a retired composition. Attempts already
landing settle first, then the remaining graph is reconciled to their actual
outcomes. No cascade can abandon an uncertain push or roll back a landed base.

The reverse-edge index is a rebuildable scheduling aid. Source events wake
known consumers, but missed wakes are repaired by polling and landing-start
revalidation. A restart rebuilds it before admitting dependency-bearing work;
absence from the cache is never evidence that there are no dependencies.
Read dependency graphs only for enqueued changes after an authorized writer's
queue command; do not scan every PR body or let arbitrary contributors trigger
graph discovery. Sources may be unqueued, but graph reads never enqueue them.
Retain at most 32 direct edges per change and the bounded walks above. Charge
permission and graph reads to ADR 0006's per-installation budget, preserving its
10% settlement reserve; back off failures without starving unrelated queues.

### Policy authority and cross-repository trust

**Each repository's governing default-branch configuration remains authoritative.**
B cannot import A's conditions, approvals, classifier labels, owners, authority
mode or bypass grants. A and B each pass their own exact-revision gates and
retain their own default-branch config commit and semantic digest as evidence.
No dependency declaration or topic can satisfy either repository's policy.

Stage A defines a per-queue `dependencies` section with `mode: ordered`,
`allow_from` and `allow_to` lists of canonical repository URLs, and the optional
`single_maintainer` boolean described above (default false). Absence is off.
B's `allow_from` must name A; A's `allow_to` must name B. Both lists are resolved
against the served scope, never arbitrary HTTP hosts, and both queues must be
enabled. Empty lists allow no edges, and there are no wildcard or transitive
grants. Every traversed edge needs bilateral consent; changing either list
invalidates affected unstarted compositions. No dependency keys, including
`single_maintainer`, are accepted by the public loader before the complete gate
is active; candidate schema/test support cannot enable self-acknowledgement early.
Exact schema fixtures belong to implementation PR 1.

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

At declaration binding or explicit pin time, require the declarer's read
authorization on A before reading its lifecycle or expanding its graph. An
unknown edit actor cannot bind an edge; require authenticated redeclaration.
Reuse the driver's fresh per-user permission read, binding the returned stable
user ID to the actor, as ADR 0007 does for `WriteGrant`.
Add a read-level evaluator; `CanWrite` is not a read-access test. Unknown,
mismatched, denied or unreadable grants fail closed. Record actor, grant and
observation basis. A service/App credential or a B writer's access cannot stand
in for the declarer's. Recheck access before dependency work and landing-start;
revocation blocks affected work rather than reusing a stale grant.

Views and audit exports enforce their existing authorization boundaries.
For any audience lacking read access to A, use one generic public reason,
`Dependency cannot be verified`, for unknown, closed, superseded, landed,
unavailable and out-of-scope sources. Do not expose typed codes, source evidence,
status transitions or a success signal about A in that view. A public check must
use this redaction if any of its audience lacks access. Already-visible
declaration text is not permission to disclose more. Typed reasons, cycle paths,
source IDs and full diagnostics appear only in authorized views; rendering uses
recorded audience authorization and fails closed when it is unknown.

**Accepted residual, 2026-10-30 ([#203](https://github.com/CanadaApollo6/openmerge/issues/203)).**
Redaction hides A's state, not B's own CI. In Stage A, B is tested only after
A has landed. If B's own checks then fail, the generic `checks-failed` dequeue
shows an audience without access to A that B was tested, and therefore that A
had landed. This is accepted only when B's CI is visible to that same audience;
for example, repository readers can see B's workflow runs. It is not accepted
for an audience that cannot see B's CI. The dequeue names no source and reveals
no private observation newer than the one that let B be tested. The metamorphic
privacy tests exclude only this exit, B's own `checks-failed`, from byte
comparison; any other queue exit caused by private state fails them.

### Status and audit evidence

Reuse the `WaitingReason` projection introduced by the
[#158 work](https://github.com/CanadaApollo6/openmerge/pull/158), now present in
`internal/status/waiting.go`. Add presentation code `dependencies` with text
such as `Waiting for dependency acme/api#42`, adding `at revision 7` only for
an explicit pin. Apply the audience redaction above before exposing any details.
`Details` identifies blocking edges and what clears them; `URL` links only to a
safe, authorized change view. For a declaration hold, status and `Details` name
the required acknowledger class: a B writer who is neither an editor nor B's
author, submitter or accountable human. Without the single-maintainer opt-in,
if none is eligible, state that the hold blocks indefinitely and give the recovery
above. If attribution is unresolved, require authenticated redeclaration or a fresh authenticated declaration edit
before acknowledgement. In a repository with `dependencies.single_maintainer: true`,
also show the authenticated writer self-acknowledgement option and any
nonblocking multi-writer warning on the hold/status itself. Show a completed
self-acknowledgement distinctly, including its actor and event reference, even
after the hold clears. Repositories
without the opt-in keep the strict acknowledger class unchanged.
Preserve terminal/settlement/paused precedence.
#163's `Waiting for queue entry conditions` takes precedence over dependency
waiting while entry conditions are pending, including before auto-admission;
once admitted, show the dependency reason before generic queue-position text.
When B has its own higher-priority gate, keep dependency details (including any
declaration hold) available in its authorized full brief.

CLI, API and the existing OpenMerge check consume the same projection; add no
comment stream or second authoritative state machine. Bound and escape graph
summaries, state when more blockers exist, and keep full paginated evidence in
authorized reads. Status rendering never fetches mutable forge state.

Each dependency decision records:

- the journal namespace and B's queue, change, revision, head, declaration event
  ID, digests, stable forge source alias and any eligible writer acknowledgement
  clearing its declaration hold, including the exclusion/permission evidence.
  A self-acknowledgement is explicitly identified with its distinct event kind,
  actor, overridden exclusions, and governing single-maintainer opt-in evidence;
- every resolved edge and mode, optional pin, actual landed revision/head and
  queue identity, alias binding source, revision-less binding baseline and its
  informational comparison to the landed revision, declarer's read grant,
  observation event IDs, timestamps and queue watermarks;
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
Stage B or accepting its configuration**. Stage B is deferred until ordered
landing has real usage and wait/CI-cost evidence. A follow-up ADR must specify
scheduling, execution grants, manifest publication/receipts and acceptance
evidence first.

For an initial two-repository chain A then B, create one immutable composition
manifest before CI. It lists the full dependency closure, canonical change pins,
per-repo base, queue ref, batch ID, tested and landing SHAs (including marker/head
distinction), policy digests, composition generation and manifest digest.
A queue ref is a fetch locator; the commit ID and manifest digest bind evidence.
All jobs for this composition consume the same pinned state. Revision-less
Stage A declarations must resolve to explicit authorized revision pins before
Stage B can consume unlanded inputs; Stage A's default is not a co-testing pin.

A push superseding unlanded A@7 with A@8 invalidates all unstarted Stage B
compositions consuming A@7, including transitive consumers and ADR 0009 suffixes.
Preserve queue positions while waiting for explicit repinning and fresh
composition evidence. A dequeue, declaration change or invalidated source
composition similarly retires affected speculative consumers. No late result
revives discarded work, and attempts already landing settle through their fences.

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
This ten-minute allowance remains provisional and unvalidated. Stage B's
follow-up must validate it and the resource limits.

If B's start misses the deadline, policy/check evidence changes, or a component
base differs from the pinned composition's expected transition (A at its exact
settled landing SHA, B at its pinned predecessor), discard the unused
cross-repository result and return to ordered waiting/retesting against actual
landed state.
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
and batch/attempt, with declaration history/holds keyed by stable forge source
alias across queue-owned changes, not in a new meaning for an old event.

Stage A proposes additive wire-v1 kinds `dependency_journal_initialized`,
`dependency_declaration_acknowledged`,
`dependency_declaration_self_acknowledged`, `dependency_policy_applied`,
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

Declaration records include the actor/read grant, alias bindings and revision-less
non-digest baselines, source-alias-keyed expected prior declaration basis, complete
normalized edges and the durable loosening hold, including an empty declaration
and excluded editing actors, retaining their union across coalesced windows and
any unresolved unknown or unauthenticated loosening actor. Acknowledgements bind
the current declaration event/digest, fresh writer grant and ADR 0007 exclusion
evidence. Self-acknowledgements use their distinct event kind and also bind the
repository's governing single-maintainer opt-in and overridden exclusions. Replay
must reject self-acknowledgement without that policy evidence or with unresolved
unknown/unauthenticated editing actors. Status and scoped audit exports preserve
the distinct event kind and evidence rather than presenting it as independent
acknowledgement. Journal initialization records bind the receipt database
identity and incarnation token; recovery rotates the token before new work.
Observations include the source revisions and bases they read; a delayed success
cannot clear a newer stale/unavailable finding. Invalidation pins exact discarded
composition IDs and cause. Decision records pin the attempt and inputs atomically
with the unchanged durable start.
Replays reconstruct both passing and waiting decisions without external reads.

No SQL migration in Stage A. Store new types in the existing journal and reuse
queue/event indexes, dedupe keys and bounded per-queue scans. Rebuild the reverse
graph from declared/observed facts and retain bounded active state in a newly
versioned snapshot codec, preserving old decoding and legacy cursor encoding
when no dependency facts exist. Do not activate general snapshot persistence,
retention or compaction. Coordinate the snapshot version bump with #163's
`HeldRequests` and cached `PendingConditions`: preserve their replay/round-trip
semantics and test combined held-request/declaration-hold snapshots. Its first
persisted `pending_conditions` field on `pr_observed` or held-request snapshot
already crosses a rollback boundary, even without a queue command. Dependency
rollback checks must include that boundary as well as new kinds/codec versions;
no version bump or cleared hold makes those fields old-reader compatible.
Measure scan cost on isolated historical/growth fixtures before proposing indexes;
adding one requires an amended Hold plan.

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
runs. Riel St. Amand owns post-period-B drain and whole-database backup acceptance,
including the recorded evidence before deployment. Do not change live App grants,
trial CI or queue behavior during the pair.

PR numbers below are planned slices, not issue numbers. Inert components may
merge before the drain, but this ADR authorizes no feature binary deployment
during the pair. A binary containing Hold dependencies remains held even if
its last PR is docs or dormant code. Touching batching, landing or check
observation inherits Hold; dormant paths do not waive it.

| PR | Scope and exit tests | Class |
|---|---|---|
| 1 | Reference/parser, candidate policy schema including the queue-scoped single-maintainer opt-in and pure bounded graph evaluation; canonical escaping, URI/journal scope, revision-less aliases and optional pins, dedupe/conflicting modes, cycle diagnostics, limits and bilateral grants; public loader still rejects keys | Inert |
| 2 | Journal namespace/incarnation, loosening holds/eligible acknowledgements keyed by forge source alias, side projections and snapshot codec; required/unknown-field checks, immutable folds, dedupe, alias/observation races, poll coalescing, revision-less binding baselines, replay versus snapshot-tail (including #163 held requests), unchanged wire/cursor bytes; divergent histories with identical queue/change IDs and revision numbers, restore/clone/reinitialization rejection; distinct audited self-acknowledgement event/replay with governing opt-in evidence, opt-in acceptance and rejection without it (including multi-writer repositories), head-only opt-in refusal, multi-writer self-acknowledgement and opt-in revocation fixtures below, unknown/unauthenticated-editor rejection even with opt-in, and retarget-to-enrolled-base fixture preserving the prior declaration/hold under a new change ID; no runtime emission | Inert |
| 3 | Scoped observations, identity-bound declarer read grants, reconciliation and reverse wakes; fake contracts and HTTP fixtures for denial, ID mismatch/revocation, closure, source retargeting, merged flags, unserved refs, polling repair, partial reads, writer-command admission and ADR 0006 budgets; no production wiring | Inert |
| 4a | Planner gate, decision companion and landing-start transaction; planner/simulator depths 1/4, bisection/retries, direct-source lock races, concurrent cycle diagnostics, declaration holds with writer-author/editor exclusion by default, explicit single-maintainer self-acknowledgement and policy-change approval, head-only opt-in refusal, multi-writer self-acknowledgement and landing-start opt-in revocation fixtures below, unchanged strict behavior for multi-writer repositories without opt-in, unresolved attribution blocking both paths, tightening without acknowledgement, held-request auto-admission and dequeue/re-request preserving holds, crash atomicity and dependency wait clocks | Hold |
| 4b | Stage A composition invalidation and reverse cascades; source rewinds, declaration/policy edits, suffix order, restart, late results and both cancellation capabilities; unlanded pushes only stale explicit pins or preserve revision-less waits | Hold |
| 5 | Shared WaitingReason, CLI/API/check and audit evidence, distinct self-acknowledgement status/export and nonblocking multi-writer warning on the hold/status itself for opted-in repositories; golden tests make private-source existence, closure, supersession and landing indistinguishable to unauthorized B audiences, with typed diagnostics only for authorized readers; wait-age flag, pagination, unchanged precedence, no rendering-time fetches and scoped export | Hold |
| 6 | Publish schema/loader and wire the complete gate together; mixed enabled/disabled repos, fail-closed first cycle, drain/backup/restore rehearsal, two-repo real-git and forge-sandbox acceptance, operating guide; Stage B remains deferred until real ordered-landing usage and a follow-up ADR | Hold |

Every slice preserves byte-identical legacy wire fixtures and non-opted-in
digests. Differential replay compares next actions at **every revision**, and
seeded legacy simulations compare results at depths 1 and 4. Queues with no
dependency adoption or history emit no dependency events, make no new dependency
reads and retain legacy check output. Disabling adoption cannot clear a retained
declaration hold or waive an existing edge. Until PR 6, candidate schemas and
test wiring cannot accept unenforced public config. Stage A exits only when every
slice and its evidence passes.

The real-git acceptance uses two bare repositories/caches: B waits with no CI,
A lands, B becomes testable and lands. Review pushes to unlanded A keep a
revision-less B waiting without declaration edits or re-approval; an explicit
A@7 waits stale after A@8 and never accepts A@8's landing. Record the actual
landed revision. At unchanged B head, remove its last edge, weaken a pin or
retarget A with only pre-edit native approvals: without single-maintainer opt-in,
B remains held until a fresh eligible writer acknowledgement, excluding the
editor, author, submitter and accountable humans. PR 2 and 4a cover a
writer-author's B with a surviving native approval: they remove `Depends-On` at the same head and attempt self-acknowledgement;
B without the single-maintainer opt-in cannot land before A through that bypass.
Repeat in a multi-writer repository without opt-in to prove the strict rule is
unaffected. In an opted-in repository, the same authenticated writer can clear
the hold only through the distinct audited self-acknowledgement event, with
normal review/CI gates still enforced. Unknown or unauthenticated loosening
actors block even with opt-in. PR 2 and 4a add these fixtures:

- B's head enables the opt-in and drops its edge, but the governing default-branch
  config has no opt-in: self-acknowledgement is refused.
- Opted-in, independent writer available, self-ack still audited and distinct:
  self-acknowledgement clears the hold and the nonblocking multi-writer warning
  appears on the hold/status itself.
- Opt-in revoked after self-acknowledgement but before landing: retain the audited
  event, refuse to clear the hold at landing-start, and require an eligible
  independent acknowledgement.

PR 5 verifies distinct status and export evidence and the nonblocking warning on
the hold/status when an opted-in repository has multiple writers.
An eligible independent writer can acknowledge the removal, subject to B's
ordinary review policy. An old acknowledgement cannot clear a later edit, including a return to the same digest.
PR 2 also retargets B to another enrolled base after loosening: the new change ID
retains the source-alias-keyed prior declaration and hold. Tightening alone needs
no acknowledgement, and tightening after loosening does not clear its hold.
PR 2 and 4a also cover multiple editors in one coalesced window, unknown or
unauthenticated loosening actors blocking every acknowledgement until attribution
is restored, and no eligible writer blocking indefinitely without opt-in. Verify
recovery by
adding an eligible writer or a replacement PR with a fresh alias/declaration and
independent approval where required; repository rename/transfer preserves the
original alias's hold.
Source rewind, lost access and crash around B's durable start produce the
documented wait or fenced outcome.
A second database with identical queue/change IDs and A@7 at a different head
must reject the first database's reference; repeat after restore/reinitialization
and physical-clone token rotation. Test dependency waits across conflict-grace
and `max_wait` expiry, with no dequeue or rebuild. A real forge sandbox verifies
scoped URL resolution, queue-ref CI and access boundaries without changing the
live App. Retain binary SHAs, configs/digests, commands/exits, source
decision/start/settlement IDs and
actual tested/landed remote SHAs. Stage B requires a new release decision and
additional manifest-consumption and partial-landing acceptance, not PR 6's flag.

### Owner decisions (2026-10-27)

Riel St. Amand (`CanadaApollo6`) accepted all recommendations on 2026-10-27:

1. Confirm narrow Stage A: distinct repositories in a single installation and
   the same GitHub organisation, strict verification only, with explicit bilateral
   `allow_from`/`allow_to` consent. No federation or implicit adoption.
2. Accept the canonical URI spelling and restore/incarnation procedure above.
   Rotate the token after restore, clone, or reinitialization, reconcile remote
   refs, and require explicit redeclaration and fresh authorization of old references.
3. Reject external dependencies in repositories OpenMerge does not serve.
   Closed-unmerged and outside-queue sources cannot satisfy an edge, and dequeue
   preserves waiting dependents. There is no manual "mark satisfied" override;
   use ordinary corrective changes or an authorized declaration edit.
4. Defer Stage B co-tested speculative landing until ordered landing has real
   usage. A follow-up ADR and release decision must close its execution, CI,
   coordination, and partial-landing gates. The ten-minute start allowance remains
   provisional and unvalidated.
5. Accept 32 direct edges per declaration and transitive limits of 256 nodes,
   512 edges, and depth 16. Accept the two-repository real-git and isolated
   forge-sandbox selection above. Riel St. Amand owns post-period-B drain and
   whole-database backup acceptance; record that evidence before deployment.
6. Accept explicit per-repository single-maintainer self-acknowledgement through
   governing per-queue `dependencies: {single_maintainer: true}` as specified in the
   Decision; B's repository alone governs B's acknowledgement.
   Only this opt-in permits an otherwise-excluded authenticated, identified writer
   to acknowledge a loosening edit. Unknown/unauthenticated edit attribution still
   blocks. Record a distinct `dependency_declaration_self_acknowledged` event,
   visible in status and audit export, with the opt-in and exclusion evidence.
   Enabling it is a policy change requiring ADR 0008 governing-owner approval in
   authority mode or normal policy review otherwise. It never applies implicitly
   or waives ordinary approvals. Show a nonblocking warning on the hold/status
   itself when an opted-in repository has multiple eligible writers;
   self-acknowledgement stays permitted and distinctly audited. Re-check the
   governing opt-in at landing-start; revocation after self-acknowledgement
   restores the hold pending eligible independent acknowledgement.
   Repositories without it retain the strict rule unchanged.

## Consequences

- Coordinated work becomes visible and ordered without making OpenMerge a forge
  or requiring PRs as permanent identity. Existing queues remain unchanged until
  explicit adoption; dependency-bearing waits avoid unnecessary CI.
- Stage A requires a single installation and the same organisation on GitHub.
  It does not coordinate separate installations even on one server.
- Authors maintain compatible intermediate states and any optional exact pins.
  Revision-less Stage A edges survive source review pushes; loosening edits need
  an eligible non-editor writer acknowledgement with ADR 0007 exclusions by
  default. An explicit, reviewed single-maintainer opt-in permits an authenticated
  writer's audited self-acknowledgement, sacrificing that independent check on
  dependency loosening. Unknown/unauthenticated edit attribution still blocks it;
  ordinary approvals are never waived. Multi-writer repositories without opt-in
  keep the strict rule; opted-in ones receive a nonblocking warning on the
  hold/status itself. In `github` mode this key has the same protection as any
  other governing-config key; `openmerge` authority mode adds ADR 0008 owner
  approval.
  Ordered landing costs serial CI latency and does not test an API/client pair
  together.
- Cross-queue reads, landing-start locks, retention and trust adoption add
  operational cost. Restores require a new namespace and fresh declarations.
  Replayable observations explain why work waited or landed; unknowns stop only
  affected work rather than granting permission or stopping settlement.
- New journal history is a binary downgrade boundary even after disabling the
  feature. Riel St. Amand owns post-period-B drain and backup acceptance. Stage B
  remains deferred until ordered landing has real usage, and its ten-minute
  allowance is provisional and unvalidated. A topic, timeout or pair of successful
  tests cannot establish cross-repository atomicity.

## Alternatives considered

- **Shared topic as authority.** Convenient grouping, but mutable membership,
  naming collisions, hidden changes and no inherent order make it unsafe as
  consent to execute or land. Keep topics descriptive only.
- **Mandatory exact pins in Stage A.** Rejected as the default because each
  review push would require editing and reauthorizing B before A has landed.
  The chosen middle option binds a revision-less reference to one journal-scoped
  change and requires an actual OpenMerge landing. It is safe for Stage A's
  ordering guarantee because B consumes no unlanded A code, and the landed
  revision is recorded. Exact pins remain optional and become mandatory for
  Stage B's identified speculative inputs.
- **Unpinned PR URLs following the latest head.** Rejected as moving checkout
  inputs or proof of satisfaction. Stage A URL aliases bind once to a durable
  change, never follow a new queue identity or treat the latest head as landed.
  Declaration edits still require fresh authorization.
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
