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

Approval commands use stable comment author IDs and PR 3a's identity-bound
grants; legacy refresh/queue/dequeue retain their shared authorization handler. Native issue comments are sorted by creation time/ID;
the durable cursor is inclusive. Every consumed comment has an immutable
outcome, so edits and duplicate/concurrent processing cannot create new commands.
App-self comments are excluded by BotLogin, matching the legacy path; stable
App-ID exclusion is a later improvement. Inline review comments cannot issue
commands. The legacy parser and its whole-comment grammar remain unchanged.
Round 1 routes refresh, queue and dequeue through the same legacy handler; the
parity tests below cover operator clears, waiting queues, quota and replies.

Bare receipts call `PrepareBareReceipt`: Supplied copies Current from recorded
brief evidence, replacing only its supplied digest. Fresh memberships never
enter this identity. Malformed digest rejections retain at most 1024 UTF-8 bytes,
with 64 retained rejections per actor; subsequent commands record an immutable
capacity outcome and advance the cursor. Mismatch replies explain supplied and
current digests, revision and head. No v1 receipt binding is substituted.
Unapprove binds the authenticated processing revision. Dedupe and evidence
acceptance share a queue-locked transaction and roll back together.

Native-source admission first rejects nonwriters and definite policy exclusions.
A hypothetical stance asks the shared evaluator which requirements the actor
can affect; it never authorizes a landing. Unknown pools remain conservative.
Commands authenticate writers independently of current membership: accepted
receipts and withdrawals retain their revision bindings even while the writer
is outside a pool. Rejoining cannot revive a withdrawn older native approval. Native exact-head fork-trust observations remain available
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
  `TestStageANativeNoiseFilteringRetainsAuthenticatedCommands`; unauthorized
  commands allocate no source pins, while authenticated withdrawals remain
  durable. Typed boundary checks
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
  `TestStageAStaticMappingSwapCannotBorrowPool` rejects a user/team identity
  substituted between the governing read and fresh pool read.
  `TestStageAGoverningAttributesIndependentOfOpenChanges` proves that opening a
  sibling change cannot alter the governing attribute set or existing receipts;
  changing attribute content still refreshes policy. The internal all-attributes
  read now uses targeted `.gitattributes` pathspecs against the immutable tree;
  it does not emit unrelated source names. Attribute/content overflow remains
  unknown, never a partial policy digest. Bounded commit and blob caches reuse
  known governing content and CODEOWNERS/attribute parses.
  `TestGoverningAllAttributesPinnedAndBounded` covers real local Git, unobserved
  directories, moving refs and governing-file overflow.
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
  `TestStageACommandsRetainBindingsAcrossMembershipChanges` proves both receipt
  acceptance and withdrawal retention while a writer is outside a team, followed
  by rejoining; membership never changes the recorded digest.
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

Final results and exact commands follow below. The implementation is a
reviewable series: dormant ingestion/command/gate wiring; integration coverage;
contract documentation; governing-attribute identity; command binding across
membership changes; and the final evidence update. The final evidence commit
changes documentation only. There is no deployment or under-10%-runtime claim.

Earlier nonzero attempts are retained, not reclassified as passes: syntax/import
and lint repairs; a build metadata hook initially reading the pre-start state;
an obsolete run interrupted after edits during compilation; an obsolete hot
millisecond-poll test run terminated and replaced with explicit check wakes;
a test-only batch-size override reset by the governing config read (confirmed
as size 1 before fixing the fixture); the deliberately reproduced attribute-set
regression; and the deliberately reproduced membership/command-binding
regression. An interim replay also exited 2 because a test file was changed
before replay finished; the harness refused a report. The final replay ran on
a clean, unchanged checkout. The first auxiliary replay inspection exited 1
because it incorrectly expected the older deployment's legacy-state hash to
match; the corrected inspection checks exact structural shapes and retains the
raw hash differences. Only processes started by this task were terminated. The final
successful full checks and differentials below supersede those attempts.

## Original pre-review gate results

Verified code/test source: `7626f6ee17cbbc1008ae37809714cfb11e47fd00` (clean in both final replay reports).
The subsequent evidence-only commit leaves this source tree unchanged.
SHA-256 of `git ls-tree -r` over cmd/internal/schema/scripts/tools, module files,
Makefile and lint config: `bcc7377e361fc08a093f5c05f0ac9b902cd3d925686742f0c6254915b444eba6`.

| Final gate | Actual exit | Evidence |
|---|---:|---|
| `make check` | 0 | `check-commands.log`; fmt, lint, race/short tests, build |
| `make test-sim-exits` | 0 | `sim-exits-terminal.log`; required profiles unchanged |
| Compatibility vs main | 0 | 17 legacy exits, 1055 retry/digest records, 84 wire fixtures identical |
| Legacy landing SQL | 0 | 7 statements identical; all five negative controls detected |
| Legacy plans/actions/wakes, depths 1/4 | 0 | 227662 short, 5615998 required-full, 188 fixture checkpoint streams identical |
| Real Git commit IDs | 0 | Squash, merge, rebase, fast-forward tested/landed IDs identical; mandatory self-test passed |
| Strict journal replay vs main | 0 | Gate 2/4 PASS; 2159/2159 actions/state/snapshots; 8676 timer probes; self-test exit 0 |
| Journal replay vs deployed 3f77a88 | 1 | Gate 2 FAIL, Gate 4 PASS; known nil-classifications exception, described below |
| Corrected exact-shape inspection | 0 | 2159 real + 7 synthetic checkpoints checked; no allowlist changes |

The strict main comparison has identical structural state, legacy state and
snapshot hashes at every checkpoint; the synthetic pair has 7/7 checkpoints
and 15 timer probes. Live config SHA-256 in both final reports is
`3efb43bbc05f5f0b03c005331f79064829fa018e9f6446224682e00dd357fead`. No new event type or ConfigApplied is generated by
legacy replay.

The deployed comparison is deliberately reported as raw **exit 1**, not a pass.
All 2159 action checkpoints and 8676 timer probes agree; Gate 4 passes. Its raw
snapshot and legacy-state hashes are 0/2159 because that older state/codec lacks
the added field. Every non-allowlisted structural difference, including the
seven synthetic checkpoints, is exactly `classifications` absent -> a strictly
nil `queue.classifications` map (`nil: true`, `zero: true`, empty value).
The existing reviewed absent-to-nil exceptions for `outsidePushes`,
`approvalFacts`, `approvalWaits`, `receipts` and `landingDecisions` remain intact;
none is populated. The classifications exception is verified separately rather
than added to or weakening the harness allowlist. The deployed-pair self-test
likewise retains exit 1. This is the already-reviewed main-versus-deployed
boundary and does not authorize deployment of this Hold PR.

The final JSON reports are `replay-main-terminal.json` and
`replay-deployed-terminal.json` in the cache directory. The corrected inspection
log is `logs/replay-inspection-corrected.log`; its assertions require the exact
nil-map node, not an empty non-nil map or arbitrary added fields.

## Executed validation commands and actual exit statuses

All commands below ran from `/home/riels/openmerge-sa-11a` with the environment above.
Logs are named by run label in `/home/riels/.cache/openmerge-11a/logs/`.
The command runner captures the child status directly, without a pipeline.

| Run | Exit | Log SHA-256 |
|---|---:|---|
| `compile` | 0 | `42edae6d65dc40a8fcf4ebc07e40d247cd7570da2912d3dbd2e0c14c3e933470` |
| `compile2` | 0 | `0de3d06a7683f42aad00a30722025d4608085cc089b145e920b12f63ffa3945e` |
| `components` | 1 | `546862b77f2384f9e3097e6ebd00a119d0020f5572d3d08636e9dc5b74752c66` |
| `components2` | 1 | `546862b77f2384f9e3097e6ebd00a119d0020f5572d3d08636e9dc5b74752c66` |
| `components3` | 1 | `659bcfd8b4a2c45f9286e4929e455cfe02729b8e9860ed20cbd4c6cedd849f45` |
| `stage-a-first` | 1 | `39ade3b62520b02b185db117a3611a984e53d6e260572d15fdd007be044014ff` |
| `stage-a-second` | 0 | `28fb7f82fc54ad624020f82167c6cb84089d521759a62563279a44c14fe36a11` |
| `stage-a-third` | 1 | `8fe5fb69e9254505a468f0532cd4b7750226b4dccd1366d26c3ed43f2da6e11e` |
| `stage-a-fourth` | 0 | `44f30ceedf92767aec42d834cfcece1f91ece20b5752851339b8952ff04a1b58` |
| `focused` | 0 | `79008227de826ea78beaf3216b3ec7ab8b80c4721c75b8b7d9a8898a158fa3ac` |
| `integration-final` | 0 | `cef7c8a57520f773b4c2e3f9541899eb31ec8bd04dd7f213b8866e27117a9a30` |
| `check-initial` | 2 | `ad14219145f5a3d2edf68bcf6218f24348185bd750d582b8889ca5721de9ba2d` |
| `check` | 2 | `81af251cd7bdf2af2bf21a0804350bcf1ef356ea2db84e5d46fe68a9ad72c70f` |
| `check-final` | 2 | `04d0bf296221132417a93d54e817e221b519e5bc23bcd9e8b068f7b9d5773f0c` |
| `final-identity` | 1 | `cae824d8a88eb431b19510ac3793c358926e715e99245db54626168d0e6b1d57` |
| `composition-diagnostic` | 1 | `bdeafad34efeecb192ba53f1c62058cb292d4c861d7fe967e805c7ee71c5f65e` |
| `composition-fixed` | 0 | `49f4c95fac1bdfec63369de36eaddd4a638a603fab8ea8e1fe1c6f09e1755e7e` |
| `check-stable` | 0 | `7d066029f073d862cd9e10582eab784edbb5278d1f5932caf7bf2b5637afcb36` |
| `sim-exits` | 0 | `26e3d327a41dcc48b576b71ede2513cc7c616b1a345b2f17e2f2acc5fb4fdb15` |
| `compatibility` | 0 | `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749` |
| `legacy-sql` | 0 | `ab22c0b5e090c552fcb505089d132fa81b6793fa330d277ad02890069ccefd33` |
| `legacy-depths` | 0 | `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453` |
| `commit-ids` | 0 | `8d8851653ddbe0eecb722980ef50291eda0db72425e32dfbfa024959281dea3f` |
| `replay-main` | 0 | `3a6ae7bbc166646693736704eeaa429312396027ba9ad653d3391a75731d21c4` |
| `replay-deployed` | 1 | `5c3bda50e1c0a52417618e55597e9cc0708ffe559faf2638992531472b34ddd4` |
| `attribute-regression` | 1 | `6ce0ecd79b8e40c4eb7c65b303b62c5f8bb686571e0e379e7fcfb97561fc208c` |
| `attributes-fixed` | 0 | `cd693ef16c7b9c94da7f240cf0c02e2b3f198120ece11498106081bdd0c018ab` |
| `check-attributes` | 0 | `0947b95e07f63eb77a460936c0b4e5a1365a283557f6f68d4faf91da5de82842` |
| `sim-exits-final` | 0 | `55eacc74f3bba5ef745996f755b3ff0df7e9471e6f7a443d8ee27b4551c73f49` |
| `compatibility-final` | 0 | `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749` |
| `legacy-sql-final` | 0 | `ab22c0b5e090c552fcb505089d132fa81b6793fa330d277ad02890069ccefd33` |
| `legacy-depths-final` | 0 | `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453` |
| `commit-ids-final` | 0 | `72ce4af59b37668bf6e396d66795ff260937cca9ebb4936f4d49624a6ca921b5` |
| `replay-main-final` | 0 | `da4d801a547c633a9c308cbf78ef8f63c6bc52ca7a426e1cd1bac9f96719456f` |
| `replay-deployed-final` | 2 | `57a520787a650c27c88e006a2edef25f590162017702eccafb8c4c89a30f02d9` |
| `membership-command-regression` | 1 | `1ee50a4876df061127553a7bb08d1c9b1b3150e0e9a5eab6e52a06aec2842041` |
| `membership-command-fixed` | 0 | `d4def122407e8623cd028eb06dfd49adb2c463764cc999723c8a6a54e39318ae` |
| `check-commands` | 0 | `e766d0901249f3462def3457885fc8adac5af553318bad6a275d00f98b304256` |
| `sim-exits-terminal` | 0 | `2fc9256d0ce5de1169d24d90a9b72e779073de326496f25016d4e60b350c57e3` |
| `compatibility-terminal` | 0 | `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749` |
| `legacy-sql-terminal` | 0 | `ab22c0b5e090c552fcb505089d132fa81b6793fa330d277ad02890069ccefd33` |
| `legacy-depths-terminal` | 0 | `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453` |
| `commit-ids-terminal` | 0 | `0f1c03e703e05c6407da7ef1a823d2262964f91d7beb91073d956a97bdb17b9d` |
| `replay-main-terminal` | 0 | `b683bcef2f2b8f1fba1a9041a2e591525cebcbc293f54961cc2f5786b75cef71` |
| `replay-deployed-terminal` | 1 | `9614bec751be683dc3b9e1bd9f7ceeaa8ae9c386c005307306d6132580befb27` |
| `replay-inspection-terminal` | 1 | `e6ecaafe9b0303583f6c4c76e6a058a973a78b3f8c7797015b949732a810023b` |
| `replay-inspection-corrected` | 0 | `0b554123b04ecef4cc41fb09ede76a5a35e559cad1507ffebe8b96a3ebb68144` |

```sh
# compile
go test ./internal/serve ./internal/store -run '^$' > /home/riels/.cache/openmerge-11a/logs/compile.log 2>&1; echo exit=$?
# actual exit=0

# compile2
go test ./internal/serve ./internal/store -run '^$' > /home/riels/.cache/openmerge-11a/logs/compile2.log 2>&1; echo exit=$?
# actual exit=0

# components
go test ./internal/queue ./internal/executor ./internal/status ./internal/serve ./internal/store > /home/riels/.cache/openmerge-11a/logs/components.log 2>&1; echo exit=$?
# actual exit=1

# components2
go test ./internal/queue ./internal/executor ./internal/status ./internal/serve ./internal/store > /home/riels/.cache/openmerge-11a/logs/components2.log 2>&1; echo exit=$?
# actual exit=1

# components3
go test ./internal/queue ./internal/executor ./internal/status ./internal/serve ./internal/store > /home/riels/.cache/openmerge-11a/logs/components3.log 2>&1; echo exit=$?
# actual exit=1

# stage-a-first
go test ./internal/serve -run '^TestStageA' -count=1 -v > /home/riels/.cache/openmerge-11a/logs/stage-a-first.log 2>&1; echo exit=$?
# actual exit=1

# stage-a-second
go test ./internal/serve -run '^TestStageA' -count=1 -v > /home/riels/.cache/openmerge-11a/logs/stage-a-second.log 2>&1; echo exit=$?
# actual exit=0

# stage-a-third
go test ./internal/serve -run '^TestStageA' -count=1 -v > /home/riels/.cache/openmerge-11a/logs/stage-a-third.log 2>&1; echo exit=$?
# actual exit=1

# stage-a-fourth
go test ./internal/serve -run '^TestStageA' -count=1 -v > /home/riels/.cache/openmerge-11a/logs/stage-a-fourth.log 2>&1; echo exit=$?
# actual exit=0

# focused
go test ./internal/serve ./internal/queue ./internal/status ./internal/executor ./internal/store -count=1 > /home/riels/.cache/openmerge-11a/logs/focused.log 2>&1; echo exit=$?
# actual exit=0

# integration-final
go test ./internal/serve ./internal/queue ./internal/executor ./internal/status -run 'TestStageA|TestR3ApprovalThenOlderReviewDismissedLater|TestNextStepMachineValues|TestLoadBuildMetadata' -count=1 -v > /home/riels/.cache/openmerge-11a/logs/integration-final.log 2>&1; echo exit=$?
# actual exit=0

# check-initial
make check > /home/riels/.cache/openmerge-11a/logs/check-initial.log 2>&1; echo exit=$?
# actual exit=2

# check
make check > /home/riels/.cache/openmerge-11a/logs/check.log 2>&1; echo exit=$?
# actual exit=2

# check-final
make check > /home/riels/.cache/openmerge-11a/logs/check-final.log 2>&1; echo exit=$?
# actual exit=2

# final-identity
go test ./internal/serve ./internal/queue ./internal/classification -run 'TestStageA|TestClassificationEmitter' -count=1 -v > /home/riels/.cache/openmerge-11a/logs/final-identity.log 2>&1; echo exit=$?
# actual exit=1

# composition-diagnostic
go test ./internal/serve -run '^TestStageACompositionRejectsOnlyUnapprovedMember$' -count=1 -v > /home/riels/.cache/openmerge-11a/logs/composition-diagnostic.log 2>&1; echo exit=$?
# actual exit=1

# composition-fixed
go test ./internal/serve -run '^TestStageACompositionRejectsOnlyUnapprovedMember$' -count=1 -v > /home/riels/.cache/openmerge-11a/logs/composition-fixed.log 2>&1; echo exit=$?
# actual exit=0

# check-stable
make check > /home/riels/.cache/openmerge-11a/logs/check-stable.log 2>&1; echo exit=$?
# actual exit=0

# sim-exits
make test-sim-exits > /home/riels/.cache/openmerge-11a/logs/sim-exits.log 2>&1; echo exit=$?
# actual exit=0

# compatibility
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-11a-main > /home/riels/.cache/openmerge-11a/logs/compatibility.log 2>&1; echo exit=$?
# actual exit=0

# legacy-sql
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-11a-main > /home/riels/.cache/openmerge-11a/logs/legacy-sql.log 2>&1; echo exit=$?
# actual exit=0

# legacy-depths
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-11a-main > /home/riels/.cache/openmerge-11a/logs/legacy-depths.log 2>&1; echo exit=$?
# actual exit=0

# commit-ids
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a > /home/riels/.cache/openmerge-11a/logs/commit-ids.log 2>&1; echo exit=$?
# actual exit=0

# replay-main
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-11a/replay-main.json > /home/riels/.cache/openmerge-11a/logs/replay-main.log 2>&1; echo exit=$?
# actual exit=0

# replay-deployed
python3 scripts/check-live-journal-replay.py --baseline /home/riels/.cache/sa9b-r2-deployed --candidate /home/riels/openmerge-sa-11a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --out /home/riels/.cache/openmerge-11a/replay-deployed.json > /home/riels/.cache/openmerge-11a/logs/replay-deployed.log 2>&1; echo exit=$?
# actual exit=1

# attribute-regression
go test ./internal/serve -run '^TestStageAGoverningAttributesIndependentOfOpenChanges$' -count=1 -v > /home/riels/.cache/openmerge-11a/logs/attribute-regression.log 2>&1; echo exit=$?
# actual exit=1

# attributes-fixed
go test ./internal/serve ./internal/gitops ./internal/forge/fake -run 'TestStageA|TestGoverningAllAttributesPinnedAndBounded|TestGoverningFilesContract' -count=1 -v > /home/riels/.cache/openmerge-11a/logs/attributes-fixed.log 2>&1; echo exit=$?
# actual exit=0

# check-attributes
make check > /home/riels/.cache/openmerge-11a/logs/check-attributes.log 2>&1; echo exit=$?
# actual exit=0

# sim-exits-final
make test-sim-exits > /home/riels/.cache/openmerge-11a/logs/sim-exits-final.log 2>&1; echo exit=$?
# actual exit=0

# compatibility-final
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-11a-main > /home/riels/.cache/openmerge-11a/logs/compatibility-final.log 2>&1; echo exit=$?
# actual exit=0

# legacy-sql-final
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-11a-main > /home/riels/.cache/openmerge-11a/logs/legacy-sql-final.log 2>&1; echo exit=$?
# actual exit=0

# legacy-depths-final
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-11a-main > /home/riels/.cache/openmerge-11a/logs/legacy-depths-final.log 2>&1; echo exit=$?
# actual exit=0

# commit-ids-final
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a > /home/riels/.cache/openmerge-11a/logs/commit-ids-final.log 2>&1; echo exit=$?
# actual exit=0

# replay-main-final
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-11a/replay-main-final.json > /home/riels/.cache/openmerge-11a/logs/replay-main-final.log 2>&1; echo exit=$?
# actual exit=0

# replay-deployed-final
python3 scripts/check-live-journal-replay.py --baseline /home/riels/.cache/sa9b-r2-deployed --candidate /home/riels/openmerge-sa-11a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --out /home/riels/.cache/openmerge-11a/replay-deployed-final.json > /home/riels/.cache/openmerge-11a/logs/replay-deployed-final.log 2>&1; echo exit=$?
# actual exit=2

# membership-command-regression
go test ./internal/serve -run '^TestStageACommandsRetainBindingsAcrossMembershipChanges$' -count=1 -v > /home/riels/.cache/openmerge-11a/logs/membership-command-regression.log 2>&1; echo exit=$?
# actual exit=1

# membership-command-fixed
go test ./internal/serve -run '^TestStageA' -count=1 -v > /home/riels/.cache/openmerge-11a/logs/membership-command-fixed.log 2>&1; echo exit=$?
# actual exit=0

# check-commands
make check > /home/riels/.cache/openmerge-11a/logs/check-commands.log 2>&1; echo exit=$?
# actual exit=0

# sim-exits-terminal
make test-sim-exits > /home/riels/.cache/openmerge-11a/logs/sim-exits-terminal.log 2>&1; echo exit=$?
# actual exit=0

# compatibility-terminal
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-11a-main > /home/riels/.cache/openmerge-11a/logs/compatibility-terminal.log 2>&1; echo exit=$?
# actual exit=0

# legacy-sql-terminal
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-11a-main > /home/riels/.cache/openmerge-11a/logs/legacy-sql-terminal.log 2>&1; echo exit=$?
# actual exit=0

# legacy-depths-terminal
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-11a-main > /home/riels/.cache/openmerge-11a/logs/legacy-depths-terminal.log 2>&1; echo exit=$?
# actual exit=0

# commit-ids-terminal
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a > /home/riels/.cache/openmerge-11a/logs/commit-ids-terminal.log 2>&1; echo exit=$?
# actual exit=0

# replay-main-terminal
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-11a/replay-main-terminal.json > /home/riels/.cache/openmerge-11a/logs/replay-main-terminal.log 2>&1; echo exit=$?
# actual exit=0

# replay-deployed-terminal
python3 scripts/check-live-journal-replay.py --baseline /home/riels/.cache/sa9b-r2-deployed --candidate /home/riels/openmerge-sa-11a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --out /home/riels/.cache/openmerge-11a/replay-deployed-terminal.json > /home/riels/.cache/openmerge-11a/logs/replay-deployed-terminal.log 2>&1; echo exit=$?
# actual exit=1

# replay-inspection-terminal
python3 /home/riels/.cache/openmerge-11a/inspect-replays.py /home/riels/.cache/openmerge-11a/replay-main-terminal.json /home/riels/.cache/openmerge-11a/replay-deployed-terminal.json > /home/riels/.cache/openmerge-11a/logs/replay-inspection-terminal.log 2>&1; echo exit=$?
# actual exit=1

# replay-inspection-corrected
python3 /home/riels/.cache/openmerge-11a/inspect-replays.py /home/riels/.cache/openmerge-11a/replay-main-terminal.json /home/riels/.cache/openmerge-11a/replay-deployed-terminal.json > /home/riels/.cache/openmerge-11a/logs/replay-inspection-corrected.log 2>&1; echo exit=$?
# actual exit=0

```


## Round 1 review fixes

Trial class remains **Hold**. This round addresses B1–B5 and S1–S3. The original
pre-review results above remain historical evidence, not validation of this round.
Only main and this branch were fetched, using explicit refspecs (exit 0).
`origin/main` remains `f8066a4f3bc9f2bfb110950b0c6c9aea4cf5ea6f`; no rebase was needed.

- B1: The final native response is ingested with its attributed review and
  approval-command sources before evaluation. The gate reloads the durable
  fold; atomic start rederives every decision against that journal. A differing
  fold rejects the start. `TestStageAFreshNegativeStancesBeforeStart` covers
  request-changes, dismissal and unapprove at the final read and in a journal
  race immediately before start, with zero starts in every case.
- B2: Build metadata validates the refreshed classification base by ancestry
  against the current queue base, using the same predicate as ingestion/gating.
  `TestStageABuildMetadataSurvivesRewriteThenForwardMove` covers A→C, rewrite to
  sibling B, then B→D. Pure-loader tests also reject unrelated bases and
  ancestry-read errors.
- B3: The strict metadata parser reads structurally selected generated member
  commits only. Contributor messages, including valid forged pairs, are not
  metadata authority. All four methods are covered by
  `TestStageAGateIgnoresContributorReservedTextForEveryMethod`.
- B4: Transient WriteGrant errors return before consuming a command. Only
  definite identity failures and an authenticated nonwriter produce denial.
  `TestStageACommandTransientWriteGrantIsRetried` checks 503 followed by success.
- B5: refresh/queue/dequeue reuse the legacy command handler. Their quota,
  operator clears, waiting-queue behavior, immutable outcomes and replies are
  shared; only approve/unapprove extend the grammar.
  `TestStageALegacyCommandsMatchLegacyPrimitives` compares ordered event
  payloads, all four operator-clear block states and replies.
  `TestLegacyCommandWaitingStopsSharedPass` checks the inclusive cursor and
  subsequent comments; `TestLegacyAndStageACommandsHonorQuotaWithoutConsumption`
  and `TestStageAApprovalCommandsHonorQuotaWithoutConsumption` check backoff.
- S1: A typed approval-wait outcome stops the executor before CI provenance,
  refresh, reopen, retry or timer handling. RefreshChecks is tested through
  `s.execute` at retry_failed 0 and 1 in
  `TestStageAExecuteApprovalWaitPrecedesCheckEvents`, for RefreshChecks and Land.

- S2: Definite configured/governing user or team lookup failures, including
  unreadable team membership, create a durable ConfigSource block before any
  base observation. Recovery retains the basis-checked clear. Transport, rate
  limit and incomplete-membership failures abort the cycle, set the in-memory
  pause, and append no configuration/base fallback.
  `TestStageADefiniteIdentityFailureBlocksAcrossBaseMove` and
  `TestStageATeamMembershipFailuresDifferentiateDurability` cover moved and
  unmoved bases, lookup versus membership, and definite versus transient errors.
- S3: The Stage A governing-content LRU holds 32 commit/path request entries;
  its parsed CODEOWNERS/attributes LRU holds 128 immutable blob entries. Neither
  stores grants or memberships. Only complete content is cached. Complete
  classifications with unchanged immutable inputs skip diff/governing reads;
  a forward base pin remains valid, while a rewrite refreshes. A new governing
  provenance is journaled at the same semantic generation before later polls
  skip it. Incomplete evidence is retried.
  `TestStageAGoverningCacheReusesPinnedPolicyContent`,
  `TestStageAForwardBaseThenUnchangedPollSkipsImmutableReads`,
  `TestStageANativeNoiseUsesOneStaticPoolRead` and
  `TestGoverningCacheBoundsParsedBlobsAndPreservesAbsence` pin these bounds.
  Noise filtering shares one fresh eligibility input across all reviews in its
  source pass. Entry expressions without review terms skip eligibility reads.

The mutable PR-summary skip remains disabled on the isolated candidate path.
A summary does not prove stable author identity/accountability, title/body
intent, fork-trust observations, native review dismissals or issue comments;
permission and team membership changes have no PR-summary version at all.
These reads remain fresh: configuration resolves static identities/team
readability, observation acquires PR facts and attributed native stances,
commands acquire complete comment evidence, and decision-time eligibility
refreshes grants/pools. Reconciliation repeats metadata/native reads per PR,
not per review. Native noise uses one group/pool read for its entire review
pass (the 20-review test asserts author plus one static-group lookup). The
immutable diff/content/parse work is skipped or cached as described above.
Legacy queues retain the original unchanged-summary skip.

Repository-wide attribute identity still includes attribute files outside open
diffs, so opening a sibling PR cannot change an existing receipt. A targeted
`diff-tree` against the empty tree emits only `.gitattributes` matches; per-PR
probes read CODEOWNERS candidates, policy files and changed-path ancestors.
`TestGoverningLargeTreeDoesNotPause` uses 50,001 source files whose names alone
exceed the old 2 MiB listing budget, plus nested/empty attribute files. It
returns complete governing evidence.
`TestGoverningTargetedInventoryRejectsAttributeDirectory` preserves fail-closed
special-mode handling.

The first old-reader negative control unexpectedly exited 0. Investigation
found that embedding bytes.Buffer promoted ReadFrom, allowing os/exec's
io.Copy fast path to bypass the bounded Write method. The buffer now uses
composition; `TestDiffOutputBudgetAppliesToPipeCopies` checks both io.Copy and
an actual git subprocess. With that bound enforced, the old recursive reader
negative control exits 1 on this large tree, while the targeted reader exits 0.
This fixes bounded memory rather than enlarging the cap.

Round-1 commands, every actual exit status and log hashes are recorded below. Cache root: `/home/riels/.cache/openmerge-77-r1`. Heavy jobs
use the same Postgres URL and limits as above, with GOTMPDIR/TMPDIR under that
cache root and GOCACHE under `~/.cache/go-build`. No live export is repeated.

Validation process exception: despite the root's instruction to leave all heavy
jobs to its serialized runner, the command helper reported two direct
`go build ./internal/executor ./internal/serve` invocations (both exit 0).
One was part of a gofmt/diff-check compound command lasting 0.171528285 seconds;
no timestamp/log was retained for the standalone build. They used the default
environment without a host-load wait or explicit GOFLAGS/GOMEMLIMIT/cache
settings. This violates the requested load procedure and is recorded here,
not treated as required gate evidence. All root-run required gates use the
serialized runner and the specified environment.

## Round-1 final gate results

Validated source commit: `ca1c0924e60cb46f09e77db5360d5a2dd93d8f7b`. The final evidence commit changes
only this document. SHA-256 of `git ls-tree -r` over cmd/internal/schema/scripts/
tools, module files, Makefile and lint configuration:
`82a62737434b8546af4fed355269e160215642405509b30bee6968d706338769`.

The final explicit fetch of main and this branch exited 0; main remains
`f8066a4f3bc9f2bfb110950b0c6c9aea4cf5ea6f`, so no rebase was required. The
baseline worktree remains clean. The source commit and exact author/trailer
verification exited 0. Simulator and compatibility were reconfirmed on the
clean source commit after all code changes.

| Gate | Actual exit | Result |
|---|---:|---|
| make check (`check-final`) | 0 | fmt/lint, full race/short suite, build |
| make test-sim-exits (`sim-exits-final`) | 0 | required simulator profiles pass |
| compatibility (`compatibility-final`) | 0 | 17 exits, 1055 retry/digest records, 84 wire fixtures identical |
| legacy SQL | 0 | seven statements identical; all five negative controls detected |
| depth-1/4 differential | 0 | 227662 short + 5615998 full checkpoints, 188 fixture streams identical |
| real Git commit IDs | 0 | tested/landed IDs identical for all four methods; self-tests pass |
| strict journal replay vs main | 0 | Gate 2/4 PASS; 2159 actions/state/snapshots identical; 8676 timer probes |
| strict journal replay vs 3f77a88 | 1 | Gate 2 FAIL / Gate 4 PASS; known absent-to-nil classifications boundary |
| exact replay-shape inspection | 0 | 2159 real + 7 synthetic checkpoints per pair; no allowlist changes |

Both replay reports identify clean source `ca1c092`. Main's mandatory synthetic
self-tests exit 0 (22 tests); its seven synthetic checkpoints and 15 timer probes
match exactly. The deployed-pair self-tests retain raw exit 1 for the same
known classifications boundary. Both deployed real and synthetic action/wake
streams match; raw snapshot hashes are 0/2159 and 0/7 because the older codec
lacks the field. Exact-shape inspection verifies that every non-allowlisted
structural difference is only classifications absent → strictly nil, zero and
empty. The five existing nil-only exceptions are unchanged. No gate or self-test
failure is relabeled as a pass. These results preserve legacy equivalence
against main and do not authorize deployment of this Hold PR.

## Round-1 complete command ledger

Logs and JSON reports are under `/home/riels/.cache/openmerge-77-r1/`. The
runner captures each child status directly. The two helper builds are reported
separately because no raw log was retained and the required load wrapper was
not used. Initial failures remain in this ledger: fixture/import repairs; a
missing immutable-read skip; lint cleanup; and the deliberately failing old
reader negative control after the output-bound fix.

| Run | Actual exit | Log SHA-256 |
|---|---:|---|
| `focused-first` | 1 | `9d0e6062cf23dde6e7bdb2c8cff65186a52848bfce17ba3391505137991d48b4` |
| `build-executor` | 0 | `0a9027f23b60d9884e14d874f88fca6028aa586fc3e5bd22550964d2607918d5` |
| `focused-second` | 1 | `c085d693beef90362c2258e3d56b078638bd42245d71eee88685814564fe6270` |
| `gate-regressions` | 1 | `703d1930191cc67e2c90018af4aaf01b656855bd8cb491f1537090aaea86cc67` |
| `focused-third` | 1 | `0eff900ee5670f73739d42fb116c425e74fca01d0cd7b272d046c8e44cc7f034` |
| `command-executor` | 0 | `a255fd67ec191649a23a7d1cbc15f539638b39a9229644c9753ea1db788e728e` |
| `sim-exits` | 0 | `082deb53a7e49b1ce6c61aa13d59c11e81b0101223ad348e375d4c4e630bfa0f` |
| `helper-build-standalone` | 0 | `helper-reported; no retained log` |
| `helper-build-compound` | 0 | `helper-reported; no retained log` |
| `governing-git` | 0 | `f2e2c5ab06b31e164f88037e7812450cb892acc102ff6f2b7381759675bce7a1` |
| `compatibility` | 0 | `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749` |
| `large-tree` | 0 | `4313acd361a910d22e4c513cd7ed1a00b01cb406cdaabef6fbe6d61e9ce672f9` |
| `large-tree-negative` | 0 | `5054a1dbe58c13289a4fb94eeb6efd6207aa5e4a148bb5a662ccb41435a80b15` |
| `large-tree-bounded` | 0 | `f1e387d89042fbb19efcd3e059db4c1189b18578bc6cfe595a058a5e2e83c6f3` |
| `large-tree-negative-bounded` | 1 | `b6f44ada8a46f2843e94784f2d628a4fba27a887701dbc74f17abc2202f9564f` |
| `focused-complete` | 1 | `f5c8a2b585040831a17568aae9345e4706b07f3a08a7aac1c02232d92fa1e887` |
| `focused-final` | 0 | `cffb4a31f6577fb01859ac0dc305f007d948e88e85aa8a1cb2da038294ad10fd` |
| `check` | 2 | `5f4706499f8ac769e6dcf1e3e68507eea635781a7fbd5698dd7f26025f56e4aa` |
| `check-final` | 0 | `f986d43fe905d88621dd4d4e08c8c016d8525d025576784980da326cf66029b7` |
| `legacy-sql` | 0 | `ab22c0b5e090c552fcb505089d132fa81b6793fa330d277ad02890069ccefd33` |
| `legacy-depths` | 0 | `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453` |
| `commit-ids` | 0 | `517dde6fba0319ba59f1f2a253763dd67ca88d037d8401c31273babec57ffb54` |
| `replay-main` | 0 | `49a2002c091ec1da831ab7e5fe54a3c29c6797ad65707ef23a15614b3b5d320d` |
| `replay-deployed` | 1 | `4867647cfbbd48b85a742288eba456246762d4ea42907259d22d1cbb01c8839f` |
| `replay-inspection` | 0 | `0b554123b04ecef4cc41fb09ede76a5a35e559cad1507ffebe8b96a3ebb68144` |
| `sim-exits-final` | 0 | `5c829ea42ab86fcbb0d8e5926c9626aff9d291730f5af478d31091a29be19d2b` |
| `compatibility-final` | 0 | `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749` |

Exact underlying commands follow; all root-run commands use the runner environment
described above. The two helper builds used the default environment as disclosed.

```sh
# focused-first: actual exit=1
go test ./internal/serve ./internal/executor -run 'TestStageA|TestLoadBuildMetadata|TestRefreshChecksApprovalWait' -count=1 -v

# build-executor: actual exit=0
go test ./internal/executor ./internal/gitops -run 'TestLoadBuildMetadata|TestFirstParentCommits|TestRefreshChecksApprovalWait' -count=1 -v

# focused-second: actual exit=1
go test ./internal/serve -run 'TestStageA|TestLegacyCommandWaiting' -count=1 -v

# gate-regressions: actual exit=1
go test ./internal/serve -run 'TestStageAFreshNegativeStancesBeforeStart|TestStageABuildMetadataSurvivesRewriteThenForwardMove|TestStageAGateIgnoresContributorReservedTextForEveryMethod|TestStageAGateRejectsMalformedGeneratedMemberMetadata' -count=1 -v

# focused-third: actual exit=1
go test ./internal/serve -run 'TestStageA|TestLegacyCommandWaiting' -count=1 -v

# command-executor: actual exit=0
go test ./internal/serve ./internal/executor -run 'TestStageACommand|TestStageALegacyCommands|TestLegacyCommandWaiting|TestLegacyAndStageACommands|TestStageAApprovalCommands|TestStageAExecuteApprovalWait|TestRefreshChecksApprovalWait' -count=1 -v

# sim-exits: actual exit=0
make test-sim-exits

# helper-build-standalone: actual exit=0 (helper-reported)
go build ./internal/executor ./internal/serve

# helper-build-compound: actual exit=0 (helper-reported)
go build ./internal/executor ./internal/serve

# governing-git: actual exit=0
go test ./internal/gitops ./internal/classification -run 'Governing|FirstParent' -count=1 -v

# compatibility: actual exit=0
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-11a-main

# large-tree: actual exit=0
go test ./internal/gitops -run 'TestGoverningLargeTreeDoesNotPause|TestGoverningTargetedInventoryRejectsAttributeDirectory' -count=1 -v

# large-tree-negative: actual exit=0
go test -overlay /home/riels/.cache/openmerge-77-r1/large-tree-negative.json ./internal/gitops -run '^TestGoverningLargeTreeDoesNotPause$' -count=1 -v

# large-tree-bounded: actual exit=0
go test ./internal/gitops -run 'TestGoverningLargeTreeDoesNotPause|TestGoverningTargetedInventoryRejectsAttributeDirectory|TestDiffOutputBudgetAppliesToPipeCopies' -count=1 -v

# large-tree-negative-bounded: actual exit=1
go test -overlay /home/riels/.cache/openmerge-77-r1/large-tree-negative.json ./internal/gitops -run '^TestGoverningLargeTreeDoesNotPause$' -count=1 -v

# focused-complete: actual exit=1
go test ./internal/serve ./internal/executor ./internal/gitops ./internal/classification -run 'TestStageA|TestLoadBuildMetadata|TestApprovalWaitOutcome|TestGoverning|TestFirstParent|TestDiffOutputBudget|TestLegacyCommand|TestLegacyAndStageACommands' -count=1 -v

# focused-final: actual exit=0
go test ./internal/serve ./internal/executor ./internal/gitops ./internal/classification -run 'TestStageA|TestLoadBuildMetadata|TestApprovalWaitOutcome|TestGoverning|TestFirstParent|TestDiffOutputBudget|TestLegacyCommand|TestLegacyAndStageACommands' -count=1 -v

# check: actual exit=2
make check

# check-final: actual exit=0
make check

# legacy-sql: actual exit=0
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-11a-main

# legacy-depths: actual exit=0
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-11a-main

# commit-ids: actual exit=0
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a

# replay-main: actual exit=0
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-77-r1/replay-main.json

# replay-deployed: actual exit=1
python3 scripts/check-live-journal-replay.py --baseline /home/riels/.cache/sa9b-r2-deployed --candidate /home/riels/openmerge-sa-11a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-77-r1/replay-deployed.json

# replay-inspection: actual exit=0
python3 /home/riels/.cache/openmerge-11a/inspect-replays.py /home/riels/.cache/openmerge-77-r1/replay-main.json /home/riels/.cache/openmerge-77-r1/replay-deployed.json

# sim-exits-final: actual exit=0
make test-sim-exits

# compatibility-final: actual exit=0
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-11a-main

```

## Round 2 review fixes

Trial class remains **Hold**; activation is still fixture-only. Round 2 starts
from `da37919fdcaab92b68b3955219d35b4cdd34f073`. An explicit fetch of only main
and `stage-a/11a-shadow-gate` exited 0; main remains
`f8066a4f3bc9f2bfb110950b0c6c9aea4cf5ea6f`.

- **B1:** Every native-source pass now records its complete eligibility view
  through the semantic observation dedupe transaction, including missing native
  approvals, denied/unknown grants, and changed team/owner pools. New stances
  (request-changes/dismissal/unapprove) retain their attributed journal path.
  The durable fingerprint and eligibility acquisition basis fence the locked
  atomic start; a later changed observation makes an older gate stale, including
  permission revoke-then-restore. Unchanged polls append nothing. Delayed input
  ingestion cannot overwrite newer durable eligibility. No forge call runs under
  the queue lock. `TestStageAFreshNegativeStancesBeforeStart` now exercises
  withdrawal, revoked-writer, team-removal and grant-ABA as well as the original
  request-changes/dismissal/unapprove cases. The before-start cases pause the
  first worker's gate while a second worker polls in a goroutine and commits to
  the real isolated Postgres schema; neither LandingStarted nor Landed is allowed.
- **B2:** `GoverningCache.TreeDeriver` admits only definite, complete governing
  files into the immutable blob parse cache. The regression
  `TestGoverningCacheIncompleteThenCompleteSameBlob` first reads present,
  object-ID-bearing but incomplete empty CODEOWNERS/attributes, then reads both
  complete at the same object IDs and verifies the real parse products.
- **B3:** A pass-local grant lookup memoizes results by immutable reviewer ID,
  shared by stance admission and eligibility. It is discarded after the pass;
  the next pass re-reads permissions even after denial. Stale-head positive
  reviews are excluded before any permission lookup. The call-count regression
  `TestStageANativeGrantCallsPerDistinctReviewer` checks 1 and 30 approvals each
  for 1 and 3 reviewers, plus stale-head noise: exactly one grant call per current
  reviewer on each of two polls, with revocation between polls.
- **S1:** The real GitHub adapter normalizes user 404 and ordinary team/member
  403/404 to ErrIdentityUnavailable after rate-limit normalization. Rate-limited
  403/429, 5xx and timeouts remain transient. The 48-case real-driver loopback
  matrix `TestStageAHTTPIdentityFailuresBlockOrRetry` covers user/team/member
  reads, 404, ordinary 403, Retry-After, exhausted quota, secondary-rate-limit
  message, 429, 503 and timeout; each runs with and without base movement and
  verifies durable block versus retry plus readable recovery.
- **Nits:** Removed the unreachable command default arm. Governing content cache
  keys now include repository path. Existing 32-content/128-parse-entry LRUs
  retain at most 32 MiB/128 MiB of governing source input respectively under
  MaxGoverningBytes (1 MiB per acquisition/blob); parsed matchers, tokens and Go
  object overhead are additional, bounded by parser limits and entry count.
  These are source-byte bounds, not a claim of a measured heap ceiling.
  Native responses are reused within source attribution and the final gate.
  The ordinary command pass and authenticated command refresh can still read
  NativeReviews separately: the latter deliberately rechecks the current PR
  before binding a command. Final landing also takes its own fresh read.

Round-2 runs, exact commands, resource environment and log hashes follow below.
Historical round-1 results above do not substitute for this round's gates.

Implementation commit: `eae7fd6753a4d5161f7a5490b494180aa04d0440`.
All remaining source validation runs target that implementation; the final
follow-up commit changes only this evidence file.

### Round-2 run discipline

Logs and machine-readable exits are retained under
`/home/riels/.cache/openmerge-77-r2/` (`results.jsonl`, `logs/`). The runner waits
for other Go test/build executables and journal/differential jobs before every
heavy job. A Go launcher additionally checks before each helper `go test` or
`go build`; a Python subprocess guard checks before direct helper `.test`
executables. The guard excludes descendants of the current heavy-job owner so
normal `GOFLAGS=-p=2` package execution is one job rather than a deadlock. There
were no delegated helper builds or direct builds outside the runner this round.

Every job inherits `GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB` (`6GiB` for both replays),
`GOTMPDIR=/home/riels/.cache/openmerge-77-r2/gotmp`,
`GOCACHE=/home/riels/.cache/go-build`, and the supplied shared-test-Postgres URL.
`TMPDIR` also points under that cache. No initdb was run, no process was killed,
and no live DB/status/units/remote OpenMerge refs were touched. The replay reads
only the existing saved JSONL export; it does not query the live service.

The first focused run exited 1 for an omitted store import, corrected before
`focused-2` (exit 0). The first `make check` exited 2 on test lint findings
(unchecked HTTP response writes and copying a worker mutex), corrected before
`check-2` (exit 0). The race-enabled HTTP/atomic-start matrix exited 0. After
strengthening the cache test from parse-product inspection to its Generated
behavior, the full final check was rerun. These failed attempts remain in the
ledger rather than being replaced by successful runs.

### Round-2 final results

Legacy equivalence: **PASS** against unchanged `origin/main` `f8066a4`.
Final explicit-refspec, no-tags fetch exited 0; main and the remote PR branch
were still `f8066a4` and `da37919`. No rebase was needed. The clean implementation
source digest reported by the commit-ID harness is
`ec5c5db479d3632e151f28d2c47142abf217c59e1d238585a963b56247485d88`.

- Final `make check`: exit 0 (fmt, lint, complete race/short suite, build).
- Astra's unchanged original withdrawal/revoked-writer overlay probes: exit 0;
  all four final-read/before-start cases reject, including both original races.
- `make test-sim-exits`: exit 0.
- Compatibility: exit 0; 17 legacy exits, 1055 retry/digest records and all
  84 wire fixtures are byte-identical.
- Legacy SQL: exit 0; seven statements identical, with all five negative
  controls detected (each control's probe exits 0 and the comparison rejects
  its deliberately different trace).
- Depth-1/4: exit 0; 227662 short-corpus checkpoints, 5615998 required-full
  checkpoints and 188 fixture streams identical. No nil-only exceptions.
- Four-method commit-ID differential: exit 0; tested and landed SHAs identical
  for squash, merge, rebase and fast-forward. Mandatory self-tests return
  0 for identical, 1 for mutant and 2 for dirty, exactly their expected statuses.
- Strict journal replay vs main: exit 0; 2159/2159 actions, boundaries and
  snapshot hashes identical, plus all seven synthetic checkpoints.
- Strict replay vs `3f77a88`: raw exit 1, retained rather than normalized.
  Exact-shape inspection exits 0: actions/boundaries match at all 2159 real
  and seven synthetic checkpoints; the only non-allowlisted difference remains
  classifications absent -> strictly nil. Gate 4 passes. No exception allowlist
  was changed. The existing five known exceptions remain unchanged.

### Round-2 complete command/exit ledger

Every entry below is the actual root job status, including failed attempts.
The replay memory limit is 6GiB; all other jobs use 4GiB. All jobs inherit the
runner environment and guards described above. Logs use the entry name plus
`.log` in the round-2 cache's `logs/` directory.

- `focused`: exit **1**, GOMEMLIMIT `4GiB`; log SHA-256 `09ef058d910e19da4cc35f2246bba4e910dcf51614445d9b0ba881e754546b9e`.
- `focused-2`: exit **0**, GOMEMLIMIT `4GiB`; log SHA-256 `ce819181ebde403aa5032d22520e7b3a9946e4a788d878b57f223f87faf2f8c6`.
- `check`: exit **2**, GOMEMLIMIT `4GiB`; log SHA-256 `4c685ca969c820e07c6b6b3dfe0aba2678ed61184a260e05a351a1947b4c1b93`.
- `http-races`: exit **0**, GOMEMLIMIT `4GiB`; log SHA-256 `c87c433d2169e8b2e329a357a93973add9d581d031982527f96d70de09972f9a`.
- `check-2`: exit **0**, GOMEMLIMIT `4GiB`; log SHA-256 `ad78204e2de09acc70d2c5e3c9880baec98d425f435a6f9ce682ed2b25bad916`.
- `check-final`: exit **0**, GOMEMLIMIT `4GiB`; log SHA-256 `0e51f831a9e42aa974d525d39cad8de4b2a83b377d4f6b08f2444497fc6530f8`.
- `astra-races`: exit **0**, GOMEMLIMIT `4GiB`; log SHA-256 `8ed4b587e0b1c9c5cbc754bceb24c20ea2d88cc8d102cc6a80c1fa635cfc0f66`.
- `sim-exits`: exit **0**, GOMEMLIMIT `4GiB`; log SHA-256 `fa326e54b83fca0da733360279844be5b29a02490b5827494bd370ed8f461083`.
- `compatibility`: exit **0**, GOMEMLIMIT `4GiB`; log SHA-256 `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749`.
- `legacy-sql`: exit **0**, GOMEMLIMIT `4GiB`; log SHA-256 `ab22c0b5e090c552fcb505089d132fa81b6793fa330d277ad02890069ccefd33`.
- `legacy-depths`: exit **0**, GOMEMLIMIT `4GiB`; log SHA-256 `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453`.
- `commit-ids`: exit **0**, GOMEMLIMIT `4GiB`; log SHA-256 `c940efadee4a771d9b9848ed8edeed910b4be073c408cae7655a904547bfd6a3`.
- `replay-main`: exit **0**, GOMEMLIMIT `6GiB`; log SHA-256 `3f070e644fe208ea05d322c825b87add42a43258093208965cdfad470a96d55e`.
- `replay-deployed`: exit **1**, GOMEMLIMIT `6GiB`; log SHA-256 `2af215135ca9939ca198d96443bc2300cf6ab7f81c60826d4823b7e2d5f4dfea`.
- `replay-inspection`: exit **0**, GOMEMLIMIT `4GiB`; log SHA-256 `0b554123b04ecef4cc41fb09ede76a5a35e559cad1507ffebe8b96a3ebb68144`.

Exact commands (executed through the host-load runner):

```sh
# focused: actual exit=1, GOMEMLIMIT=4GiB
go test ./internal/serve ./internal/classification ./internal/forge/github -run 'TestStageA|TestGoverningCache|TestIdentity|TestTeamMembership' -count=1 -v

# focused-2: actual exit=0, GOMEMLIMIT=4GiB
go test ./internal/serve ./internal/classification ./internal/forge/github -run 'TestStageA|TestGoverningCache|TestIdentity|TestTeamMembership' -count=1 -v

# check: actual exit=2, GOMEMLIMIT=4GiB
make check

# http-races: actual exit=0, GOMEMLIMIT=4GiB
go test ./internal/serve -run 'TestStageAHTTPIdentityFailuresBlockOrRetry|TestStageAFreshNegativeStancesBeforeStart' -race -count=1 -v

# check-2: actual exit=0, GOMEMLIMIT=4GiB
make check

# check-final: actual exit=0, GOMEMLIMIT=4GiB
make check

# astra-races: actual exit=0, GOMEMLIMIT=4GiB
go test -overlay /home/riels/.cache/openmerge-77-r2/astra-race-overlay.json ./internal/serve -run '^TestReview77R2NegativeRace$' -count=1 -v

# sim-exits: actual exit=0, GOMEMLIMIT=4GiB
make test-sim-exits

# compatibility: actual exit=0, GOMEMLIMIT=4GiB
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-11a-main

# legacy-sql: actual exit=0, GOMEMLIMIT=4GiB
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-11a-main

# legacy-depths: actual exit=0, GOMEMLIMIT=4GiB
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-11a-main

# commit-ids: actual exit=0, GOMEMLIMIT=4GiB
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a

# replay-main: actual exit=0, GOMEMLIMIT=6GiB
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-77-r2/replay-main.json

# replay-deployed: actual exit=1, GOMEMLIMIT=6GiB
python3 scripts/check-live-journal-replay.py --baseline /home/riels/.cache/sa9b-r2-deployed --candidate /home/riels/openmerge-sa-11a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-77-r2/replay-deployed.json

# replay-inspection: actual exit=0, GOMEMLIMIT=4GiB
python3 /home/riels/.cache/openmerge-11a/inspect-replays.py /home/riels/.cache/openmerge-77-r2/replay-main.json /home/riels/.cache/openmerge-77-r2/replay-deployed.json

```

Runner and guard SHA-256 values:

- `heavy.py`: `afd16f0e86fbc5330a68fb3fd8d6106f9f679ffccd5489b273ab66c55ee40b9a`
- `guard-bin/go`: `0f6b059f29b4109d98bd3d3bd11ae17174cc9756cd70d5edabd84a92d5779281`
- `guard-python/hostguard.py`: `62e64ec5161f28318a0d58c61ef9ebba0b1c0ab2284970195ce4c4909539217b`
- `guard-python/sitecustomize.py`: `aa907bb032d0ca3dae2bcc79a13aa1439c40663bad044578260de363c409492f`
- `astra-race-overlay.json`: `eff51f2741a418d9140f3e762dbfde17635a03c07562ffb11d26b796d5920861`

The final follow-up commit contains only this evidence update. Trial class remains **Hold**.


## Round 3: database ordering fence (resumed interrupted attempt)

The first implementation attempt of this exact round stopped on model capacity
with uncommitted changes based on `c444fc0`. This continuation reviewed and
retained that diff, including the new acquisition fence and Postgres tests.
Its inherited `make check` (make PID 1162370, Go PID 1163381) was allowed to finish;
no other agent's process was stopped. Earlier round-3 attempts and their actual
nonzero exits are retained below rather than replaced with successful reruns.
Only explicit main and feature-branch refspecs were fetched (exit 0);
`origin/main` remained `f8066a4`. Trial class remains **Hold**.

Observation time now precedes the native review read and is retained through
commands and eligibility. Every acquisition captures the queue's committed
`events.id` watermark before its forge reads. Ingestion, command passes, approval
probes and the final gate carry this acquisition through their writes. Under the
queue lock, a competing relevant commit after the watermark rejects the read
before dedupe or writes and again at atomic start/rejection. Own committed
appends advance the watermark only to their own final event ID. Nested reads
reuse it; wall-clock skew and an ABA cannot advance it past competing evidence.
The existing projection rejects acquisition-start regressions and resolves equal
starts in journal commit order. No external read occurs under the queue lock.

Eligibility dedupe now compares the candidate's approval fingerprint against the
current journal projection under that lock. Pool diagnostic reasons, top-level
reasons, read IDs/times and membership ordering are non-semantic. This also fixes
the side-key deny→allow→deny masking case when another gate journals the allow.

`TestStageAFreshNegativeStancesBeforeStart` covers final-read, before-start and
captured-but-delayed response interleavings, with real separate workers and
Postgres commits for request changes, dismissal, unapprove, withdrawal, revoked
write permission, team removal and grant ABA. It checks the specific post-injection
negative eligibility event instead of accepting any eligibility observation.
`TestStageAApprovalReadUsesDatabaseOrder` uses an artificially newer timestamp
and verifies that the database fence still rejects competing configuration,
revision, classification, accountability, intent and eligibility commits.
`TestStageAEligibilityJournalDedupe` verifies diagnostic-only polling emits no
events and a denial after a competing journaled allowance is retained. Astra's
original `TestReview77R3DelayedNativeRace` is also rerun unmodified via Go overlay.

Progress is intentionally unbounded under continuously changing meaningful
evidence. Identical polls dedupe; real relevant changes invalidate pending reads.
A sufficiently quiet acquisition-to-start interval is necessary. Source/evidence
caps fail closed and do not bound retries or guarantee landing. Worker diagnostics
expose `waiting for stable approval evidence` on stale retries. The same contract
is recorded in `internal/queue/landing-decisions.md`.

### Round-3 gate results and reproduction

Implementation commit: `8845437a4ab7b99516f892fd9761e025036e2a7a`.
All implementation code was unchanged between the inherited successful check,
subsequent gates and this commit. The final follow-up commit adds evidence only.
The final explicit-refspec fetch exited **0**; `origin/main` is still
`f8066a4f3bc9f2bfb110950b0c6c9aea4cf5ea6f`, so no rebase was necessary.

The first ordering run exited 1 on test-only `IdentitySet.Known` field names;
those were corrected to `Resolved`. The first check exited 2 on the tagged-switch
lint suggestion; the corrected inherited check exited 0. The continuation's first
commit-ID run exited 2 because the candidate was uncommitted; after the
implementation commit its rerun exited 0. These failures are retained below.

Final results: complete fmt/lint/race-short/build check 0; focused race regressions
0; Astra's original delayed-response probe 0; simulator exits 0; compatibility 0
(17 exits, 1055 retry/digest records, 84 wire fixtures); legacy SQL 0 (seven exact
statements, all five negative controls detected); depth-1/4 differential 0
(227662 short, 5615998 full-corpus checkpoints and 188 fixture streams; no nil-only
exceptions); all four commit methods 0. Commit-ID self-tests retain identical=0,
mutant=1 and dirty=2, all expected.

Strict replay vs main exited **0**: all 2159 real and seven synthetic checkpoints
match actions, boundaries, derived state and snapshot hashes. Strict replay vs
`3f77a88daf1cb99961c22b4daebaeb58f9087bb0` exited **1** for the existing
absent-to-strictly-nil `classifications` field. Exact-shape inspection exited
**0**: every checkpoint's only non-allowlisted difference has that exact shape;
actions/boundaries match and Gate 4 passes. Existing exceptions remain
`outsidePushes`, `approvalFacts`, `approvalWaits`, `receipts`, `landingDecisions`.
No exception allowlist was changed.

All jobs, including helper builds/test executables, used the existing host-load
guards; only one heavy job ran at a time. Environment: `GOFLAGS=-p=2`,
`GOMEMLIMIT=4GiB` (`6GiB` for replay), shared test Postgres URL from the brief,
GOTMPDIR/GOCACHE/TMPDIR beneath `~/.cache`. No initdb, live database/status,
systemd, live refs or real GitHub API call in code/tests/simulator. Saved immutable
journal exports were replay inputs. The inherited check was allowed to finish;
no test process was killed. PR metadata/push operations are the authorized GitHub
operations. Trial deployment class remains **Hold**.

The inherited results/logs are in `~/.cache/openmerge-77-r2`; continuation results,
runner, overlay and logs are in `~/.cache/openmerge-77-r3`. Exact root commands,
actual statuses, memory limits and log SHA-256 values follow. Each log retains
helper exit statuses and negative-control details.

- `r3-focused`: exit **0**, `4GiB`; log `/home/riels/.cache/openmerge-77-r2/logs/r3-focused.log`; SHA-256 `cede673cb471989720173f5530dd6762ec7817f2d3c13a9f1afe29b8b442c258`.
- `r3-ordering`: exit **1**, `4GiB`; log `/home/riels/.cache/openmerge-77-r2/logs/r3-ordering.log`; SHA-256 `42d6d0b0212a43a5dc1e92c0933a6d7c30bcdafd029c1c06f0975d3058432ef4`.
- `r3-ordering-2`: exit **0**, `4GiB`; log `/home/riels/.cache/openmerge-77-r2/logs/r3-ordering-2.log`; SHA-256 `bd6c816a58d21ae8cd9d6de48534d0c828425d24b33b50c6cbd3ee2bcb9905e7`.
- `r3-check`: exit **2**, `4GiB`; log `/home/riels/.cache/openmerge-77-r2/logs/r3-check.log`; SHA-256 `9838c78a7cd5ef0d50fbf7701a120dbb9d0388ab84ceb17e8d7db2b5e1f05107`.
- `r3-check-2`: exit **0**, `4GiB`; log `/home/riels/.cache/openmerge-77-r2/logs/r3-check-2.log`; SHA-256 `8fd9e1c2adae08d5a3766daabf60b4a5c587c0fa7deb26fbfb1890003acc3c13`.
- `ordering`: exit **0**, `4GiB`; log `/home/riels/.cache/openmerge-77-r3/logs/ordering.log`; SHA-256 `e5546fdd3309d1fde661b2d68fd7c943a022540cd22a3fcfa6a9d88969ae9531`.
- `astra-delayed`: exit **0**, `4GiB`; log `/home/riels/.cache/openmerge-77-r3/logs/astra-delayed.log`; SHA-256 `94a1e3c6f12712c6a383b97e9f179eb2eeac821c85956f69704b89b0db20fefb`.
- `sim-exits`: exit **0**, `4GiB`; log `/home/riels/.cache/openmerge-77-r3/logs/sim-exits.log`; SHA-256 `c9d5d419398e3a29ca4e4fa43a74f7cb968b3bef4ca83978d7f4f435b56aa7a1`.
- `compatibility`: exit **0**, `4GiB`; log `/home/riels/.cache/openmerge-77-r3/logs/compatibility.log`; SHA-256 `8609790ee807f78ae7fadd31e6503a6a932f9f874af818197be4849fe5a93749`.
- `legacy-sql`: exit **0**, `4GiB`; log `/home/riels/.cache/openmerge-77-r3/logs/legacy-sql.log`; SHA-256 `ab22c0b5e090c552fcb505089d132fa81b6793fa330d277ad02890069ccefd33`.
- `legacy-depths`: exit **0**, `4GiB`; log `/home/riels/.cache/openmerge-77-r3/logs/legacy-depths.log`; SHA-256 `3c25309b33c985c03a3aff2a5c961878e2dd1b080a03ec27dc5894981e7b3453`.
- `commit-ids`: exit **2**, `4GiB`; log `/home/riels/.cache/openmerge-77-r3/logs/commit-ids.log`; SHA-256 `e24a7137b1e0eaf4e2abd51b56574988452a0beeea77be8b15b12f3fed21dc00`.
- `commit-ids-2`: exit **0**, `4GiB`; log `/home/riels/.cache/openmerge-77-r3/logs/commit-ids-2.log`; SHA-256 `01799bca7999baf38d7c52eeaf7c6e44b7bd203c29facc457f18635d0599e68b`.
- `replay-main`: exit **0**, `6GiB`; log `/home/riels/.cache/openmerge-77-r3/logs/replay-main.log`; SHA-256 `7757488e4f994dd0705782d46e7986479e0de988a448f6c7e512435cddfe97cf`.
- `replay-deployed`: exit **1**, `6GiB`; log `/home/riels/.cache/openmerge-77-r3/logs/replay-deployed.log`; SHA-256 `1efd2facde58ae7bf93830e911f918eb75dc523a8de87dca048eedfe2b85f028`.
- `replay-inspection`: exit **0**, `4GiB`; log `/home/riels/.cache/openmerge-77-r3/logs/replay-inspection.log`; SHA-256 `0b554123b04ecef4cc41fb09ede76a5a35e559cad1507ffebe8b96a3ebb68144`.

```sh
# r3-focused: actual exit=0, GOMEMLIMIT=4GiB
go test ./internal/serve ./internal/store -run 'TestStageAFreshNegativeStancesBeforeStart|TestStageA|TestAtomicLanding|TestLandingDecision' -count=1 -timeout=10m

# r3-ordering: actual exit=1, GOMEMLIMIT=4GiB
go test ./internal/serve -run 'TestStageAFreshNegativeStancesBeforeStart|TestStageAEligibilityJournalDedupe|TestStageAApprovalReadUsesDatabaseOrder' -race -count=1 -v

# r3-ordering-2: actual exit=0, GOMEMLIMIT=4GiB
go test ./internal/serve -run 'TestStageAFreshNegativeStancesBeforeStart|TestStageAEligibilityJournalDedupe|TestStageAApprovalReadUsesDatabaseOrder' -race -count=1 -v

# r3-check: actual exit=2, GOMEMLIMIT=4GiB
make check

# r3-check-2: actual exit=0, GOMEMLIMIT=4GiB
make check

# ordering: actual exit=0, GOMEMLIMIT=4GiB
go test ./internal/serve -run 'TestStageAFreshNegativeStancesBeforeStart|TestStageAEligibilityJournalDedupe|TestStageAApprovalReadUsesDatabaseOrder' -race -count=1 -v

# astra-delayed: actual exit=0, GOMEMLIMIT=4GiB
go test -overlay /home/riels/.cache/openmerge-77-r3/astra-overlay.json ./internal/serve -run '^TestReview77R3DelayedNativeRace$' -count=1 -v

# sim-exits: actual exit=0, GOMEMLIMIT=4GiB
make test-sim-exits

# compatibility: actual exit=0, GOMEMLIMIT=4GiB
python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-sa-11a-main

# legacy-sql: actual exit=0, GOMEMLIMIT=4GiB
python3 scripts/check-legacy-landing-sql.py /home/riels/openmerge-sa-11a-main

# legacy-depths: actual exit=0, GOMEMLIMIT=4GiB
python3 scripts/check-legacy-plan-action-depths-1-4.py /home/riels/openmerge-sa-11a-main

# commit-ids: actual exit=2, GOMEMLIMIT=4GiB
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a

# commit-ids-2: actual exit=0, GOMEMLIMIT=4GiB
python3 scripts/check-legacy-commit-ids.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a

# replay-main: actual exit=0, GOMEMLIMIT=6GiB
python3 scripts/check-live-journal-replay.py --baseline /home/riels/openmerge-sa-11a-main --candidate /home/riels/openmerge-sa-11a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-77-r3/replay-main.json

# replay-deployed: actual exit=1, GOMEMLIMIT=6GiB
python3 scripts/check-live-journal-replay.py --baseline /home/riels/.cache/sa9b-r2-deployed --candidate /home/riels/openmerge-sa-11a --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl --strict-state --out /home/riels/.cache/openmerge-77-r3/replay-deployed.json

# replay-inspection: actual exit=0, GOMEMLIMIT=4GiB
python3 /home/riels/.cache/openmerge-11a/inspect-replays.py /home/riels/.cache/openmerge-77-r3/replay-main.json /home/riels/.cache/openmerge-77-r3/replay-deployed.json

```

Runner/artifact hashes:

- `/home/riels/.cache/openmerge-77-r3/heavy.py`: `4e74df6ac816eec6cc70395d60e057c2003d437bc1feb00dd7aed61827a1e42f`.
- `/home/riels/.cache/openmerge-77-r3/gates.py`: `ffc687f7bdd9d0f01057a72be54a5f42d1847c5f383f42bbec5fcdc339e90cd2`.
- `/home/riels/.cache/openmerge-77-r3/remaining-gates.py`: `b60205e313240fa840ba5a1e21a5b15435a288fdd75ed6198c5c6023ab2909dd`.
- `/home/riels/.cache/openmerge-77-r3/astra-overlay.json`: `dde3da064f68951df2c622edbaad6d7d6b35e8b93698108225bd1f842475e650`.
- `/home/riels/.cache/openmerge-77-r2/guard-bin/go`: `0f6b059f29b4109d98bd3d3bd11ae17174cc9756cd70d5edabd84a92d5779281`.
- `/home/riels/.cache/openmerge-77-r2/guard-python/hostguard.py`: `62e64ec5161f28318a0d58c61ef9ebba0b1c0ab2284970195ce4c4909539217b`.
- `/home/riels/.cache/openmerge-77-r2/guard-python/sitecustomize.py`: `aa907bb032d0ca3dae2bcc79a13aa1439c40663bad044578260de363c409492f`.
