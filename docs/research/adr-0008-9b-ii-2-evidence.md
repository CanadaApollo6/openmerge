# ADR 0008 PR 9b-ii-2a evidence

Trial class: **Hold**. Base: local main `d449adcfb80c31dac02fad89417100f3c2cf1ef6`
(includes #125, #127 and #128). The complete 9b-ii-2 runtime integration is too
large for one reviewable PR. The [ownership plan](adr-0008-9-plan.md#pr-9b-ii-2-review-split-2026-10-21)
now divides it into **2a receipt/preflight hardening**, **2b atomic lifecycle**,
**2c worker policy/projections**, and **2d complete admission**. This run implements
2a and stops. No runtime integration, deployment or organization acceptance is
claimed. 9a's public activation barrier stays until 9c; production constructors
never set the internal opt-in. PR 11 retains organization release acceptance.

## Implemented review requirements

| #128 requirement | Implementation and proof |
| --- | --- |
| 1: configured database identity | `internal/store/database_identity.go:24` obtains the credential-free identity from the actual store pool's configured host, port, database and search_path, without database I/O. The DSN helper at line 13 shares this format with `cmd/openmerge/setup_verify.go:57`. `internal/serve/authority_activation.go:38` uses that identity for both receipt validations; the request no longer has a caller-supplied expected identity. `TestDatabaseIdentityUsesConfiguredConnection` checks the actual isolated schema, and `TestDatabaseIdentityFormat` checks URL/keyword DSNs and credential exclusion. |
| 1: empty identities and mismatch regression | `internal/authority/emission.go:87` rejects empty receipt or expected identities, including empty == empty. `TestFirstEmissionDrainBackupGate` covers all three combinations. `TestAuthorityActivationReceiptBeforeIO` supplies a self-consistent receipt valid for another database, closes the configured store, and requires the receipt-validation diagnostic before any store/forge work. Each failure now asserts its specific diagnostic, so a closed-pool error cannot disguise a bypass. An overlay replacing the configured identity with the receipt's own identity fails this exact test with closed pool instead of the required database-bound validation error. |
| 2: preflight precedence | `internal/serve/stage_a_gate.go:230` documents the explicitly enabled authority preflight's limited guarantee: local check-derived and available non-review input shape/budget errors precede ordinary policy handling. `approved-reviews-by` is unknown until final fresh acquisition. Review-derived overflow can surface only at final admission after successful CI, and an approval-read/quota failure can preempt it. The preflight clears cached approvals on a local PR copy before constructing values; no extra forge read is introduced. |
| 2: chosen behavior test | `internal/serve/landing_checks_test.go:477`, `TestLandingChecksPreflightDefersReviewListOverflow`, runs inside `testSynctest` with its fixture inside the bubble. Preflight ignores 10,001 stale cached approvals and journals nothing. The final gate reads one fresh reviewer whose login exceeds the condition-input byte cap; it records fresh eligibility, returns typed condition-input overflow, and emits neither LandingStarted nor LandingChecksRecorded. The existing failed/pending/successful preflight test proves the queued NativeReviews quota failure remains for final acquisition. |
| 3: documentation nits | `docs/research/adr-0008-9-plan.md:63` and its first-emission section attribute caller max-age/BinaryRevision validation to completed 9b-ii-1. `internal/serve/authority_activation.go:16` names lifecycle slice 9b-ii-2b. Its doc comment at line 27 explicitly states that the receipt can still expire during the adoption transaction, including queue-lock wait and commit. The gate checks before I/O and again before entering adoption; it does not promise transaction-time expiry enforcement. |

The documented preflight limit is deliberate. Cached legacy reviews can differ
from the fresh eligible set and cannot establish its final evidence size.
Performing another approval read on pending/failed CI would spend forge quota
and change the existing read ordering. Final admission retains complete fresh
review evidence and bounded validation before authorization. The oversized login
is a synthetic boundary input, not a claim about GitHub's accepted login lengths.

Independent review caught a false-positive test before full verification: an
unspecific error assertion could accept the closed pool after a receipt bypass.
The validation-specific assertions and failing identity mutation above correct
that gap. The mutation is retained only as an offline overlay artifact; the
committed implementation always uses the configured store identity.

## A2 boundary and remaining runtime rows

`internal/serve/authority_activation.go:35` requires explicit internal opt-in
before it derives identity or validates a receipt. Production
`cmd/openmerge/installations.go:118` continues constructing ordinary targets
without that opt-in or authority hooks. Config parsing, queue folding, planner,
batching and fence protocols are unchanged.

`TestAuthorityBarrierJournalCannotInstallRuntimeAuthority`
(`internal/serve/authority_barrier_test.go:48`) loads draft, pin and raw-activation
histories, parses a candidate, reloads GitHub config and checks no hooks or owner
policy are installed. It now supplies a valid receipt bound to the actual store
and requires the explicit opt-in error at line 104. No authority pin, observation
or verification is emitted. `TestAuthorityBarrierPinCannotSelectAuthorityCheckGate`
continues covering depths 1/4. A handwritten journal pin can still select existing
fail-closed protections; this does not claim arbitrary authority journals behave
like pin-free GitHub journals.

This proves the boundary for **2a only**. The complete runtime seam is not wired
here, and its A2 proof is still mandatory in **2d**.

| Original 9b-ii-2 runtime row | Disposition in updated ownership table |
| --- | --- |
| No-approvals classification, briefs, eligibility and decision invocation | Deferred to 2c (`adr-0008-9-plan.md:149`). |
| Atomic matching ConfigApplied/pin and new queues; ordinary GitHub gets no authority event/downgrade boundary | Deferred to 2b (line 152). |
| First-cycle observations, warnings, status/CLI/API/health, author messages and per-check projections | Deferred to 2c (line 153). |
| Every landing start's current findings/exact digest under lock, depths 1/4; admitted starts settle; unchanged batching/fence | Deferred to 2d (line 154). |
| Mode-specific rollback pin, fresh re-enable and all-queue digest | Deferred to 2b (line 155). |
| Rollback removes only authority admission requirements | Deferred to 2d (line 156). |
| Rollback stops new observations/warning eligibility, preserves delayed-origin warnings/history/unrelated blocks | Deferred to 2c (line 157). |
| Invalidate on default-branch, selector, actor or method changes | Deferred to 2b (line 160). |
| Mixed repository/base transitions and lifecycle quota/read errors | Deferred to 2b (line 163). |
| Source-repair races, admission quota/read failures, no-approvals starts and complete-seam A2 | Deferred to 2d (line 164 and the split description). |

9c's governing acquisition, complete push gate and pin/observation-before-fence
acceptance remain outside these slices. An intermediate internal pin caller is
not activation acceptance. Public rejection can be removed only with 9c's gate.

## Verification

Verified implementation: `010967e8eb00d9fb0e13b78d88d26fc73a13e9e7`.
Baseline: `d449adcfb80c31dac02fad89417100f3c2cf1ef6`. The complete driver
returned **PASS** on 2026-10-21 UTC. Later changes are evidence only.

[Commands and results](adr-0008-9b-ii-2-artifacts/summary.json),
[artifact hashes](adr-0008-9b-ii-2-artifacts/manifest.json),
[raw-cache hashes](adr-0008-9b-ii-2-artifacts/cache-manifest.json),
[full make check log](adr-0008-9b-ii-2-artifacts/make-check.log),
[focused race log](adr-0008-9b-ii-2-artifacts/focused-final.log),
[additional check commands](adr-0008-9b-ii-2-artifacts/additional-checks.json),
[expected identity-mutation failure](adr-0008-9b-ii-2-artifacts/identity-mutation.log),
[depth contracts](adr-0008-9b-ii-2-artifacts/contracts-candidate.json), and
[normalized simulator results](adr-0008-9b-ii-2-artifacts/sim-normalized.json)
are retained. Raw replay streams and complete command logs remain under
`/home/riels/.cache/adr0008-9b-ii-2-verification`; the mutation overlay remains
under `/home/riels/.cache/adr0008-9b-ii-2-mutation`.

- `make check`: **exit 0**, formatting, lint, full race suite and build.
- Focused receipt/identity, activation/A2, public agreement, preflight and worker
  failure-precedence race checks: **exit 0**. The deliberate identity-self-binding
  overlay mutation: **expected exit 1**, caught by the mismatch regression.
- Strict offline live-journal replay: **2,159/2,159** identical actions,
  structural states, snapshot hashes and boundary checkpoints; **8,676** boundary
  probes match. Gates 2/4 and all **22** mandatory synthetic self-tests pass.
  No differing or allowlisted fields.
- Depth-1/4 approval and legacy-block contracts: **90/90** byte-identical,
  including plan/snapshot hashes and sizes. SHA-256:
  `88e727ebe7ea5b570f352c5c661c2cd1642c1b5120f3d3ed086993f4dd00362a`.
- Seeded simulator: both exits **0**, all **146/146** normalized blocks
  byte-identical. SHA-256:
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Existing wire fixtures: **104/104** unchanged, including **90** registry
  fixtures, no added fixtures/events. Manifest SHA-256:
  `d9fc673fbd7ec5410cf1491778157508533ca5c804932caaecb39204f2ae2776`.
- Verified binary SHA-256:
  `3ccf571146359742686256b26e3bf046f6ce2c95fe9eae98bcba611c4e29e191`.

Reproduce from clean checkouts:

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable'
python3 scripts/verify-adr0008-9b-ii-2.py \
  --baseline /home/riels/.cache/adr0008-9b-ii-2-main-d449adc \
  --out /home/riels/.cache/adr0008-9b-ii-2-verification-rerun
```

The driver retains each command and exit status, verifies frozen offline export
hashes and requires clean source trees. All Go commands export both Go 1.27.3
GOROOT and PATH and use the shared heavy flock, GOFLAGS=-p=2 and a 4 GiB memory
limit (6 GiB for replay). Database tests use isolated schemas on shared test
Postgres localhost:54329. No live database, status API, systemd unit, openmerge/*
ref or real application GitHub API is accessed. Git push and PR creation are the
explicitly requested publication operations. No SQL migration/index, Makefile,
CI, Dockerfile, test flag, event type or wire field changes are included. Existing
fixtures are unchanged. Subsequent changes to the verified implementation are
evidence documents and retained receipts only.
