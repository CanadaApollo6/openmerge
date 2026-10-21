# ADR 0008 PR 9b-i evidence

Trial class: **Hold**. This change implements **9b-i, prerequisite hardening**,
from the [9b review split](adr-0008-9-plan.md#pr-9b-review-split-2026-10-21).
The full 9b lifecycle and admission wiring exceeds one reviewable PR. This run
stops after 9b-i. The public activation barrier remains until 9c; deployment
remains held until period B drains, and PR 11 owns organization acceptance.

## Implemented prerequisites

| Plan row or carry-over | Implementation and proof |
| --- | --- |
| Wire/state draft adoption; #112(1) | `internal/queue/authority_adoption.go:27` validates the actual policy-bound forge input and queue base, reinterprets fresh typed inventory with caller findings discarded, and requires findings equality with the authenticated draft as well as full protection fingerprint equality. `TestAuthorityDraftAdoptionInterpretsFactsAndRequiresMatchingFindings` covers stripped and invented findings, mismatched App/base inputs, and negative evidence. |
| Persisted fold; #112(1) | `internal/queue/authority.go:184` requires retained interpretation context before raw activation can establish verification. Optional `verification_input` is additive and strictly decoded. `TestAuthorityRawActivationCannotFoldVerified` exercises historical pins, stripped findings, incomplete coverage and bad context through wire decode, fold and restart. |
| Persisted snapshot provenance | `internal/queue/authority.go:284` validates retained interpreted adoption provenance. The projection preserves its original adoption source across same-binding governing-commit updates. A retained baseline-shaped snapshot with an unsupported raw-activation pass fails with a replay/reverify hint. `TestAuthoritySnapshotRejectsRawActivationVerification` and `TestAuthorityAdoptedProvenanceSurvivesSameBindingRebind` cover both cases. Historical wire fixtures remain readable and unchanged. |
| #106(b), #112(2) | `internal/store/write_restriction.go:225` takes the existing queue lock and checks the latest durable pin/config before either full state replay or bounded proposed-history lookup. `TestAuthorityAdoptionReceiptsAndNoOpRetry` retries three times with an impossible draft ID and unusable read input, then restarts and retries again. The same pin receipt returns with no added events. |
| #112(3) | `internal/store/write_restriction.go:198` returns `AuthorityAdoptionReceipt` with explicit `adopted` or `pinned-unverified`, queue ID, durable pin event ID, and adopted proposed-source event ID (zero when none was adopted). Its operator string preserves that distinction. The internal activation caller and its output wiring remain 9b-ii. |
| #112 emission max-age and BinaryRevision | The baseline already implements `EmissionReceipt.ValidateForBinary` in `internal/authority/emission.go:74` and invokes it before DB/forge work in `cmd/openmerge/setup_verify.go:82`. New `TestVerificationReceiptRejectedBeforeDatabaseOrSeeingRead` covers zero/negative age, stale drain/restore, and another binary at the command boundary. No receipt gate was removed or bypassed. 9b-ii must validate receipt max-age and attested `BinaryRevision` at the internal activation caller before first emission; command-boundary checks do not satisfy that caller gate. |
| #112 stale `setup --verify` | The baseline already returns an error with `retry --verify` when an applied seeing read loses its source basis (`cmd/openmerge/setup_verify.go:195`). `TestVerifyTargetsStaleBasisRetainsEvidenceAndRequiresRetry` proves nonzero outcome, retained evidence, unchanged verification time and successful fresh retry. |
| #120 exact final budget | `internal/executor/landing_decision.go:29` supplies the final attempt ID to the callback before evidence construction. `internal/serve/landing_checks.go:16` includes it in the builder header, with no later mutation. `TestLandingChecksBuilderBudgetsFinalAttempt` uses a 4,096-byte escaped attempt ID, fills the actual wire envelope to exactly 1 MiB, verifies the counted size against encoding and decoding, and rejects another check. |
| #120 permanent builder failures and direct caps | `internal/queue/landing_checks_size.go:38` defines typed invalid evidence separately from overflow. Builder boundary errors retain that classification. `internal/serve/worker.go:295` records an operator block and blocked check status for either permanent failure. Header, collections, source revisions, member identity and per-member references have direct caps. Tests cover invalid UTF-8, times, identities and condition input, counts and both worker outcomes. |
| #120 overflow precedence and oracle | `internal/serve/stage_a_gate.go:287` collects bounded authority evidence for every member before returning unmet conditions. Legacy approval evaluation still returns at its original first unmet member. `TestLandingChecksGateOverflowPrecedesUnmetMembers` covers overflow in the first and later member, plus bounded unmet conditions and absence of start/check events. New synctest tests use `testSynctest` with fixtures inside the bubble. `FuzzLandingChecksJSONSizeMatchesEncoder` covers escaping, Unicode, null tables, nullable provenance, numeric boundaries and timestamps. |
| 9a nits | `internal/config/config.go:53` documents that `Load` leaves reserved authority fields empty. `internal/authority/actor.go:45` documents repository-admin as historical-wire-only. `schema/openmerge.v1.json:19` replaces the opaque `not: {}` with a diagnostic string `const` contradictory to the existing object type, preserving rejection of every `break_glass` value. The existing public schema/loader/CLI agreement corpus remains green. |

## A2 merge-safety boundary

The check-evidence gate now requires explicit internal `Target.AuthorityRequired`
opt-in as well as an OpenMerge pin (`internal/serve/stage_a_gate.go:238`). No
production constructor sets that opt-in. This slice adds no production activation
caller. `AdoptAuthorityPin` remains called only by tests. Production `installationBundle.target`
(`cmd/openmerge/installations.go:118`) supplies legacy rule, permission and
classic-protection checks, with no authority callbacks. Setup's
`LoadAuthorityCandidate` parses a local candidate for setup only. Runtime
`repositoryConfig` still uses `config.Load`, which rejects `openmerge`.

The regression `TestAuthorityBarrierJournalCannotInstallRuntimeAuthority`
(`internal/serve/authority_barrier_test.go:47`) goes beyond parser rejection.
It loads handcrafted draft, pin and raw-activation events from the journal,
confirms pins really fold through `State.AuthorityPin()`, parses an OpenMerge
setup candidate, reloads the ordinary GitHub config twice, and asserts:

- No authority callback or mandatory owner policy is installed.
- GitHub planner configuration is unchanged.
- No policy pin, authority observation or verification event is emitted.
- A proposed draft retains legacy push options. A surviving pin preserves the
  baseline fail-closed push behavior and cannot supply usable authority context.

The final point is deliberate: a hand-crafted journal pin requires journal write
access and can change GitHub-mode behavior in the fail-closed direction. Push
options fail closed (pre-existing on main); landing now returns
`LandingChecksRequiredError` where main recorded checks. This behavior is retained.
Baseline pins already affect push guards and status. This change does **not**
claim arbitrary authority-bearing journals act
like pin-free GitHub journals. It proves that journal data alone cannot promote
the dormant activation seam. Raw activation cannot establish verified state,
including through a retained snapshot. Ordinary GitHub behavior is compared
separately by strict replay, simulator and depth contracts below.

`TestAuthorityBarrierPinCannotSelectAuthorityCheckGate` covers public GitHub
approval queues at depths 1 and 4. The expanded
`TestLandingChecksLegacyGateAndPinRace` runs an oversized unused check with a
pin present before acquisition or appearing at commit. Both retain the legacy
reader and existing `LandingChecksRequiredError`, with no start or check-evidence
events. A handcrafted pin cannot select the new builder or its permanent errors.

9b-ii must repeat A2 against its complete internal activation caller. A pin,
draft or candidate must not become its enablement signal. This prerequisite
proof does not stand in for that future runtime proof.

## Round 1 verification

Verified implementation: `81d523f982d01112975fbb43b97686ea6f114c56`. Baseline:
`9a1b4af968d66ebb71a9ab0f8541ca30dcbc160e`. Verification completed on
2026-10-21 UTC. This records the round-1 implementation; round-2 results follow below.
[Commands and results](adr-0008-9b-artifacts/summary.json),
[artifact hash manifest](adr-0008-9b-artifacts/manifest.json),
[raw-cache hash manifest](adr-0008-9b-artifacts/cache-manifest.json),
[full check log](adr-0008-9b-artifacts/make-check.log),
[depth contracts](adr-0008-9b-artifacts/contracts-candidate.json), and
[simulator normalization](adr-0008-9b-artifacts/sim-normalized.json) are retained.

- `make check`: **exit 0**, formatting, zero lint issues, complete race suite and build.
- Strict offline live-journal replay: **2,159/2,159** identical actions, full
  structural states, snapshot hashes and boundary checkpoints. All **8,676**
  boundary probes agree. Gates 2 and 4 pass, as do all **22** mandatory synthetic
  self-tests. No differing fields or allowlisted differences.
- Depth-1 and depth-4 approval and legacy-block contracts: **90/90** byte-identical
  records, including plan/snapshot hashes and sizes. SHA-256:
  `88e727ebe7ea5b570f352c5c661c2cd1642c1b5120f3d3ed086993f4dd00362a`.
- Existing wire fixtures: **104/104** unchanged, including 90 registry fixtures.
  No added event type or fixture.
- Seeded simulator: both trees **exit 0**, all **146** normalized blocks
  byte-identical. SHA-256:
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Verified binary SHA-256:
  `95b41bec7b15b82df6b951f81b86dddf5cf6e892bf9dee81187831085b1624d0`.

Reproduce from a clean checkout using the retained baseline worktree:

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable'
python3 scripts/verify-adr0008-9b.py \
  --baseline /home/riels/.cache/adr0008-9b-main-9a1b4af \
  --out /home/riels/.cache/adr0008-9b-verification-rerun
```

The reusable driver is `scripts/verify-adr0008-9b.py`. The driver uses the same
strict replay and normalization code as 9a, checks clean source trees, verifies
the frozen export hashes, and retains each command, exit status and full log.
No live database export is taken. Raw streams remain in the cache paths named
by the summary and hash manifest.

All Go commands export both Go 1.27.3 GOROOT and its PATH prefix. Heavy commands
use `/home/riels/.cache/openmerge-heavy.lock`, `GOFLAGS=-p=2`, and a 4 GiB
memory limit, raised to 6 GiB for replay. DB tests use isolated schemas in the
shared test Postgres on localhost:54329. No live DB, status API, service units,
organization settings or remote OpenMerge refs are accessed. Application tests
use fake or intercepted forge calls; no real GitHub application calls occur.

An initial full verification was stopped when review identified the pin-only
check-evidence selection. Only the process tree started for that superseded run
was terminated. The next full run found three bootstrap race cases that hard-coded
a source revision advanced by raw activation in the baseline. Their injected
repair was therefore stale after the hardened fold. The tests now capture the
current revision before injection, preserving the pre-push source-change check.
The full focused bootstrap/barrier/landing-check set then passed with `-race`.
The reported final run verifies both corrections.

No SQL migration/index, Makefile, CI, Dockerfile or test-flag changes are included.
There are no new event types and no existing fixture modifications. The new
pin context and snapshot provenance are optional additive snake_case fields.

## Round 2 review corrections

Verified implementation: `8e19b0e9e327b6ee043e84f7c3295f25629dbaa3`. Baseline:
`9a1b4af968d66ebb71a9ab0f8541ca30dcbc160e`. Trial class remains **Hold**;
public authority activation remains blocked, and this run stops at 9b-i.

| Review item | Change and proof |
| --- | --- |
| Astra P2 / #120 real worker precedence | `internal/executor/executor.go:279` invokes authority-only `LandingDecisionGate.ValidateChecks` before rerun-permission or policy rejection. `internal/serve/stage_a_gate.go:339` builds/validates all evidence with the final attempt ID before returning ordinary unmet conditions to the original executor handling. Final admission still rereads approvals and validates evidence. `TestLandingChecksWorkerFailurePrecedence` runs through `s.execute`, inside `testSynctest` with fixtures inside each bubble: failed required CI plus overflow/invalid/bounded extra checks in either position, authority enabled/disabled, exact permanent blocked status reason and no landing/check-evidence events. Existing two-member precedence tests cover the oversized member first or later. |
| Low 1: ownership | `docs/research/adr-0008-9-plan.md:92` splits prerequisite wire/state work from lifecycle wiring; the tables assign each original requirement once to 9a, 9b-i, 9b-ii or 9c, with no ambiguous 9b ownership. |
| Low 2: runtime receipt | `docs/research/adr-0008-9-plan.md:117` assigns max-age and attested `BinaryRevision` validation at the internal activation caller, before first emission, to 9b-ii. Existing command-boundary verification does not discharge that runtime gate. |
| Low 3: rejected snapshot cursor | **Safe fallback chosen.** `internal/store/outside_push.go:72` discards unusable derived progress and rebuilds from the journal in bounded 256-event passes. Original cursor bytes remain the CAS expectation; immutable BaseMoved dedupe keys preserve existing receipts. `internal/serve/outside_push.go:78` logs the reason. `TestOutsidePushRejectedAuthorityCursorRescansWithoutDuplicateReceipts` covers a rejected raw-activation v7 snapshot, competing CAS, restart, bounded scanning and no lost/duplicate receipts. No operator cursor deletion is required. |
| Low 4: orphan/size validation | `internal/queue/authority.go:58` rejects `VerificationInput` without activation; `internal/queue/authority_adoption.go:25` bounds it to 1,000 selectors, 1,000 refs and 1 MiB JSON. `TestAuthorityVerificationInputRequiresActivationAndBoundedSize` covers orphaned inputs, collection limits and oversized App/selector/ref values through validation and decoding. |
| Low 5: raw vs interpreted evidence | `internal/queue/authority_adoption.go:83` requires full deep equality between interpreted and retained evidence before folding raw evidence. `TestAuthorityDraftAdoptionRejectsRawCoverageWithSameFingerprint` hand-builds a pin with reordered raw Coverage, confirms its fingerprint is unchanged, round-trips the event and proves it remains unverified. |
| Low 6: journal boundary | The A2 section above explicitly states that journal write access can create a pin that changes GitHub-mode behavior in the fail-closed direction: push options already failed closed on main; landing now returns `LandingChecksRequiredError` where main recorded checks. That behavior is retained. |

The optional concurrent adoption test was not added. The mandatory regression
coverage and all existing pin idempotence tests run in the complete race suite.

[Round-2 commands and results](adr-0008-9b-artifacts/round-2/summary.json),
[artifact hashes](adr-0008-9b-artifacts/round-2/manifest.json),
[raw-cache hashes](adr-0008-9b-artifacts/round-2/cache-manifest.json), and
[full make check log](adr-0008-9b-artifacts/round-2/make-check.log) are retained
separately from round 1. The complete driver returned **PASS** on 2026-10-21 UTC.

- `make check`: **exit 0**, formatting, zero lint issues, complete race suite and build.
- Strict replay: **2,159/2,159** identical actions, structural states, snapshot
  hashes and checkpoints; **8,676** matching boundary probes; gates 2/4 and
  all **22** mandatory synthetic self-tests pass, with no allowed differences.
- Depth contracts: **90/90** byte-identical; SHA-256
  `88e727ebe7ea5b570f352c5c661c2cd1642c1b5120f3d3ed086993f4dd00362a`.
- Seeded simulator: **146/146** normalized blocks byte-identical, both exits 0;
  SHA-256 `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Existing wire fixtures: **104/104** unchanged, including 90 registry fixtures;
  no added event type or modified existing fixture.
- Verified binary SHA-256:
  `04c1a4c513cf35b2a7ffab4338cba87da017b096873b5195988132e6bebdd43f`.

Reproduce with the same toolchain/DB exports and driver command above, using
`--out /home/riels/.cache/adr0008-9b-round2-verification-rerun`.
All Go commands used the required flock, parallelism and memory limits; replay
used 6 GiB. Only the shared test Postgres and frozen offline journal export were
used. No live DB/status/service refs or application GitHub API calls occurred.
No migrations/indexes, Makefile, CI, Dockerfile or test flags changed.
Subsequent changes to the verified implementation are evidence only.

## Remaining 9b-ii and 9c work

All 9b runtime rows remain 9b-ii: conditional atomic config/pin emission and new
queues, no-approvals classification/brief/eligibility/decision invocation,
first-cycle observations and PRs 4–8 projections, all landing/remedy starts at
depths 1 and 4 checking current sources and exact digest under lock, settlement
after drift, rollback and fresh re-enable, every served queue receiving the
digest, and default-branch/selector/actor/method invalidation. Source-repair
races, mixed repository/base transitions, quota/read failures and no-approvals
start acceptance belong with that wiring. Batching and fence protocol are
unchanged here.

9c still owns typed governing acquisition/import/setup integration, every
generated-object push guard, pin/observation-before-fence ordering and complete
real-git activation acceptance. The older future-schema repository-admin fixture
and semantic governing-config equality remain 9c. The old-worker drain,
restore-tested backup, attested emitting binary and v7 downgrade procedure
remain required before any emission deployment. This run stops at 9b-i.
