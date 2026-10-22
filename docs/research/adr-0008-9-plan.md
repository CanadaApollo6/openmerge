# ADR 0008 PR 9 split

9c baseline: local main `051798c` (9a and all 9b slices landed in #125,
#127, #128, #129, #130, #133, #134; test speed-up #131). Every slice is **Hold** until dogfood period B drains.
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
   attempts continue settling. Keep 9a's public rejection until 9c-3 completes.
3. **9c — complete forge gate and activation acceptance.** Wire discovery,
   reconciliation, import and setup to governing validation, typed readers,
   compiler/content validation and immutable per-operation push contexts. Put
   the reloading guard on all five mutating Git methods and enforce durable
   pin/observation-before-fence at all bootstrap/cycle sites. Remove the public
   rejection only in 9c-3, after the complete gate and real-git acceptance.
   The sequential 9c review split below keeps activation last.
   All governing failures, including invalid landed config, defer/block work;
   `import --accept` cannot waive them. Real organization switch remains PR 11's
   final release acceptance; fake-only activation is never release evidence.

9a and 9b are independently safe because no public configuration can request
OpenMerge authority. 9c-1 and 9c-2a/2b/2c/2d are independently safe behind that same barrier. Only 9c-3
removes rejection, after the complete gate and all prior rows have landed. Ordinary omitted
or explicit `github` configuration follows legacy behavior throughout. Each slice
must pass `make check`, strict offline 2,159-checkpoint live-journal replay and
byte-identical normalized seeded simulator results, including legacy approvals.

## Exhaustive PR 9 requirement ownership

### PR 9b review split (2026-10-21)

The 9b lifecycle, projection and admission wiring is too large to review together
with its prerequisite corrections. Assign each prerequisite and runtime row below
to one of two independently safe changes:

- **9b-i, prerequisite hardening:** #106(b)'s idempotent pin writer; #112's
  interpreted draft adoption, raw-activation fold protection, early retry check
  and durable adopted/pinned-unverified receipts; command-boundary regression
  coverage for the existing emission-age/binary and stale-verification gates;
  #120's typed permanent evidence failures, final attempt budget, direct caps,
  size oracle and precedence tests. Include 9a's documentation/schema nits and
  regression coverage showing candidate/draft/pin data cannot install runtime
  authority hooks. No production activation caller or new event type is added.
- **9b-ii, runtime wiring:** all runtime rows assigned to 9b-ii below: atomic
  config/pin lifecycle including new queues; no-approvals owner enforcement;
  first-cycle observation, warning, status, author and check projections;
  rollback/re-enable and all-queue digest/invalidation; every landing-start
  source/digest check under lock, with source-repair, mixed-state, quota/read
  and no-approvals tests. Consume the internal activation caller and durable
  receipt output completed in 9b-ii-1, including its emission receipt max-age and attested
  `BinaryRevision` checks; 9b-ii-2a hardens database binding. Prove A2 again against
  that complete runtime seam: a candidate or journal-loaded draft/pin must never
  enable it by itself.

9b-i leaves the existing public rejection and production target construction
unchanged. Its adoption writer remains without a production caller. Its raw
activation guard is deliberately stricter about *verification*: historical wire
pins remain readable, but cannot establish verified authority from an
uninterpreted inventory. 9b-ii retains the public barrier too; 9c-2a/2b/2c/2d own the
complete forge gate; only 9c-3 removes that barrier. Neither 9b slice is
deployment or organization acceptance. The original prerequisite run stopped at 9b-i.

### PR 9b-ii review split (2026-10-21)

Runtime lifecycle, worker projections and lock-time admission are too large to
review with the emission boundary and inherited runtime corrections. Split again:

- **9b-ii-1 — runtime foundation:** add the package-private, explicit-opt-in
  activation caller around the existing adoption writer. Validate receipt max-age
  and attested running BinaryRevision before I/O and again before emission;
  return and log the durable adopted/pinned-unverified outcome. Fix the inherited
  cursor reset warning/CAS ordering, document future-version cursor replacement,
  and make authority check preflight local so approval quota/read failures cannot
  preempt check-input shape/budget validation. Recheck A2 at this caller boundary.
- **9b-ii-2 — runtime integration:** owns every remaining runtime row below.
  Integrate the caller into atomic config/pin and new-queue lifecycle, owner
  enforcement without approvals, observations/projections, rollback/re-enable,
  all-queue digest/invalidation and every landing start. Prove A2 again against
  the **complete** integrated runtime seam; the foundation proof is not a
  substitute. Include source-repair, mixed-state, quota/read, no-approvals and
  depth-1/4 admission tests with that integration.

Both slices remain **Hold** and independently safe behind 9a's public rejection.
9b-ii-1 installs no hooks and has no production activation call site. Production
constructors never set its explicit opt-in. 9b-ii-2 must preserve that boundary;
only 9c-3 may remove it. The 9b-ii-1 run stopped after publishing its foundation.

### PR 9b-ii-2 review split (2026-10-21)

The complete runtime seam spans config adoption, every worker projection and
lock-time landing authorization. Keep those changes independently reviewable:

- **9b-ii-2a — receipt/preflight hardening (this PR):** resolve #128's review
  before extending activation: derive a server-side cluster/database/schema/journal-genesis digest through the
  connected store, share its resolver with setup, reject empty identities, and
  document/test the local preflight's review-input limit. Correct stale ownership
  text and document transaction-time receipt expiry. No lifecycle hook is added.
- **9b-ii-2b — atomic lifecycle:** consume the 9b-ii-1 caller in matching atomic
  config/pin emission, including new queues, rollback, fresh re-enable, all-queue
  digest propagation and default-branch/selector/actor/method invalidation. Test
  mixed repository/base transitions and lifecycle quota/read failures. No
  production constructor enables authority; the public rejection stays.
- **9b-ii-2c — worker policy and projections:** activate no-approvals owner
  classification/brief/eligibility/decisions and first-cycle observations,
  warning eligibility, status/CLI/API/health, author and per-check projections.
  Exercise rollback's delayed-origin warnings/history/unrelated blocks against
  that wiring. Keep the explicit internal opt-in and public rejection.
- **9b-ii-2d — complete admission:** every landing/remedy start checks current
  findings and exact digest under the existing queue lock, depths 1/4. Prove
  admitted starts settle, source-repair races, quota/read failures and
  no-approvals starts; rollback removes only authority admission requirements.
  Repeat A2 against the **complete runtime seam**, including
  the lifecycle and projection slices. Keep batching/fence protocol unchanged.

All four remain **Hold** and independently safe because production constructors
never opt in and 9a's public activation barrier remains. Each intermediate slice
must prove candidates and journal-loaded drafts/pins cannot enable the hooks it
adds; those proofs do not discharge 2d's complete-seam A2. Only 9c-3 removes the
public barrier. This run publishes 9b-ii-2a and stops. No runtime integration is
claimed by the review corrections.

### PR 9c review split (2026-10-21)

These are six sequential PRs, all **Hold**, with no SQL migrations,
indexes, tables, new forge capability or legacy GitHub behavior changes. Only
9c-1 is implemented in this run. Every slice retains strict wire-v1 decoding,
existing fixture bytes, `make check`, strict 2,159-checkpoint replay, normalized
simulator equality, depth-1/4 contracts and GitHub projection/CLI byte equality.
Each slice is gated on fresh legacy GitHub equality evidence before the next
slice starts. 9c-1 and all four 9c-2 slices remain publicly disabled and production
constructors remain opted out; each is independently safe at that boundary.

1. **9c-1 — admission hardening:** #135 items 1–5: action-bounded admission
   reads; parent-derived, deadline-bounded persistence and retry even when the
   read-error block cannot persist; additive read_error variant/snapshot and read_error-only drain proofs
   (general drain ownership remains 9c-2c);
   cryptographic scratch schema names; exact exponential deadlines; sanitized
   reason classes and governing-pin comment. Focused tests cover timeout under
   queue-lock contention, shutdown cancellation, classification and backoff.
2. **9c-2a — evidence acquisition and config blocking:** acquire governing
   evidence in discovery/setup/import. `import --accept` cannot waive it;
   missing/unresolved/incomplete evidence cannot activate. Invalid landed config
   blocks/defers work. Focused tests cover each acquisition boundary, waiver
   refusal, incomplete evidence and invalid landed config. Retain public rejection.
3. **9c-2b — push gate and ordering:** typed reader/compiler/content validation,
   immutable per-operation context and a store-reloading guard before **every**
   generated-object push; a post-build read is evidence, never the guard. Supply
   direct `EnsureFence` options and registration/cycle/first-queue
   pin → observation → fence ordering, including #90 `refreshWorker` end-to-end
   and #106(c) configTip-unset startup deferral. Refresh #130 `VerificationInput`
   whenever live-branch `ExpectedRefs` changes before refreezing/adoption.
   Bound #106(a) empty-OldSHA history against the build base with independent
   per-commit path budgets. Focused real-git rejection tests prove rejected
   intermediate/marker/fence objects leave remote refs unchanged; test each push
   guard, ordering site, drift and history budget. Retain public rejection.
4. **9c-2c — propagation and operational gates:** after 2b, implement the
   repository-wide “all served queues carry digest X” completion-signal API;
   per-queue receipts cannot authorize protection removal. Complete old-worker
   inventory/drain, tested backup receipts/reconciliation and v7 downgrade gates,
   including pre-switch `--verify` and the receipt lock-wait/commit expiry
   limitation. Focused tests cover mixed/incomplete queue propagation, inventory,
   missing/stale drain/backup receipts, reconciliation and downgrade refusal.
   Retain public rejection.
5. **9c-2d — integrated real-git acceptance:** default-branch rename/deletion,
   cold-cache every-merge-method land/read-back/decisions, #82 history-boundary
   and #84 tested-commit acquisition reproductions, remote-ref invariance,
   mixed-GitHub commit IDs/permissions/event traces and integrated `import --accept`
   regression. These focused integrated tests consume 2a/2b/2c contracts without
   moving their ownership. Retain public rejection and production opt-out.
6. **9c-3 — public activation and production opt-in only:** last and smallest;
   accept `policy_authority: openmerge` in schema/loader/CLI together and enable
   production constructors using the completed wiring. Require 9c-1 and every
   2a/2b/2c/2d row landed with equality evidence. Add tests that **fail unless**
   the completion-signal API and pre-switch `--verify`/drain receipt refusal
   exist **and are consulted** by public activation; absent/incomplete propagation
   or missing/invalid receipts must refuse activation. Exercise `AuthorityCompletion.RequireComplete`, the actual repository-wide source (including missing/stale discovery), and the actual `setup --verify` / `EmissionReceipt.ValidateForBinary` refusal, with spies/mutations proving each was consulted; per-queue adoption receipts cannot substitute. Include the #138 recovery rows below. Only then remove the
   barrier. Retain deployment Hold until dogfood period B drains.

The real organization switch remains **PR 11**, in the sandbox with the owner.
It is the final release acceptance gate and is outside all implementation slices;
fake-only activation is never release evidence.

Each row assigns the original requirement once; later slices consume earlier
contracts without transferring their ownership.

| ADR PR 9 bullet / requirement | Slice |
| --- | --- |
| Scope/config: public OpenMerge acceptance in schema/loader/CLI and production constructor opt-in | 9c-3 |
| Scope/config: root schema + loader + CLI agreement, reserved fail-closed publication; pure default queue and governing-owner coverage validation | 9a |
| Scope/config: resolve/obtain actual governing evidence in discovery/setup/import; import acceptance cannot waive; missing/unresolved/incomplete evidence cannot activate | 9c-2a |
| Owner enforcement: pure no-approvals decision/digest, no ordinary floor, mandatory exact-revision policy owners, `none` cannot waive; governing rather than proposed owner semantics | 9a |
| Owner enforcement: runtime classification, briefs, eligibility and decision invocation without approvals | 9b-ii-2c |
| Owner enforcement: invalid landed config blocks work | 9c-2a |
| Wire/state: idempotent prerequisite pin writer, interpreted draft adoption and persisted fold provenance protection | 9b-i |
| Wire/state: conditional matching ConfigApplied/pin emission and new-queue lifecycle wiring; no ordinary GitHub events or downgrade boundary | 9b-ii-2b |
| Wire/state: first-cycle observations, warnings, status/CLI/API/health, author messages, per-check evidence from PRs 4–8 | 9b-ii-2c |
| Wire/state: every landing start checks source findings/digest under lock at depths 1/4; admitted starts settle; unchanged batching/fence protocol | 9b-ii-2d |
| Wire/state: mode-specific rollback pin, fresh re-enable evidence and digest reaches every served queue | 9b-ii-2b |
| Wire/state: rollback removes only authority-specific admission requirements | 9b-ii-2d |
| Wire/state: rollback stops new authority observations/warning eligibility, retains delayed-origin warnings/history/unrelated blocks | 9b-ii-2c |
| Forge/migrations: repository-wide “all served queues carry digest X” completion signal before redundant protection removal; per-queue receipts never authorize removal | 9c-2c |
| Forge/migrations: typed reader/compiler/content wiring and immutable context/reloading guard before every generated-object push | 9c-2b |
| Forge/migrations: registration/cycle/first-queue pin-observation-fence ordering, direct EnsureFence options, post-build read never a guard | 9c-2b |
| Forge/migrations: invalidate on default branch/selector/actor/method changes | 9b-ii-2b |
| Forge/migrations: no new capability/migration, old-worker inventory/drain, backup receipt/reconciliation and v7 downgrade gate incl. pre-switch verify | 9c-2c |
| Tests: static invalid coverage/default queue/selector and owner removal/head/stale eligibility cases | 9a |
| Tests: mixed repository/base lifecycle transitions and lifecycle quota/read errors | 9b-ii-2b |
| Tests: source repair races, admission quota/read errors and no-approvals starts | 9b-ii-2d |
| Tests: real default rename/deletion, cold-cache all-method gitops land/read-back/decisions; #82/#84; rejected objects leave refs unchanged; mixed GitHub commit IDs/permissions/traces | 9c-2d |
| Tests: PR 11 organization release gate (owner-executed sandbox switch, outside every implementation slice) | PR 11 |

## Carry-over disposition

| Review carry-over | Slice and required proof |
| --- | --- |
| #135 items 1–5: admission read timeout and parent persistence/retry; read_error fixture/snapshot/drain/ADR; crypto/rand schemas; exponential deadlines; reason classes and governing-pin comment | 9c-1 |
| #130: unchanged unadopted cycles and draftless same-binding rebind reads | 9b-ii-2c: lifecycle skips seeing reads without a draft; once-per-reconciliation observations include unadopted pins; read-count and failed-reader rebind tests |
| #130 / Sonnet #133: VerificationInput drift when live-branch ExpectedRefs changes without a binding change | 9c-2b: refresh VerificationInput whenever live-branch ExpectedRefs changes, bind/refreeze interpretation inputs and invalidate stale adoption before the complete push gate can activate; 9b-ii-2c does not change persisted adoption input |
| #106(a): empty OldSHA full-history inventory and cumulative paths | 9c-2b: bound introduced history against build base; independent per-commit path budgets; large real-repo first build/cleaned ref regression |
| #106(b): pin writer called on every executor op / cleanup no-op | 9b-i: idempotent pin before journal scans/writes; repeated no-op operations produce no extra pin events |
| #106(c): startup configTip unset with pin present/hooks absent | 9c-2b: defer creation instead of failing construction; existing fence read-first no-op remains valid |
| #112(1): draft fingerprint strips findings | 9b-i: interpreted fresh evidence/findings equality required before adopting verified draft; raw evidence cannot fold verified activation |
| #112(2): AdoptAuthorityPin idempotence before ProposedVerification scan | 9b-i: retry returns before history scan |
| #112(3): adopted versus pinned-unverified receipt contract | 9b-i: explicit output and durable receipt/source IDs |
| #112(3): internal activation caller receipt/output wiring | 9b-ii-1: expose and log adopted versus pinned-unverified outcome; 9b-ii-2b consumes it in lifecycle integration |
| #112 small: existing setup --verify emission receipt gates | 9b-i: command-boundary regressions for max-age and attested BinaryRevision validation |
| #112 small: internal activation caller emission receipt gate | 9b-ii-1: validate emission receipt max-age and attested running BinaryRevision before I/O and before emission |
| #112 small: stale setup --verify basis | 9b-i: nonzero exit with retry hint |
| #90: branches.go refreshWorker is first fence-bootstrap site | 9c-2b: end-to-end test through refreshWorker, direct serve PushOptions and pin/observation before first fence |
| #120: empty attempt_id budget, untyped builder errors, overflow precedence, indirect caps, size-oracle fuzz | 9b-i: fix before any internal activation; typed permanent blocks, exact final attempt budget, expanded oracle/precedence tests |
| #127: cursor warning before successful CAS | 9b-ii-1: log only after committed CAS; repeated competing repairs remain quiet |
| #127: unsupported future-version cursor fallback | 9b-ii-1: operating docs and regression cover overwriting derived future-version cursor |
| #127: ValidateChecks approval quota/read precedence | 9b-ii-1: local check preflight without DB/forge approval reads; fresh complete admission read retained |
| #128: configured database receipt identity, empty identities, preflight review-input precedence, stale ownership/transaction-expiry documentation | 9b-ii-2a |
| #138: ExpectedRefs/input drift recovery | 9c-2c documents rollback → fresh proposed verification → re-enable. Applied verification cannot safely mint proposed-adoption provenance; direct re-adoption is deliberately unavailable. 9c-3 must test this runbook through public activation and require current completion. |
| #138: external base advance between plan/build | 9c-2c documents conservative transient failure and normal base observation/replan recovery. 9c-3 must test public recovery; do not classify arbitrary ancestry/read failures as a successful base-moved observation. GitHub path stays unchanged. |
| #138: seeing-drift assertion, shared input comparison, draftless floor query | 9c-2c comments the assertion, centralizes `queue.AuthorityInputsMatch` with explicit historical-nil policy, and skips AdoptAuthorityPin draft-floor reads for draftID zero. |
| Acceptance #82 history boundary / #84 tested acquisition | 9c-2d: reproduce in cold-cache real-git acceptance |
| Acceptance rejected intermediate/marker/fence refs unchanged and mixed GitHub compatibility | 9c-2d: real remote ref read-back plus permission/event/commit comparisons |
| Owner decision: no approvals means no ordinary floor | 9a: pure evaluation and atomic decision tests |
| Owner decision: mandatory eligible exact-revision policy owners despite none | 9a: governing-owner evaluation and stale/none tests |
| Owner decision: dedicated break_glass team | 9a: schema/candidate/loader rejection of repository-admin declarations; retain historical wire |
| Owner decision: import --accept cannot waive evidence | 9c-2a: actual acquisition/import acceptance regression |

## First-emission and release boundary

9a emits no new events or v7 state. 9b retains the public barrier even though
internal activation tests exercise events. Before any 9c deployment, drain every old
worker after period B, retain inventory and a tested pre-emission backup receipt,
and verify the attested emitting binary. This includes operator `--verify` before
config activation. The internal activation caller already validates emission receipt max-age and attested `BinaryRevision` in
9b-ii-1; 9b-ii-2a binds it to the configured database. The existing
`setup --verify` command gate is separate. Local receipt validation occurs before identity I/O; server-side identity and
receipt age are checked before forge reads and again before calling adoption; a receipt can still expire while the adoption
transaction waits for the queue lock or commits. This is not a transaction-time
expiry guarantee.
After new events or v7 cursors, rollback to `github` is not a
binary downgrade: use a compatible reader or stop workers, restore the verified
backup and reconcile subsequent real refs/evidence before resuming. Keep outside
warnings and all immutable evidence through rollback/retirement. PR 11 owns the
actual organization switch, never this implementation worktree.

Historical baseline: at 9c-1 there was **no repository-wide “all served queues carry digest X” completion signal**. 9b-ii-2b provides independent per-queue receipts only; they do not authorize removing redundant protection. 9c-2c provides `Server.AuthorityCompletion` / `Store.RepositoryAuthorityCompletion` and authenticated `/v1/authority-completion`; `AuthorityCompletion.RequireComplete` is the refusal contract. It covers all registered partitions (including other installations and paused/retired/excluded) plus fresh selected discovery, not a worker subset. 9c-3 must consult it before the public switch/protection-removal sequence.

The optional `write_restriction_observed.read_error` and its v7 snapshot field
are also strict-decoder compatibility boundaries. Include workers/readers that
lack these fields in the old-worker inventory even if they recognize the event
type or v7. Drain them before the first such observation, including admission
failures, and retain the tested backup/reconciliation receipts. Rollback to
GitHub does not erase this boundary or permit a v7-incompatible binary downgrade.
