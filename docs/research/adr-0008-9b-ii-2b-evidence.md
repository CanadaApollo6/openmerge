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
| New queues; ordinary GitHub gets no new event or downgrade boundary | `repositoryCandidate` requires both explicit opt-in and the private resolver before candidate parsing. `prepareAuthorityLifecycle` skips pin emission for GitHub unless the folded current pin is OpenMerge. Existing branch expansion registers new bases and invokes configuration independently for each worker. `TestAuthorityLifecycleMixedRepositoriesAndBases` adds a release base under authority alongside an ordinary repository, checks adjacent matching pairs and no ordinary authority events, and verifies unchanged-cycle idempotence. |
| Durable pin fold | All writes use the existing immutable events and `queue.Apply`; no worker-only pin exists. `TestApplyAuthorityConfigLifecycleAndRollback` opens another store, reloads the committed pair, and checks the interpreted activation survives. Queue rebind, snapshot and authority invalidation tests remain unchanged. |
| Mode-specific rollback and fresh re-enable | GitHub rollback pins contain no OpenMerge actor or activation. Rollback still validates the database/binary/backup receipt and does not call the authority reader. `authorityDraftFloor` rejects adoption of proposals before rollback or an unverified pin. `TestAuthorityLifecycleRollbackReceiptAndUnchangedWire`, `TestAuthorityLifecycleInvalidatesPolicyChangesAndReenable` and the store lifecycle test cover receipt rejection, zero rollback reads, stale-draft rejection and no revived applied verification. |
| Repository-wide digest propagation | The effective digest includes the full repository selector coverage and resolved actor, plus default branch, selected method and legacy queue digest. Every served base resolves it on each configuration cycle, including an unchanged governing tip. The mixed lifecycle test changes the resolved actor for all bases, injects one base's quota failure, verifies sibling pairs advance independently, and retries the failed base. This is per-queue atomicity, not a repository transaction or an all-queues-applied authorization signal. |
| Invalidate default branch, selectors, actor and method changes | `prepareAuthorityLifecycle` computes `authority.EffectiveDigest` from current resolved inputs. The changed binding uses the existing authority fold to discard old observations/verification. `TestAuthorityLifecycleInvalidatesPolicyChangesAndReenable` seeds a complete applied observation and independently changes each input, requiring unverified fresh state. A same-policy governing-commit rebind emits a matching pair without changing the digest. |
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
- Receipt age/database identity are checked before observation and immediately
  before the store call. As in 2a, this does not guarantee validity throughout
  queue-lock wait or commit.
- No 2c owner enforcement/projections or 2d admission is enabled. No production
  authority opt-in, public-barrier removal, deployment or organization acceptance
  is claimed.

## Verification

Results are recorded after the clean-tree verification driver completes.
