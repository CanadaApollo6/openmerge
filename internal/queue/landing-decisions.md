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

PR 12a's journal-backed read seam is `Store.EventsAfter(ctx, queueID, 0)` or
`JournalReader.Journals` for an explicitly scoped queue. Filter the decoded
`LandingDecisionRecorded` by BatchID, AttemptID and Approval.Basis.ChangeID
(or Remedy.ChangeID); retain journal event IDs for links. These APIs read the
immutable journal, not `State.LandingDecisions()`. Settled evidence remains in
the journal. `ApprovalFacts.Eligibility` holds only the latest read: recover
the exact fresh `ApprovalEligibilityObserved` by matching its Basis.ReadID to
the decision's EligibilityReadID (and its change/revision/head). InputFingerprint
is **pre-read**; `ApprovalWait.Fingerprint` is **post-read**, after applying the
fresh eligibility event. They intentionally identify different states.

`LandingDecisionRequiredError` is a store/executor wiring failure, distinct from
ErrStale. The worker's generic failure path completes the action as Failed;
it must never swallow this error and mark Land Done. The reverse mismatch
(executor gate with nil store policy) also fails and rolls back. ErrLandingApprovals
likewise uses the worker's generic failure path. Its rejected batch is cancelled
later by BeginConsequential, rather than completing Land as Done. PR 11a exit
**approval-rejection-worker-replan** must exercise rejection, Failed completion,
then BeginConsequential cancellation of that same batch/claim. Current
`TestLandingDecisionErrorsFailWorkerAction` pins the worker error classification.

`ReadInputs` supplies fresh eligibility only. `ReadTestedMessages` reads pinned
immutable tested member commits in member order (the single tested marker for
fast-forward); `gitops.Repo.ReadCommitMessage` provides the object reader.
For rebase, select each member's final replay commit, where PR 10 writes its
pair. The gate parses messages with `forge.ParseChangeTrailers`, shared with
PR 10 writer round-trip tests for all four methods. Only adjacent canonical
pairs in the final trailer block are authoritative. Preserved contributor body
text cannot supply them. Duplicate pairs, casing/whitespace variants, missing
pairs and classification mismatches fail closed. No writer or real commit ID
changes. The fake forge inserts the writer's blank separator only when metadata
is present, preserving legacy simulation IDs.

ADR 0008 per-check evidence must be a **separate event**, keyed by batch,
attempt and change. The strict `landing_decision_recorded` decoder is unchanged;
new check fields must not be added to this event.
