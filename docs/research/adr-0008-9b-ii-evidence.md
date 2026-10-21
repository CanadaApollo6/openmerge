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
| #112(3), internal caller outcome | `internal/serve/authority_activation.go:31` wraps `AdoptAuthorityPin`, returns its durable receipt and logs its explicit adopted/pinned-unverified string only after commit. `TestAuthorityActivationReportsDurableOutcome` exercises both outcomes with interpreted fresh inventory and findings mismatch, durable event/source IDs and idempotent retry. This caller requires an already durable matching config; atomic lifecycle integration is 9b-ii-2. |
| #112 small, runtime emission gate | `activateAuthority` validates max-age, database identity, backup hash and the attested running `BinaryRevision` before DB/forge work and again after the seeing read, before adoption. `TestAuthorityActivationReceiptBeforeIO` covers zero/negative ages, stale drain/restore, wrong binary/database, missing backup, absent opt-in and quota failure. `TestAuthorityActivationReceiptExpiresDuringSeeingRead` proves a slow read cannot carry an expired receipt to emission. |
| #127 cursor warning | `internal/serve/outside_push.go:96` logs reset only when `CommitOutsidePushAudit` returns successful committed CAS. `TestOutsidePushCursorResetWarnsOnlyAfterCAS` loses three consecutive competing CAS attempts without warning, then replaces the cursor and warns exactly once. |
| #127 future-version fallback | `docs/operating.md:1513`, Outside-push observations, explicitly documents overwriting unsupported future-version derived cursors, including after binary downgrade, without waiving journal/backup compatibility. The cursor test starts with an unsupported future-version snapshot and proves replacement. |
| #127 approval-read quota and precedence | `internal/serve/stage_a_gate.go:339` checks local exact check inputs, member condition shape and bounded evidence before executor policy handling, without DB/approval reads or journaling. Final admission retains fresh approval acquisition and complete evidence validation. `TestLandingChecksPreflightDoesNotAcquireApprovals` covers failed, pending and successful CI, preserves an injected NativeReviews quota error for the final read, and checks no preflight events. `TestLandingChecksWorkerFailurePrecedence` injects approval quota errors alongside invalid/oversized checks and retains permanent blocked outcomes in either input order. |

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

The first full run failed four newly extended worker precedence cases: their
NativeReviews failure was injected before the worker's mandatory initial trust
refresh, so it never reached the authority preflight being tested. The injection
now happens when the executor reads the tested checks, after that trust refresh.
This preserves the initial trust-read ordering and tests the intended quota seam;
no production correction was needed for that test failure. The failed run remains
in `/home/riels/.cache/adr0008-9b-ii-verification`.

Verified implementation: `86b51f3654e50d9fca1a45ac1b099f6e0404f260`.
Baseline: `6aad022966f6730307f63bfbafb1d03f84fc2a61`.
The complete driver returned **PASS** on 2026-10-21 UTC. Subsequent changes
are evidence documents and retained receipts only.

[Commands and results](adr-0008-9b-ii-artifacts/summary.json),
[artifact hashes](adr-0008-9b-ii-artifacts/manifest.json),
[raw-cache hashes](adr-0008-9b-ii-artifacts/cache-manifest.json),
[full make check log](adr-0008-9b-ii-artifacts/make-check.log),
[corrected focused race log](adr-0008-9b-ii-artifacts/focused-final.log),
[depth contracts](adr-0008-9b-ii-artifacts/contracts-candidate.json), and
[normalized simulator results](adr-0008-9b-ii-artifacts/sim-normalized.json)
are retained. Raw replay streams and complete command logs remain under
`/home/riels/.cache/adr0008-9b-ii-final-verification`, with hashes in the manifest.

- `make check`: **exit 0**, formatting, zero lint issues, complete race suite and build.
- Corrected focused race check: **exit 0**; worker quota/failure precedence,
  failed/pending/successful preflight, and receipt expiry during seeing read.
- Strict offline live-journal replay: **2,159/2,159** identical actions,
  structural states, snapshot hashes and boundary checkpoints. All **8,676**
  boundary probes match; gates 2/4 and all **22** mandatory synthetic self-tests
  pass. No differing or allowlisted fields.
- Depth-1/4 approval and legacy-block contracts: **90/90** byte-identical,
  including plan/snapshot hashes and sizes. SHA-256:
  `88e727ebe7ea5b570f352c5c661c2cd1642c1b5120f3d3ed086993f4dd00362a`.
- Seeded simulator: both exits **0**, all **146/146** normalized blocks
  byte-identical. SHA-256:
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Existing wire fixtures: **104/104** unchanged, including **90** registry
  fixtures; no added fixture or event type. Manifest SHA-256:
  `d9fc673fbd7ec5410cf1491778157508533ca5c804932caaecb39204f2ae2776`.
- Verified binary SHA-256:
  `e01817f615246ec956e66da945e6fa8a46b251c2b9fc19e1892585d53770fa35`.

Reproduce from clean checkouts:

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable'
python3 scripts/verify-adr0008-9b-ii.py \
  --baseline /home/riels/.cache/adr0008-9b-ii-main-6aad022 \
  --out /home/riels/.cache/adr0008-9b-ii-verification-rerun
```

The reproducible driver is `scripts/verify-adr0008-9b-ii.py`; it uses the frozen
offline live-journal export and the same strict comparisons as 9b-i. All Go
commands export both Go 1.27.3 GOROOT and PATH, use the shared heavy flock,
`GOFLAGS=-p=2`, and 4 GiB memory (6 GiB for replay). Database tests use isolated
schemas on shared test Postgres localhost:54329. No live DB, systemd unit,
`openmerge/*` ref, live status API or real GitHub application API is accessed.
No SQL migrations/indexes, Makefile/CI/Dockerfile/test-flag changes or existing
wire fixture edits are included. No new event types or wire fields are added.
