# ADR 0008 PR 9c-2a evidence

Trial class: **Hold**. Baseline: `c53f4253fb9c79ff8527494a7f0c307cbd6b1470`
(local main, #136). Verified implementation:
`f0449e5e076f8d93956e8584bf9bd57614d9f153`. The following receipt commit
changes documentation/artifacts only. This delivers the whole 2a slice in one
PR; no deployment, queueing, merging or organization acceptance is authorized.

## Requirement and test map

| 2a requirement | Implementation and regression |
| --- | --- |
| Actual governing acquisition in discovery and worker loading | `internal/serve/reconcile.go:216,282,625` uses `internal/config/governing_acquisition.go:41,53`: immutable landed config and selected CODEOWNERS, equality with the loaded candidate, exactly one default queue, exact supported selectors and effective eligible-owner coverage of every supported config/CODEOWNERS location. `TestAuthorityGoverningAcquisitionBoundaries` (`internal/serve/authority_acquisition_test.go:32`) drives actual `RefreshBranches` and configuration paths, including missing/uncovered/unresolved owners, incomplete files, wrong commit and failed/missing protection reads. |
| Actual governing acquisition in setup and verification | `internal/forge/github/setup_authority.go:84` and `cmd/openmerge/setup_verify.go:111` use `internal/forge/github/governing_acquisition.go:21,63,131,154,187,216`: server default ref must identify a commit; bounded tree/blob provenance and explicit completeness precede mutation/verification. Typed protection reads cover the server's current bases and protected refs; a proposed selector cannot narrow landed obligations. `TestAuthoritySetupAcquiresLandedEvidenceBeforeMutation`, `TestAuthoritySetupCannotNarrowLandedProtectionEvidence`, `TestAuthorityVerifyAcquiresServerGoverningCommit` (`internal/forge/github/governing_acquisition_test.go:24,101,127`). Failed acquisition writes neither GitHub settings nor a backup artifact. |
| Import acquisition; acceptance cannot waive governing gates | `internal/forge/github/import_read.go:134,186` acquires landed authority evidence through the same typed collector/identity/protection readers and adds blocking items. Existing `ProtectionReport.accept` refuses blocking items. `TestSetupImportGoverningEvidenceCannotBeAccepted` (`cmd/openmerge/setup_governing_test.go:21`) runs the real command, obtains actual item IDs and retries `--accept --out`: refusal, no partial output and no draft overwrite. It also refuses the public activation barrier with otherwise complete evidence. |
| Incomplete evidence cannot partially activate | `internal/serve/authority_activation.go:187` requires complete interpreted protection evidence for a new binding, even without a draft. `internal/forge/authority_acquisition.go:10` validates raw typed inventory in discovery/setup/import. Boundary tests compare durable config/commit/pin before and after every failure. The candidate-only and reassigned-grant tests at `internal/config/governing_acquisition_test.go:39` require server-derived identity/membership/write eligibility; no caller-supplied owner pool can authorize acquisition. |
| Invalid landed config blocks authority work; GitHub behavior unchanged | `internal/serve/reconcile.go:268,353` retains the last durable policy, records the config block and stops authority classification/admission until repair; cycle settlement still runs first. `TestAuthorityInvalidLandedConfigurationBlocks` (`internal/serve/authority_acquisition_test.go:101`) drives the real cycle, checks the block, excludes config/pin/start records and proves repair. `internal/serve/authority_github_compatibility_test.go:70` exports invalid-config journal/API/health bytes for both ordinary and opted-in GitHub workers into the baseline comparison. |
| Public barrier, production opt-out and sanitized errors | Public schema/loader/CLI rejection and `cmd/openmerge/installations.go:118` remain unchanged. `TestAuthorityBarrierJournalCannotInstallRuntimeAuthority` (`internal/serve/authority_barrier_test.go:49,147`) adds discovery/load entry points; the real import regression covers its new reporting path. `GoverningReadError` (`internal/config/governing_acquisition.go:18`) exposes only timeout/quota/canceled/unavailable while retaining `errors.Is`/`errors.As`; its classification test and token-bearing boundary failures reject transport-text exposure. |

## A2 and operation boundary

Acquisition is read-only. Runtime candidate parsing requires the explicit target
flag and private resolver; candidate/draft/pin data installs neither. The extended
complete-seam barrier test covers discovery/load, owner/observer/status/push and
admission paths and excludes new authority events. Production construction sets
neither opt-in. A handcrafted journal pin retains existing fail-closed effects;
database write access is not an activation security boundary.

New bindings require complete acquisition. Same-binding draftless rebinds retain
zero seeing reads. Adoption and source observations capture basis before their
independent fresh reads; acquisition cannot clear a newer finding. Setup may
propose a future policy while independently validating the landed policy.

## Verification

The clean-tree driver returned **PASS**. [Summary](adr-0008-9c-2a-artifacts/summary.json),
[additional checks](adr-0008-9c-2a-artifacts/additional-checks.json) and
[hash manifest](adr-0008-9c-2a-artifacts/manifest.json) retain compact receipts.
Raw logs/replay streams: `/home/riels/.cache/adr0008-9c-2a-all-pass`.

- `make check`: **exit 0**, zero lint issues, complete short race suite and build
  (serve **291.929s**, cmd **61.136s**). Focused acquisition/setup/import/
  lifecycle/barrier race checks: **exit 0**.
- Strict offline replay: **2,159/2,159 identical** checkpoints, actions,
  structural states and snapshot hashes; **8,676** matching boundary probes,
  gates 2/4 and all **22** mandatory self-tests pass, no differing fields.
- Depth-1/4 contracts: **90/90 byte-identical**. Simulator: both exits **0**,
  **146/146 normalized blocks byte-identical**. GitHub projections, actual CLI,
  and both invalid-config journal/projection exports: **byte-identical**.
  Existing wire fixtures: **105/105 unchanged**; no new wire type/field/fixture.
- Matching local two-package race/short timing command: serve **273.236s before →
  265.829s after**; cmd **61.030s before → 61.040s after**. These are
  package runtimes from one run per side, not a speedup or GitHub-runner forecast.
- Stress: **20/20 iterations of each selected test pass** with `GOMAXPROCS=1`,
  `-race -count=20 -cpu=1`, and **eight concurrent owned CPU burners**. Selection:
  inherited `TestAuthorityAdmissionWorkerLockedAppend` plus both new serve tests.
  Elapsed **55.039s**; all eight burners terminated and reaped. The later
  export-only fix changes no production/selected-test source. Raw receipts:
  `/home/riels/.cache/adr0008-9c-2a-stress`.
- A Go-overlay mutation removes only the complete-protection fingerprint gate.
  The real configuration-boundary regression fails with “incomplete evidence
  accepted” (**expected exit 1**). Production source was never mutated.
  Raw receipt: `/home/riels/.cache/adr0008-9c-2a-mutation`.
- Shared test Postgres retains exactly the same **94** pre-existing serve schemas;
  no new scratch schema remains. No live DB/status API, units, live OpenMerge
  refs, application GitHub API or Smart-Data-Ohio organization was accessed.

Reproduce with both Go 1.27.3 `GOROOT` and its `bin` first in `PATH`, and
`OPENMERGE_TEST_DATABASE_URL` pointing to shared test Postgres localhost:54329:

```sh
python3 scripts/verify-adr0008-9c-2a.py \
  --baseline /home/riels/.cache/adr0008-9c-2a-baseline \
  --out /home/riels/.cache/adr0008-9c-2a-rerun
```

Every Go check uses the shared heavy flock, `GOFLAGS=-p=2` and 4 GiB memory
(6 GiB for replay). New serve fixtures live inside `testSynctest`; new HTTP and
pure-reader tests assert no wall-clock ordering. An initial full pass was
superseded after self-review narrowed invalid-config handling to currently
loaded authority policy and added the opted-in GitHub byte comparison. The new
journal export initially included random bootstrap fence tokens; it now
compares every event after a captured pre-invalid-commit watermark, without any
field normalization. Early fixture/compile/lint failures were corrected before
the final clean-tree run.

## Reviewer flags

- **Hold** remains. Public activation and production opt-in remain exclusively
  9c-3. Missing/hidden/incomplete protection visibility defers a new binding;
  it cannot be supplied by a draft or waived by import acceptance.
- No migrations/indexes/tables, new forge capability, frozen build/CI/test-flag
  changes or existing wire fixture edits. GitHub runtime and import retain their
  legacy path; setup's authority-only path intentionally gains these prerequisites.
- Push guards/ordering and ExpectedRefs/VerificationInput drift remain 2b;
  propagation/drain/backup/downgrade gates remain 2c; integrated real-git acceptance
  remains 2d. Existing receipt lock-wait/commit expiry limits are unchanged.
  Owner-executed organization release acceptance remains PR 11.

## Round 2

- Setup and verification re-read the server default-branch name and commit after
  all evidence; setup rechecks again immediately before recovery capture/apply.
  HTTP regressions move the tip or rename the default during classic protection
  acquisition and require zero writes (`TestAuthorityAcquisitionRechecksDefaultAfterProtection`).
- Verification acquires once per repository after all targets are resolved and
  validated and the invocation nonce exists. The two-target regression also
  proves an invalid final target causes zero acquisitions
  (`TestVerifyAcquiresOnceAfterEveryTargetValidated`).
- Discovery reuses acquisition for an unchanged repository/default branch/tip
  and binding set, using the default branch already read by refresh. The cache
  does not replace activation, recurring observation or admission reads.
  `TestAuthorityDiscoveryReusesUnchangedAcquisition` counts governing, owner and
  protection calls and invalidates the cache when the binding changes.
  `TestAuthorityDiscoveryHiddenBypassPreservesBaseDiscovery` proves active
  repositories still add/remove bases while authority admission records a
  sanitized config-source block. Read errors return after base expansion so the
  existing discovery quota/backoff scheduler remains in control. New bindings
  still require complete visibility.
- Deterministic governing validation, ambiguous config and invalid landed config
  record config-source blocks and wait for a new governing tip. Timeout/quota/
  unavailable reads retry. `TestAuthorityDefiniteConfigFailuresWaitForNewTip`
  covers both paths; landed validation retains sanitized line/path diagnostics.
  Permission denial has a separate sanitized class. Operator acquisition now
  includes nested attributes (`TestOperatorGoverningIncludesNestedAttributes`).

Acquisition cost: each refresh reads repository metadata/default tip and branch
inventory. A warm, unchanged acquisition skips config/governing/identity/
protection reads. A cold or changed policy reads configuration candidates; authority
acquisition additionally reads governing content, resolves CODEOWNERS identities,
then reads protection inventory. Owner resolution scales with each owner: one
user lookup, or team lookup plus paginated membership, then one repository grant
read per member (a 200-member team can require 200 grant calls). Protection reads
include ruleset pagination/details and classic protection for relevant refs.
Operator reads additionally use the immutable recursive tree and bounded blobs,
branch inventory (at most ten 100-branch pages), and final metadata/ref rechecks.
A truncated recursive tree fails closed. Both limits are documented in operating
instructions; complete protection visibility is in setup help too.

Round-2 receipts supersede the earlier run above. Tested production revision:
`4e3bd92aadf33f7e33d22defc0659293aea3b56d` against main `c53f425`.
Raw logs/manifests: `/home/riels/.cache/openmerge-137-r2-approved-evidence`;
stress: `/home/riels/.cache/openmerge-137-r2-stress`.

- `make check`: **exit 0**, zero lint issues, full short race suite and build;
  focused authority/governing/setup/import/barrier race checks **exit 0**.
- Strict replay: **2,159/2,159 identical** actions, states, snapshots and boundary
  checkpoints; **8,676** matching probes; gates 2/4 and all **22** self-tests pass.
- Contracts **90/90**, simulator **146/146**, GitHub projections/status CLI and
  invalid-config exports: **byte-identical**. Wire fixtures **105/105 unchanged**.
- Final run retains every required check from the verifier and omits only its
  duplicate timing-only measurement; no repository script/test flag was changed.
- Selected regressions plus the inherited held-lock test: **20 iterations each
  pass**, `-race -cpu=1`, `GOMAXPROCS=1`, **eight owned CPU burners**, all reaped
  (elapsed 58.767s). New worker tests run entirely inside `testSynctest`.
- Shared test Postgres has exactly the same **94** pre-existing serve schemas;
  no scratch schema remains. Hold/A2, frozen paths and live-system restrictions
  remain unchanged. The follow-up receipt commit changes only this document.

## Round 3

- Setup carries its acquired branch/tip into recovery apply and rechecks after
  final preflight, immediately before each write. The HTTP regression moves the
  tip during apply preflight and requires zero writes
  (`TestAuthoritySetupRechecksAfterApplyPreflight`).
- Verification requires acquisition, deduplicates repository casing, retains a
  branch/tip recheck per repository, and checks after all target observations.
  `TestVerifyAcquiresOnceAfterEveryTargetValidated` proves movement records no
  events and exposes only a sanitized retry error.
- Live owner/grant/membership failures block but retry after `RulesInterval`;
  immutable content failures wait for a changed branch/tip. Worker and discovery
  regressions prove same-tip grant restoration recovers, content failures remain
  blocked, and a repaired tip clears each durable block. Non-file diagnostics
  omit `line 0`.
- Discovery success and mutable-failure cache entries expire after `RulesInterval`
  (default 10 minutes), without sliding renewal. The next discovery after expiry
  revalidates complete protection visibility, subject to discovery scheduling and
  read-error backoff. Cache hits within the TTL preserve round-2 read counts;
  the cache certifies past acquisition only, never current admission. Synctest
  detects a new unrelated hidden-bypass ruleset at the same tip at TTL expiry
  (`TestAuthorityDiscoveryProtectionCacheExpires`). Operating docs record these
  semantics. Hold/A2 and all frozen surfaces remain unchanged.

Round-3 receipts: implementation `1461dba90f0a65285459f687ecb33e0385b04dd6`
against main `c53f425`; logs and SHA-256 manifest:
`/home/riels/.cache/openmerge-137-r3-approved-evidence`.

- `make check`: **exit 0**, zero lint issues, complete short race suite/build;
  focused authority/governing checks and isolated serve/CLI race run pass.
- Strict replay: **2,159/2,159 identical** actions, states, snapshots and boundary
  checkpoints; **8,676** matching probes, gates 2/4 and all **22** self-tests pass.
- Simulator **146/146**, depth contracts **90/90**, GitHub projections/CLI and
  invalid-config goldens: **byte-identical**. Wire fixtures **105/105 unchanged**.
- Selected regressions and inherited held-lock test: **20 iterations each pass**
  with `-race -cpu=1`, `GOMAXPROCS=1` and **eight owned CPU burners**, all reaped
  (`stress.log`). New cadence tests use `testSynctest`.
- Test Postgres namespace lists match exactly before/after, including the same
  **94** pre-existing serve schemas; no scratch object remains. This receipt
  commit changes only this document.

## Round 4

- Restore the shared diagnostic formatter, including GitHub Stage A's `line 0:`
  prefix. Only authority-produced diagnostics opt out of nonexistent file lines.
  Exact-output tests cover both identity-budget paths, accountable identities,
  the queue-candidate diagnostic and the corresponding authority message.
- Classify live accountable-identity validation and missing user/team owners as
  mutable failures. Worker and discovery caches retain the block within
  `RulesInterval`, then permit same-tip recovery. Synctest regressions count
  governing/identity reads across repeated cycles and preserve discovery retry
  deadlines; immutable content still waits for a changed tip.
- Add the partial setup regression: tip movement after write one stops at 1/2,
  retains the backup and recovery instruction, and resumes without duplicate
  writes. Remove the test-only admin-reader acquisition wrapper; the production
  candidate acquisition remains required. Exercise the actual discovery cache
  hit with a case-variant repository name. Hold/A2 and frozen surfaces remain.

Round-4 receipts: implementation `73fc50bb6a959b8e1826cf233f8d0603552ec5c1`
against main `c53f425`; raw logs, summaries and SHA-256 manifest:
`/home/riels/.cache/openmerge-137-r4-approved-evidence`.

- `make check`: **exit 0**, zero lint issues, complete short race suite/build;
  focused authority/governing and isolated serve/CLI race runs pass, including
  the new Stage A exact-output tests.
- Strict replay: **2,159/2,159 identical** actions, states, snapshots and boundary
  checkpoints; **8,676** matching probes, gates 2/4 and all **22** self-tests pass.
- Simulator **146/146**, depth contracts **90/90**, GitHub projections/CLI and
  invalid-config goldens: **byte-identical**. Wire fixtures **105/105 unchanged**.
- New retry/deadline regressions fail against an external overlay of round-3
  production code and pass with round 4; the verification checkout stays clean.
- Test Postgres namespace lists match exactly before/after, including the same
  **94** pre-existing serve schemas; no scratch object remains. This receipt
  commit changes only this document.
