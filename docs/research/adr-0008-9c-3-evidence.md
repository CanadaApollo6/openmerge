# ADR 0008 PR 9c-3 round-4 review evidence

Round 4 retains **Hold**. Completion compares the partition identities, installation
count, completeness, reasons, digests and governing commits across discovery,
ignoring only watermarks, and returns the final snapshot. Ordinary sibling
appends pass; partition registration still refuses. Positive authority-boundary
lookups are cached per store process; negatives query again. Restore stops all
workers before replacing history. The runtime role needs UPDATE, DELETE or
TRUNCATE on events for the first-emission lock; registration waits on the queues
SHARE lock.

Setup verification is the primary public first-emission gate. A positive DraftID
already implies a boundary; lifecycle writer gates remain defense in depth for
internal/draftless callers, covered directly at store level and through the
internal activation seam. Startup runtime construction now pins database identity
in the tested constructor. Runtime receipt errors preserve cause identity while
omitting its text. The discovery_round2_test synctest conversion is inherited
from main merge #142, not authored by this PR.

Round-4 verification is recorded below once the clean-tree run completes.

## Retained round-3 evidence

Classification: **Hold**. Round 3 enforces the user-selected **one database per
client (installation)** model. No migration is needed. All queues count, including
other repositories and paused, retired or excluded partitions. First emission
holds `LOCK TABLE queues IN SHARE MODE` until transaction completion: PostgreSQL
queue INSERTs take the conflicting ROW EXCLUSIVE lock. The READ COMMITTED count
therefore includes a registration committed while first emission waited, and a
registration arriving after the lock cannot commit before the boundary.
The existing database-wide events lock, watermark and whole-database receipt
remain intact.

Tenancy is a separate sanitized check, not a completion reason. Setup checks it
before receipt loading (also after a retained boundary); the public resolver
checks before verification/protection I/O; the final activation/push guard checks
before discovery and again after the last discovery read. None caches a verdict.
Rollback resolves GitHub before consulting tenancy. Shared multi-tenant authority
requires a future ADR for an installation-scoped watermark, lock, receipt,
completion and per-tenant export/restore.

New behavioral tests: `TestAuthorityTenancyEmission`,
`TestAuthorityTenancyConcurrentRegistration`,
`TestAuthorityTenancyRegistrationWaitsForEmission`,
`TestVerificationTenancyBeforeReceipt`,
`TestPublicAuthorityTenancyLifecycle`,
`TestPublicAuthorityTenancyPushAndRollback`,
`TestPublicAuthorityTenancyFinalInventory`,
`TestPublicAuthorityTenancyGithubUnaffected`.
Store and command fixtures run on the shared test Postgres; serve fixtures are
created and closed inside `testSynctest`. Race synchronization observes actual
PostgreSQL lock waits, without wall-clock ordering.

| Round-3 mutation | Named test |
| --- | --- |
| `tenancy-emission` | `TestAuthorityTenancyEmission` |
| `tenancy-registration-lock` | `TestAuthorityTenancyConcurrentRegistration` |
| `tenancy-registration-held` | `TestAuthorityTenancyRegistrationWaitsForEmission` |
| `tenancy-setup` | `TestVerificationTenancyBeforeReceipt` |
| `tenancy-lifecycle` | `TestPublicAuthorityTenancyLifecycle` |
| `tenancy-push-preflight` | `TestPublicAuthorityTenancyPushAndRollback` |
| `tenancy-push-final` | `TestPublicAuthorityTenancyFinalInventory` |

Completion-only races now register an additional branch in the same installation;
cross-installation refusal is independently asserted by the tenancy tests, so
completion mutations cannot be hidden by the earlier tenancy guard.

The clean-tree round-3 verifier returned **PASS** on implementation
`bae36308f0b01f6001881e98710d1ca7c5a2880e`, against main
`75c4029447ae4d24a439ae6ef8ad6103ebcf3926`. The final evidence follow-up changes only this document
and the JSON receipt; runtime, tests and verifier source remain exactly tested.

- `make check`: **0**, zero lint issues, complete short race suite and build.
  Serve took **306.976 s** inside this check.
- Focused public/acceptance/completion/barrier and Stage A exact-output race
  checks: **0**.
- All **46/46** mutations failed their named behavioral tests with exit 1,
  including all seven tenancy mutations above. Build failures do not count.
- Strict replay (`GOMEMLIMIT=6GiB`): **2,159/2,159** identical actions,
  structural states, snapshot hashes and boundary checkpoints; **8,676** probes.
  Gates 2/4 and self-tests pass with no non-allowlisted differences.
- Simulator: both exits **0**, **146/146** normalized blocks identical.
  SHA-256 remains `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Depth-1/4 contracts: **90/90 identical**. GitHub status/API/health/author
  projections, actual CLI bytes and legacy Git object/ref/command probes are
  identical. Existing GitHub projection/CLI goldens and Stage A exact-output
  checks pass unchanged.
- MixedGithubBytes remains
  `cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee`.
- **105/105** retained wire fixtures (90 registry fixtures) are identical.
  Direct Git blob comparison also confirms all **211** testdata files from
  `72574eb` and original PR head `bc9779c`, and all **208** from main
  `75c4029`, remain unchanged.
- Every new/changed test passes **20 iterations** with
  `GOMAXPROCS=1 -race -cpu=1`, under **eight owned CPU burners**.
  Independent `/proc` observation confirmed all eight accumulated CPU time
  and were reaped. Public serve: **541.884 s**; real-Git base recovery:
  **204.817 s**; config: **3.142 s**; changepolicy: **2.050 s**;
  store: **12.056 s**; CLI: **19.973 s**; gitops: **33.989 s**.

Fresh CI-like package timings, both exits **0**, with
`GOMAXPROCS=2 go test -p=1 -race -short -count=1 -timeout=15m`:

| Package | Main 75c4029 (s) | Round 3 (s) | Delta (s) |
| --- | ---: | ---: | ---: |
| `internal/serve` | 268.647 | 290.395 | +21.748 |
| `internal/gitops` | 95.543 | 94.956 | -0.587 |
| `internal/queue` | 158.953 | 158.335 | -0.618 |
| `cmd/openmerge` | 64.395 | 64.606 | +0.211 |

These are single local suite measurements, not daemon throughput or a causal
performance estimate. Package times exclude lock waiting; every measured package
and stress group is below 900 seconds. No timeout or test setting changed.

Reproduce with the existing command below, using
`--out "$HOME/.cache/openmerge-143-r3/rerun"`. Full logs, overlays, source hashes
and manifests are retained at `~/.cache/openmerge-143-r3/full-r3/`;
independent burner and fixture-blob receipts are in
`~/.cache/openmerge-143-r3/`.

Round-3 receipt: [verification/source hashes](adr-0008-9c-3-artifacts/verification.json),
SHA-256 `aa3cf454c350194f344d6b1df5d4d0cb23bf0e94a41087d6f2eb918d5fc6278d`.

No migrations, Makefile, CI configuration, test flags, Dockerfile, shared GitHub
formatters, Stage A retry behavior or existing fixtures changed. No live database,
units, OpenMerge refs/status API or Smart-Data-Ohio was touched. Code, tests and
simulator made no real forge API calls. Publication is confined to this requested
branch and the Hold PR body, with no PR comment or queue operation.

Prior-round evidence follows for provenance; its timings and mutation counts are
historical. The initial round-3 full attempt exposed two completion tests whose
multi-installation fixtures conflicted with the new model. Those test scenarios
were changed to same-installation branches and the complete verifier was rerun;
only the clean PASS above is acceptance evidence.

---

# ADR 0008 PR 9c-3 round-1 evidence

Classification: **Hold**. Deployment remains prohibited until dogfood period B drains. PR 11 remains the owner-executed organization switch. Tested implementation: `845684399fb3c2db4a05890814f62e3901ef8314`. Baseline: `75c4029447ae4d24a439ae6ef8ad6103ebcf3926`. This round merges main `75c4029` normally and addresses both reviewers' requested changes without migrations or fixture edits.

## Review resolutions

| Item | Result | Behavioral tests |
| --- | --- | --- |
| 1 | The first proposed verification checks the watermark inside its commit transaction, under an events-table lock that conflicts with appends in every queue. Explicit READ COMMITTED overrides role/DSN defaults and takes fresh snapshots after lock waits. | `TestReviewFirstEmissionAppendRace`, `TestAuthorityEmissionSerializesAllQueueAppends`, `TestAuthorityEmissionRefreshesSnapshotAfterAppendLockWait`, `TestAuthorityEmissionLiveBackupWatermark` |
| 2 | Completion repeats its entire durable repository-wide snapshot after the final outbound read and requires exact equality, including watermarks. | `TestPublicAuthorityCompletionPartitionRace` (new partition in another installation and existing-partition append) |
| 3 | All five push operations consult fresh completion; a real Git Land runs the guard inside its session. | `TestPublicAuthorityEveryPushConsultsCompletion`, `TestPublicAuthorityCompletionRealLandCallBudget` |
| 4 | Missing object/exit 128, diagnostic exit 1, failed ls-remote, cancellation during ancestry and deadline retain their original classes; none becomes BaseMovedError. | `TestBuildBaseProofPreservesFailureClasses` |
| 5 | Receipt age, binary revision, backup hash and drain/restore attestations gate only first authority emission. Steady state, re-pins, re-enable and rollback do not load the receipt. Rollback keeps the configured DB identity check. | `TestAuthorityEmissionReceiptBoundary`, `TestVerificationReceiptOnlyBeforeBoundary`, `TestVerificationReceiptValidationSanitizedBeforeSeeingRead`, `TestAuthorityActivationReceiptBeforeIO`, `TestPublicAuthorityReceiptNotRequiredAfterBoundary`, `TestPublicAuthorityRollbackKeepsDatabaseBinding` |
| 6 | Sanitized public inventory and completion discovery wrappers retain quota RetryAfter, cancellation and deadline causes. | `TestReviewPublicInventoryErrorClass`, `TestPublicAuthorityCompletionErrorClasses` |
| 7 | Production options carry the authority runtime, App mismatch refuses, and target completion reads use their own Git cache. Reentering the worker cache would wait on its own held lock. | `TestProductionAuthorityRuntimeWiring`, `TestReview4AuditPreservesWorkerQuota`, `TestAuthorityCompletionDedicatedCacheAvoidsSessionLock` |
| 8 | Unlisted repository/base targets stay opted out, retain exact GitHub ConfigApplied bytes and refuse landed OpenMerge config. The independently computed binding must equal the reviewed binding. | `TestPublicAuthorityUnlistedQueueStaysGithub`, `TestPublicAuthorityReviewedBindingMustMatch` |
| 9 | Dispatch performs cheap durable checks. Full discovery runs once in the final inside-lock guard; completion results remain uncached. Reconcile keeps fresh selected-branch discovery. | `TestPublicAuthorityCompletionRealLandCallBudget`, `TestPublicAuthorityReconcileInventoryBudget` |
| 10 | Removed both obsolete claims that public activation is unavailable. | Operating guide reviewed against production opt-in. |
| 11 | Historical Stage A candidate composition and public corpus remain exercised without fixture changes. | `TestHistoricalStageAAuthoritySchemaComposition` |
| 12 | Import cases again require exact exit 1 and Ready=false. Non-blocking activation guidance cannot make an incomplete policy import ready. | `TestSetupImportGoverningEvidenceCannotBeAccepted`, `TestAuthorityAcceptanceImportGoverningEvidenceCannotBeAccepted` |
| 13 | Audit options carry only the history-cache configuration. The current audit history path already bypassed bundle creation; narrowing options also removes latent authority-cache wiring. | `TestReview4AuditPreservesWorkerQuota` |
| 14 | The mutation table below includes every push operation, negative ancestry, both races, receipt boundaries, production wiring and membership. | Named behavioral failures only; build failures never count. |

## Boundary and concurrency choices

The authority-emission boundary is `write_restriction_verified`, `policy_authority_applied`, or `write_restriction_observed`. A legacy outside-push acknowledgement has its separate historical operator receipt contract; it cannot waive the stronger authority gate. The broader binary-downgrade event list remains unchanged.

The first-emission transaction acquires canonical queue locks, then `SHARE ROW EXCLUSIVE` on `events`. PostgreSQL INSERT takes `ROW EXCLUSIVE`, so every queue's append conflicts without adding locks to ordinary append paths. Competing first emitters also serialize. The lock tests observe actual PostgreSQL lock waits. One holds an append behind the first-emission transaction; another holds first emission behind an uncommitted append, commits that append and requires a fresh-watermark refusal even when the connection defaults to repeatable read.

SQL uses the `type` column with `EXISTS (... LIMIT 1)`, never JSONB detoast or a whole-journal bool_or. `max(id)` uses the existing primary key. There is no type-only index and this PR adds no migration: proving absence scans the discriminator, and later queries can scan the legacy prefix before the first match. LIMIT 1 stops on that match. Positive results are now cached in-process, relying on the documented worker-stop-before-restore procedure; negatives remain uncached.

Before the boundary, receipt failure reports `emission receipt stale/invalid`, separately from protection read failure. Production pins the configured database identity at startup and retains cheap comparisons after the receipt gate ends. A rollback never requires receipt validation. Retained events and v7 cursors still prohibit an incompatible binary downgrade.

The receipt window is rechecked inside the first-emission transaction after lock waits. It can still expire between the last window check and commit; receipt validation is not a lease.

The final completion snapshot follows the last outbound read. Semantic equality excludes journal watermarks and retains partition identity, installation count and the complete folded safety verdict. The interval from this database check to the remote Git push remains irreducible, as with other immediate-before-write guards. Freeze configuration, selected refs and installations during the switch; completion is not a distributed lease.

## Forge budget

The tests assert logical calls with one active installation and single-page inventories. For I active installations, the additional completion budget per push is one installation listing, I repository listings, two branch listings, two default reads, two ls-remote reads and two durable snapshots. Dispatch adds no discovery. Git object preparation and existing lease reads are separate costs.

An unchanged configuration reconcile performs one installation listing, I repository listings, one branch listing, one default read, one base-ref read and one team lookup. It adds no protection observation until the independent RulesInterval observer runs. Configuration cache misses and changed policies have their existing acquisition costs.

At one page per listing, completion costs I+5 REST requests and two Git ref reads per push; resolver discovery adds I+2 REST requests per reconcile. Paginated inventories multiply HTTP requests. Large installations must budget the sum across active queues and operator polls, plus identity/protection traffic. Tune concurrency and reconcile cadence within quota; never cache the completion verdict to reduce cost.

## Mutation table

All **39/39** overlays failed their named behavioral tests with exit 1. A compile failure never counts. The verifier requires the named test failure and rejects build failures.

| Mutation | Test that killed it |
| --- | --- |
| `public-reader-error-class` | `TestPublicAuthorityReaderPreservesSanitizedErrorClasses` |
| `public-github-loader` | `TestPublicAuthorityGithubConfigurationBytes` |
| `setup-live-backup-call` | `TestPublicAuthoritySetupVerifyRequiresLiveBackupWatermark` |
| `typed-base-move` | `TestAuthorityAcceptanceBaseAdvanceBeforeBuild` |
| `final-governing-recheck` | `TestPublicAuthorityActivationRechecksGoverningTip` |
| `require-complete` | `TestPublicAuthorityActivationConsultsCompletion` |
| `repository-source` | `TestPublicAuthorityActivationConsultsCompletion` |
| `pre-switch-verification` | `TestPublicAuthorityActivationRequiresVerification` |
| `live-backup-watermark` | `TestAuthorityEmissionLiveBackupWatermark` |
| `production-constructor` | `TestPublicAuthorityProductionConstructor` |
| `first-emission-final-watermark` | `TestReviewFirstEmissionAppendRace` |
| `first-emission-global-append-lock` | `TestAuthorityEmissionSerializesAllQueueAppends` |
| `receipt-boundary-ignore-first` | `TestAuthorityEmissionReceiptBoundary` |
| `receipt-boundary-require-after` | `TestAuthorityEmissionReceiptBoundary` |
| `runtime-receipt-after-boundary` | `TestPublicAuthorityReceiptNotRequiredAfterBoundary` |
| `rollback-receipt-required` | `TestPublicAuthorityReceiptNotRequiredAfterBoundary` |
| `setup-receipt-after-boundary` | `TestVerificationReceiptOnlyBeforeBoundary` |
| `setup-receipt-validation-bypass` | `TestVerificationReceiptValidationSanitizedBeforeSeeingRead` |
| `public-unlisted-adopts-first` | `TestPublicAuthorityUnlistedQueueStaysGithub` |
| `reviewed-plan-binding-bypass` | `TestPublicAuthorityReviewedBindingMustMatch` |
| `inventory-error-class-erasure` | `TestReviewPublicInventoryErrorClass` |
| `negative-ancestry-any-error` | `TestBuildBaseProofPreservesFailureClasses` |
| `negative-ancestry-diagnostic` | `TestBuildBaseProofPreservesFailureClasses` |
| `production-runtime-wiring` | `TestProductionAuthorityRuntimeWiring` |
| `production-app-binding` | `TestProductionAuthorityRuntimeWiring` |
| `production-completion-cache` | `TestReview4AuditPreservesWorkerQuota` |
| `runtime-first-receipt-bypass` | `TestAuthorityActivationReceiptBeforeIO` |
| `legacy-ack-waives-authority-receipt` | `TestAuthorityEmissionLiveBackupWatermark` |
| `rollback-configured-database-binding` | `TestPublicAuthorityRollbackKeepsDatabaseBinding` |
| `first-emission-default-isolation` | `TestAuthorityEmissionRefreshesSnapshotAfterAppendLockWait` |
| `completion-verdict-equality` | `TestPublicAuthorityCompletionPartitionRace` |
| `completion-partition-race` | `TestPublicAuthorityCompletionPartitionRace` |
| `completion-discovery-error-class` | `TestPublicAuthorityCompletionErrorClasses` |
| `completion-dispatch-duplicate` | `TestPublicAuthorityCompletionRealLandCallBudget` |
| `completion-consult-PushBuild` | `TestPublicAuthorityEveryPushConsultsCompletion` |
| `completion-consult-PushLand` | `TestPublicAuthorityEveryPushConsultsCompletion` |
| `completion-consult-PushEnsureFence` | `TestPublicAuthorityEveryPushConsultsCompletion` |
| `completion-consult-PushAdvanceFence` | `TestPublicAuthorityEveryPushConsultsCompletion` |
| `completion-consult-PushDeleteBranch` | `TestPublicAuthorityEveryPushConsultsCompletion` |

## Verification

The clean-tree verifier returned **PASS** on the tested implementation above.

- `make check`: **0**, zero lint issues, complete short race suite and build. Serve took **303.155 s** inside this check.
- Focused public/acceptance/completion/barrier and Stage A exact-output race checks: **0**.
- Strict replay with `GOMEMLIMIT=6GiB`: **2,159/2,159** identical actions, structural states, snapshot hashes and boundary checkpoints; **8,676** probes. Gates 2/4 and self-tests pass, with no non-allowlisted differences.
- Simulator: both exits **0**, **146/146** normalized blocks identical. Its normalized SHA-256 remains `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Depth-1/4 contracts: **90/90** identical. GitHub status/API/health/author projections, actual CLI bytes, Stage A exact-output and legacy Git object/ref/command probes pass unchanged.
- MixedGithubBytes remains `cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee`.
- Existing wire fixtures: **105/105 unchanged**, including 90 registry fixtures. Direct Git blob comparison confirms all **211** fixtures present at the original PR head `bc9779c`, and all **208** present on main `75c4029`, remain byte-identical. No testdata file changed in this round.
- Every new/changed test passes **20 iterations**, `GOMAXPROCS=1 -race -cpu=1`, with **eight owned CPU burners**. Independent `/proc` observation confirmed all eight were alive and accumulated CPU time; all observed PIDs were subsequently reaped. Public serve group **481.117 s**; real-Git base recovery **192.973 s**. Config **2.933 s**, changepolicy **2.212 s**, store **7.914 s**, CLI **16.987 s**, gitops **31.163 s**.

CI-like `GOMAXPROCS=2 go test -race -short -count=1 -p=1 -timeout=15m`:

| Package | Main 75c4029 (s) | Tested implementation (s) | Delta (s) |
| --- | ---: | ---: | ---: |
| `internal/serve` | 266.833 | 287.275 | +20.442 |
| `internal/gitops` | 84.426 | 94.627 | +10.201 |
| `internal/queue` | 163.568 | 163.201 | -0.367 |
| `cmd/openmerge` | 64.306 | 64.540 | +0.234 |

These are single local test-suite measurements, not daemon throughput or a causal performance estimate. The baseline was freshly run in this session in a clean detached `75c4029` worktree; its completed exit-0 log is reused by the verifier. Package times exclude lock waiting. Every measured package and stress group remains below 900 seconds. No timeout or CI flag changed.

## Reproduce and inspect

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
# Set OPENMERGE_TEST_DATABASE_URL to the shared test Postgres localhost:54329.
python3 scripts/verify-adr0008-9c-3.py \
  --baseline "$HOME/.cache/openmerge-143-r1-main-75c4029" \
  --out "$HOME/.cache/openmerge-143-r1-rerun"
```

The baseline must be clean main `75c4029`. The verifier validates the shared scratch-DB allowlist before subprocesses, creates a unique scratch database, and drops it in `finally`. Every Go command uses the heavy flock, GOFLAGS=-p=2 and 4 GiB memory, with 6 GiB for strict replay. GOROOT and PATH are both pinned to Go 1.27.3. Full logs, overlays, manifests, fixture identity and independent burner observation are retained at `~/.cache/openmerge-143-r1/full-r6/`.

All new pool fixtures are created and closed inside synctest bubbles. Serve uses `testSynctest`; command tests prewarm process-wide signal/DNS initialization before their bubbles. Race tests synchronize through callbacks, channels and observed PostgreSQL lock waits. Ancestry cancellation uses FIFO synchronization. Existing receipt-expiry tests advance virtual time inside their bubbles; no wall-clock ordering is used.

No migrations, Makefile, CI configuration, test flags, Dockerfile, shared GitHub formatters or Stage A retry behavior changed. The live OpenMerge DB, systemd units, live refs/status API and Smart-Data-Ohio were untouched. Code/tests/simulator made no real forge API calls. Publication is limited to the requested branch and Hold PR body; no deployment, PR merge, queue or GitHub comment.

Development runs with lint failures, the old diagnostic assertion and a noncompiling scratch mutation were excluded from acceptance. The final clean-tree run reran every gate. The final documentation-only follow-up changes no runtime, test or verifier source from the tested commit.

Historical receipt: [prior-round verification/source hashes](https://github.com/CanadaApollo6/openmerge/blob/72574ebf64c625e8431ecbfdd3de104f43aa0f04/docs/research/adr-0008-9c-3-artifacts/verification.json), SHA-256 `3c75fc211e6a9d8151dcf800567055b23cd1c65a68e99241ff1f6da0f38c75c3`.
