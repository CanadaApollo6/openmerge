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
`ReadInputs` must read actual authoritative trailers from the immutable tested
commits (the tested marker for fast-forward), plus fresh forge eligibility;
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
and queue removal cannot rewrite snapshots or prevent fence settlement.

Rejections use `PrepareLandingRejection` and `Store.RejectLanding`: fresh
eligibility is inserted before the rejection in the same transaction, with
observation time no later than each wait basis. Pre-read fingerprints prevent
stale rejection observations from overwriting newer evidence.

`legacy-start-only` uses a nil current approval policy and writes only the unchanged
start. Historical approval facts are not evidence that policy still applies.
Decision projection is omitted for legacy snapshots. Snapshots retain frozen
attempts and apply the existing approval/classification/receipt/full-snapshot
admission preflights; overflow identifies the admitted change. The replay tool
permits only an absent-to-nil dormant decision map, never populated evidence.
