# Dormant approval evidence (Stage A 6)

The four additive wire-v1 observations have no production producer. `serve`,
executor, planner, conditions, configuration and landing are unchanged.
`approvalFacts` stays nil for legacy queues. Snapshot v5 retains sources,
first-observation attribution and bounds; v2–v4 decode with absent facts.
Compaction remains disconnected.

## Acquisition and attribution

`ApprovalBasis` identifies the change, revision/head and acquisition read.
11a must journal a revision boundary before its observations. Producers call
`CheckApprovalBoundary` to get a typed `ApprovalBoundaryError` before writing;
`Apply` retains its existing no-error signature and silently drops unbound
observations. 11a must call the typed preflight before journaling.

Accountability receives resolved stable-ID agent mappings and their governing
**policy digest**. Mapping precedes forge account kind. Actor, author, submitter
and all accountable humans remain separate. Accountability grants no permission
or fork trust; git names/emails are not identities. Evaluation rejects a policy
digest mismatch, including after an agent-mapping edit. Eligibility Actors must
carry effective account kinds after the same mapping. GitHub IDs use canonical
`classification.GitHubUserID` spellings.

Intent normalizes CRLF/CR to LF and trims surrounding whitespace. Its digest is
SHA-256 over Go JSON `["openmerge/intent/v1", title, body]`. Evaluation requires
current revision/head intent evidence matching `Current.IntentDigest`. Native
stances always have null receipts; intent/brief bindings cannot be inferred.

Source keys separate native reviews, dismissals of a review, and commands.
Timeline enrichment joins the dismissal key, retaining the **first** observed
bound, original submission evidence and provenance. Re-observation cannot renew
attribution, time evidence or bounds. ReadID is audit metadata only.

Native first discoveries on a novel head do not need to postdate local polling.
The approval projection retains the set of prior heads, starting with its first
approval observation; revision records stay byte-identical for legacy journals. On H1 → H2 → H1, first
observation requires a submission time strictly after `boundary.At + 3s`
(second timestamp precision plus the Q4 two-second clock allowance). Missing or
ambiguous proof attributes APPROVALS and dismissals to revision zero, never
authorization. A matching-head REQUEST is conservatively attributed to the
current revision even within the three-second/pre-boundary ambiguity window. A pre-history
snapshot with revision >1 and no prior-head set also requires proof; this
unknown history stays unknown through subsequent boundaries until full replay. Previously
observed barriers and command sources keep their attributed revision through
head reuse. Older native approvals may be pruned at revision advancement.

A dismissal retains `OriginalSubmission` and the reviewed commit separately.
Those prove attribution of the **original review**, never the dismissal action's
time. A known original's pinned revision is authoritative, regardless of source
arrival order; a cross-boundary promotion also adopts the original attribution
basis/revision, retaining the dismissal action time and bounds. An unseen original
uses the same head/epoch proof as a review.
Dismissal time always comes from PR 3b's dismissal DTO, including fallback on
revision 1 and later revisions. An old review dismissed today cannot erase a
current head-epoch request. The additive wire fixtures pin shared forge/policy
field names (including their existing exported-name JSON spelling).

## Pure set fold and recovery

Every new source recomputes the changed user's effective stance from its
attributed source **set**, not incremental Latest/frontier state. Derived indices
are caches only. Both encoding and decoding recompute them from Sources and the
current revision and reject any mismatch; a forged Latest cannot hide a retained
request. Encoding also validates attribution before returning persistable bytes. Arrival order, read grouping, duplicate polls and snapshot/tail
splits cannot select different stances. Two positive candidates exist: the
highest-sequence approval in each channel. Each must clear **every** relevant
withdrawal/request barrier using a known time strictly after its pinned upper
bound and, within one sequence namespace, a strictly higher sequence. A lower-ID
blocker with ambiguous time still blocks. Valid positive candidates across
channels use source time, with a stable source-key tie-break.

Normal review IDs and command IDs have independent sequence namespaces.
Dismissal timeline IDs never compare numerically to review IDs. Requests win
against withdrawals with equal, unknown or ambiguous times. A withdrawal can
replace a request only with strict time proof (and sequence proof in a shared
namespace). A dismissal can clear only native requests whose review sequence is at most the
dismissed review ID, with strict time proof. It cannot clear a later native
request or any command request. Native dismissals do not revoke command
approvals. Native approvals still clear pinned dismissal time barriers with Q4
proof. Fallback-time dismissals cannot mask later requests. Outstanding
requests take precedence over withdrawals; same-kind presentation ties use
source time and source key. Withdrawal queries use sorted prefix maxima rather
than pairwise scans. Local observation time is never action-order evidence.

Q4 barriers remain pinned: an exact second-resolution timestamp has a **+1s**
upper bound; clearing requires strictly after that bound, not merely after its
reported second. Fallback includes precision and clock allowance. Missing bounds
cannot be cleared on that revision. Any revision change clears current request
and withdrawal indices; retained historical barriers remain pinned. A fresh
attributed action can recover on the new revision.
No override exists. Recovery on the same revision requires no further reviewer
action, same-ReadID refresh or blockers-first emission. 11a emits each newly
acquired source once; duplicates are safe and optional for audit, not clearance.

`Apply` copies only the changed projection maps and changed event payload;
it never JSON-round-trips the growing source set. Snapshot evidence retains all
first-observation source pins needed to prevent old sources rebinding. This is
bounded at 4096 sources **per change**, not per queue. Arbitrary eviction would
lose those pins and is unsafe. On revision advancement, native APPROVAL sources
with a positive attribution older than the new revision are pruned: head/time
proof prevents them authorizing again. Requests, withdrawals, ambiguous sources
and all command pins remain. Saturated clears when this recovers capacity.
Exhaustion clears every decision cache and stays fail-closed for the epoch,
including after subsequent observations and restart. Incoming barriers replace
positive noise first; an all-barrier overflow is represented by Saturated and
must be rejected at 11a admission. No existing barrier is evicted. This does
not affect another PR's evaluation. No production ingestion exists yet. Before
11a activation, acquisition must filter non-writer/ineligible reviewers using
fresh identity/grant evidence **before** allocating stance pins, including their
dismissals; unknown eligibility blocks the gate instead of being silently
filtered. If a reviewer becomes eligible, 11a must acquire their complete review
history then. Source-budget admission must reject an oversized change explicitly;
it must not discard blockers or let public-repository noise consume other
changes' budgets. Activation must retain this resource gate. Benchmarks exercise
1500 and 4095 mixed approve/request/unapprove sources for one user.

11a also owns a **queue-level admission budget**: the 64 MiB decoder limit is
shared by the whole queue, not available to each change. Encoding rejects facts
over 8 MiB for one change or 48 MiB collectively with a typed
`ApprovalSnapshotBudgetError` naming the change whose admission exceeds the
budget. 11a must reject/roll back that change, leaving the previous recoverable
queue intact, and reserve the remaining 16 MiB for legacy projections. The final
encoder also checks the actual whole-snapshot 64 MiB bound. It never emits an
oversized snapshot. At roughly 4.6 MiB per saturated change, only about ten fit
the approval reserve; a source-count-only admission rule is insufficient.

## Decision and downstream gates

`EvaluateApprovals` takes explicitly fresh eligibility with owner-token pools
(`owned:user:login`, `owned:team:org/team`). It re-runs
`classification.ResolveOwnerEligibility` on stored governing evidence and
replaces **both** owner projections and **both** known flags. Stored eligibility,
classification-time pools and their `Fresh=true` flags never authorize. Fresh
membership resolves classification-time membership-only unknowns without
rewriting semantic evidence. A rule matching a historical `unknown` facet
remains conservative until normal reclassification (11a). Other incomplete
inventory evidence still blocks.

The PR 5 evaluator deduplicates IDs, excludes author/submitter/all accountable
humans, requires fresh writer and applicable-pool eligibility, and blocks eligible
requests for changes. `ReviewAttribute` uses that same native-inclusive set even
for receipt-only requirements. No implicit reviewer pool is added.

7a must record acceptance/rejection keyed by **comment ID**; a late rejected
receipt is never a stance. Re-run native-after-receipt and
receipt-after-native-then-intent-edit using actual receipt records then. Current
component tests use shared receipt types only. Brief authentication is 7a's gate.

8's fingerprint includes current effective stances, relevant metadata, recorded
policy digest and ConfigApplied digest; revision-zero and non-effective historical
sources and polling ReadIDs/times are excluded. It remains disconnected from
planner timers and CI keys.

`PrepareApprovalDecision` returns the exact fresh eligibility, accountability,
intent, all effective current-revision stances in `ReliedOn`, current receipt
identity, evaluation and
snapshot containing that eligibility. 9 must journal **that exact event**
atomically with its snapshot and reject the decision if the transaction fails;
it must not acquire/reconstruct a different eligibility event after evaluation.
The counted subset is `Evaluation.Requirements[].ReliedOn`.
The helper performs no external I/O and cannot prove acquisition freshness.
11a must preserve channel sequence namespaces and cross-channel ordering above.

## Wait projection (Stage A 8, trial Hold)

`ApprovalGateRejected` pins the tested batch ID/SHA and ordered composition, and
records a nonempty set of failed members with current read bases, fingerprints
and sorted distinct waiting reason codes. It marks that batch and its suffix
for the existing invalidation effect. A started landing cannot be rejected.
The ensuing retirement keeps admission order, IDs and ages, and uses the existing
speculative cancellation ledger. No failure/timeout event, AIMD reduction or
defect sample is manufactured. Real unresolved bisection groups remain barriers;
empty waiting groups are retired. Fresh selection (including its max-wait age),
retained groups, bisection, fast-forward remainder/prefix recovery and speculative
admission all use the same wait exclusion. Returned members get fresh batch IDs.

Waits are absent until a gate rejection: unapproved changes may initially test.
Only changed decision inputs wake a wait. Current effective stances (including
receipts), intent, eligibility, revision, semantic classification generation and
policy digest, and ConfigApplied digest contribute to the fingerprint. Historical
sources at revision zero, ineffective sources, polling read IDs/times and
provenance-only classification refreshes do not. A classification generation
advance explicitly invalidates affected not-yet-landing batches because build
metadata has no generation field (PR 10 carry-over); started attempts settle.

Reconciliation calls the dormant `worker.approvalInputProbe` seam each cycle,
including with no active batch and unchanged PR summaries. PR 11a supplies fresh
read-only permission/membership acquisition. `PrepareApprovalInputChange` emits
nothing for equal inputs. `ApprovalInputsChanged` carries the fresh eligibility,
current revision/head/read basis and old/new fingerprints; replay rechecks all
bindings, rejects stale read times and cannot wake a subsequently replaced wait.
No test-ref construction or check refresh occurs during probing. Errors preserve
the wait. Public config and production acquisition remain disabled until 11a/11b.

Snapshots retain waits in an optional field (absent for legacy queues), validate
current fingerprints and revision bindings, and resume the same plan after replay.
Once either new event is recorded, rollback needs a compatible database restore.

The dormant `serve.mergeResultWithApprovals` contract evaluates the complete
condition expression with the evaluator's eligible native-inclusive review set
(`ApprovalEvidence`; unknown eligibility is a missing value, never an empty
list). Its nil opt-in calls the original `mergeResult` unchanged, including for
legacy queues whose conditions use reviews. For non-passing opt-in conditions,
review-neutralized evaluation distinguishes genuine CI failure from a review
wait; only causally unresolved branches contribute unknown-input reasons.
11a must consume `WaitingForApproval` as `ApprovalGateRejected` before entering
check observation/timer/retry paths. Merely treating it as pending CI would still
time out. Independent approval-policy requirements remain `EvaluateApprovals`'s
landing gate, even when the condition expression itself passes. The contract
tests inject that rejection now; no executor integration is enabled here.


### 11a follow-up obligations from Stage A 8 review

- A failing `approvalInputProbe` must not prevent reconciliation of other PRs:
  log and continue, or isolate each probe. The dormant seam currently returns
  the error; 11a must supply isolation before enabling production acquisition.
- Same-input probes journal nothing and therefore do not advance persisted
  acquisition freshness. Only journaled observations advance that freshness.
- `not(approved-reviews-by = X)` with X present is classified as an approval
  wait. Removing that review input can make the negation pass; 11a must preserve
  the complete-expression outcome when consuming `WaitingForApproval`.
- Optionally narrow per-event wait refresh to events that affect approval
  inputs; the current unconditional refresh is correct but does extra work.
