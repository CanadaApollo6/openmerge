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
`Apply` retains its existing no-error signature and rejects unbound observations.

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
The revision record retains the set of prior heads. On H1 → H2 → H1, first
observation requires a submission time strictly after `boundary.At + 3s`
(second timestamp precision plus the Q4 two-second clock allowance). Missing or
ambiguous proof attributes revision zero, never authorization. A pre-history
snapshot with revision >1 and no prior-head set also requires proof; this
unknown history stays unknown through subsequent boundaries until full replay. Previously
observed sources keep their attributed revision through head reuse.

A dismissal retains `OriginalSubmission` and the reviewed commit separately.
Those prove attribution of the **original review**, never the dismissal action's
time. A known original's pinned revision is authoritative, regardless of source
arrival order; an unseen original uses the same head/epoch proof as a review.
Dismissal time always comes from PR 3b's dismissal DTO, including fallback on
revision 1 and later revisions. An old review dismissed today cannot erase a
current head-epoch request. The additive wire fixtures pin shared forge/policy
field names (including their existing exported-name JSON spelling).

## Pure set fold and recovery

Every new source recomputes the changed user's effective stance from its
attributed source **set**, not incremental Latest/frontier state. Derived indices
are caches only. Arrival order, read grouping, duplicate polls and snapshot/tail
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
namespace). Fallback-time dismissals cannot mask later requests. Outstanding
requests take precedence over withdrawals; same-kind presentation ties use
source time and source key. Withdrawal queries use sorted prefix maxima rather
than pairwise scans. Local observation time is never action-order evidence.

Q4 barriers remain pinned: an exact second-resolution timestamp has a **+1s**
upper bound; clearing requires strictly after that bound, not merely after its
reported second. Fallback includes precision and clock allowance. Missing bounds
cannot be cleared on that revision. Any revision change clears current requests
and withdrawals; a fresh attributed action can recover on the new revision.
No override exists. Recovery on the same revision requires no further reviewer
action, same-ReadID refresh or blockers-first emission. 11a emits each newly
acquired source once; duplicates are safe and optional for audit, not clearance.

`Apply` copies only the changed projection maps and changed event payload;
it never JSON-round-trips the growing source set. Snapshot evidence retains all
first-observation source pins needed to prevent old sources rebinding. This is
bounded at 4096 sources **per change**, not per queue. Arbitrary eviction would
lose those pins and is unsafe. Exhaustion fails closed for that change; it does
not affect another PR's evaluation. No production ingestion exists yet. Before
11a activation, acquisition must filter non-writer/ineligible reviewers using
fresh identity/grant evidence **before** allocating stance pins, including their
dismissals; unknown eligibility blocks the gate instead of being silently
filtered. If a reviewer becomes eligible, 11a must acquire their complete review
history then. Source-budget admission must reject an oversized change explicitly;
it must not discard blockers or let public-repository noise consume other
changes' budgets. Activation must retain this resource gate. Benchmarks exercise
1500 and 4095 mixed approve/request/unapprove sources for one user.

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
intent, relied-on attributed stances, current receipt identity, evaluation and
snapshot containing that eligibility. 9 must journal **that exact event**
atomically with its snapshot and reject the decision if the transaction fails;
it must not acquire/reconstruct a different eligibility event after evaluation.
The helper performs no external I/O and cannot prove acquisition freshness.
11a must preserve channel sequence namespaces and cross-channel ordering above.
