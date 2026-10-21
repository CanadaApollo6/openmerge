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
| 1: server-side database identity (round 2) | `internal/store/database_identity.go` reads cluster system identifier, current database name/OID, unambiguous effective store schema name/OID and journal genesis in a read-only repeatable-read transaction. Setup and runtime share the resolver. See round 2 below for privileges, restore limits and collision regressions. The original DSN-only identity was rejected in round 1 and is removed. |
| 1: empty identities and mismatch regression | `internal/authority/emission.go` rejects empty receipt or expected identities, including empty == empty. `TestAuthorityActivationReceiptBeforeIO` rejects local attestation errors before identity I/O; the different-database case leaves the store open, derives its actual identity, and rejects before the forge reader. `ErrBinaryRevisionMismatch` is asserted with `errors.Is`. |
| 2: preflight precedence | `internal/serve/stage_a_gate.go:230` documents the explicitly enabled authority preflight's limited guarantee: local check-derived and available non-review input shape/budget errors precede ordinary policy handling. `approved-reviews-by` is unknown until final fresh acquisition. Review-derived overflow can surface only at final admission after successful CI, and an approval-read/quota failure can preempt it. The preflight clears cached approvals on a local PR copy before constructing values; no extra forge read is introduced. |
| 2: chosen behavior test | `TestLandingChecksPreflightDefersReviewListOverflow` constructs its fixture inside `testSynctest`. Preflight ignores 10,001 stale cached approvals and journals nothing. `s.execute` acquires fresh reviews including an oversized login, persists eligibility, returns typed condition-input overflow, permanently blocks the operator source and reports `landing-checks-too-large`. Neither LandingStarted nor LandingChecksRecorded is emitted. |
| 3: documentation nits | `docs/research/adr-0008-9-plan.md:63` and its first-emission section attribute caller max-age/BinaryRevision validation to completed 9b-ii-1. `internal/serve/authority_activation.go:16` names lifecycle slice 9b-ii-2b. Its doc comment at line 27 explicitly states that the receipt can still expire during the adoption transaction, including queue-lock wait and commit. Local age/binary checks precede identity I/O; server-side binding is checked before forge reads and before adoption. There is no transaction-time expiry guarantee. |

The documented preflight limit is deliberate. Cached legacy reviews can differ
from the fresh eligible set and cannot establish its final evidence size.
Performing another approval read on pending/failed CI would spend forge quota
and change the existing read ordering. Final admission retains complete fresh
review evidence and bounded validation before authorization. The oversized login
is a synthetic boundary input, not a claim about GitHub's accepted login lengths.

Historical round-1 validation: independent review caught a false-positive test: an
unspecific error assertion could accept the closed pool after a receipt bypass.
The validation-specific assertions and failing identity mutation above correct
that gap. The mutation is retained only as an offline overlay artifact; the
round-1 implementation used the configured store identity. Round 2 replaces that
insufficient DSN binding with server-side identity; the old overlay is historical
evidence only.

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

## Round-1 verification (historical)

Verified implementation: `010967e8eb00d9fb0e13b78d88d26fc73a13e9e7`.
Baseline: `d449adcfb80c31dac02fad89417100f3c2cf1ef6`. The complete driver
returned **PASS** on 2026-10-21 UTC. These results predate the round-2 implementation below.

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
fixtures are unchanged. Those round-1 results do not verify the round-2 implementation.

## Round 2 — connected database identity and worker overflow

The P2 was real: configured DSN strings collapsed `options`-selected schemas,
role defaults, alternate endpoints and database reincarnations. The v2 receipt
identity is `openmerge-db-v2:sha256:` followed by a SHA-256 digest of a JSON-framed
sequence of cluster `system_identifier`, `current_database()` name and catalog
OID, effective store schema name and catalog OID, and earliest event ID, UTC
microsecond `recorded_at` and canonical JSONB payload text. No connection secrets
or raw payloads appear in the identity or its diagnostics.

The resolver uses a read-only repeatable-read transaction and no migrations,
indexes or new tables. Every existing persistent store table, including
`schema_migrations`, must resolve uniquely in `current_schemas(true)` and share a
schema. It rejects shadowed copies rather than choosing the first copy. Missing,
split or empty histories fail closed. `pg_control_system()` access is required;
unavailable/denied access produces an explicit EXECUTE privilege diagnostic and
no DSN fallback. ACL failure is tested only inside a uniquely named scratch
database; shared database privileges are untouched.

Why these inputs: the server system identifier follows the actual connected
cluster regardless of DSN/host ordering; database and schema OIDs distinguish
recreation; effective schema resolution handles options and role defaults;
genesis binds the receipt to an existing journal origin and is stable under
ordinary appends/reconnects. This is not a full-history digest, authenticated
attestation, restore counter or machine identity. A physical clone preserving
cluster identity, database/schema OIDs and genesis, or a later divergent history
sharing that genesis, is indistinguishable. No read-only catalog/genesis design
can distinguish those copies. Restore/recreation requires the operator to repeat
the receipt procedure; no universal restore-detection guarantee is claimed.

Activation requires `w.AuthorityRequired` before identity I/O; production still
leaves it unset. Local receipt validity is checked before DB access, then actual
identity is resolved before forge reads and again immediately before adoption.
Identity changes or expiry during adoption remain outside this caller's
transaction guarantee. `setup --verify` and the read-only
`setup rulesets --print-database-identity` use the same resolver. Existing
hand-written endpoint receipts must be regenerated; the format/privileges and
limits are documented in [operating instructions](../operating.md#receipt-database-binding).

`TestDatabaseIdentityEffectiveSchemaAndReconnect` checks two options-selected
schemas, ambiguity rejection, appends, genesis replacement and pool reconnects.
`TestDatabaseIdentityRoleDefaultSchema` changes a role's default schema without
changing its DSN and matches each actual store. `TestDatabaseIdentityDatabaseIncarnation`
checks a second database and drop/recreate at the same name, plus denied cluster
identity access. All scratch schemas/databases/roles are uniquely named and
removed by test cleanup. `TestDatabaseIdentityEmptyJournalFailsClosed` covers an
uninitialized history. The worker overflow regression now drives `s.execute`
and asserts the operator block/status, with all fixtures inside the synctest
bubble. Clearing cached approvals is documented as an allocation optimization;
removing the review attribute is what makes preflight reviews unknown.

### Round-2 verification results

The clean-tree driver returned **PASS** for production implementation
`287cb0feeb5efa78209b330797ddce33b1de4ae0` against baseline
`d449adcfb80c31dac02fad89417100f3c2cf1ef6`. Subsequent tracked changes only strengthen the
recreation regression by preserving the genesis timestamp across drop/recreate,
and retain documentation/artifacts. That exact final test source passed the
focused race run via overlay. It requires the database OID to distinguish the
same name/schema/genesis; an overlay omitting database OID fails with
`database incarnation collided` (expected exit 1). Production code is unchanged
from the clean-tree run.

- `make check`: **exit 0**, formatting, lint, complete short race suite and build.
- Final focused identity/activation/A2/preflight/command/sentinel race run:
  **exit 0**. Database-OID removal mutation: **expected exit 1**.
- Strict replay: **2159/2159** identical actions,
  structural states and snapshot hashes, **8,676** matching boundary probes;
  gates 2/4 and all mandatory self-tests pass. No differing/allowlisted fields.
- Depth-1/4 contracts: **90/90 byte-identical**. SHA-256:
  `88e727ebe7ea5b570f352c5c661c2cd1642c1b5120f3d3ed086993f4dd00362a`.
- Seeded simulator: both exits **0**, **146/146 normalized blocks byte-identical**.
  SHA-256: `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Existing fixtures: **104/104 unchanged**, including **90** registry fixtures;
  no new fixtures/events/wire fields. Manifest:
  `d9fc673fbd7ec5410cf1491778157508533ca5c804932caaecb39204f2ae2776`.
- Clean implementation binary SHA-256:
  `1032a2e96e478e2a0bfc49432bb8e8077f2ff75a021805817db0942435fd3891`.

Retained [summary/commands](adr-0008-9b-ii-2-artifacts/round-2/summary.json),
[make check log](adr-0008-9b-ii-2-artifacts/round-2/make-check.log),
[focused race log](adr-0008-9b-ii-2-artifacts/round-2/focused-final.log),
[additional commands/source hashes](adr-0008-9b-ii-2-artifacts/round-2/additional-checks.json),
[expected mutation failure](adr-0008-9b-ii-2-artifacts/round-2/identity-oid-mutation.log),
[mutation patch](adr-0008-9b-ii-2-artifacts/round-2/identity-oid-mutation.patch),
[depth contracts](adr-0008-9b-ii-2-artifacts/round-2/contracts-candidate.json),
[normalized simulator blocks](adr-0008-9b-ii-2-artifacts/round-2/sim-normalized.json),
[artifact hashes](adr-0008-9b-ii-2-artifacts/round-2/manifest.json), and
[raw-cache hashes](adr-0008-9b-ii-2-artifacts/round-2/cache-manifest.json).
Full streams/logs remain in `/home/riels/.cache/adr0008-9b-ii-2-round2-verification`; overlays remain in `/home/riels/.cache/adr0008-round2-strengthening`.
The same reproduction command above applies with `--out /home/riels/.cache/adr0008-9b-ii-2-round2-verification`.
All Go commands use both exported Go 1.27.3 GOROOT/PATH and the shared flock,
GOFLAGS=-p=2, 4 GiB limit (6 GiB for replay). No live DB/status/systemd/OpenMerge
refs or application GitHub APIs were accessed. A2/public barriers remain intact;
this is still Hold, with no queue/merge or runtime deployment acceptance.

## Round 3 — numeric genesis ordering and diagnostic corrections

Verified clean implementation: `10b60faf6e02ab6cdefda5ede454859642eff3e5`,
against unchanged baseline `d449adcfb80c31dac02fad89417100f3c2cf1ef6`.
The following evidence commit adds only this section and retained artifacts.
Trial class remains **Hold**; no queue/merge or runtime acceptance is claimed.

The round-2 query's `ORDER BY id` resolved to the text output column from
`id::text`. That selected lexicographic rather than numeric genesis and forced
`Limit → Sort → Seq Scan`. The production query now aliases the journal as `e`
and orders explicitly by `e.id`. The same query builder is used by the resolver
and the EXPLAIN regression. No migration, index or table was added.

`TestDatabaseIdentitySequenceGapAndIndexedGenesis` rolls back the first insert,
so the first committed ID is 2. Appends through IDs 10, 100 and 10,000 preserve
the identity. With ordinary planner settings after ANALYZE, EXPLAIN JSON must
contain the existing `events_pkey` Index Scan and no Sort or Seq Scan. The passing
plan is `Limit → Index Scan`. An offline overlay restoring `ORDER BY id` fails
both stability and plan assertions, reproducing `Sort Key: ((id)::text)` and the
full-journal Seq Scan (expected exit 1).

The store already had direct overflow coverage in
`TestLandingChecksValidateLockedBindings/overflow`, independent of the earlier
worker gate. It supplies an oversized landing-checks expression directly to
`StartLanding`. The assertion now also requires the returned error itself to be
`LandingChecksTooLargeError`, with no wrapper, no stale-work classification,
no appended events or landing projection, and the claim still in flight.
Disabling the store's `errors.Is` propagation branch makes this exact subtest
fail with the stale-work wrapper (expected exit 1). Thus the branch is reachable
and directly protected; the fresh-review worker regression remains separate.

Only SQLSTATE `42501` from `pg_control_system()` now produces EXECUTE grant
advice. Other failures report a sanitized SQLSTATE or fixed cause category.
Connection configuration, cancellation/deadline and network failures likewise
retain a safe cause without printing driver/server text, DSNs or passwords.
`TestReceiptIdentityDiagnostics` covers privilege and non-privilege SQLSTATEs,
secret-bearing errors, malformed connection configuration and a canceled actual
connection attempt. The existing scratch-database denied-EXECUTE test still
passes. The receipt comment is wrapped, and operating instructions explicitly
state that a `pg_dump` restore into a new cluster changes the system identifier
and invalidates the original receipt.

### Round-3 verification results

The unchanged clean-tree driver returned **PASS**:

- `make check`: **exit 0** (format, lint, complete short race suite and build).
- Focused identity, diagnostics, store overflow, activation/A2 and preflight
  race run: **exit 0**. Both overlay mutations: **expected exit 1**.
- Strict offline replay: **2,159/2,159** identical actions, structural states,
  snapshot hashes and boundary checkpoints; **8,676** matching probes. Gates
  2/4 and all **22** mandatory self-tests pass, with no differing fields.
- Depth-1/4 contracts: **90/90 byte-identical**. SHA-256:
  `88e727ebe7ea5b570f352c5c661c2cd1642c1b5120f3d3ed086993f4dd00362a`.
- Simulator: both exits **0**, **146/146 normalized blocks byte-identical**.
  SHA-256: `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Existing fixtures: **104/104 unchanged**, including **90** registry fixtures;
  no new fixtures/events/wire fields. Manifest SHA-256:
  `d9fc673fbd7ec5410cf1491778157508533ca5c804932caaecb39204f2ae2776`.
- Clean implementation binary SHA-256:
  `43fdac3e0cff3f656b95310c9b358d71ae386cf8082a97197c402a617a8e38d6`.

Retained [summary/commands](adr-0008-9b-ii-2-artifacts/round-3/summary.json),
[make check log](adr-0008-9b-ii-2-artifacts/round-3/make-check.log),
[focused race log and passing plan](adr-0008-9b-ii-2-artifacts/round-3/focused-final.log),
[additional commands/source hashes](adr-0008-9b-ii-2-artifacts/round-3/additional-checks.json),
[numeric-order mutation failure](adr-0008-9b-ii-2-artifacts/round-3/numeric-order-mutation.log),
[numeric-order mutation patch](adr-0008-9b-ii-2-artifacts/round-3/numeric-order.patch),
[overflow propagation mutation failure](adr-0008-9b-ii-2-artifacts/round-3/overflow-mutation.log),
[overflow mutation patch](adr-0008-9b-ii-2-artifacts/round-3/overflow-propagation.patch),
[depth contracts](adr-0008-9b-ii-2-artifacts/round-3/contracts-candidate.json),
[simulator blocks](adr-0008-9b-ii-2-artifacts/round-3/sim-normalized.json),
[artifact hashes](adr-0008-9b-ii-2-artifacts/round-3/manifest.json), and
[raw-cache hashes](adr-0008-9b-ii-2-artifacts/round-3/cache-manifest.json).
Full streams/logs remain in `/home/riels/.cache/adr0008-9b-ii-2-round3-verification`;
mutation overlays remain in `/home/riels/.cache/adr0008-9b-ii-2-round3-focused`.
Reproduce with the earlier driver command and
`--out /home/riels/.cache/adr0008-9b-ii-2-round3-verification-rerun`.

All Go commands export both Go 1.27.3 GOROOT/PATH and use the shared heavy lock,
GOFLAGS=-p=2 and 4 GiB limit (6 GiB for replay). Only shared test Postgres
localhost:54329 was used. The four schemas left by earlier/unknown runs were
not attributable to this run and were not dropped. The recorded
[before](adr-0008-9b-ii-2-artifacts/round-3/schemas-before-driver.txt) and
[after](adr-0008-9b-ii-2-artifacts/round-3/schemas-after-driver.txt) lists match
exactly; this run left no additional schemas. Scratch databases/roles also use
test cleanup. No live DB/status API, systemd unit, `openmerge/*` ref or real
application GitHub API was accessed. Git/PR publication is explicitly requested.
No Makefile/CI/Dockerfile/test-flag changes were made. Wire-v1 remains additive,
existing fixtures are untouched, and A2/public activation barriers are retained.
