# Dormant approval evidence (Stage A 6)

The four additive wire-v1 observations have no production producer. `serve`,
executor, planner, existing conditions, configuration and landing are unchanged.
`State.approvalFacts` is nil until an accepted observation. The projection never
advances planner revisions or CI timers. Its fingerprint is available for PR 8;
revision changes affect it without reclassifying. Snapshot v5 retains attribution,
first-observation barriers and channel frontiers; v2/v3/v4 decode with absent
approval facts. Store compaction remains disconnected.

## Recording and ordering

`ApprovalBasis` identifies the change, observed revision/head, and the read that
supplied the evidence. Record a revision boundary before its observations.
`Accountability` takes resolved stable-ID agent mappings from governing config,
before forge kind. Actor, PR author, optional submitter and accountable people
remain separate. An unmapped bot records no accountable human; accountability
never grants fork trust. Reviewer Actors must already carry effective account
kinds after the same configured mappings. No git name/email is an identity input.
GitHub user keys are constructed with `classification.GitHubUserID`; the wire
validator and decision resolver reject other spellings, including leading zeros.

Intent is the normalized title and body (CRLF/CR to LF, surrounding whitespace
trimmed). Its digest is SHA-256 of the JSON string array
`["openmerge/intent/v1", title, body]`, encoded by Go's JSON encoder. PR 7a must
reuse this binding; it owns brief hashing, receipt acceptance and command wiring.
A native stance always has a null receipt: intent and brief are **not recorded**.

Source keys separate native review IDs, dismissal-of-review IDs and command IDs.
A dismissal discovered later with a timeline event ID joins the original
review's dismissal key. Its first time bound remains pinned, including across
polls and snapshot/restart. Acquisition provenance (clock source, precision,
uncertainty and observation) remains in the first observation. Dismissals use
**dismissal** time, never submission time. Event IDs are not ordered against
review IDs. Normal sources use ID sequence within a channel; independent
namespaces need comparable source times. Across channels, new log observations
win unless proven older or barred by an outstanding blocker. Re-observation of
an existing source never moves it to the end of the log.

A withdrawal/request for changes sets a conservative barrier. Positive evidence
requires a known source time strictly after its upper bound. Equal/unknown times
cannot clear it; a distinct blocker cannot lower the bound. An unbounded barrier
cannot be cleared on that revision. To clear a bounded barrier, acquire the
blocker again and record it with the **same ReadID** as the clearing review or
receipt. The fold checks this re-observation; local wall time never substitutes
for forge time. Keep ingestion serialized through this fold. A rejected positive
source is not later promoted by duplicate polling; use a fresh action after
re-observing the blocker. Without a comparable bound, create a new revision and
get a fresh attributed review/receipt. There is no operator override.

First discovery pins native attribution permanently. Previously observed sources
keep their revision across H1 → H2 → H1. First discovery on revision >1 requires
a source timestamp strictly after that revision's recorded boundary, on its
exact head; otherwise revision zero marks historical/ambiguous evidence. This
is deliberately conservative when source/observation precision cannot prove
which head epoch was reviewed. Fresh review after the boundary recovers. A
newly discovered dismissal joins its original review's recorded attribution when
available. Commands pin the current revision at authenticated processing;
`unapprove` cannot rebind a late positive source. The journal retains all original
observations; snapshots retain source attribution and current indices, bounded
at 4096 sources per change. Capacity exhaustion fails closed without dropping
attribution; activation-time record/membership budgets remain PR 7a's gate.

## Decision boundary and the PR 4b carry-over

`EvaluateApprovals` takes an explicitly fresh eligibility observation, including
`OwnerPools` keyed by governing owner tokens (`owned:user:login` or
`owned:team:org/team`). It constructs the fresh resolver from that same read. It never consumes stored eligibility or classification-time
pools, even when they say `Fresh=true`. It calls
`classification.ResolveOwnerEligibility(stored.Evidence, freshResolver)` and
replaces **both** owner projections and **both** known flags in the detached
input immediately before `changepolicy.Evaluate`. No governing-tree re-read,
union of memberships, generation change or classification rewrite occurs.
Owner membership is recorded in the observation, so a membership-only change
also changes the approval fingerprint and survives replay. `Current` is PR 7a's
validated current brief binding; this component does not accept receipt commands
or validate brief digests. The caller must acquire new identity/membership/grant observations at each
future gate invocation; the pure function cannot prove external acquisition.

Unresolved membership at classification time does not strand owner obligations:
fresh resolution replaces their unresolved status/pools/known flags, as pinned
by `TestDecisionNeverConsumesStoredFreshOwnerPools`. Durable `unknown` facets
and diagnostic reasons remain classification evidence. A rule explicitly matching
`unknown` therefore continues to apply conservatively, even if the historical
reason was membership. Removing such a classification facet requires a normal
new classification generation with refreshed derivation (PR 11a), invalidating
old receipts; eligibility reads alone do not silently rewrite semantic bindings.
Other incomplete tree/inventory evidence still blocks as before.

The PR 5 evaluator deduplicates stable IDs, excludes author/submitter/all
accountable people, applies fresh writer and applicable-pool eligibility, and
blocks on eligible requests for changes. Its `ReviewAttribute` shares the same
native-inclusive review set even for receipt-only rules. There is no implicit
extra reviewer pool. Missing membership/eligibility yields unknown, preserving
nested condition negation semantics. Production condition wiring waits for 11a.

Receipt ordering tests use the shared stance/receipt types now; 7a owns the
receipt wire records and authentication. They pin native-after-receipt and
receipt-after-native-then-intent-edit without creating commands in this PR.
