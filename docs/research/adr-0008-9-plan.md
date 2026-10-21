# ADR 0008 PR 9 split

Baseline: local main `7207a72` (#115 PR 6, #117 PR 7, #119 PR 8,
#122 toolchain). Every slice is **Hold** until dogfood period B drains.
No slice authorizes deployment or organization acceptance. No SQL migrations or
indexes, Makefile/CI/Dockerfile/test-flag changes, or modifications to existing
wire fixtures. Existing wire-v1 contracts remain strict and additive.

## Merge boundaries

1. **9a — authority contracts and governing-owner decisions.** Publish reserved
   root keys together in schema/loader, accept explicit `github`, and reject
   `openmerge` with an activation-unavailable diagnostic in both public paths.
   Keep the candidate parser for isolated setup/proposed verification. Require a
   dedicated team in new declarations (the older role wire variants stay readable).
   Add pure branch/governing-commit validation: exactly one default queue, exact
   selector compilation, complete selected CODEOWNERS, and effective resolved
   coverage of every config and CODEOWNERS path. Add approval-free owner policy
   evaluation/digest and reuse classification, briefs, eligibility and atomic
   decision contracts without an ordinary reviewer floor. Policy edits still
   require exact-revision governing owners, independently of user `none` rules.
   This is dormant: no runtime authority switch or new event emission.
2. **9b — durable lifecycle and admission wiring, still disabled publicly.**
   Own config/pin atomic emission and fold, new-queue pins, draft adoption,
   rollback and re-enable semantics; activate the prerequisite projections at
   the internal wiring seam. Wire classification/brief/eligibility refresh for
   no-approvals authority queues. All landing starts (including remedy seams),
   depths 1/4, check current findings and digest under the queue lock. Started
   attempts continue settling. Keep 9a's public rejection until 9c completes.
3. **9c — complete forge gate and activation acceptance.** Wire discovery,
   reconciliation, import and setup to governing validation, typed readers,
   compiler/content validation and immutable per-operation push contexts. Put
   the reloading guard on all five mutating Git methods and enforce durable
   pin/observation-before-fence at all bootstrap/cycle sites. Remove the public
   rejection only with the complete gate and real-git acceptance in this slice.
   All governing failures, including invalid landed config, defer/block work;
   `import --accept` cannot waive them. Real organization switch remains PR 11's
   final release acceptance; fake-only activation is never release evidence.

9a and 9b are independently safe because no public configuration can request
OpenMerge authority. 9c is independently safe only when its complete gate passes;
if it cannot, retain rejection and split further before merging. Ordinary omitted
or explicit `github` configuration follows legacy behavior throughout. Each slice
must pass `make check`, strict offline 2,159-checkpoint live-journal replay and
byte-identical normalized seeded simulator results, including legacy approvals.

## Exhaustive PR 9 requirement ownership

### PR 9b review split (2026-10-21)

The 9b lifecycle, projection and admission wiring is too large to review together
with its prerequisite corrections. Keep the row ownership below at 9b, delivered
in two independently safe changes:

- **9b-i, prerequisite hardening:** #106(b)'s idempotent pin writer; #112's
  interpreted draft adoption, raw-activation fold protection, early retry check
  and durable adopted/pinned-unverified receipts; command-boundary regression
  coverage for the existing emission-age/binary and stale-verification gates;
  #120's typed permanent evidence failures, final attempt budget, direct caps,
  size oracle and precedence tests. Include 9a's documentation/schema nits and
  regression coverage showing candidate/draft/pin data cannot install runtime
  authority hooks. No production activation caller or new event type is added.
- **9b-ii, runtime wiring:** all runtime rows assigned to 9b below: atomic
  config/pin lifecycle including new queues; no-approvals owner enforcement;
  first-cycle observation, warning, status, author and check projections;
  rollback/re-enable and all-queue digest/invalidation; every landing-start
  source/digest check under lock, with source-repair, mixed-state, quota/read
  and no-approvals tests. Wire the prerequisite receipt output to the internal
  activation caller. Prove A2 again against that complete runtime seam: a
  candidate or journal-loaded draft/pin must never enable it by itself.

9b-i leaves the existing public rejection and production target construction
unchanged. Its adoption writer remains without a production caller. Its raw
activation guard is deliberately stricter about *verification*: historical wire
pins remain readable, but cannot establish verified authority from an
uninterpreted inventory. 9b-ii retains the public barrier too; 9c still owns the
complete forge gate and the only removal of that barrier. Neither 9b slice is
deployment or organization acceptance. Stop after publishing 9b-i in this run.

Each row assigns the original requirement once; later slices consume earlier
contracts without transferring their ownership.

| ADR PR 9 bullet / requirement | Slice |
| --- | --- |
| Scope/config: root schema + loader + CLI agreement, reserved fail-closed publication; pure default queue and governing-owner coverage validation | 9a |
| Scope/config: resolve/obtain actual governing evidence in discovery/setup/import; import acceptance cannot waive; missing/unresolved/incomplete evidence cannot activate | 9c |
| Owner enforcement: pure no-approvals decision/digest, no ordinary floor, mandatory exact-revision policy owners, `none` cannot waive; governing rather than proposed owner semantics | 9a |
| Owner enforcement: runtime classification, briefs, eligibility and decision invocation without approvals | 9b |
| Owner enforcement: invalid landed config blocks work | 9c |
| Wire/state: conditional matching ConfigApplied/pin emission, new queues, persisted fold, draft adoption; no ordinary GitHub events or downgrade boundary | 9b |
| Wire/state: first-cycle observations, warnings, status/CLI/API/health, author messages, per-check evidence from PRs 4–8 | 9b |
| Wire/state: every landing start checks source findings/digest under lock at depths 1/4; admitted starts settle; unchanged batching/fence protocol | 9b |
| Wire/state: mode-specific rollback, no new authority observations/warning eligibility, retain delayed-origin warnings/history/unrelated blocks; fresh re-enable evidence; digest reaches every served queue | 9b |
| Forge/migrations: typed reader/compiler/content wiring and immutable context/reloading guard before every generated-object push | 9c |
| Forge/migrations: registration/cycle/first-queue pin-observation-fence ordering, direct EnsureFence options, post-build read never a guard | 9c |
| Forge/migrations: invalidate on default branch/selector/actor/method changes | 9b |
| Forge/migrations: no new capability/migration, old-worker inventory/drain, backup receipt/reconciliation and v7 downgrade gate incl. pre-switch verify | 9c |
| Tests: static invalid coverage/default queue/selector and owner removal/head/stale eligibility cases | 9a |
| Tests: source repair races, mixed repository/base state transitions, quota/read errors and no-approvals starts | 9b |
| Tests: real default rename/deletion, cold-cache all-method gitops land/read-back/decisions; #82/#84; rejected objects leave refs unchanged; mixed GitHub commit IDs/permissions/traces; PR 11 organization release gate | 9c |

## Carry-over disposition

| Review carry-over | Slice and required proof |
| --- | --- |
| #106(a): empty OldSHA full-history inventory and cumulative paths | 9c: bound introduced history against build base; independent per-commit path budgets; large real-repo first build/cleaned ref regression |
| #106(b): pin writer called on every executor op / cleanup no-op | 9b: idempotent pin before journal scans/writes; repeated no-op operations produce no extra pin events |
| #106(c): startup configTip unset with pin present/hooks absent | 9c: defer creation instead of failing construction; existing fence read-first no-op remains valid |
| #112(1): draft fingerprint strips findings | 9b: interpreted fresh evidence/findings equality required before adopting verified draft; raw evidence cannot fold verified activation |
| #112(2): AdoptAuthorityPin idempotence before ProposedVerification scan | 9b: retry returns before history scan |
| #112(3): adopted versus pinned-unverified operator outcome | 9b: explicit output and durable receipt/source IDs |
| #112 small: emission max-age and attested BinaryRevision | 9b: validate receipt age/binary attestation before first emission |
| #112 small: stale setup --verify basis | 9b: nonzero exit with retry hint |
| #90: branches.go refreshWorker is first fence-bootstrap site | 9c: end-to-end test through refreshWorker, direct serve PushOptions and pin/observation before first fence |
| #120: empty attempt_id budget, untyped builder errors, overflow precedence, indirect caps, size-oracle fuzz | 9b: fix before any internal activation; typed permanent blocks, exact final attempt budget, expanded oracle/precedence tests |
| Acceptance #82 history boundary / #84 tested acquisition | 9c: reproduce in cold-cache real-git acceptance |
| Acceptance rejected intermediate/marker/fence refs unchanged and mixed GitHub compatibility | 9c: real remote ref read-back plus permission/event/commit comparisons |
| Owner decision: no approvals means no ordinary floor | 9a: pure evaluation and atomic decision tests |
| Owner decision: mandatory eligible exact-revision policy owners despite none | 9a: governing-owner evaluation and stale/none tests |
| Owner decision: dedicated break_glass team | 9a: schema/candidate/loader rejection of repository-admin declarations; retain historical wire |
| Owner decision: import --accept cannot waive evidence | 9c: actual acquisition/import acceptance regression |

## First-emission and release boundary

9a emits no new events or v7 state. 9b retains the public barrier even though
internal activation tests exercise events. Before 9c deployment, drain every old
worker after period B, retain inventory and a tested pre-emission backup receipt,
and verify the attested emitting binary. This includes operator `--verify` before
config activation. After new events or v7 cursors, rollback to `github` is not a
binary downgrade: use a compatible reader or stop workers, restore the verified
backup and reconcile subsequent real refs/evidence before resuming. Keep outside
warnings and all immutable evidence through rollback/retirement. PR 11 owns the
actual organization switch, never this implementation worktree.
