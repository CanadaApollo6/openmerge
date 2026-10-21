# ADR 0008 PR 9b-ii-2b evidence

Trial class: **Hold**. Base: local main
`b9f00f86854c62a206ba2815a12b19a114a779c5`, including #125, #127, #128 and #129.
This change implements the entire **2b atomic lifecycle** slice in the
[ownership plan](adr-0008-9-plan.md). It does not split 2b further.
Public activation remains rejected. Deployment and organization acceptance remain
held; worker projections belong to 2c, admission to 2d, and the complete forge
activation gate to 9c.

## Requirements and implementation

| 2b requirement | Implementation and proof |
| --- | --- |
| Matching atomic config/pin on entry, active policy changes and exit; resolved actor | `internal/serve/reconcile.go:104` prepares the internal lifecycle and routes config emission through `activateAuthority`. `internal/serve/authority_activation.go:79` consumes the 9b-ii-1 caller with the connected v2 database identity and receipt checks. `internal/store/write_restriction.go:356` validates the matching commit/digest, takes the existing queue lock, and commits ConfigApplied followed by PolicyAuthorityApplied in one transaction. `TestAuthorityLifecycleAdoptsDraftWithAtomicConfig` proves interpreted draft adoption through the actual configuration path. Store tests cover invalid pairs/bindings/drafts, full-config idempotence, concurrent retries/pairs and historical window backfill. |
| New queues; ordinary GitHub gets no new event or downgrade boundary | `internal/serve/reconcile.go:209` requires both explicit opt-in and the private resolver before candidate parsing. `internal/serve/authority_activation.go:145` skips pin emission for GitHub unless the folded current pin is OpenMerge. Existing branch expansion registers new bases and invokes configuration independently for each worker. `TestAuthorityLifecycleMixedRepositoriesAndBases` adds a release base under authority alongside an ordinary repository, checks adjacent matching pairs and no ordinary authority events, and verifies unchanged-cycle idempotence. |
| Durable pin fold | All writes use the existing immutable events and `queue.Apply`; no worker-only pin exists. `TestApplyAuthorityConfigLifecycleAndRollback` opens another store, reloads the committed pair, and checks the interpreted activation survives. Queue rebind, snapshot and authority invalidation tests remain unchanged. |
| Mode-specific rollback and fresh re-enable | GitHub rollback pins contain no OpenMerge actor or activation. Rollback still validates the database/binary/backup receipt and does not call the authority reader. `internal/store/write_restriction.go:311` rejects adoption of proposals before rollback or an unverified pin. `TestAuthorityLifecycleRollbackReceiptAndUnchangedWire`, `TestAuthorityLifecycleInvalidatesPolicyChangesAndReenable` and the store lifecycle test cover receipt rejection, zero rollback reads, stale-draft rejection and no revived applied verification. |
| Repository-wide digest propagation | `internal/serve/authority_activation.go:131` includes the full repository selector coverage and resolved actor, plus default branch, selected method and legacy queue digest. Every served base resolves policy inputs on each configuration cycle, including an unchanged governing tip. An unchanged adopted pin short-circuits before receipt/identity transactions, live seeing reads and queue-lock writes; changed or unadopted pins still pay that cost. The mixed lifecycle test changes the resolved actor for all bases, injects one base's quota failure, verifies sibling pairs advance independently, and retries the failed base. There is **no repository-wide “all served queues carry digest X” completion signal** yet. Per-queue receipts do not authorize removing redundant protection; 9c owns providing that signal. |
| Invalidate default branch, selectors, actor and method changes | `internal/serve/authority_activation.go:131` computes `authority.EffectiveDigest` from current resolved inputs. The changed binding uses the existing authority fold to discard old observations/verification. `TestAuthorityLifecycleInvalidatesPolicyChangesAndReenable` seeds a complete applied observation and independently changes each input, requiring unverified fresh state. A same-policy governing-commit rebind emits a matching pair without changing the digest. |
| Mixed repositories/bases and lifecycle quota/read errors | The mixed test covers main/release queues, a new base, an ordinary repository, actor drift, a failed base, rollback and later GitHub changes. `TestAuthorityLifecycleReadFailuresDoNotPublishConfigOrPin` uses `testSynctest`, with every fixture inside its bubble, for resolver/read failures and receipt expiry during a slow read. Store tests recheck the observation basis after acquiring the queue lock. |

The normal `ConfigApplied` payload remains unchanged, including omitted depth-1
window bounds. When legacy window recovery needs a historical config event, the
store commits that backfill in the same transaction before the matching target
config/pin pair. Planner, queue fold, batching and fence protocols are unchanged.
For identical digests, the legacy folded `ConfigCommit` retains its original
value; the authority pin records the actual governing commit. This preserves
`TestAuthorityPinRebindsGoverningCommitWithSameDigest`.

## A2 argument

Production `cmd/openmerge/installations.go:118` does not set `AuthorityRequired`
or install the package-private lifecycle resolver. The runtime candidate reader
requires both; a candidate, journal draft/pin or valid receipt cannot set either.
`prepareAuthorityLifecycle` and `activateAuthority` independently require opt-in.
The public schema and `config.Load` rejection remain unchanged.

`TestAuthorityBarrierJournalCannotInstallRuntimeAuthority` now invokes atomic
activation with a valid database-bound receipt after loading draft, pin and raw
activation histories. It also supplies a resolver without opt-in, calls lifecycle
preparation, reloads GitHub config, and presents an actual OpenMerge candidate to
the runtime reader. No resolver runs, no authority events appear, and the public
candidate remains rejected. `TestAuthorityLifecycleRequiresFlagAndResolver`
covers neither, flag-only and resolver-only; the positive lifecycle tests cover
both. Existing pin-only depth-1/4 and legacy pin-race tests remain in the suite.

Journal write access can still select pre-existing fail-closed protections.
This proof does not equate arbitrary authority-bearing journals with pin-free
GitHub journals. Ordinary GitHub compatibility is measured separately below.
The complete integrated A2 proof remains required in 2d.

## Reviewer flags

- The internal resolver supplies independently acquired actor/repository identity
  and the observation request. 9c still owns actual governing acquisition and
  validation, every push guard, and pin/observation-before-first-fence acceptance.
  `TestAuthorityLifecycleDirectConstructorDoesNotBootstrapFence` preserves the
  direct constructor's refusal to bootstrap an authority fence with the incomplete
  gate. The managed new-base lifecycle is exercised with an existing fake fence.
- A fresh read can commit a pinned-unverified outcome. It cannot make incomplete
  evidence verified. After rollback, adoption requires a new proposed record
  beyond the rollback/unverified boundary; older verification remains history.
- 9c retains the receipt lock-wait/commit gap: receipt age/database identity are checked before observation and immediately
  before the store call. As in 2a, this does not guarantee validity throughout
  queue-lock wait or commit.
- No 2c owner enforcement/projections or 2d admission is enabled. No production
  authority opt-in, public-barrier removal, deployment or organization acceptance
  is claimed.

## Verification

Verified implementation: `e72e841419ea92512227de216ee506d90c3c81ea`.
The complete clean-tree driver returned **PASS** against `b9f00f8` on
2026-10-21 UTC. The following commit retains evidence only.

- `make check`: **exit 0**, formatting, lint, complete short race suite and build.
- Focused lifecycle, activation, public-barrier, queue/store and config race tests:
  **exit 0**.
- Strict offline replay: **2,159/2,159** identical actions, structural states,
  snapshot hashes and boundary checkpoints; **8,676** matching boundary probes.
  Gates 2/4 and all **22** mandatory self-tests pass. No differing or allowlisted
  fields.
- Depth-1/4 contracts: **90/90 byte-identical**. SHA-256:
  `88e727ebe7ea5b570f352c5c661c2cd1642c1b5120f3d3ed086993f4dd00362a`.
- Seeded simulator: both exits **0**, **146/146** normalized blocks byte-identical.
  SHA-256: `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Existing fixtures: **104/104 unchanged**, including **90** registry fixtures;
  no new event types, fixtures or wire fields. Manifest SHA-256:
  `d9fc673fbd7ec5410cf1491778157508533ca5c804932caaecb39204f2ae2776`.
- Verified binary SHA-256:
  `a8e66183ceab05611d73ba2d15255b2ad8d54c3df470e238f22df6868ff9c6ee`.

Retained artifacts are the compact [summary and exact commands](adr-0008-9b-ii-2b-artifacts/summary.json),
[make check log](adr-0008-9b-ii-2b-artifacts/make-check.log),
[focused race log](adr-0008-9b-ii-2b-artifacts/focused.log),
[source hashes](adr-0008-9b-ii-2b-artifacts/source-hashes.json),
[artifact manifest](adr-0008-9b-ii-2b-artifacts/manifest.json),
[raw-cache manifest](adr-0008-9b-ii-2b-artifacts/cache-manifest.json),
[schema comparison](adr-0008-9b-ii-2b-artifacts/schema-cleanup.json), and
[stopped-run cleanup audit](adr-0008-9b-ii-2b-artifacts/cleanup-audit.json).
The full replay report, streams, contract records and simulator output remain in
`/home/riels/.cache/adr0008-9b-ii-2b-verification`; they are hashed rather than
copied into this PR.

Reproduce from clean checkouts:

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable'
python3 scripts/verify-adr0008-9b-ii-2b.py \
  --baseline /home/riels/.cache/adr0008-9b-ii-2b-main-b9f00f8 \
  --out /home/riels/.cache/adr0008-9b-ii-2b-verification-rerun
```

The driver verifies clean trees and frozen export hashes. Every heavy command
uses the shared flock, `GOFLAGS=-p=2`, and 4 GiB memory, raised to 6 GiB for replay.
Both Go 1.27.3 GOROOT and PATH are exported. Only the shared test Postgres on
localhost:54329 and frozen offline journal export are used.

One initial new store concurrency test blocked its own competing writer through
the same test hook. That run was stopped, its identified scratch schemas were
removed, and the test now uses a separate store for the competing writer.
The final focused and full suites pass. The namespace lists before and after
the complete driver match exactly; no unattributed pre-existing schema was
removed. Earlier focused checks also caught two ineffective string replacements
in new test inputs and an unnecessary change to same-digest commit folding.
The test inputs were corrected and the fold change was removed before the
verified implementation commit.

No live database/status API, systemd unit, remote `openmerge/*` ref, real
application GitHub API or Smart-Data-Ohio organization is accessed. Git push and
PR creation are the requested publication operations. No SQL migration, index,
new table, Makefile, CI, Dockerfile, test-flag or existing fixture changes are
included. This remains Hold, without queueing or merging.

## Round 2 — lifecycle corrections (Hold)

The four blocking regression paths were run against `30b088a` before fixing
production code and all failed: unchanged approval readiness, authority depth
1→4, rollback followed by a GitHub rebind, and adopted activation rebind.
The adopted test also demonstrated the redundant unchanged-cycle seeing read.

- Final authority digests, rather than intermediate Stage A digests, control
  readiness/summary invalidation. `TestAuthorityLifecycleUnchangedApprovalReadiness`
  preserves readiness and summaries with approvals enabled.
- Historical depth recovery recomputes authority-bound digests from the retained
  prior actor binding and historical candidate. Ordinary GitHub still uses its
  exact legacy digest checks. `TestAuthorityLifecycleDepthOneToFour` covers omitted
  depth-1 bounds followed by speculative depth 4.
- A GitHub rollback pin remains historical proof of rollback, not a permanent
  governing-commit constraint. Push acceptance requires successfully loaded GitHub
  mode and the current durable config digest; required unresolved workers still
  fail closed. This avoids new authority events/downgrade boundaries on later
  GitHub commits. `TestAuthorityLifecycleRollbackReceiptAndUnchangedWire` covers
  unrelated default-branch rebind and later rule changes with empty build, land
  and fence push options.
- Same-binding rebind with no new draft retains authenticated activation and
  interpretation context under the queue lock. `TestAuthorityLifecycleAdoptsDraftWithAtomicConfig`
  checks adoption, zero additional reads/events on an unchanged cycle, preserved
  activation after unrelated rebind, verified status and valid push options.
  Runtime observation is explicitly seeded as the separate 2c seam.
- Resolver/activation failures publish an operator waiting message and nudge
  discovery. The existing in-bubble `testSynctest` failure matrix asserts both.

Invalidation is intentionally conservative and broader than the four named
triggers: `EffectiveDigest` includes the legacy queue digest, so changes to any
effective queue rule reset verification. The invalidation matrix now includes
an entry-condition change. No effective rule change is exempt. Queue display
names are metadata rather than effective rules; the adopted lifecycle test
checks that renaming one preserves the digest and verification.

There is **no repository-wide “all served queues carry digest X” completion
signal**. Per-queue receipts do not authorize removing redundant protection.
The ownership table assigns providing that signal to **9c**. 9c also retains the
receipt lock-wait/commit expiry gap. Public rejection and unset production
opt-in/resolver (A2) remain unchanged; no queueing or merging is authorized.
