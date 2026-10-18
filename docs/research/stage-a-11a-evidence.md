# Stage A 11a: ingestion, commands and the shadow gate

Trial deployment class: **Hold**. Do not deploy this PR until period B drains.
No live database, live status API, systemd unit, live App permission, or remote
`openmerge/*` ref was read or changed. Tests use the fake forge/local Git and
isolated schemas in the shared test Postgres on port 54329.

The implementation starts from `origin/main` at
`f8066a4f3bc9f2bfb110950b0c6c9aea4cf5ea6f`, in
`/home/riels/openmerge-sa-11a`, branch `stage-a/11a-shadow-gate`.
The untouched baseline checkout is `/home/riels/openmerge-sa-11a-main`.
Only main was fetched, with the explicit refspec
`refs/heads/main:refs/remotes/origin/main` (exit 0); worktree creation exited 0.
The original working tree remains untouched.

## Activation boundary and implementation

`stageAPolicy` is unexported. Its only installer is `shadowFixture` in
`internal/serve/stage_a_test.go`. No public option, target, loader, schema or
production constructor installs it. Public config/schema and wire registry are
unchanged; new approval keys remain rejected. Candidate config appears only in
isolated tests. No Stage B/C settings or new event/action types are added.
11b still owns the applied-config digest composition: the isolated shadow pin
is explicitly domain-separated and is not a public ConfigApplied contract.
The brief endpoint continues withholding candidate requirements with
`policy_unavailable`; this PR does not manufacture an 11b digest.

Ingestion journals the revision boundary before its approval evidence, including
revision 1. Intent/accountability, classifications and native observations are
semantically deduplicated. Static names are re-resolved at unchanged governing
commits; unresolved names block the config source, with basis-checked recovery.
Governing files use the default-branch commit. Forward base moves preserve the
first diff pin. Rewrites, including a rewind exactly to that original pin,
commit refreshed classifications and the base observation together. Provenance
and unrelated configuration formatting do not invalidate receipt identity.

Authenticated whole-comment commands use stable comment author IDs and PR 3a's
identity-bound grants. Native issue comments are sorted by creation time/ID;
the durable cursor is inclusive. Every consumed comment has an immutable
outcome, so edits and duplicate/concurrent processing cannot create new commands.
App-self comments and inline review comments cannot issue commands. The legacy
parser and its whole-comment grammar remain unchanged.

Bare receipts call `PrepareBareReceipt`: Supplied copies Current from recorded
brief evidence, replacing only its supplied digest. Fresh memberships never
enter this identity. Malformed digest rejections retain at most 1024 UTF-8 bytes,
with 64 retained rejections per actor; subsequent commands record an immutable
capacity outcome and advance the cursor. Mismatch replies explain supplied and
current digests, revision and head. No v1 receipt binding is substituted.
Unapprove binds the authenticated processing revision. Dedupe and evidence
acceptance share a queue-locked transaction and roll back together.

Source admission first rejects nonwriters and definite policy exclusions. A
hypothetical stance is used only to ask the shared evaluator whether an actor
can affect a current requirement, never to authorize a landing. Unknown pools
remain conservative. Native exact-head fork-trust observations remain available
for fork changes even when a rule requires `none`. All source-capacity, typed
boundary, classification-sequence and snapshot-size failures reject the
candidate event; no other change's facts are removed to make room. Snapshot
validation groups sources by user instead of rescanning every user's full set.

Approval-only check-condition waits are consumed before executor CI timers,
ChecksObserved, retry or timeout handling. The same eligible review set supplies
admission, retries, reconciliation and final nested-condition evaluation. Policy
requirements remain additive. The final gate reuses recorded classification,
re-reads stances/intent and grants/membership, checks immutable tested messages
through `ParseChangeTrailers` and cache-only `ReadCommitMessage`, and calls the
atomic PR 9 start exactly once. Rejection persists fresh inputs and waits and
invalidates the composition. Settlement remains first under configuration
failure, pause, removal and installation loss. Existing checks include the
reviewer brief path, digest and minimal wait reason on the candidate path.

## Carry-over coverage

- PR 6 older-dismissal ordering: main already contains the sequence guard in
  `recomputeUser`; `TestR3ApprovalThenOlderReviewDismissedLater` exercises every
  permutation with snapshot/restart after every event. The 11a emitter uses this
  fold rather than reconstructing an approval set from raw review order.
- PR 6 candidate budget attribution: `TestRemapAdmissionBudgetError`,
  `TestSharedAdmissionReserveAllowsThreeLargeAndSmaller`,
  `TestStanceAdmissionUsesSharedReserve`,
  `TestReceiptAdmissionNamesAdmittedChange` and
  `TestAdmissionChecksFullSnapshotBound` pin the admitted-change attribution,
  shared reserve and 64 MiB full-snapshot bound. `RecordEvidence` validates the
  candidate under the queue lock and never evicts another change.
- PR 6 revision-1 evidence and noise: `TestStageAShadowGateDepthsAndIdentityIsolation`,
  `TestStageAIdentitySwapRevocationAndNoiseAdmission` and
  `TestStageANoiseFromExcludedWriterDoesNotPinSources`; typed boundary checks
  run in `preflightStageA` and the existing admission APIs.
- PR 4a/4b retention and caps remain covered by
  `TestClassificationSnapshotTailAndImmutableApply`, the shared-admission tests,
  and `TestClassificationEmitterRejectsContradictoryEvidence` (aggregate versus
  per-file facts, known owners without rows, selected owners blob mismatch and
  out-of-sequence generation). `RecordedBrief` rejects a head inconsistent with
  the live revision. The batch gate verifies the pinned base is still an
  ancestor of the current queue base; rewrites refresh before gate use.
- PR 4b decision-time owners: `TestStageAUnReadableTeamAndDecisionTimeOwners`
  checks changed membership, unreadable teams and a stable recorded brief digest.
  Queue evaluation continues to call `ResolveOwnerEligibility` on stored
  governing evidence with fresh pools, never stored classification pools.
- PR 7a/7b endpoint withholding remains covered by the unchanged status policy
  tests; `TestNextStepMachineValuesRemainVerbatim` prevents display truncation
  suffixes in Path/URL. `docs/api.md` documents default-branch rename yielding
  `policy_digest_mismatch` after pinning.
- PR 8 outcome contract: `TestStageAReviewConditionWaitDoesNotTimeout` uses a
  nested expression and records a wait before any timeout path. Receipt wait
  invalidation via `approval_inputs_changed` is listed in `approvals.md`.
- PR 9/9b: `TestStageAApprovalRejectionWorkerReplan/approval-rejection-worker-replan`
  proves rejection → worker Failed completion → retry of the same claim →
  BeginConsequential cancellation, with no landing start.
  `TestStageAReceiptGateAllMethods` checks one decision/start per successful
  receipt gate for squash, merge, rebase and fast-forward. Existing
  `TestLandingDecisionErrorsFailWorkerAction` preserves typed miswiring errors.
  `TestAtomicLandingPreflightRaces`, `TestAtomicDecisionExecutorCrashWindows`,
  `TestAtomicLandingLastStartStale`, `TestAtomicLandingAllMembersValidatedBeforeInsert`
  and `TestDecisionSnapshotSettlesAfterPauseRemovalAndHeadChange` cover real
  Postgres races, crash windows and immutable settlement.
  Both stale index/writer descriptions identified in the brief are corrected.

Additional integration tests cover multiple installations and bases at depths
1/4, dropped webhooks, revoked accepted receipts, exact-head fork trust for
`none`, stale intent receipts, inclusive cursors, edited comments, bounded
malformed receipts, concurrent dedupe and an intent race before receipt commit.

## Reproduction and command ledger

All heavy jobs wait for running `go test`, `*.test`, or replay jobs before
starting. Jobs run sequentially with `GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`
(`6GiB` for journal replay),
`GOTMPDIR=/home/riels/.cache/openmerge-11a/go-tmp`,
`GOCACHE=/home/riels/.cache/go-build`, and
`OPENMERGE_TEST_DATABASE_URL=postgres://postgres@localhost:54329/postgres?sslmode=disable`.
No initdb was run. Only the obsolete test processes started by this task were
terminated; their actual aggregate exit status is retained below.
Logs and machine-readable command/exit records are retained under
`/home/riels/.cache/openmerge-11a/`.

The full saved export pair is read offline, without exporting again:

- `events-20261018T051000Z.jsonl`, SHA-256
  `9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`.
- `queues-20261018T051000Z.jsonl`, SHA-256
  `418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`.
- Deployment comparison checkout: `3f77a88daf1cb99961c22b4daebaeb58f9087bb0`.

Results and exact commands follow after the sequential gates complete.
