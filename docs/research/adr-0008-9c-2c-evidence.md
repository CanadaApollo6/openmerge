# ADR 0008 PR 9c-2c evidence

Trial class: **Hold**. Baseline: main `eaddd84` (#137/#138 landed; #139 is a
test-only serve speed-up). This is the whole propagation/operational slice in one
PR. Public rejection and production opt-out remain; no deployment, organization
acceptance, queueing or merging. No migrations/indexes/tables, forge capability,
event/wire changes, existing fixture changes or Makefile/CI/Dockerfile/test-flag
changes.

## Requirement and carry-over map

| Plan ownership | Implementation and tests |
| --- | --- |
| 9c-2c :176/:221, repository completion | `internal/store/authority_completion.go:82` `RepositoryAuthorityCompletion` folds every registered partition in one read-only repeatable-read snapshot against explicit expected digest(s) and the live governing tip; `:61` `RequireComplete` is the refusal contract. `internal/serve/authority_completion.go:21` `AuthorityCompletion` freshly resolves installation/repository access, default branch name/tip and complete branch inventory before and after the snapshot. `internal/status/authority_completion.go:20` authenticated GET `/v1/authority-completion`; `:15` exported `AuthorityCompletionSource` with a compile-time assertion that `*serve.Server` implements it. `cmd/openmerge/admin_completion.go:24` `openmerge admin authority-completion` (exit 0 only when complete for exactly the requested intent). Tests: `TestRepositoryAuthorityCompletion` (per-partition reasons), `TestAuthorityCompletionDiscovery`, `TestAuthorityCompletionEndpoint`, `TestAdminAuthorityCompletionCLI`. |
| :176/:225, inventory/drain and tested backup/reconciliation | `internal/authority/operations.go:39` exhaustive all-host inventory with stop time/binary identities; source/isolated-restore identity, dump hash/capture time, restored watermark and reconciliation/evidence attestation. `cmd/openmerge/setup_verify.go:207` receipt identity/backup bytes are rechecked before the final governing-ref check; `:219` pure age/binary recheck runs immediately before the store call, with no I/O in between. Tests: `TestOperationalReceipts`, `TestFirstEmissionDrainBackupGate`, `TestVerificationReceiptRejectedBeforeDatabaseOrSeeingRead`, `TestVerifyAcquiresOnceAfterEveryTargetValidated`, `TestAuthorityActivationReceipt*`. |
| :176/:225/:280, v7 downgrade, pre-switch verify and lock-wait limit | `internal/store/authority_downgrade.go:27` read-only journal/persisted-cursor refusal, including v7 without authority events and retired histories; decided refusals wrap `ErrAuthorityDowngradeRefused`. `cmd/openmerge/admin_downgrade.go:14` migration-free `admin downgrade-check`, which reports refusal and an unreadable database differently, without echoing connection details. Tests: `TestAuthorityDowngradeGate`, `TestAdminDowngradeCheckCLI`; real dump/restore driver `scripts/check-adr0008-9c-2c-restore.py`. `docs/operating.md:1915` covers the pre-switch verify gate, inventory/restore/reconciliation and receipt expiry during lock wait/commit. |
| #138 ExpectedRefs/input recovery | Runbook `docs/operating.md:2040`; ownership in `adr-0008-9-plan.md`. Decision: no direct re-adoption (see below). Existing input-drift lifecycle/store regressions remain the executable proof. 9c-3 tests the runbook through public activation. |
| #138 external base advance before build | Documented in `docs/operating.md:2040` as a conservative transient failure followed by normal base observation and replanning; 9c-3 owns the public recovery test. No GitHub or gitops behaviour change. |
| #138 nits | `internal/queue/authority_adoption.go:28` `AuthorityInputsMatch` is the single ExpectedRefs/selectors/App comparison used by `serve/push.go`, `authority_activation.go` and `store.authorityInputsMatch` (which still tolerates historical nil input explicitly). `internal/store/write_restriction.go:555` skips the draft-floor query when `draftID` is zero. `internal/serve/push_test.go:144` comments the `seeing-drift` relaxation. |

## Completion contract and decisions

**Served queues.** "Served" conservatively means every registered partition of the
requested repository across installations, including paused, blocked, dormant,
retired, recovery-only and worker/base-allowlist-excluded partitions, plus every
selected base in fresh complete discovery and every explicit target. No historical
partition can silently drop out of the proof. Historical partitions under another
installation keep completion false. Multiple live installation scopes fail closed
rather than producing independent partial successes: aggregate multi-installation
success is not supported and is never inferred from individual receipts. A retired
history that cannot reach the intended policy keeps completion false, so redundant
protection stays; nothing deletes history or treats inactivity as a safe exclusion.

`digest` is the operator's intended common effective digest X. Effective queue
rules can differ per base, so `targets` optionally maps bases to expected digests,
with `digest` as the common default. The API echoes the explicit intent and never
picks the target from whichever queue answers first. Each row carries the persisted
ID, installation, base, lane, watermark, governing commit, actual/expected digest
and one reason: `registration_missing`, `pin_missing`,
`installation_inventory_unavailable`, `unsupported_lane`, `digest_mismatch`,
`discovery_inputs_stale`, `adoption_missing`, `verification_incomplete`,
`governing_commit_stale` or `complete`. Each reason has a direct test assertion.
Empty or unavailable inventories never succeed; a governing tip,
default-branch name or branch inventory change during the read is a retryable
error, not a stale success. Errors are sanitized and nothing is cached.

**For 9c-3.** `AuthorityCompletion.RequireComplete` (and the CLI's exit status) is
the refusal contract. The plan requires 9c-3 public tests that fail unless both
this repository source and the actual pre-switch verify/drain receipt refusal are
consulted, with spies or mutations, so that helpers merely existing is not enough.
Propagation writes stay possible while completion is false: only the final
activation/protection-removal step consumes completion. No per-queue adoption
receipt authorizes removing redundant protection. The signal is point-in-time, not
a lease. Freeze branch/config changes and recheck immediately before each
resumable operator write. This slice performs no protection-removal write.

**#138 re-adoption.** Direct re-adoption after input drift stays unavailable. A
durable adoption authenticates an immutable **proposed** verification event with
the exact interpretation inputs and fingerprint. An applied verification has
different fold semantics and does not carry that provenance. Reusing it as a
proposal, or reviving the invalidated proposal, would weaken the existing
floor/invalidation proof. A safe shortcut needs its own authenticated re-adoption
contract. The documented rollback → fresh proposal → re-enable procedure keeps the
current wire contract.

**#138 base advance.** A failed live-base ancestry proof (`internal/gitops/prepared_push.go:42`)
can mean the base moved, history is incomplete, or Git failed. Mapping every such
failure to a successful base-moved observation would invent evidence. The push
path also does not own journal appends. Normal reconciliation already observes
the moved tip (`BaseMoved`) and replans, so the existing conservative transient
path stays. A typed distinction and a public recovery regression are 9c-3 work.
The GitHub path is unchanged because `validatePush` returns before any added
command when `AuthorityRequired` is false.

**Draftless floor skip.** With `draftID == 0`, `BindAuthorityDraft` refuses
(`id <= 0`) and returns the caller's pin with activation/input cleared. Lifecycle
pins (`authority_activation.go:167`) only ever carry a binding. The skipped floor
read therefore cannot change the written event, and the guarded insert is
unchanged.

## A2 and operational limits

The completion and downgrade APIs and the CLI are read-only. They install no
lifecycle, observer, push or admission hooks. Production constructors still set
neither `AuthorityRequired` nor the private resolver. Candidate, draft/pin,
emission or completion data cannot set either opt-in, and public
schema/loader/CLI rejection is unchanged. The complete-seam
`TestAuthorityBarrierJournalCannotInstallRuntimeAuthority` stays in the focused
set. Journal write access can still select pre-existing fail-closed protections;
it is not an authenticated activation boundary. The new status route and CLI are
additive: every existing GitHub formatter, route and byte is unchanged (projection
and CLI goldens below).

Inventory completeness, restore execution and ref/evidence reconciliation remain
operator attestations. Local validation does not discover remote hosts or prove
that a backup was restored; it does hash the actual backup bytes. The new receipt
fields mean older receipts must be regenerated. All writers and readers must stay
stopped during the procedure. Receipt preparation comes before the final governing
I/O, and the pure age/binary check immediately precedes the verification store
call. Adoption keeps its pre-call full validation. None of this rules out expiry
or endpoint replacement during the queue-lock wait or commit. Mode rollback keeps
the immutable journal/v7 boundary. `downgrade-check` never erases it, and it
certifies only this ADR's boundary, not every other difference between binaries.
Nested `write_restriction` sources (for example in `landing_checks_recorded`)
only exist after a top-level `write_restriction_*` event in the same journal,
which the check already refuses.

## Verification

Verified implementation: `4a926c31fa3ad2b09909ac0eeac21e534c8386b8` against
baseline `eaddd84`; the commit after it adds evidence only. Compact receipts:
[summary and exact commands](adr-0008-9c-2c-artifacts/summary.json),
[additional checks](adr-0008-9c-2c-artifacts/additional-checks.json),
[source hashes](adr-0008-9c-2c-artifacts/source-hashes.json),
[manifest](adr-0008-9c-2c-artifacts/manifest.json).

- `make check`: **exit 0**, golangci-lint **0 issues**, complete short race
  suite/build. Focused propagation/operations/barrier/Stage A exact-output race
  set: **exit 0**.
- Strict replay: **2,159/2,159 identical** actions, structural states, snapshot
  hashes and boundary checkpoints; **8,676** probes, gates 2/4 and self-tests pass;
  no non-allowlisted fields.
- Simulator: both exits **0**, **146/146 normalized blocks byte-identical**.
  Depth-1/4 contracts **90/90 identical**. GitHub status/API/health/author
  projections and `queue status` CLI bytes identical. Wire fixtures
  **105/105 unchanged**, no new fixture.
- `internal/serve` (`go test -race -short -count=1 -timeout=15m`, isolated):
  **190.638 s at eaddd84 → 189.874 s at 4a926c3**; inside `make check`
  **206.510 s**. These are single local runs, not a GitHub-runner forecast. No
  sleeps or real-git acceptance were added to serve, and the one new serve test
  runs inside `testSynctest` with its fixture in the bubble.
- Stress: `GOMAXPROCS=1 go test -race -count=20 -cpu=1` over authority, store,
  status, serve and cmd/openmerge (`Completion|OperationalReceipts|FirstEmission|Downgrade|VerificationReceipt|VerifyAcquires|AuthorityInputDrift|AuthorityActivationReceipt`)
  with **eight owned CPU burners**: **exit 0**, 131 s, all five packages ok. The real dump/restore driver
  also ran **3×** under the same load: **3/3 PASS**. Only the burner PIDs this
  run started were killed, and all were reaped.
- Mutations (each expected **exit 1**, tree restored afterwards): removing the
  post-snapshot tip/default-name recheck, the lane reason or the adoption reason;
  skipping the CLI intent-echo check; letting an incomplete CLI answer exit 0; and
  reporting an unavailable database as a downgrade refusal; removing the governing-tip or foreign-installation reason. Earlier mutations
  (incomplete propagation, inventory/reconciliation removal, final receipt
  refusal, receipt I/O after the final governing read, cursor scanning, v7
  refusal, default-name binding) were kept from the previous implementer's pass.
- Real `pg_dump -Fc` / `pg_restore` on two owned scratch databases per run:
  identical restored immutable events and watermark, different server identities,
  source downgrade-check **1**, pre-emission restore **0**; scratch databases
  dropped. This does not claim live Git ref or release reconciliation; those remain
  operator/PR 11 acceptance requirements.

Raw logs, replay streams and dumps are kept outside the repository at
`~/.cache/adr0008-9c-2c-verification-opus` (`final/`, `stress/`,
`timing-before/`). Both Go 1.27.3 GOROOT and PATH were exported; every Go command
ran under the shared heavy flock with `GOFLAGS=-p=2` and 4 GiB of memory (6 GiB
for replay).

Reproduce with the same toolchain variables and the shared test Postgres URL:

```sh
python3 scripts/verify-adr0008-9c-2c.py \
  --baseline "$HOME/.cache/adr0008-9c-2c-baseline-eaddd84" \
  --out "$HOME/.cache/adr0008-9c-2c-rerun"
python3 scripts/check-adr0008-9c-2c-restore.py \
  --binary bin/openmerge --out "$HOME/.cache/adr0008-9c-2c-restore-rerun"
```

No live database, status API, systemd unit, live OpenMerge ref, application GitHub
API or Smart-Data-Ohio organization was accessed. The only publication is the
requested branch push and PR. Hold, public rejection and production opt-out remain.
