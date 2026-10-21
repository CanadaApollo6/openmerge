# ADR 0008 PR 9b-ii-1 evidence

Trial class: **Hold**. Base: local main `6aad022966f6730307f63bfbafb1d03f84fc2a61`
(includes #125 and #127). The [ownership plan](adr-0008-9-plan.md#pr-9b-ii-review-split-2026-10-21)
splits runtime work into independently safe **9b-ii-1, runtime foundation**, and
**9b-ii-2, runtime integration**. This PR implements the first and stops.
No deployment or organization acceptance is authorized. Public activation stays
rejected until 9c; PR 11 owns the organization acceptance gate.

## Implemented rows and carry-overs

| Requirement | Implementation and proof |
| --- | --- |
| #112(3), internal caller outcome | `internal/serve/authority_activation.go` wraps `AdoptAuthorityPin`, returns its durable receipt and logs its explicit adopted/pinned-unverified string only after commit. `TestAuthorityActivationReportsDurableOutcome` exercises both outcomes with interpreted fresh inventory and findings mismatch, durable event/source IDs and idempotent retry. This caller requires an already durable matching config; atomic lifecycle integration is 9b-ii-2. |
| #112 small, runtime emission gate | `activateAuthority` validates max-age, database identity, backup hash and the attested running `BinaryRevision` before DB/forge work and again after the seeing read, before adoption. `TestAuthorityActivationReceiptBeforeIO` covers zero/negative ages, stale drain/restore, wrong binary/database, missing backup, absent opt-in and quota failure. `TestAuthorityActivationReceiptExpiresDuringSeeingRead` proves a slow read cannot carry an expired receipt to emission. |
| #127 cursor warning | `internal/serve/outside_push.go` logs reset only when `CommitOutsidePushAudit` returns successful committed CAS. `TestOutsidePushCursorResetWarnsOnlyAfterCAS` loses three consecutive competing CAS attempts without warning, then replaces the cursor and warns exactly once. |
| #127 future-version fallback | `docs/operating.md`, Outside-push observations, explicitly documents overwriting unsupported future-version derived cursors, including after binary downgrade, without waiving journal/backup compatibility. The cursor test starts with an unsupported future-version snapshot and proves replacement. |
| #127 approval-read quota and precedence | `internal/serve/stage_a_gate.go` checks local exact check inputs, member condition shape and bounded evidence before executor policy handling, without DB/approval reads or journaling. Final admission retains fresh approval acquisition and complete evidence validation. `TestLandingChecksPreflightDoesNotAcquireApprovals` covers failed, pending and successful CI, preserves an injected NativeReviews quota error for the final read, and checks no preflight events. `TestLandingChecksWorkerFailurePrecedence` injects approval quota errors alongside invalid/oversized checks and retains permanent blocked outcomes in either input order. |

## A2 boundary

The caller is package-private and requires the existing explicit
`Target.AuthorityRequired` opt-in. No production constructor sets that opt-in,
and no production path calls `activateAuthority`. Neither candidates nor loaded
drafts/pins set it. The caller never installs classification, owner enforcement,
push or observation callbacks. The public schema/loader barrier is unchanged.

`TestAuthorityBarrierJournalCannotInstallRuntimeAuthority` now explicitly tries
the caller after loading each draft, pin and raw-activation history, parsing a
candidate and reloading ordinary GitHub config. Every call rejects before I/O,
no hooks are installed and no pin/observation/verification is emitted. Existing
depth-1/4 pin-only gate and legacy pin-race tests remain part of the focused run.
As in #127, a hand-written journal pin can affect fail-closed push/landing
behavior; journal write access is not a supported activation route. This does
not claim arbitrary authority journals behave like pin-free GitHub journals.
9b-ii-2 must repeat A2 over the complete integrated runtime seam.

## Deferred rows

The ownership table assigns all remaining original runtime rows to **9b-ii-2**:
no-approvals classification/brief/eligibility/decision invocation; conditional
atomic config/pin and new-queue lifecycle; first-cycle observations, warnings,
status/CLI/API/health, author messages and per-check evidence; every landing
start's current-source/exact-digest lock-time admission at depths 1/4 and
settlement after drift; mode-specific rollback, delayed-origin warnings,
history/unrelated blocks, fresh re-enable, all-queue digest propagation;
default-branch/selector/actor/method invalidation; source-repair races, mixed
repository/base transitions, quota/read and no-approvals admission tests.
Batching and fence protocols are unchanged. 9c retains complete governing
acquisition/push integration and the only removal of the public barrier.

## Verification

Final verification artifacts and results are recorded below after execution.
The reproducible driver is `scripts/verify-adr0008-9b-ii.py`; it uses the frozen
offline live-journal export and the same strict comparisons as 9b-i. All Go
commands export both Go 1.27.3 GOROOT and PATH, use the shared heavy flock,
`GOFLAGS=-p=2`, and 4 GiB memory (6 GiB for replay). Database tests use isolated
schemas on shared test Postgres localhost:54329. No live DB, systemd unit,
`openmerge/*` ref, live status API or real GitHub application API is accessed.
No SQL migrations/indexes, Makefile/CI/Dockerfile/test-flag changes or existing
wire fixture edits are included. No new event types or wire fields are added.
