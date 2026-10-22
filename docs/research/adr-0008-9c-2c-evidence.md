# ADR 0008 PR 9c-2c evidence

Trial class: **Hold**. Baseline local main `07a1cf8` (#137/#138 landed).
This is the whole propagation/operational slice in one PR. Public rejection and
production opt-out remain; no deployment, organization acceptance, queueing or
merging. No migrations/indexes/tables, forge capability, event/wire changes,
existing fixture changes or Makefile/CI/Dockerfile/test-flag changes.

## Requirement and carry-over map

| Plan ownership | Implementation and tests |
| --- | --- |
| 9c-2c :176/:221, repository completion | `internal/store/authority_completion.go`: `RepositoryAuthorityCompletion` reads all registered partitions in one read-only repeatable-read snapshot, with explicit expected digest(s). `internal/serve/authority_completion.go`: `AuthorityCompletion` freshly resolves repository access, default ref/tip and complete branches before/after the snapshot. `internal/status/authority_completion.go`: authenticated GET `/v1/authority-completion`. `TestRepositoryAuthorityCompletion`, `TestAuthorityCompletionDiscovery`, `TestAuthorityCompletionEndpoint`. |
| :176/:225, inventory/drain and tested backup/reconciliation | `internal/authority/operations.go`, `emission.go`: exhaustive all-host inventory with stopped-at/binary identities; source/isolated-restore identity, dump hash/capture time, restored watermark and reconciliation/evidence attestation. `cmd/openmerge/setup_verify.go`: recheck receipt identity/backup bytes before the final governing-ref check, then pure age/binary immediately before the store call, as well as before seeing reads. Existing activation checks remain. `TestOperationalReceipts`, `TestFirstEmissionDrainBackupGate`, `TestVerificationReceiptRejectedBeforeDatabaseOrSeeingRead`, extended `TestVerifyAcquiresOnceAfterEveryTargetValidated`. |
| :176/:225/:280, v7 downgrade and lock-wait limit | `internal/store/authority_downgrade.go`: read-only journal and persisted cursor refusal, including v7 without authority events and retired histories. `cmd/openmerge/admin_downgrade.go`: migration-free `admin downgrade-check`. `TestAuthorityDowngradeGate`; isolated dump/restore driver `scripts/check-adr0008-9c-2c-restore.py`. `docs/operating.md` states the pre-switch verify gate, inventory/restore/reconciliation procedure and receipt expiry during lock-wait/commit limitation. |
| #138 ExpectedRefs/input recovery | Runbook in `docs/operating.md`, ownership in `adr-0008-9-plan.md`. Keep rollback → fresh proposal → re-enable; applied verification cannot re-adopt. Existing input-drift lifecycle/store regressions remain the executable recovery proof. 9c-3 owns testing it through public activation. |
| #138 external base advance before build | Document conservative transient failure and normal base-observation/replan recovery in `docs/operating.md`; public recovery test assigned to 9c-3 in the plan. No GitHub or gitops behavior changes. |
| #138 nits | `queue.AuthorityInputsMatch` centralizes full ExpectedRefs/selectors/App comparison; serve/push, lifecycle and store use it, with explicit store tolerance for historical missing inputs. AdoptAuthorityPin skips the draft-floor query for draftID zero. The seeing-drift assertion explains why positive provenance may disappear. Existing input-drift/store/push tests cover these paths. |

## Completion contract and decisions

“Served” conservatively includes every registered partition for the requested
repository across installations, including paused, blocked, dormant, retired, recovery-only
and worker/base-allowlist-excluded partitions, plus selected bases from fresh
complete discovery and explicit targets. No historical partition can silently
disappear from this proof. Historical installation partitions without current
discovery keep completion false; multiple live installation scopes fail closed
rather than issuing independent partial successes. Aggregate multi-installation
success is deliberately unsupported, never inferred from individual receipts. Retired histories which cannot reach the intended
policy keep completion false; retain redundant protection rather than deleting
history or inferring a safe exclusion from inactivity.

`digest` is the operator's intended common effective digest X. Because effective
queue rules can differ per base, `targets` optionally maps bases to their expected
digests, with `digest` as a common default. The API echoes the explicit intent;
it never chooses the target from an arbitrary successful queue. Each row names
the persisted ID, watermark, governing commit, actual/expected digest and reason.
Missing pins/registration/adoption, stale discovery inputs/default-branch name/governing commit,
unverified evidence, unsupported lanes and mixed digests prevent completion.
Empty or unavailable inventories never succeed. Successful responses can be
incomplete; errors are sanitized and retried without caching.

`AuthorityCompletion.RequireComplete` is the reusable refusal contract for 9c-3.
The plan requires public tests proving both this repository source and actual
pre-switch verify/drain refusal are consulted (including mutations/spies), rather
than merely asserting the helpers exist. Propagation writes must remain possible
while incomplete: the final activation/protection-removal step consumes completion,
not each per-queue propagation write. No per-queue adoption receipt authorizes
removing redundant protection. The signal is point-in-time, not an indefinite
lease; freeze branch/config changes and recheck immediately before each resumable
operator write. This slice performs no protection-removal write.

Direct re-adoption after input drift is deliberately unavailable. The durable
adoption authenticates an immutable **proposed** verification event with exact
interpretation inputs/fingerprint; applied verification has different fold semantics
and does not supply that provenance. Reusing it as a proposal or reviving the old
proposal would weaken the existing floor/invalidation proof. A safe shortcut
requires a separately designed authenticated re-adoption contract. The documented
rollback/fresh-proposal procedure preserves the current wire contract.

An unsuccessful live-base ancestry proof can indicate base movement, incomplete
history or Git failure. Mapping all such failures to a successful base-moved
observation would invent evidence. Keep the existing conservative transient path;
normal reconciliation observes the actual base and replans. A typed distinction
and integrated public recovery regression remain 9c-3 work, without changing the
GitHub path here.

## A2 and operational limits

The completion and downgrade APIs are read-only. They install no lifecycle,
observer, push or admission hooks. Production constructors still set neither
`AuthorityRequired` nor the private resolver. Candidate, draft/pin, emission or
completion data cannot set either opt-in; public schema/loader/CLI rejection is
unchanged. The complete-seam `TestAuthorityBarrierJournalCannotInstallRuntimeAuthority`
remains part of verification. Journal write access can still select pre-existing
fail-closed protections; it is not an authenticated activation boundary.

Inventory completeness, restore execution and ref/evidence reconciliation remain
operator attestations; local validation does not discover remote hosts or prove
backup restoration. Actual backup bytes are hashed. New receipt fields require
regenerating older receipts. All writer/readers must remain stopped during the
procedure. Receipt preparation precedes the final governing I/O; pure age/binary validation
immediately precedes the verification store call. Adoption retains its pre-call
full validation. These checks do not
exclude expiry or endpoint replacement during queue-lock wait/commit. Mode rollback
retains the immutable journal/v7 boundary. Downgrade-check never erases it and
certifies only this ADR's boundary, not every older binary difference.

## Verification

Pending the clean-tree driver and isolated restore/stress runs. Raw logs and
commands will be retained under `~/.cache/adr0008-9c-2c-verification`.
Baseline isolated `go test -race -short -count=1 -timeout=15m ./internal/serve`:
**exit 0, 290.964s**. All Go checks export both Go 1.27.3 GOROOT/PATH and use the
shared heavy flock, GOFLAGS=-p=2, 4 GiB memory (6 GiB for replay).
