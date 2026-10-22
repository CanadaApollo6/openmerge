# ADR 0008 PR 9c-2b evidence

Trial class: **Hold**. Base: local main `816ebf23a72ea54e31d0d972d402e975883ddddc`
(#136 and #137 included). Implements the whole **9c-2b** slice in one PR.
Public OpenMerge configuration remains rejected and production constructors
remain opted out. No deployment, organization acceptance, queueing or merging.

## Push inventory

Every generated-object push passes dispatch validation and, after local object
inspection/content validation, the store-reloading guard immediately before
publication. No Git read, credentials refresh or warning callback intervenes
between the final guard and the push. The same locked session constructs and
inspects each actual OID once. Evaluation uses the activation's pinned seeing
inventory; current applied evidence/revision is only the drift guard.

| Push | Boundary and write | Proof |
| --- | --- | --- |
| Test refs / batch branches, including merge/squash/rebase intermediates and fast-forward markers | `internal/gitops/build.go:284` → push at `:291` | `TestAuthorityRejectsBeforeEveryPushFamily`, `TestAuthorityActualIntermediateCommitAndAtomicFence`, `TestAuthorityFinalGuardEveryPushSite` |
| Atomic landing base + generated fence | `internal/gitops/fence.go:130` → `pushLease` at `:133` | Same all-family tests; actual intermediate and atomic fence rejection |
| Fence advance | `internal/gitops/fence.go:166` → `pushLease` at `:169` | All-family rejection/final-guard tests |
| First fence | `internal/gitops/fence.go:206` → `pushLease` at `:209` | All-family tests and integrated first-fence tests below |
| Branch deletion | `internal/gitops/delete_branch.go:38` → `pushLease` at `:41` | All-family tests, absent/changed-ref read-first tests |

Shared inspection: `internal/gitops/prepared_push.go:18`; final validation and
recheck: `internal/forge/push_options.go:135`; immutable serve context and
store-reloading guard: `internal/serve/push.go:38` and `:182`.
`Driver.DeleteRef` disposable test-ref cleanup remains the ADR's explicit
exemption; no sixth Git method exists. Future generated push paths must use
this boundary.

## Plan row and carry-over map

| 2b ownership | Implementation and tests |
| --- | --- |
| Plan :165/:222: typed reader/compiler/content, immutable context and every push guard | Internal lifecycle adapter in `internal/serve/push.go:38` consumes validated configuration/typed interpreted observation; requires authenticated adopted inputs, clones intents and captures queue identity. All five integrated guards reject source drift and store closure in `internal/serve/authority_push_runtime_test.go:28`. Local validators retain content checks even with App bypass. `internal/gitops/push_validation_test.go:405` proves every final guard runs twice total and the next Git invocation after acceptance is push; rejection compares complete real bare-remote refs and zero push calls. |
| Plan :223/:249, #90: registration/cycle/first queue pin → observation → fence and direct options | `internal/serve/serve.go:487` prepares before bootstrap; `internal/serve/branches.go:393` observes before the real first bootstrap site. `TestAuthorityLifecycleFirstFenceEntrypoints` (`authority_push_runtime_test.go:28`) runs New/registration, actual refreshWorker and cycle with cold real Git, governing files, a private lifecycle resolver and no external preparation hooks; asserts adopted durable pin/observation before direct EnsureFence, then reads back the real fence. Existing 21-case real-Git ordering/reload matrix remains. |
| Plan :241, #106(c): configTip-unset startup; existing-ref no-op | `internal/serve/push.go:105` preserves pending-config options; `serve.go:487` lets EnsureFence read an existing remote even when configuration/observation is unavailable, while absent creation defers. `TestCachedGitHubPinCannotSkipRequiredValidation` covers pin-present/hooks-absent startup and existing-ref success. `TestAuthorityStartupUnavailablePreparationPreservesRealFenceNoop` (`authority_push_runtime_test.go:190`) reads complete real refs before/after failed internal preparation. The integrated unadopted case also defers absent creation without failing construction. |
| Plan :238, #130 / Sonnet #133: ExpectedRefs drift at unchanged binding | `internal/serve/authority_activation.go:183` detects full interpretation-input drift, resets observation cadence and durably invalidates adoption before any fallible seeing read for the unchanged binding. `internal/store/write_restriction.go:308`, `:428`, `:520` prevent both retry/preservation paths from retaining stale adoption; `internal/queue/authority.go:189` invalidates positive provenance/revision while retaining known violations. `TestAuthorityLifecycleAdoptsDraftWithAtomicConfig` (`authority_lifecycle_test.go:397`) exercises same-tip and same-binding rebind changes with failed protection reads, rejects old contexts and refreezes current inputs through rollback/fresh proposal/re-enable. `TestAuthorityInputDriftInvalidatesBothWriters` (`store/authority_config_test.go:390`) covers draftless/prior-draft retries, durable floor, restart and legitimate recovery. `TestAuthorityInputInvalidationKeepsViolations` (`queue/authority_adoption_test.go:259`) retains negatives across snapshots. |
| Plan :239, #106(a): empty OldSHA and independent path budgets | `internal/executor/push.go:28` binds BuildBaseSHA into the transient intent; `serve/push.go:218` authenticates it against durable current base or current-digest tested speculative batches, at freeze and each guard. `gitops/prepared_push.go:18` proves live-base ancestry and inventories only commits introduced above that exact build base. Shared/real/fake path budgets reset per commit, retaining aggregate byte/graph bounds and incremental byte rejection. `TestAuthorityBuildCreationBoundsLargeHistoryAndCleanedRef` (`gitops/push_validation_test.go:487`) uses >10,000 prior commits for first build and deleted-ref rebuild; `TestPreparedPathBudgetsArePerCommitRealGit` (`:514`) accepts two separately bounded 60,000-path commits and rejects one over 100,000. |

## A2 and compatibility

The package-private adapter requires both `AuthorityRequired` and the private
lifecycle resolver; neither is installed by production constructors, candidates,
receipts or replayed pins. All five push entry points and first-fence preparation
are exercised by `TestAuthorityBarrierJournalCannotInstallRuntimeAuthority`
(`internal/serve/authority_barrier_test.go:49`). Raw activation without interpreted
adoption cannot construct a usable push context, even through explicit hooks.
Existing journal pins retain their baseline fail-closed behavior; arbitrary
journal write access is not an authenticated activation route.

Zero/disabled authority options return before added validation I/O.
`TestDisabledAuthorityOptionsPreserveLegacyBuildBytesAndIO` compares deterministic
squash OIDs and exact normalized argv with populated-but-disabled context/hooks.
The unchanged 3a legacy probe compares every raw object, ref and command for all
four merge methods over create/build/update/advance/land/delete: **24/24 real-Git
and 24/24 fake checkpoints byte-identical**. Hashes retain the 3a baseline values:
real `c4540c89e3f93b997c6132bb976532d7701b0a07e434a70868123cb80b626d0e`,
fake `041deff2b2f2fd0985c4c1d185267ad9e759d6af1404ee9ef6eda6eee2f342fd`.

## Verification

Implementation revision: `3c47d2fb38d4d53099cbb7843a2796f514a3e829`.
The clean-tree driver returned **PASS** at that revision. The following commit
changes this evidence document only.

- `make check`: **exit 0**, zero lint issues, complete short race suite and build.
  Focused push/authority/governing/barrier and Stage A exact-output race checks:
  **exit 0**.
- Strict replay: **2,159/2,159 identical** actions, structural states, snapshot
  hashes and checkpoints; **8,676** matching probes, gates 2/4 and all **22**
  self-tests pass. No differing/allowlisted fields.
- Simulator **146/146**, depth contracts **90/90**, GitHub projections/status CLI
  and Stage A exact-output tests: **byte-identical**. Wire fixtures **105/105**
  unchanged; no new fixture. Simulator SHA-256:
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`;
  contracts `88e727ebe7ea5b570f352c5c661c2cd1642c1b5120f3d3ed086993f4dd00362a`.
- Matching isolated `-race -short -count=1 -timeout=15m` package runs: serve
  **266.523s → 288.971s (+22.448s)**; gitops **78.921s → 93.627s (+14.706s)**.
  Full `make check` package times: serve **305.922s**, gitops **98.413s**.
  These are local measurements, not GitHub-runner predictions. Fast-import and
  direct tree construction keep the large-repository regressions lean; no real
  waits or frozen test-flag changes were added.
- Main real-Git/ordering/input-drift/held-lock matrix: **20 iterations each pass**,
  `GOMAXPROCS=1 -race -count=20`, **eight owned CPU burners**, all reaped;
  package times gitops **137.803s**, serve **417.605s**, store **56.385s**
  (wall **432.491s**). Final startup and invalidation-first follow-ups also pass
  **20 iterations each** under the same stress conditions (wall **14.981s**
  and **180.791s**, all burners reaped). The strengthened invalidation-first
  regression fails against an external overlay of the previous implementation
  and passes on the final code. All interactions are synchronous or explicitly
  injected; no wall-clock deadline ordering is used.
- Shared test Postgres namespace lists match exactly: **110 namespaces**,
  including the same **94** pre-existing serve schemas; no scratch object left.
- Initial lint/test-fixture development failures and the superseded verification
  run are retained but excluded from acceptance. Final receipts are under
  `final-r3`, `legacy-final`, and the three `stress*.json` files.


Final summary SHA-256: `26baac21b89e7f296b335085b15ce348b3ac22ac2e52c688c3c671b303f178f6`.
Raw logs, exact commands and manifests are retained at
`/home/riels/.cache/openmerge-9c2b-evidence`.
Reproduce with both Go 1.27.3 GOROOT/PATH exported and shared test Postgres
localhost:54329 using `scripts/verify-adr0008-9c-2b.py --baseline
/home/riels/.cache/openmerge-9c2b-baseline --out <scratch-directory>`;
run `scripts/check-adr0008-3a-legacy.py` against the same baseline for raw object
and argv comparison. Every Go command holds the shared heavy flock with
GOFLAGS=-p=2 and 4 GiB memory (6 GiB for replay). Real Git uses owned local bare
remotes; forge identities/protection transports are fakes. No live DB/API,
service units, live refs, real GitHub application calls or Smart-Data-Ohio
organization is accessed.

## Reviewer flags

- Adoption invalidation is intentionally conservative: live-ref drift requires
  current verification preparation through rollback/fresh proposal/re-enable;
  it cannot silently reuse the old proposal. Mutable read failures retain bounded
  retry/cadence handling; no new failure cache is introduced.
- No SQL/migrations/indexes/tables, forge capability, event type, wire field,
  existing fixture, Makefile/CI/Dockerfile or frozen test flag is changed.
  BuildBaseSHA exists only in transient options, never deployed action payloads.
- No 2c propagation/drain/backup/v7 gates, 2d integrated acceptance or 9c-3 public
  activation is claimed. Existing receipt lock-wait/commit expiry and Git
  advertisement/lease race limitations remain. GitHub signature and unsupported
  pattern/rule semantics still fail closed; all-parent merge content selection
  remains the documented conservative local choice. PR 11 owns sandbox release
  acceptance; Hold remains until dogfood period B drains.
