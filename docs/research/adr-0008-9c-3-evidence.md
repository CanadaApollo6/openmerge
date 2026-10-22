# ADR 0008 PR 9c-3 round-5 evidence

Hold remains. Round 5 removes the queues table lock that deadlocked with
TakeNotification. GitHub notification, Notify and InitializeFence row locks keep
their existing strengths. Registration gains only a shared transaction advisory
lock; it briefly waits during gated first emission, through commit. Caller
contexts and the drained no-network transaction bound the intended wait; there
is no new independent timeout. Production supplies no gate after the boundary.
Direct SQL registration and old binaries do not participate in this protocol:
stop old workers and freeze installation registration during the switch.

## Issue #144 test-only tenancy lock pins

Hold remains. This follow-up adds no production changes. The three subtests of
`TestAuthorityTenancyConcurrentFirstEmissionRegistration` reuse the concurrent
registration protocol from `TestAuthorityTenancyConcurrentRegistration` for
`ApplyAuthorityConfigWithEmissionGate`,
`AdoptAuthorityPin`, and `CheckAuthorityEmissionBackup`. Each waits until a real
second-installation registration holds shared tenancy behind a registration-key
holder, then observes the entry point waiting for exclusive tenancy before any
queue advisory and events lock. Releasing registration must produce a sanitized tenancy
refusal, no authority boundary, and no partial journal events. Adoption starts
with only its required governing config. Synchronization uses PostgreSQL lock
state and channels inside synctest, with no sleeps or new production seams.

The preflight lock remains in the lock-order inventory and in production. Its
new pin checks that preflight waits for pending registration before returning a
tenancy verdict, even though the committing transaction also rechecks tenancy.

Scratch-copy Go overlays remove exactly one entry point's lock per mutation.
All three compile and fail the named subtest with
`tenancy ExclusiveLock granted=false not observed`, exit 1:

| Mutation | Removed lock | Test that killed it |
| --- | --- | --- |
| M8 | `CheckAuthorityEmissionBackup` exclusive tenancy | `TestAuthorityTenancyConcurrentFirstEmissionRegistration/preflight` |
| M9 | `AdoptAuthorityPin` gated exclusive tenancy | `TestAuthorityTenancyConcurrentFirstEmissionRegistration/adopt-pin` |
| M10 | `applyAuthorityConfig` gated exclusive tenancy | `TestAuthorityTenancyConcurrentFirstEmissionRegistration/apply-config` |

The new test passed `GOMAXPROCS=1 go test -race -count=20` with eight owned CPU
burners. All eight accumulated CPU ticks during the run and were terminated and
reaped afterward. The store package reported 4.677 s. Mutation and verification
logs, scratch overlays, and the burner observations are retained locally in
`~/.cache/openmerge-144-verification/`.

CI-like `GOMAXPROCS=2 go test -race -short -count=1 -p=1 -timeout=15m`
passed for both packages on main `7aa71a3` and this test-only branch:

| Package | Main 7aa71a3 (s) | Issue #144 branch (s) |
| --- | ---: | ---: |
| `internal/store` | 63.895 | 75.273 |
| `internal/serve` | 329.261 | 335.314 |

After the reboot, `make check` passed with exit 0 (479.748 s wall time),
including formatting, zero lint issues, the full short race suite, and build.

These are single local suite timings, excluding compilation and flock waiting,
not a causal performance estimate. The baseline ran in a clean detached worktree.
All Go commands exported Go 1.27.3 GOROOT and PATH and ran under the heavy flock
with `GOFLAGS=-p=2 GOMEMLIMIT=4GiB`, against the shared test Postgres on
localhost:54329. No existing fixture, production code, migration, Makefile, CI
configuration, test flag, or Dockerfile changed.

## PR #145 round-1 ordering pins

The verification-commit path and the apply-config/adopt-pin paths now also
assert that the backend waiting for exclusive tenancy holds no granted queue
advisory for the target queue. The query matches `lockQueue`'s scoped two-int
key (namespace `1330464081`, `hashtext(test scope + "/" + queue ID)`) and joins
the pending tenancy request by backend PID and test scope. Unrelated lock
holders cannot trip the assertion. Preflight takes no queue advisory.

Scratch source overlays independently remove tenancy or acquire events/queue
before tenancy. Each compiled mutation fails its named behavioral test; build
failures do not count. The earlier M8/M9/M10 removal pins remain effective.

| Mutation | Changed ordering | Test that killed it |
| --- | --- | --- |
| M8 | Remove preflight tenancy | `TestAuthorityTenancyConcurrentFirstEmissionRegistration/preflight` |
| M9 | Remove adoption tenancy | `TestAuthorityTenancyConcurrentFirstEmissionRegistration/adopt-pin` |
| M10 | Remove config tenancy | `TestAuthorityTenancyConcurrentFirstEmissionRegistration/apply-config` |
| M9c | Adoption queue before tenancy | `TestAuthorityTenancyConcurrentFirstEmissionRegistration/adopt-pin` |
| M10c | Config queue before tenancy | `TestAuthorityTenancyConcurrentFirstEmissionRegistration/apply-config` |
| Verification queue-before-tenancy | Verification queue before tenancy | `TestAuthorityTenancyConcurrentRegistration` |
| M8 events-before-tenancy | Preflight events before tenancy | `TestAuthorityTenancyConcurrentFirstEmissionRegistration/preflight` |
| M9 events-before-tenancy | Adoption events before tenancy | `TestAuthorityTenancyConcurrentFirstEmissionRegistration/adopt-pin` |
| M10 events-before-tenancy | Config events before tenancy | `TestAuthorityTenancyConcurrentFirstEmissionRegistration/apply-config` |
| Verification events-before-tenancy | Verification events before tenancy | `TestAuthorityTenancyConcurrentRegistration` |
| Verification removal | Remove verification tenancy | `TestAuthorityTenancyConcurrentRegistration` |

The two unmutated tests passed `GOMAXPROCS=1 go test -race -count=20`
under eight owned CPU burners (store test time 6.643 s). All eight accumulated
CPU ticks and were terminated and reaped afterward. All 11 scratch mutations
failed with the expected assertion and exit 1.

`make check` passed with exit 0: formatting, zero lint issues, the complete
short race suite and build. The store package reported 81.889 s and serve
359.091 s; these are single local suite timings, not performance estimates.
All Go commands used the pinned Go 1.27.3 GOROOT/PATH, heavy flock,
`GOFLAGS=-p=2 GOMEMLIMIT=4GiB` and the test Postgres on localhost:54329.

Round-1 validation logs, source overlays, mutation results and CPU burner
observations are retained in `~/.cache/openmerge-145-r1/`.

## Round-5 lock-order inventory

The code inventory is in `internal/store/locks.go`. Advisory tenancy namespace
`1330464084` uses the two-int key space, distinct from queue `1330464081`,
registration `1330464082`, test migration `1330464083` and the production bigint
migration key. The second key is hashtext(test scope), empty in production.

| Path | Locks in acquisition order |
| --- | --- |
| canonicalQueue / only queues INSERT | shared tenancy; registration advisory; queues ACCESS SHARE lookup then ROW EXCLUSIVE insert; canonical_queues upsert and queues FK KEY SHARE. Case-history folding reads events with ACCESS SHARE only. |
| Notify / NotifyDelivery | webhook delivery insert and queues FK KEY SHARE; queues ROW EXCLUSIVE and update row lock. No queue advisory, tenancy, or events write lock. |
| TakeNotification | queues ROW SHARE and FOR UPDATE; queues ROW EXCLUSIVE for flag update. No tenancy, queue advisory or events lock. |
| InitializeFence | queue advisory; queues ROW EXCLUSIVE/update row lock; events ACCESS SHARE subquery. |
| Append / EmitWithPolicy / Record / landing and observation writers | queue advisory; projection ACCESS SHARE reads; action/observation locks as needed; events ROW EXCLUSIVE insert; queues FK KEY SHARE. |
| verification commit | if gated, exclusive tenancy FIRST; ascending queue advisories; tenancy/boundary reads; events SHARE ROW EXCLUSIVE; event inserts and queues FK KEY SHARE. Ungated starts with queue advisories. |
| AdoptAuthorityPin / ApplyAuthorityConfig | if gated, exclusive tenancy FIRST; queue advisory; tenancy/boundary reads; events SHARE ROW EXCLUSIVE; event inserts and queues FK KEY SHARE. Ungated starts with queue advisory. |
| first-emission preflight | exclusive tenancy FIRST; inventory ACCESS SHARE reads; events SHARE ROW EXCLUSIVE; fresh boundary/watermark reads. No queue row lock. |
| other readers, completion, journal, snapshots | ACCESS SHARE reads; no tenancy or conflicting event table lock. |
| migration | migration advisory before schema DDL; startup-only, outside supported concurrent serving. |

No queues table lock is upgraded by emission. Notification row holders never
wait on tenancy, queue advisories or events; registration never takes queue
advisories or event write locks. Existing writers holding queue advisories can
finish before emission takes events. ACCESS SHARE reads are compatible with the
event lock. There is no reverse edge to close the reported cycle. Plan and BeginConsequential take the queue advisory before action-row locks and
poison event inserts. Claim commits and releases action-row locks before its
separate queue-locked poison-publication transaction. `Complete` updates hold
action rows only, with no tenancy or event lock.

`TestAuthorityTenancyNotificationOverlap` retains Astra's real TakeNotification
probe with a deterministic seam after FOR UPDATE and before UPDATE. The gate
releases that seam while emission holds its locks. Both must commit; restoring
the old queues SHARE lock reproduces 40P01 and fails the named test.
`TestAuthorityTenancyConcurrentWriters` runs bounded notification consumption,
Notify, real registrations, ordinary appends and first emission together. A
pending second-installation registration forces tenancy refusal and no boundary.
`TestAuthorityTenancyConcurrentWritersSuccess` also commits a real first boundary
under that writer load, with notifications finishing while emission holds locks.
The adapted registration tests observe actual advisory waits, including proving
that emission holds no events write lock while waiting for shared tenancy.

`TestRepositoryAuthorityCompletion/second_installation` keeps all requested
partitions complete while a second installation's unrelated queue makes the
aggregate incomplete. `TestSameAuthorityCompletionFields` pins every queue field,
complete, installation count, request and queue count; only watermarks may differ.
`TestAuthorityTenancyBoundaryDefense` pins the explicit post-boundary in-tx
inventory. This is defense in depth: production never supplies an emission gate
once the boundary exists.

A restore under a live process invalidates the positive boundary cache; stop all
workers before restoring. `/v1/authority-completion` adds `installation_count`;
an older strict admin CLI fails closed against the new server and must upgrade.

## Round-5 verification results

Clean-tree implementation `4d9aa0f157c0d62b1b0baa9a5ad80319b01e01aa` passed against fresh main
`75c4029447ae4d24a439ae6ef8ad6103ebcf3926`. The final follow-up changes only inventory comments,
documentation and this receipt; executable code and tests remain as verified.

- `make check`: **0**, zero lint issues, complete short race suite and build;
  serve **306.964 s**. Focused propagation,
  completion, barrier and Stage A exact-output race checks: **0**.
- **69/69** named behavioral mutations killed (53 retained, 16 round-5 additions).
  No build failures count. The old-table-lock overlay reproduces exactly
  `notification=ERROR: deadlock detected (SQLSTATE 40P01)` while emission commits.
  Late exclusive acquisition fails with `emission acquired events before tenancy`.
- Strict replay with `GOMEMLIMIT=6GiB`: **2,159/2,159** identical actions,
  structural states, snapshot hashes and boundary checkpoints; **8,676** probes.
  Gates, self-tests and non-allowlisted differences all pass.
- Simulator **146/146** identical; normalized SHA-256
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
  Contracts **90/90** identical. GitHub projection/CLI goldens and legacy Git
  object/ref/command probes pass. MixedGithubBytes remains
  `cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee`.
- Existing fixtures: **211/211** identical to both `fe80399` and original PR head
  `bc9779c`; **208/208** identical to main. Wire inventory **105/105** unchanged.
- New/changed tests passed **20 iterations**, `GOMAXPROCS=1 -race -cpu=1`,
  with **eight owned CPU burners**, all reaped. The separate completion-field
  table supplement also passed 20 iterations with eight owned burners. Independent
  `/proc` observations saw all eight accumulating CPU in the full run; the short
  supplement observation saw all eight with nonzero CPU ticks before reaping.

Loaded stress package times:

- `stress-public-serve`: internal/serve 572.701 s.
- `stress-public-base-recovery`: internal/serve 201.575 s.
- `stress-public-config-store-cli-gitops`: internal/config 3.124 s, internal/changepolicy 2.017 s, internal/store 97.406 s, cmd/openmerge 18.260 s, internal/gitops 37.407 s.
- `stress-completion-fields`: internal/serve 1.039 s.

CI-like `GOMAXPROCS=2 go test -race -short -count=1 -p=1 -timeout=15m`:

| Package | Main 75c4029 (s) | Round 5 (s) | Delta (s) |
| --- | ---: | ---: | ---: |
| `internal/serve` | 265.607 | 289.044 | +23.437 |
| `internal/gitops` | 93.328 | 96.468 | +3.140 |
| `internal/queue` | 164.530 | 164.122 | -0.408 |
| `cmd/openmerge` | 64.140 | 64.611 | +0.471 |

These are single local suite timings, not daemon throughput or causal performance
estimates. All measured packages and stress groups remain below 900 seconds.
No timeout, Makefile, CI, test flag, Dockerfile, migration, shared GitHub formatter,
Stage A retry behavior or existing fixture changed. GitHub registration adds
only the shared advisory lock and the equality evidence above remains unchanged.

Round-5 mutation additions (all killed by named assertions):

| Mutation | Test |
| --- | --- |
| `tenancy-registration-shared` | `TestAuthorityTenancyConcurrentRegistration` |
| `tenancy-exclusive-after-events` | `TestAuthorityTenancyConcurrentRegistration` |
| `tenancy-boundary-defense` | `TestAuthorityTenancyBoundaryDefense` |
| `completion-installation-verdict` | `TestRepositoryAuthorityCompletion` |
| `notification-table-deadlock` | `TestAuthorityTenancyNotificationOverlap` |
| `completion-field-Reason` | `TestSameAuthorityCompletionFields` |
| `completion-field-Digest` | `TestSameAuthorityCompletionFields` |
| `completion-field-ExpectedDigest` | `TestSameAuthorityCompletionFields` |
| `completion-field-GoverningCommit` | `TestSameAuthorityCompletionFields` |
| `completion-field-ID` | `TestSameAuthorityCompletionFields` |
| `completion-field-Installation` | `TestSameAuthorityCompletionFields` |
| `completion-field-Base` | `TestSameAuthorityCompletionFields` |
| `completion-field-Lane` | `TestSameAuthorityCompletionFields` |
| `completion-field-installation-count` | `TestSameAuthorityCompletionFields` |
| `completion-field-complete` | `TestSameAuthorityCompletionFields` |
| `completion-field-queue-count` | `TestSameAuthorityCompletionFields` |

The retained `tenancy-registration-lock` and `tenancy-registration-held` recipes
now remove exclusive advisory tenancy, and are killed by the adapted
`TestAuthorityTenancyConcurrentRegistration` and
`TestAuthorityTenancyRegistrationWaitsForEmission`. The new shared-lock recipe
removes registration's shared advisory lock. The late-lock recipe places the
exclusive acquisition after `checkAuthorityEmission` has locked events.

Logs, overlays, manifests and source hashes: `~/.cache/openmerge-143-r5/full-r3/`;
fixture and independent burner observations: `~/.cache/openmerge-143-r5/`;
completion-field supplemental stress: `~/.cache/openmerge-143-r5/supplement/`.
The initial clean-tree rejection for a generated Python bytecode directory, an
interrupted preliminary mutation run, and a development-only invalid nested
synctest subtest were excluded. They are not acceptance results. The final
clean-tree verifier reran all gates. No live database, systemd units, live
refs/status API, real forge call from code/tests/simulator, or Smart-Data-Ohio was
touched. No PR comment, queue or deployment was performed.


---

# ADR 0008 PR 9c-3 round-4 review evidence

Round 4 retains **Hold**. Completion compares the partition identities, installation
count, completeness, reasons, digests and governing commits across discovery,
ignoring only watermarks, and returns the final snapshot. Ordinary sibling
appends pass; partition registration still refuses. Positive authority-boundary
lookups are cached per store process; negatives query again. Restore stops all
workers before replacing history. The runtime role needs UPDATE, DELETE or
TRUNCATE on events for the first-emission lock. Round 5 replaces the queues
SHARE lock with the advisory tenancy protocol described below.

Setup verification is the primary public first-emission gate. A positive DraftID
already implies a boundary; lifecycle writer gates remain defense in depth for
internal/draftless callers, covered directly at store level and through the
internal activation seam. Startup runtime construction now pins database identity
in the tested constructor. Runtime receipt errors preserve cause identity while
omitting its text. The discovery_round2_test synctest conversion is inherited
from main merge #142, not authored by this PR.

## Round-4 verification

The clean-tree verifier returned **PASS** on implementation
`7323d90cba72f76c410775cec4fe65c189669c8d`, against a fresh clean
main `75c4029447ae4d24a439ae6ef8ad6103ebcf3926` run. This evidence follow-up
changes only documentation and the JSON receipt; runtime, tests and verifier
source remain exactly tested.

- `make check`: **0**, zero lint issues, complete short race suite and build;
  serve **310.154 s**. Focused public/completion/barrier/Stage A exact-output: **0**.
- **53/53** named behavioral mutations killed with exit 1, without build failures:
  retained 39, seven round-3 tenancy rows below, and seven new round-4 rows.
  The existing `completion-verdict-equality` overlay now bypasses semantic
  comparison and fails `TestPublicAuthorityCompletionPartitionRace/register`.
  Its `/append` case now accepts a real, complete sibling partition's append.
- Strict replay (`GOMEMLIMIT=6GiB`): **2,159/2,159** identical actions, structural
  states, snapshot hashes and boundary checkpoints; **8,676** probes, gates 2/4
  and self-tests pass; no non-allowlisted differences.
- Simulator: **146/146** identical normalized blocks, both exits **0**, SHA-256
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Depth-1/4 contracts: **90/90 identical**. GitHub projection/CLI goldens,
  status/API/health/author and actual CLI bytes, Stage A exact-output, and
  legacy Git object/ref/command probes pass unchanged.
- MixedGithubBytes SHA-256 remains
  `cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee`.
- Retained wire fixtures **105/105**, registry fixtures **90/90**, unchanged.
  Direct blob comparison: all **211** testdata files at `833157a`, `72574eb`
  and `bc9779c`, and all **208** at `75c4029`, remain identical.
- Every new/changed test passes **20 iterations**, `GOMAXPROCS=1 -race -cpu=1`,
  under **eight owned CPU burners**. Independent `/proc` observation confirms
  all eight accumulated CPU and were reaped. Public serve **581.640 s**;
  real-Git recovery **211.307 s**; config **3.716 s**, changepolicy **2.167 s**,
  store **19.746 s**, CLI **19.582 s**, gitops **36.803 s**. No timeout changed.

| New round-4 mutation | Named test |
| --- | --- |
| `S7-adopt-gate-skipped` | `TestAuthorityLifecycleEmissionDefenseInDepth` |
| `S8-config-gate-skipped` | `TestAuthorityLifecycleEmissionDefenseInDepth` |
| `A4-no-gate-created` | `TestPublicAuthorityActivationEmissionDefenseInDepth` |
| `W-no-db-identity-pin` | `TestProductionAuthorityRuntimeWiring` |
| `E3-receipt-cause-kept-in-message` | `TestPublicAuthorityRuntimeReceiptErrorSanitized` |
| `boundary-positive-cache-disabled` | `TestAuthorityBoundaryPositiveCache` |
| `boundary-negative-cache` | `TestAuthorityBoundaryPositiveCache` |

S7/S8 are exercised directly through the store lifecycle writers with stale
watermark, invalid receipt and mixed tenancy, asserting no partial append.
A4 exercises internal activation with a deliberately nonexistent draft and a
journal append after preflight, requiring the committing gate's watermark
refusal before draft lookup. It does not claim this is reachable through the
normal public setup flow. W tests the startup constructor's database pin and
sanitized failure; E3 drives a runtime receipt read through configuration and
checks cause identity and omission of the private diagnostic.

Fresh CI-like `GOMAXPROCS=2 go test -race -short -count=1 -p=1 -timeout=15m`:

| Package | Main 75c4029 (s) | Round 4 (s) | Delta (s) |
| --- | ---: | ---: | ---: |
| `internal/serve` | 265.228 | 289.957 | +24.729 |
| `internal/gitops` | 96.262 | 94.782 | -1.480 |
| `internal/queue` | 164.276 | 164.621 | +0.345 |
| `cmd/openmerge` | 64.594 | 64.886 | +0.292 |

These are single local suite measurements, not daemon throughput or a causal
performance estimate. Package durations exclude heavy-lock waiting. All are
below 900 seconds. Both GOROOT and PATH used Go 1.27.3; Go commands retained the
heavy flock, GOFLAGS=-p=2 and 4 GiB limit (6 GiB for replay).

Full logs, overlays, manifest and normalized outputs:
`~/.cache/openmerge-143-r4/full-r1/`. Independent burner and fixture observations:
`~/.cache/openmerge-143-r4/{burners-observed,fixture-blobs}.json`.
The isolated small test database was dropped; scratch files stayed on disk under
the cache, outside `/tmp`. No live database, systemd unit, live refs/status API,
Smart-Data-Ohio, real forge API call, migration, existing fixture, Makefile, CI,
test flag or Dockerfile changed. Shared GitHub formatters and Stage A retry
behavior are unchanged. Publication remains limited to the requested branch and
Hold PR body; no comment, queue, merge or deployment.

## Retained round-3 evidence

Classification: **Hold**. Round 3 enforces the user-selected **one database per
client (installation)** model. No migration is needed. All queues count, including
other repositories and paused, retired or excluded partitions. First emission
previously held `LOCK TABLE queues IN SHARE MODE` until transaction completion (superseded in round 5): PostgreSQL
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

## Retained round-2 mutation table

Round 2: all **39/39** overlays failed their named behavioral tests with exit 1. A compile failure never counts. The verifier requires the named test failure and rejects build failures.

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

## Retained round-2 verification

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
