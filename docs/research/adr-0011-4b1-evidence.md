# ADR 0011 slice 4b-1 evidence (Hold)

This ships the first half of slice 4b, using the requested scope split. The full
slice was estimated above the roughly 2,500-line review budget once the complete
suffix, restart, late-result, cancellation-capability and review-push matrix was
included. **4b-2 remains required.** Public dependency configuration is still
rejected. Production acquisition and wake dispatch remain slice 6 integration.

The branch starts at `origin/main` **0f6d1537f7b0ecfbb3e09ea2b4bfc15eee453629**.
Prerequisite PR bodies read: #195, #197, #178, #183, #188 and #193.

## Item-to-test map

| 4b-1 item | Proof |
| --- | --- |
| Composition before build I/O, exact consumer identity, atomic admission | `store.TestDependencyCompositionInvalidationThroughFoldAndReplay`, `TestDependencyCompositionAdmissionIsAtomic`, `TestDependencyCompositionOutsideAdmissionAndWrongReferencesRefused`; existing plan/claim/Emit tests at depths 1 and 4 |
| Composition declaration/policy/source basis, strict codec, maximum-size batch | `dependency.TestCompositionCompanionStrictCodecAndSemanticBasis`, `TestCompositionMaximumBatchFitsEventCap` |
| Source rewind, lost bilateral consent, declaration/final-edge and policy edits | `store.TestDependencyCompositionInvalidationThroughFoldAndReplay` subtests `rewind`, `source-policy`, `declaration`, `consumer-policy` |
| Real Git source rewind | `store.TestDependencyRealGitSourceRewindRetiresComposition`: two `t.TempDir()` bare repositories, Git plumbing for fixture objects, production `gitops.IsAncestor`; no real forge/API calls or parsed Git history |
| Reverse cascades, bounded/cyclic graph, rebuild and missed-wake repair | `dependency.TestReverseCascadeScopeRebuildBoundsAndCycles`, `store.TestDependencyCascadeRebuildAndPollingRepair` |
| Latch first invalidation, exact retired IDs and cause, no revival after recovery | `store.TestDependencyCompositionInvalidationThroughFoldAndReplay/rewind-and-recover`, `TestDependencyCompositionOutsideAdmissionAndWrongReferencesRefused` |
| Planner/fold/store agreement, queue order and landing boundary | `queue.TestDependencyCompositionDiscardExactSuffixAndLandingBoundary`, `TestDependencyDiscardBeforeAlreadyInvalidatedSuffix`; store invalidation test applies the actual stored event through the raw queue fold and every snapshot/tail cut |
| Remaining eligible wait budget across multiple source waits and restart | `store.TestDependencyEligibleClockAccumulatesRewindIntervals`, updated `TestDependencyEligibilityStableAcrossUnchangedRefresh` |
| Expensive clock verification before locks; cheap preparation CAS | `store.TestDependencyClockPreparationRaceRefusesBeforeWrites`, `TestDependencyEligibleClockHistoryGrowth`; source settlement lock tests from #195 still pass |
| Clearing holds requires fresh composition; normal approvals remain mandatory | Updated `store.TestDependencyDeclarationHoldRefusalsGatePlannerAndStart`, `TestDependencySelfAcknowledgementAndTighteningThroughStart`, `TestDependencyOptedSelfAcknowledgementStillRequiresApprovalDecision`; original claim is explicitly refused after acknowledgement |
| Replay/cursor compatibility and malformed snapshots | Every-cut helper in store composition tests; `dependency.TestCompositionSnapshotRejectsForgedWrapperAndFutureCause`; unchanged legacy/golden suites |
| Rollback refusal, real rows for both new kinds | `store.TestDependencyCompositionDowngradeRefusal` isolates each real new row and checks inventory/refusal; `TestAuthorityDowngradeEventCoverage` |
| Activation and writer tripwire | `journalreader.TestNoProductionNewEventOrSnapshotWriters`, unchanged activation tests and public-loader rejection |

No serve fixtures were added. Existing wire/golden files are unchanged; one new
wire fixture describes the new queue invalidation kind. No existing event gains
a field. Dependency base v10 is nested in the existing v9 cursor only after
composition history; old v8 and legacy encodings remain supported and unchanged.

## Mutations

**10/10 mutants were killed.** Each compiled, failed a behavioral assertion, and was restored. The runs
used `-race -short -count=1`, the required toolchain/resource wrapper and the
specified disposable test database. Compilation failures were not counted.

| Mutation | Detecting test | Result |
| --- | --- | --- |
| Omit build composition append | Store composition invalidation `/refresh` | Killed: missing exact build companion |
| Ignore satisfying source digest | Dependency strict codec/semantic basis | Killed: different satisfying landing retained composition |
| Remove invalidation latch | Store invalidation `/rewind-and-recover` | Killed: recovered evidence revived the old batch |
| Ignore exact retirement companion ID | Store outside-admission/wrong-reference test | Killed: wrong retirement reference accepted |
| Drop previously eligible time | Store accumulated rewind-clock test | Killed: remaining budget was reset |
| Remove new queue kind from downgrade inventory | Store real-event downgrade test | Killed: inventory/refusal boundary missing |
| Insert new-kind literal in an unrelated production writer | Journalreader writer tripwire | Killed: forbidden literal outside allowlist |
| Ignore exact invalidation cause | Store outside-admission/wrong-reference test | Killed: wrong cause accepted |
| Reuse stale clock preparation watermark | Store preparation-race test | Killed: raced preparation reused |
| Require the entire suffix instead of the planner's next reason segment | Queue mixed-reason regression | Killed: planner segment refused by fold |

Reproduction: apply the named single guard mutation, run the named focused test
with the command environment below, check for its assertion failure, then restore
the source. Session logs and scripts are in `/tmp/openmerge-4b-evidence/` and were
not committed to the repository.

## Commands and timings

Every Go command used:

```sh
flock /home/riels/.cache/openmerge-heavy.lock env \
  GOFLAGS=-p=2 GOMEMLIMIT=4GiB \
  GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3 \
  PATH=/home/riels/.local/share/mise/installs/go/1.27.3/bin:$PATH \
  TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' \
  OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' \
  go test -race -short -count=1 -timeout=590s ./internal/PACKAGE
```

Packages ran individually. The baseline was a `git archive origin/main` copy in
`/tmp/openmerge-4b-evidence`, so implementation edits could not contaminate it.
The protected main checkout was never used. All database fixtures use disposable
schemas. No Makefile, CI, test flags, Dockerfile, migrations, indexes, status API,
protected refs or live installation changes.

All affected packages passed individually with `-race -short -count=1`.
These are single local elapsed samples, including suite variation; they are not
statistical estimates or guarantees about CI speed.

| Package | `0f6d153` baseline | Final | Delta |
| --- | ---: | ---: | ---: |
| serve | 309.240s | 316.275s | +7.035s |
| store | 217.082s | 225.232s | +8.150s |
| queue | 138.921s | 139.102s | +0.181s |
| dependency | 7.702s | 7.913s | +0.211s |
| journalreader | 19.713s | 19.573s | -0.140s |
| provenance/journal | 68.614s | 68.570s | -0.044s |

The unchanged public config suite also passed (1.595s). `golangci-lint run ./...`
reported **0 issues**; gofmt and `git diff --check` are clean. The final queue run
includes the mixed-reason regression. Store, dependency, journalreader and
provenance/journal were rerun after the performance changes. Full serve has a
+7.035s sample delta; the controlled affected-worker measurement below isolates
+1.893s, with no added serve fixtures or cycles. Both measurements are retained
instead of treating the package difference as precise added test cost.

Performance investigation kept the affected worker fixture within the requested
added-time budget without changing flags or removing tests. An intermediate dependency worker run rose from 5.795s to 16.876s.
Step timings localized most cost to the active build cycle. The new v10 base was
flattened instead of nesting another full v8 validation, unchanged queue rows no
longer revalidate semantic source inputs, and non-build actions no longer prepare
formation-clock history. The same existing worker then passed in **7.688s**,
a **+1.893s** local increase in the final-code rerun. No serve test or worker
cycle was added.

The 256-refresh clock-growth fixture recorded 4.337ms initially and 251.011ms with
256 unchanged refreshes. Its activated plan took 264.992ms total, with **10.844ms**
from the first advisory-lock request to return: expensive history work remained
outside the lock. These are isolated race-mode samples, not throughput guarantees.
The temporary timing instrumentation was removed.

The legacy differential harness ran seeds 17/42/101, merge/squash/rebase/fast-forward
and depths 1/4: **24 scenarios and 6,335 serialized queue events plus results**
match the isolated main archive byte-for-byte. Both hashes are
`feaf5c1fd8cc8c70cd40550afd414cc5c8bbcf205d4b55ce0e881efb13102577`.
The temporary harness was removed; existing simulation fixtures were untouched.

An initial full serve run found the old 4a expected-event-list assertion. It was
updated to require the new build companion, and the full package was rerun. The
provenance base-kind matrix now explicitly accepts dependency v10 and still
rejects future v11. Formatting changes are isolated in style commits.

## ADR interpretations and remaining limits

1. **Recorded invalidation is irreversible for that composition.** If a source
   rewind is observed and then repaired before a worker polls, the first cause
   remains latched. Recovery can make the entry eligible for fresh composition;
   it cannot revive old CI. The new retirement event pins the exact original
   companions and cause IDs.
2. **Equivalent refreshes preserve work.** Observation/read IDs and equal policy
   refresh IDs are audit identities, not changed semantic inputs. Source-base
   advances retaining the same satisfying landing do not rebuild. Changed actual
   satisfying revisions, declaration acts, acknowledgements and policy digests
   do change the basis.
3. **Landing is the settlement boundary.** A started batch keeps its decision
   and fence. Unstarted suffixes use the existing ADR 0009 discard machinery;
   no cascade abandons an uncertain push or rolls back a base.
4. **Reverse wakes schedule reconciliation.** They never substitute for fresh
   direct-edge observations or landing-start verification. A confirmed landing
   remains terminal even when that source's latest declaration changes. Missed
   wakes are repaired by polling. Acquisition and runtime dispatch remain gated
   slice 6 work, rather than adding forge calls to the pure planner.
5. **Exact clock suspension replaces #195's approximation.** Eligible intervals
   accumulate from enqueue. Recorded times are clamped in journal order when
   transaction timestamps regress; dependency waits are excluded. There is no
   existing conflict-grace clock to extend.
6. **Clock acquisition is deliberately bounded under Hold.** The read streams
   at most 32,768 eligibility-changing rows and 32 MiB, failing closed rather
   than returning partial clock state. Verification occurs before advisory
   locks; side-position and entry-identity drift refuses under them. Landing
   transactions do not scan formation-clock history. The isolated growth sample
   makes its O(history) cost visible; scalable clock-history retention remains
   necessary before high-volume activation, alongside the existing fence/lifetime
   policy prerequisite. No compaction or journal deletion is introduced.
7. **Mixed invalidation causes.** The planner retires one contiguous reason
   segment at a time. A dependency segment can precede a suffix already retired
   for another cause. Shared segment selection and a pre-commit fold-acceptance
   check prevent the #195 planner/fold stall from recurring in that case.
8. **Split boundary.** Basic suffix/fence and snapshot replay coverage is necessary
   to ship 4b-1 safely. The complete 4b-2 acceptance matrix still includes suffix
   order, restart/late results, both cancellation capabilities and unlanded-push
   explicit-pin versus revision-less semantics. Neither this PR nor its tests
   claim Stage B or complete Stage A activation.
