# ADR 0008 PR 8: landing checks

Trial class: **Hold**. Shared landing-start/check-read wiring changes even though
emission is authority-only and activation remains PR 9. This builds on main
through PR 5, without PR 6 or PR 7.

`landing_checks_recorded` is a separate additive wire-v1 event. Its only registry
fixture is `internal/queue/testdata/wire/v1/event_landing_checks_recorded.json`.
The deployed `landing_decision_recorded` and every existing fixture remain
unchanged. New objects reject unknown, missing, duplicate, and case-folded keys.

The event binds batch/landing attempt, governing config commit, effective config
and policy digests, tested SHA, intended landed SHA, merge method, queue ref,
ordered pinned PR heads, and check observation time. `required_check_source`
identifies the effective `merge_when` expression and independent rule revisions.
Each member retains the full nested/negative expression and all its typed
observed inputs, including pending/missing check knowledge. Every evaluator run
retains name, original source, producer kind/identity, normalized observed result,
native check/status ID, workflow run ID, attempt, and branch provenance.
Unavailable identities, attempts, and branch provenance are explicitly `null`.

GitHub's legacy check reader defaults unknown attempts to one for retry
compatibility. `AttemptInferred` marks that default without changing its retry
meaning; audit records `null`. An explicit attempt URL or job-attempt read clears
the inference. The event does not turn neutral/skipped/cancelled results into
success or treat negative/alternative expressions as a flat success-check list.

The executor shares one provenance-filtered and retry-selected CheckRuns read
with the authority gate. There is no second forge read to reconstruct history.
The gate evaluates `checkSnapshot`/`policyValuesWithApprovals` using that read,
then `Store.StartLanding` validates the claim, batch membership, tested SHA,
config/policy bindings, and independent source revisions under the queue lock.
It inserts fresh eligibility, decisions, one checks event, and LandingStarted in
one transaction. A final-start failure rolls back every preceding insert. Lost
commit responses do not duplicate evidence; settlement uses the existing fence.
Fast-forward keeps the tested marker separate from the contributor head it lands.

Ordinary GitHub queues, including legacy approval-free queues, emit **zero new
events per landing**. After PR 9 activates authority, an admitted authority
attempt emits **one additional event per batch landing**, independent of member
count; rejected starts emit none. Four speculative batch positions each record
their own tested SHA and runs. Snapshot bytes are unchanged by this journal-only
event: ordinary legacy state remains **v6**; queues with authority state/history already
use **v7** from PR 1.
The fold neither advances planner revisions nor retains a new projection.

Retain all journal evidence, including these checks, decisions, eligibility,
and their dependencies, after authority rollback, settlement, or queue retirement.
No compaction, migration, or index is added. PR 10a should stream bounded queue/id
pages using `eventPage` and `events_queue_watermark` in a read-only repeatable-read
transaction, associating checks by batch/attempt. Missing historical checks mean
"not recorded". Do not add an attempt index or load whole histories for export.

Before first emission, drain all old workers on every host after period B,
retain the worker inventory, capture and test the pre-emission DB backup, and
retain its receipt. The main-before-PR-8 decoder rejects this event. After first
emission use a compatible binary, or restore the verified backup while workers
are stopped; reconcile subsequent real refs and recover their evidence before
resuming. Switching authority mode is not a binary downgrade. This PR neither
performs a live backup nor authorizes deployment.

Main contains a generated-remedy decision wire variant, but no runtime remedy
generator/start path. Its exemption payload is unchanged. The checks event has
no reviewer requirement; the store seam supports an authority start without
inventing eligibility or a review floor. PR 9 still owns complete authority
activation without approvals, and a future remedy producer must use the same
atomic seam with its existing exemption semantics.

Real GitHub organization acceptance is not executed here: real GitHub calls,
live DB/API access, and deployment operations are outside this task. PR 11 must
retain actual queue-ref check/producer/results and settlement IDs against base
history before claiming organization acceptance. Local HTTP and real-git fixtures
provide automated adapter/object evidence, including atomic start and read-back
after real-git settlement with the test DB configured.

Verification results are recorded below after execution.
