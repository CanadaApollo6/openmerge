# Landing decision snapshots (ADR 0007 Stage A 9)

Trial class **Hold**: merge is allowed; do not deploy until dogfood period B
has drained. The gate remains an internal activation seam for PR 11a/11b;
there are no new public configuration keys or production acquisition calls.

`landing_decision_recorded` is an additive wire-v1 event keyed by batch,
random landing attempt, and change. Existing `Land` and `LandingStarted`
encodings and the fence protocol are unchanged.

The approvals variant retains the recorded brief identity (change, revision,
head, classification generation, pinned diff endpoints, intent/policy/brief
digests), a diagnostic-free classification digest, labels, the pre-read approval
fingerprint, the eligibility read ID, accountability, and expanded obligations.
Classification inputs resolve through the recorded generation, not a new diff.
Each obligation names its eligible IDs and each original approval/receipt source
it used; shared approvals appear in every satisfied obligation. A deduplicated
source table freezes complete review acquisition and receipt acceptance records,
so later projection pruning cannot erase the relied-on source evidence. Native sources
have empty intent/brief/receipt bindings, meaning not recorded. PR evidence says
`not_applicable_pr` for direct-protection observations. Tested T and landed L are
separate even when equal. The generated-remedy variant records suspect, evidence
references and `on_red: revert`; it does not implement remedy generation.

Activation binds the current policy with `Store.EmitWithPolicy` and binds
`LandingDecisionGate.Commit` to `Store.StartLanding`.
`ReadTestedMessages` must read actual messages from immutable tested
commits (the tested marker for fast-forward); `ReadInputs` reads fresh forge eligibility;
it must never synthesize tested metadata from the latest classification.
The gate checks those labels using the same `ClassificationLabels` spelling
as build metadata. There is no generation in `ChangeMetadata`: generation is
read from recorded classification, whose refresh also invalidates pending work.
An absent reader, member or atomic persistence hook fails closed.

Receipt identity always comes from `brief.Identity(State.RecordedBrief(...))`.
Policy mismatch returns `BriefPolicyMismatchError`. Fresh owner membership affects
`PrepareApprovalDecision`/`EvaluateApprovals`, never recorded brief identity.
Every counted receipt is also checked with `ValidateReceipt` against that brief.

The store holds the queue lock for claim authorization, a latest-projection load,
all member/basis/receipt/admission checks, all encodings, and final start
authorization. The gate refreshes start time after reads and pure preparation;
lease authorization also includes time spent waiting for the lock and validating. Every validation completes before the first INSERT. The complete
fresh eligibility/decision set and the unchanged start commit in one transaction.
Any stale authorization, lost lease, mismatch or SQL error rolls back, including
authorization cancellation side effects. No forge request or git push runs in
that transaction. `ErrLandingDecisionStale` releases the action for a fresh gate
read after rollback, preserving the legacy stale-start completion path. Once committed, updates, pauses
and queue removal cannot rewrite live evidence or prevent fence settlement.

Rejections use `PrepareLandingRejection` and `Store.RejectLanding`: fresh
eligibility is inserted before the rejection in the same transaction, with
observation time no later than each wait basis. Pre-read fingerprints prevent
stale rejection observations from overwriting newer evidence.

`legacy-start-only` uses a nil current approval policy and writes only the unchanged
start. Historical approval facts are not evidence that policy still applies.
Decision projection is omitted for legacy snapshots. Snapshots retain frozen live
attempts and apply the existing approval/classification/receipt/full-snapshot
admission preflights; overflow identifies the admitted change. The replay tool
permits only an absent-to-nil dormant decision map, never populated evidence.

## PR 9b follow-ups

Only **live, unsettled attempts** belong in State and snapshots. Decisions are
staged while the batch is Testing immediately before the atomic start, retained
through Landing (including pause, input edits and retirement), and pruned when
the batch is discarded by landing, abandonment/failure or cancellation. A new
attempt supersedes a staged older attempt. Pruning copies the map, preserving
prior projections. The v6 snapshot format is unchanged; decoding old snapshots
validates then removes historical decisions, and encoding omits an empty map.
Legacy queues keep nil evidence and identical snapshot bytes.

PR 12a's journal-backed read seam is
`Store.LandingDecisionEvidence(ctx, queueID, batchID, attemptID)`. It reads only
`landing_decision_recorded` rows for that batch/attempt using partial identity
indexes for the decision and eligibility event types, with a 128-member limit
(129 rows detect overflow). For approvals
it reads the exact `approval_eligibility_observed` by EligibilityReadID and
change/revision/head, with a two-row limit to detect ambiguous references. Both
reads share a read-only repeatable-read snapshot. It never decodes unrelated
history or relies on `State.LandingDecisions()` / latest eligibility projections.

**Compaction retention contract:** any future journal compaction MUST retain
`landing_decision_recorded` and every referenced `approval_eligibility_observed`
with its original identity and payload, including settled attempts. Missing or
ambiguous eligibility evidence fails the bounded read; it cannot silently
substitute a newer read. `ApprovalFacts.Eligibility` remains latest-only.
InputFingerprint
is **pre-read**; `ApprovalWait.Fingerprint` is **post-read**, after applying the
fresh eligibility event. They intentionally identify different states.

`LandingDecisionRequiredError` is a store/executor wiring failure, distinct from
ErrStale. The worker's generic failure path completes the action as Failed;
it must never swallow this error and mark Land Done. The reverse mismatch
(executor gate with nil store policy) returns the same typed wiring error,
without ErrStale or ErrLandingDecisionStale. ErrLandingApprovals
likewise uses the worker's generic failure path. Its rejected batch is cancelled
later by BeginConsequential, rather than completing Land as Done. PR 11a exit
**approval-rejection-worker-replan** must exercise rejection, Failed completion,
then BeginConsequential cancellation of that same batch/claim. Current
`TestLandingDecisionErrorsFailWorkerAction` pins the worker error classification.

`ReadInputs` supplies fresh eligibility only. `ReadTestedMessages` reads pinned
immutable tested member commits in member order (the single tested marker for
fast-forward); `gitops.Repo.ReadCommitMessage` provides a cache-only object reader. Acquisition must happen separately;
the read accepts full SHAs only, never resolves remote/auth, and disables lazy
fetch, replacements, grafts and transport. Missing objects fail locally.
For rebase, select each member's final replay commit, where PR 10 writes its
pair. The gate parses messages with `forge.ParseChangeTrailers`, shared with
PR 10 writer round-trip tests for all four methods. After CRLF normalization (lone CR is rejected), only adjacent canonical
pairs in the writer-owned suffix of the final paragraph are authoritative.
Other contributor lines in that paragraph may be prose, folded continuations
or empty trailer values. Reserved keys elsewhere, unknown reserved keys and
continuation injection on reserved keys are rejected; provenance and final
batch keys must have the writer's canonical position. Duplicate pairs, casing/whitespace variants, missing
pairs and classification mismatches fail closed. No legacy writer or commit-ID changes. The fake forge inserts the writer's blank separator only when metadata
is present, preserving legacy simulation IDs.

ADR 0008 per-check evidence must be a **separate event**, keyed by batch,
attempt and change. The strict `landing_decision_recorded` decoder is unchanged;
new check fields must not be added to this event.

## Acquisition ordering and progress (11a round 3)

Before any final gate forge reads, capture the queue journal high-water mark
(`events.id`) and each observation's acquisition-start time. Carry the same
`Store.ApprovalRead` through ingestion, eligibility, commands and atomic start
or rejection. Under the queue lock, reject if a later committed stance,
receipt, eligibility, revision or classification event affects a member.
The table below defines configuration, base and rejection scope.
Checks run before dedupe as well as before start: an ABA cannot hide behind an
identical fingerprint or a worker's newer wall clock. After a successful own
append, advance the acquisition watermark only to that transaction's final ID;
never refresh it from a later unlocked journal read. Nested acquisition reuses
the guard rather than resetting its lower bound. No forge I/O runs under a lock.

Projection replay follows database commit order. Worker timestamps are
informational and never reject a later non-overlapping acquisition, including
one from a slower clock. The database watermark rejects overlapping stale reads
before they enter the journal. Eligibility poll dedupe compares
the candidate fingerprint with the current locked journal projection, including
another gate's writes. Diagnostic reasons, read IDs/times and set order do not
append events. A deny → allow → deny transition retains both semantic changes.

Progress requires a sufficiently quiet acquisition-to-start interval with a
satisfied policy. Identical polls dedupe, but every real relevant input change
must invalidate pending authorization. Continuous meaningful evidence changes
can force unlimited replans; there is no bounded-attempt or fairness guarantee.
Source/evidence caps fail closed rather than guaranteeing a landing. The stale
error exposed in worker diagnostics says `waiting for stable approval evidence`;
this is an operator-visible waiting reason for churn, distinct from the durable
policy wait reasons attached to a rejected batch. Trial class remains **Hold**.

## Acquisition invalidation scope (11a round 4)

The guard captures projection and watermark in one repeatable-read snapshot.
Its own successful appends advance both together; it never skips a competing
commit. Scope is local to one queue (one base branch): a different queue's base
or policy cannot invalidate this acquisition.

| Event type | Scope after the captured watermark | Reason / payload path |
| --- | --- | --- |
| `change_revision_observed` | Acquisition members | `data.change_id`; revision/head boundary |
| `change_classified` | Acquisition members | `data.evidence.Classification.Inputs.ChangeID`; classification inputs |
| `change_accountability_observed` | Acquisition members | `data.basis.change_id`; accountable actor |
| `change_intent_observed` | Acquisition members | `data.basis.change_id`; intent binding |
| `review_stance_observed` | Acquisition members | `data.basis.change_id`; native/command stance |
| `approval_receipt_recorded` | Acquisition members | `data.basis.change_id`; receipt evidence |
| `approval_eligibility_observed` | Acquisition members | `data.basis.change_id`; fresh eligibility |
| `approval_inputs_changed` | Acquisition members | `data.basis.change_id`; permission/membership probe |
| `approval_gate_rejected` | Waiting members, or the member's composition and its ancestors | `data.waiting[].basis.change_id` or `data.batch_id`; rejection invalidates that composition and its speculative suffix. An unrelated later batch/rejected change does not stale this read. |
| `base_moved` | All acquisition members on this queue's base, when SHA differs | `data.sha`; changes ancestry/diff inputs. Re-observing the captured SHA does not invalidate. |
| `config_applied` | All acquisition members when effective digest differs | `data.digest`; changes evaluated policy. A new authority commit with the same digest does not invalidate. |
| Other events | No approval-read invalidation | Ordinary checks, metadata, resume and dormant evidence do not change these approval inputs. Atomic start still checks the current plan. |

`TestApprovalReadFilterScopes` pins this table, including no-op base/config,
member/unrelated event pairs and ancestor/suffix dependencies. Legitimate
unrelated rejections are covered by `TestApprovalReadUnrelatedRejection`.
`TestStageAApprovalReadUsesDatabaseOrder` additionally pins real event encodings.

`ChangeRevisionObserved.Basis.At` is now captured before PR metadata/file reads.
The revision boundary therefore moves earlier by the read latency. Existing
three-second source-time slack still applies; this does not turn worker time
into an acquisition-order fence.
