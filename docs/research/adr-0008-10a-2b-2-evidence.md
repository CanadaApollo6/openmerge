# ADR 0008 PR 10a-2b-2 evidence

Classification: **Hold**. Deploy only after period B drains. Base main:
`2d6792ca8b5b4ef3729df9d4d63888b55d9ee37d` (#146, #147 and #148 landed).
This completes step 2 of the approved size gate and the original ADR 10a row.
No migration/index, queue event/wire/YAML/schema/forge, shared github formatter,
Stage A retry, Makefile/CI/test-flag or Dockerfile change is included.

## Command and frozen contract

`openmerge audit export --repo owner/name --installation 7 --from <RFC3339>
--until <RFC3339> --out <new-directory>` exports a normalized half-open range.
`openmerge audit export --manifest <manifest.json> --out <new-directory>`
re-exports the exact selection and rejects all range selectors, even empty ones.
The connection comes only from OPENMERGE_DATABASE_URL. Exit codes are 0 success,
2 usage/selectors/bounds, 1 sanitized connection/source/integrity/publication
failure. audit.Open, Reader.Export and Reader.Replay own all DB work and private
sibling-directory publication. SELECT-only role acceptance checks both command
modes, normalized bounds, tenancy mismatch, no overwrite and unchanged journal.

main.go changes only by this additive help line and its dispatch case:

```diff
+  audit export [options]  Export recorded audit evidence
+ case len(args) > 0 && args[0] == "audit":
+     return runAudit(args[1:], stdout, stderr)
```

The existing main_test.go help golden adds exactly the same line. The verifier
records the complete literal diff and checks that removing the help/dispatch
additions yields byte-identical main.go.

V1 freezes with this PR. Shape changes after merge require a version bump.
The independent Python reference now derives 30 complete files for six exports from documented journal-v1.json literals, with no Go output
as expected input. It fixes all streams, chain links, terminal/metadata/file
hashes, selection metadata and manifest self-hash. Go loads those literal journals
into scratch Postgres and compares export and replay byte for byte. The closure
vector includes a dependency, retained cycle, recorded edges and explicit absence.
Canonical primitive vectors are unchanged. The design and testdata README define
the same freeze. Under github authority landing_checks_recorded is not_recorded;
only the governing pin edge is inapplicable and retains its source.

## Carry-overs and acceptance

P3-1: TestGoverningPinQueueScoped incorporates Sonnet's queue-2 mismatched-pin
probe, plus queue-switch and backward-prefix cursor assertions. The verifier
mutates SQL queue scope and both cursor-reset conditions independently.
P3-3: a malformed/digest-mismatched pin now emits the distinct sanitized
`governing authority pin rejected by queue fold` diagnostic. It remains sticky
for later decisions even after valid replacement; operating guidance requires
preserving history and investigating, without a repair bypass.

TestAuditBareRemote uses deterministic real Git commits, a local bare remote,
explicit main ref pushes, exported tested/landed SHAs and an outside move. Its
independent oracle is rev-parse/rev-list against the remote object graph. It
also exact-replays the result. No GitHub API or openmerge refs are used.
Replay can accept fully rehashed dropped events/queues; only an external 10b
manifest anchor detects those omissions. This limitation is in operating.md.

## Realistic growth and P3-2

Inputs: one queue, 64 distinct decisions and matching check/start owners sharing
immutable batch/basis; 0/512/2048 out-of-range checks_observed payloads, 12 runs
per payload (2,377 encoded bytes), plus 0/512/2048 config transitions before the
new decisions. 201 sources selected. Payloads are real queue.EncodeEvent output,
not tiny untyped filler. No new index is created. ANALYZE and actual EXPLAIN
(ANALYZE,BUFFERS) plans are recorded outside short mode. Export includes closure;
replay includes exact closure verification and byte comparison.

Before optimization, observed export/replay seconds at 0/512/2048 were
0.814/0.985, 1.514/1.678, and 3.294/3.725. The config/pin projection re-decoded
all config events per decision. A snapshot-local cursor now retains one queue
segment's fold and advances only through new prefixes; it resets on queue
changes/backward traversal. After optimization the same inputs measured
0.815/0.984, 0.891/1.037, and 1.150/1.283. These are descriptive local single
runs, including all other retained lookups, not isolated CPU benchmarks.
The 2048-row case improves materially, justifying the small bounded cursor.
No cache survives a transaction or crosses queues. Fold/prefix/rollback tests
preserve the same serialization and authority semantics.

Plans at 0 and 512 configs used sequential scan + sort (2/514 config/pin rows);
2048 used events_pkey (2050 config/pin rows; 2257 filtered rows, 294 buffer hits,
about 0.525 ms execution in the pre-cursor lookup plan). These plans describe
the config subset, not all resolver lookup costs. Full checks/owner lookups
remain owners × retained events × checks. At fixed 64 decisions, linear local
extrapolation from 512→2048 after optimization projects roughly 14 seconds each
for export/replay at 80k configs plus 80k check rows. This uses the measured
0.259/0.246-second increments over 1536 rows of each kind; it excludes plan
changes, disk/cache effects and larger owner/check counts. This is a capacity
estimate, not a promised limit. Many owners still multiply JSON-filtered work;
measure before deployment. These historical measurements omitted production
indexes 006/007 and are not calibrated production capacity. Existing decision
identity and settlement expression indexes already cover those selectors.
No migration or index is part of this PR.

## Final verification

Authoritative measured implementation: `3e1f17e5013da8db42352ab19591bbdc1dce2c52` against
`2d6792ca8b5b4ef3729df9d4d63888b55d9ee37d`. The final evidence-only commit
changes this document; production code, tests, fixtures and verifier are identical
to the measured head. All long commands ran in the foreground and exited 0.
Go used exported GOROOT/PATH for 1.27.3, the heavy lock, GOFLAGS=-p=2,
GOMEMLIMIT=4GiB (6GiB for strict replay), and guarded localhost:54329 Postgres.
Build/test temporaries stayed under the cache directory. The driver rejects
localhost:5432 except when both CI=true and GITHUB_ACTIONS=true; act sets both.
No live database, systemd unit, live status API, openmerge ref or real GitHub
API was accessed by acceptance. No Smart-Data-Ohio write occurred.

- `make check`: **exit 0**, 561.543 s including lock time.
- Full non-short audit/command race suites: **exit 0**,
  481.158 s.
- Strict replay: **2,159/2,159 identical** actions, structural states, snapshot
  hashes and boundary checkpoints; **8,676 probes**, Gate 2 / Gate 4 PASS,
  self-test exit 0; no non-allowlisted differences. Wall time
  114.528 s.
- Simulator: **146 identical** normalized blocks. Contracts: **90/90 identical**.
- GitHub projections and actual CLI bytes identical; Stage A exact-output
  assertions pass on both sides. MixedGithubBytes remains
  `cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee`.
- **211 fixtures outside internal/audit/testdata unchanged**; existing
  LandingDecisionEvidence caller test files unchanged. The additive main/help
  diff is captured verbatim in summary.json and checked independently.
- Independent Python reference: **3 exports / 15 files identical**; complete
  Go export/replay comparisons pass, including typed dependency/absence bytes.
- Stress: **10 tests × 20 = 200 top-level passes**, GOMAXPROCS=1, -race,
  non-short, including realistic growth; 1771.938 s.
  Eight owned burners ran throughout; minimum CPU consumption
  **175,972 ticks**. All eight were terminated and reaped.
  No data race; no started test or burner remains running.

### Mutation coverage

**Original 87-target run, reclassified: 72 assertion kills, 14 panic kills,
1 equivalent, 0 survivors, 0 build/setup failures, 0 infrastructure failures.**
Eleven targets were panic-only; three also reported earlier assertion failures.
Any panic conservatively wins classification over the harness failure marker.
Build, setup and infrastructure failures never count as kills. The declared equivalent removes
closure's defensive validResolution call; the resolver constructs matching
references/dependencies together. Its validator remains directly tested.
The final verifier preserves #148's 79 targets and adds SQL queue scope,
queue/backward cursor resets, manifest-selector conflicts, inverted bounds,
zero installation, connection disclosure and source exit-code mutations.
The new queue/cursor targets fail TestGoverningPinQueueScoped; new CLI targets
fail TestAuditUsageAndSanitization and/or TestAuditSelectOnlyCommand.
Per-target outcomes and failing assertion names are in mutation-results.json.
Earlier attempts exposed two faulty mutation definitions and a staticcheck
simplification; they were corrected and the complete final run repeated.
No result from those stopped attempts counts toward this final evidence.

### Final realistic growth under race

The full race run separately times closure after scope-root spooling, complete
export, and complete replay. Same inputs as above; descriptive single runs:

| Check filler / config history | Closure (s) | Export (s) | Replay (s) |
| --- | ---: | ---: | ---: |
| 0 / 0 | 4.303 | 6.129 | 7.299 |
| 512 / 512 | 4.391 | 6.114 | 7.474 |
| 2048 / 2048 | 5.107 | 6.842 | 8.155 |

Full actual EXPLAIN plans and all growth assertions are preserved in
`evidence-full/stdout.log`; twenty loaded repetitions are in
`evidence-stress/stdout.log`. The earlier non-race before/after cursor numbers
above were initial focused measurements recorded in the implementation transcript;
they are not a comparison between race and non-race execution.

### CI-like package timings versus main

Both sides ran `GOMAXPROCS=2 go test -race -short -count=1 -p=1 -timeout=15m ./...`
and exited 0. These Go-reported package seconds exclude compilation/lock waiting.
They are descriptive single runs, not a performance gate. The realistic growth
probe is skipped in -short; the new byte/command/queue-scope checks remain small.

| Package | Main (s) | Candidate (s) | Delta (s) |
| --- | ---: | ---: | ---: |
| `cmd/openmerge` | 68.931 | 68.686 | -0.245 |
| `internal/ascii` | 1.007 | 1.006 | -0.001 |
| `internal/audit` | 11.403 | 11.185 | -0.218 |
| `internal/authority` | 1.101 | 1.110 | +0.009 |
| `internal/brief` | 3.436 | 3.530 | +0.094 |
| `internal/changepolicy` | 1.742 | 1.746 | +0.004 |
| `internal/classification` | 2.231 | 2.256 | +0.025 |
| `internal/config` | 1.649 | 1.658 | +0.009 |
| `internal/config/condition` | 1.020 | 1.020 | +0.000 |
| `internal/diagnostic` | 1.017 | 1.017 | +0.000 |
| `internal/executor` | 1.162 | 1.170 | +0.008 |
| `internal/forge` | 8.155 | 8.223 | +0.068 |
| `internal/forge/fake` | 1.448 | 1.447 | -0.001 |
| `internal/forge/github` | 64.628 | 71.923 | +7.295 |
| `internal/gitops` | 103.701 | 103.830 | +0.129 |
| `internal/queue` | 179.214 | 179.773 | +0.559 |
| `internal/serve` | 334.699 | 336.709 | +2.010 |
| `internal/sim` | 37.855 | 37.753 | -0.102 |
| `internal/sim/capacity` | 1.010 | 1.008 | -0.002 |
| `internal/status` | 11.739 | 11.870 | +0.131 |
| `internal/store` | 75.376 | 75.333 | -0.043 |
| `scripts` | 1.075 | 1.082 | +0.007 |
| `tools/dogfood` | 1.674 | 1.677 | +0.003 |

23-package total: **915.273 → 925.012 s (+9.739 s)**.
Audit: -0.218 s; CLI: -0.245 s. Wall times including compilation/lock are
966.423 s before and
931.352 s after; those are different quantities
from summed package times.

Artifacts: `/home/riels/.cache/openmerge-10b2/verified` (summary.json,
mutation-results.json, per-command logs/commands, strict replay JSON,
normalized simulator outputs, timing logs and SHA-256 manifest).
Artifact manifest SHA-256:
`b5f381a67f9e93ee9ee201011a4ca9a786ef97f1e1d3ca9d0b8e2fd5c9b18f13`.
Reproduce with the pinned exported environment and
`scripts/verify-adr0008-10a-2b-2.py --baseline <clean-2d6792c-worktree> --out <cache-directory>`.

## Round 1 (supersedes the original freeze and mutation claims above)

D1/D2 are applied before freezing. Identities are structured arrays of strings;
landing-start decision edges use batch/attempt rather than decision event ID.
The consolidated [v1 frozen specification](adr-0008-10a-design.md#v1-frozen-specification)
supersedes older descriptions, including the three-stream and metadata-hash
claims. Python independently computes semantic edges and all 30 complete files
for six literal journals; Node separately checks exact-number canonical bytes
and hashes. Approval covers all families and nonempty member ownership. Github
retry covers nonzero generation/run/attempt, excluded_later, and the retained
inapplicable authority source. Two-queue key order reverses queue IDs. Empty
now exercises Export/Replay on an existing queue with its watermark.

CLI additions: --timeout (default 5m), distinct sanitized timeout error, Linux
startup check, published path and manifest digest on success. Missing selectors
and out-of-range UTC bounds return exit 2. Canonical installation and stray
positionals have direct tests and mutation targets. SELECT-only command testing
now includes the approval-bearing journal with every evidence family. Real-git
tested SHA is checked against the bare remote graph as well as the local fixture.

### Decision-count scaling

`TestAuditDecisionCountScaling` uses the real approval fixture, one shared basis,
distinct attempts with their own decision/check/start, and no unrelated filler.
The scratch schema has only the pre-existing queue watermark index. An initial
non-race run measured:

| Decisions | Selected | Export | Replay | Export ms/decision |
| --- | --- | --- | --- | --- |
| 32 | 112 | 0.424 s | 0.474 s | 13.242 |
| 128 | 400 | 1.863 s | 2.132 s | 14.553 |
| 512 | 1552 | 12.080 s | 13.598 s | 23.593 |

These round-1 numbers used an incomplete scratch schema and are superseded by
the round-2 measurements below. The old quadratic fit and capacity projections
are withdrawn. Production migration 006 indexes decision batch/attempt identity;
007 indexes batch settlement (landed/abandoned/started), including the preceding
start selector. The growth fixture now executes those exact migration files.
Residual O(E) scans remain for batch_started, landing_checks_recorded,
test_ref_built and accountability families; multiplied across owners these can
still produce O(owners × retained events) work. Sharing one batch across many
attempts also leaves attempt filtering within that batch's settlement index
range. The authority cursor removes repeated config decoding, not those scans.
No new index, migration, production Go or serialization change is included.

Mutation reporting now distinguishes assertion kills, panic kills, build/setup
failure, infrastructure failure, equivalent, and survivor. A panic marker wins
over the Go harness's `--- FAIL:` marker; test deadline/runtime infrastructure
failures never count as kills. The original claim of 86 assertion kills was
incorrect: review reproduced 11 panic-only failures; inspection of the retained
logs also found three mixed assertion/panic targets, conservatively panic kills. The new full table and
counts below are authoritative for this revision.

### Round-1 resolution map

| Request | Resolution and verification |
| --- | --- |
| D1/D2 | Array wire identity and batch/attempt decision key; TestIdentityJSONArray, TestRound3StartReferencesExactOwningDecision, TestCompleteExportV1/closure and /approval |
| Consolidated P2 spec | Normative v1 section covers files, fields, hashes, enums, mapping, tuples, zero sentinels, timestamps and ordering; README links to it; both independent encoders verify all 30 files |
| Independent vectors | TestCompleteExportV1/{approval,github_retry,two_queue,empty}; Python derives semantic edges, Node checks exact bytes and hashes |
| Mutation categories | Panic detection precedes harness failure markers; full table below separates 74 assertion kills, 14 panic kills and two equivalents; no build/setup/infra failures or survivors |
| Decision growth | TestAuditDecisionCountScaling now records 64/256/512/1536 decisions with exact migrations 006/007; round-1 projections withdrawn, remaining lookup scans documented |
| Timeout | --timeout defaults to 5m; TestAuditSnapshotTimeout and TestAuditUsageAndSanitization verify distinct sanitized timeout and invalid duration behavior |
| CLI surviving mutants | TestAuditUsageAndSanitization rejects 007, +7 and stray positionals; cli-canonical-installation and cli-positional are assertion kills |
| Real git | TestAuditBareRemote checks tested SHA against refs/heads/main^ in the bare remote graph |
| SELECT only | TestAuditSelectOnlyCommand additionally exports the complete approval-bearing journal, including every evidence family |
| Remaining nits | TestAuditSelectOnlyCommand verifies success path/digest and missing/wrong-case selectors; TestAuditUsageAndSanitization rejects year 10000; operating.md documents Linux startup rejection and stopped-run staging cleanup |

### Complete corrected mutation table

**90 targets: 74 assertion kills, 14 panic kills, two equivalents, zero survivors,
zero build/setup failures, zero infrastructure failures.** Panic-containing runs
are conservatively panic kills even when earlier tests also asserted failure.
The two equivalents remove redundant defensive guards: closure's matching-source
validator, and the identity guard independently enforced by Identity.MarshalJSON.
The identity-json-string target instead exercises the actual array wire contract.
Removing the canonical installation check now fails the corrected value test;
the first attempted round exposed its wrong argument index and is not counted.
The table is from the complete repeated run on implementation head 2323235.

| Mutation | Outcome |
| --- | --- |
| reuse-spool-decode-storage | assertion kill |
| missing-required-source | panic kill |
| oldest-observation | assertion kill |
| accept-wrong-check-result | assertion kill |
| accept-invalid-role | assertion kill |
| omit-exact-closure | assertion kill |
| absence-source-id | assertion kill |
| adjacent-reference-duplicates | assertion kill |
| duplicate-identity | assertion kill |
| owning-decision | assertion kill |
| conflicting-spool-payload | assertion kill |
| conflicting-spool-references | assertion kill |
| event-tie-break | assertion kill |
| newest-run-in-event | assertion kill |
| authority-always-required | assertion kill |
| authority-never-required | assertion kill |
| queue-id-order | assertion kill |
| exact-closure | assertion kill |
| valid-resolution | assertion kill |
| resolved-references | assertion kill |
| missing-retained-source | assertion kill |
| scope-range | assertion kill |
| owning-decision-id | assertion kill |
| conflicting-spool-watermark | assertion kill |
| identity-structure | equivalent |
| identity-canonical | assertion kill |
| identity-html-escape | assertion kill |
| normalize-encode-error | assertion kill |
| multi-run-oldest-run-sql | assertion kill |
| multi-run-lowest-attempt-sql | assertion kill |
| authority-governing-commit | assertion kill |
| dispatch-generation-sql | assertion kill |
| additional-transaction | assertion kill |
| pin-skip-config-fold | assertion kill |
| pin-ignore-rejected-decode | assertion kill |
| pin-ignore-digest | assertion kill |
| pin-hide-rejection | assertion kill |
| rollback-status-recorded | assertion kill |
| rollback-status-absent | assertion kill |
| inapplicable-shape | assertion kill |
| sql-zero-attempt-not-normalized | assertion kill |
| closure-valid-resolution-call | equivalent |
| identity-json-string | assertion kill |
| pin-queue-scope | assertion kill |
| cli-manifest-selector-conflict | assertion kill |
| pin-cursor-no-queue-reset | assertion kill |
| pin-cursor-no-backward-reset | assertion kill |
| cli-canonical-installation | assertion kill |
| cli-positional | assertion kill |
| cli-inverted-range | assertion kill |
| cli-zero-installation | assertion kill |
| cli-leak-connection | assertion kill |
| cli-exit-source | assertion kill |
| independent-run-maxima | assertion kill |
| retain-inapplicable-check | assertion kill |
| drop-family-change_classified | assertion kill |
| drop-family-change_revision_observed | assertion kill |
| drop-family-review_stance_observed | assertion kill |
| drop-family-approval_receipt_recorded | assertion kill |
| drop-family-landing_checks_recorded | assertion kill |
| drop-family-landing_abandoned | assertion kill |
| drop-family-landing_decision_recorded | assertion kill |
| drop-family-approval_eligibility_observed | assertion kill |
| drop-family-batch_started | assertion kill |
| drop-family-test_ref_built | assertion kill |
| drop-family-change_accountability_observed | assertion kill |
| drop-family-policy_authority_applied | panic kill |
| drop-family-landing_started | panic kill |
| drop-family-landed | assertion kill |
| drop-family-checks_observed | panic kill |
| drop-family-check_rerun_requested | assertion kill |
| drop-family-check_rerun_dispatch_progress | assertion kill |
| drop-family-check_rerun_dispatched | assertion kill |
| required-call-0 | panic kill |
| required-call-1 | panic kill |
| required-call-2 | assertion kill |
| required-call-3 | panic kill |
| required-call-4 | panic kill |
| required-call-5 | panic kill |
| required-call-6 | panic kill |
| required-call-7 | panic kill |
| required-call-8 | assertion kill |
| required-call-9 | assertion kill |
| required-call-10 | panic kill |
| required-call-11 | panic kill |
| required-checks | assertion kill |
| store-cardinality | assertion kill |
| store-empty-batch | panic kill |
| store-decision-cap | assertion kill |
| store-eligibility-cap | assertion kill |

The complete format specification, expanded independent vectors and full mutation
table cross the prior 2,000-line PR gate. This was reported before proceeding,
as required by the round's approximately 1,000-line reporting threshold. The
10a-2b-2 wrapper now has a 2,500-line ceiling; the shared historical verifier
retains its 2,000-line default. No Makefile, CI or Go test flag changed.

The first full race acceptance command exhausted Go's implicit ten-minute
command deadline during TestSourceStreamingGrowth. The completed mutation
and make-check stages above remain authoritative; that interrupted acceptance
is not a pass. The verifier's explicit `--phase acceptance` continues full
non-short race acceptance with a 30-minute command deadline, then stress,
CI-like timings and every regression comparison. This phase is reported in its
summary and does not claim to rerun mutations or make-check. Go implementation
and test files are unchanged from 2323235. No test flag source file, Makefile or
CI configuration changed. Stress selects every new/changed test explicitly;
unchanged TestAuditRealisticGrowth remains in full race acceptance, while the
new 32/128/512 decision-count probe runs all 20 stress iterations.

## Completed round-1 verification

The acceptance continuation finished **PASS** on 7457dda; Go and test sources
are byte-for-byte identical to implementation commit 2323235. Every command in
that continuation exited 0. The earlier completed 90-target mutation table and
make-check exit 0 compose with this result; the interrupted ten-minute acceptance
is explicitly excluded from passing evidence.

- Strict replay: actions, structural state, snapshot hashes and boundary
  checkpoints **2,159/2,159 identical**; 8,676 boundary probes; gates 2/4 PASS,
  zero non-allowlisted fields, self-test exit 0. GOMEMLIMIT=6GiB.
- Simulator: **146 identical blocks**, both exits 0.
- Contracts: **90/90 identical**. GitHub projections/CLI and Stage A exact output
  passed on both trees. MixedGithubBytes remains
  `cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee`.
- **211 fixtures outside internal/audit/testdata unchanged**; main help/dispatch
  still differs only by the additive audit entry.
- Full non-short race acceptance: audit 657.548 s, CLI 142.338 s, exit 0.
- Stress: **25 tests ×20 = 500 top-level passes**, every selected test exactly
  20 times, GOMAXPROCS=1, race enabled, with eight owned CPU burners. Audit
  4,879.103 s, CLI 7.514 s. All eight burners were terminated and reaped;
  minimum measured burner CPU ticks: **484,579**.
- Scratch cleanup: zero new schemas and zero new roles remain. Pre-existing
  schemas/roles were preserved. The one owned schema/reader role left by the
  interrupted acceptance was explicitly dropped before continuation.
- All started local verification processes finished; none remain running.

### Scaling across verification environments

Times below are seconds. Initial unloaded measurements above were non-race with
the default 16 available Go CPUs. Full race uses GOMAXPROCS=2. Stress medians use
GOMAXPROCS=1 and eight burners; 20 measurements per size. They are different
resource/instrumentation environments and should not be compared as speedups.
Selected counts are 112/400/1552 (=3×decisions+16), confirming the workload size.

| Decisions | Full-race export | Full-race replay | Stress median export | Stress median replay |
| --- | --- | --- | --- | --- |
| 32 | 2.982 | 3.790 | 4.461 | 5.544 |
| 128 | 12.374 | 15.783 | 18.293 | 23.196 |
| 512 | 54.340 | 67.600 | 81.554 | 102.122 |

### Fresh CI-like package timings

Both runs: GOMAXPROCS=2, pinned Go 1.27.3, heavy lock, GOFLAGS=-p=2,
GOMEMLIMIT=4GiB; command `go test -race -short -count=1 -p=1 -timeout=15m ./...`.
Both passed all 23 packages. Burners had already been reaped. These are single
local runs; descriptive package times, not statistical performance guarantees.

| Package | Main seconds | Candidate seconds |
| --- | --- | --- |
| cmd/openmerge | 68.794 | 69.055 |
| internal/ascii | 1.007 | 1.008 |
| internal/audit | 11.440 | 12.894 |
| internal/authority | 1.110 | 1.103 |
| internal/brief | 3.416 | 3.390 |
| internal/changepolicy | 1.761 | 1.718 |
| internal/classification | 2.251 | 2.226 |
| internal/config | 1.667 | 1.671 |
| internal/config/condition | 1.021 | 1.019 |
| internal/diagnostic | 1.019 | 1.017 |
| internal/executor | 1.166 | 1.163 |
| internal/forge | 8.334 | 8.379 |
| internal/forge/fake | 1.449 | 1.464 |
| internal/forge/github | 72.163 | 69.810 |
| internal/gitops | 103.858 | 103.752 |
| internal/queue | 180.972 | 185.741 |
| internal/serve | 336.566 | 337.566 |
| internal/sim | 37.555 | 37.901 |
| internal/sim/capacity | 1.010 | 1.009 |
| internal/status | 11.874 | 11.909 |
| internal/store | 75.210 | 75.652 |
| scripts | 1.084 | 1.084 |
| tools/dogfood | 1.669 | 1.676 |
| Sum of package times | 926.396 | 932.207 |

Wall times including lock/build overhead: 933.217 → 938.618 s.

### Artifact provenance

- Completed mutation/make-check evidence plus the explicitly interrupted first
  acceptance: `/home/riels/.cache/openmerge-149-round1-final`.
- Passing acceptance/stress/timing/regression continuation:
  `/home/riels/.cache/openmerge-149-round1-acceptance`.
- Combined assertions and summary/manifest checksums:
  `/home/riels/.cache/149-round1-combined-evidence.json`.

Mutation/make-check artifact manifest SHA-256: `47cd05f907ea6f131484afaf4ffcb6964d6502a050ae587962b35929834ff793`.

Acceptance artifact manifest SHA-256: `f9afafedcb7d92540166c78157c2001f2555ef043e00c0eb49b2ef4b6cfd6951`.

### Round-2 spec completeness and selector pinning

The normative section now specifies exact string escapes, owner-to-edge mapping,
owner field population, selector boundaries and dependency closure. Unbounded
library request bounds are `""`; the CLI always supplies both bounds. A start
references every retained decision event of its attempt, not a member count.
Operating guidance explains how a large timeout retains a repeatable-read
snapshot and holds back vacuum.

Ten new literal journals produce 50 new frozen files using the independent
Python Decimal resolver/encoder (80 files across 16 exports in total). Six are
Sonnet's unmodified journals: approval/retry range closure, retry ordering ties,
two-attempt settlement intervals and missing pin. Their 30 Python files match
Sonnet's independent Node semantic derivations byte-for-byte. Additional remedy,
multiple-decision and repeated-selector histories pin owner population and
oldest/latest/workflow distinctions. Node's repository verifier independently
checks exact-number encoding, hashes and canonical bytes for all 80 files.
The original 30 frozen files and fixtures outside audit testdata are unchanged.
No Go export output supplies expected bytes.

Round-2 focused overlay results (each compiled and ran TestCompleteExportV1):

| Mutant | Classification |
| --- | --- |
| start-selector-unbounded | assertion_kill |
| remedy-owner-head | assertion_kill |
| accountability-latest | assertion_kill |
| test-ref-latest | assertion_kill |
| rerun-latest-request | assertion_kill |
| landed-owner-attempt | assertion_kill |
| start-multi-decision-all | assertion_kill |
| rerun-request-ignores-workflow2 | assertion_kill |
| progress-ignores-workflow2 | assertion_kill |

Nine assertion kills, zero panic kills, equivalent/unreachable targets,
survivors, build/setup failures or infrastructure failures. The workflow
no-ops retain SQL placeholders so missing parameters cannot count as kills.
The accountability mutation initially survived a single-source fixture; the
new repeated-accountability history kills it by frozen-byte assertion.
The prior 90-target table remains historical round-1 evidence, not a claim
that all 90 targets were rerun in round 2.

Private command logs, overlays, timings and schema inventories are retained in
`/home/riels/.cache/openmerge-149-r2-fix/`. Only scratch localhost:54329 was used.

Round-2 corrected non-race scaling, same shared-batch fixture and full
Export/Replay pipeline, exact production migrations 006/007 loaded:

| Decisions | Selected | Export seconds | Replay seconds | Export ms/decision |
| --- | --- | --- | --- | --- |
| 64 | 208 | 0.791441 | 0.981180 | 12.366 |
| 256 | 784 | 3.351671 | 4.136564 | 13.092 |
| 512 | 1552 | 7.119419 | 8.350652 | 13.905 |
| 1536 | 4624 | 24.967566 | 29.540945 | 16.255 |

These are descriptive single runs, not calibrated production capacity. The
representative start EXPLAIN uses `events_reporting_settlement` in every case,
filtering 63/255/511/1535 other attempts within the shared batch. Decision
identity lookup also has the migration-006 expression index. This corrects
the previous attribution of all JSON predicates as unindexed. Residual
batch_started, landing_checks_recorded, test_ref_built and accountability
lookup scans still contribute O(E) per lookup; no projection to 5,000 owners
is retained. Scaling command wall time: 80.458 s.

Fresh `make check`: exit 0 in 483.874 s (lint: zero issues; short race suite
and command build pass). Fresh offline regression comparisons: strict replay
2,159/2,159 identical in all four categories (8,676 boundary probes, no
non-allowlisted fields); simulator 146 identical blocks; contracts 90/90;
GitHub projection/CLI and MixedGithubBytes hashes unchanged; Stage A exact
output passes. Strict replay used GOMEMLIMIT=6GiB; other Go commands used 4GiB,
GOFLAGS=-p=2 and the heavy-work flock, with pinned GOROOT and PATH.

The first stress attempt was deliberately interrupted after 160.269 s to
replace a three-hour harness deadline before a likely timeout. It is not a
passing stress result. Its eight owned burners were terminated and reaped.
The interrupted 512-decision fixture left schema
`om_audit_6147eb6ab0637f57c723677e` and its `_reader` role; its 512 decision,
check and start rows identified the active fixture. Both were explicitly
dropped. The four schema/role pairs present before any round-2 command were
preserved. The replacement foreground stress run uses a six-hour deadline.

Round-2 scope currently remains below the requested 800-line reporting
threshold. New frozen histories bring the whole PR beyond the historical
2,500-line wrapper ceiling; it now has a 3,000-line whole-PR ceiling and a
separate 800-line round-2 gate. No Makefile, CI, Dockerfile or test-flag source
was changed.

### Completed resumed round-2 stress and cleanup

The shared test Postgres was stopped by a session restart at 17:03, not by
this work. The interrupted run's five scaling/six vector iterations are not
counted toward the fresh resumed run below.

Fresh foreground stress: **exit 0 in 13,827.418 s (3h 50m 27s)**, pinned Go
1.27.3, GOMAXPROCS=1, `-race -count=20`, no `-short`, all four scaling sizes
and all 16 complete-export vectors. Both changed tests passed 20 times:
**20 scaling + 20 vector iterations**, comprising **80 scaling subcases and
320 vector subcases**, with no failures or skipped sizes. Go ran under the
heavy-work flock with GOFLAGS=-p=2 and GOMEMLIMIT=4GiB. Temporary/build files
were kept under the round's cache directory.

Eight owned CPU burners ran throughout; minimum consumed CPU ticks:
**1,375,919**. All eight were terminated and reaped, and their PIDs were
confirmed absent. No started verifier/test/burner remains running.

Descriptive medians across the 20 stressed iterations (seconds; race and CPU
contention included, separate from the non-race measurements above):

| Decisions | Export median | Replay median |
| --- | --- | --- |
| 64 | 7.651454 | 10.035592 |
| 256 | 30.387307 | 38.507764 |
| 512 | 59.261386 | 75.717031 |
| 1536 | 186.655899 | 228.616055 |

After success, the previously interrupted 1,536-decision fixture schema
`om_audit_942a17a36d4e5e1622d48720` and its `_reader` role were dropped.
Together with the earlier explicit cleanup of `om_audit_6147eb6ab0637f57c723677e`
and its reader role, **no owned schemas or roles remain**. Final inventory is
byte-identical to the pre-round inventory: the four pre-existing schema/role
pairs (21f983889ef7200481ebd1c7, 634e78a95ec623f6f6526f17,
8e05e92603fcf081b6af5d20, db974bd376ed4b39e4744212, all prefixed `om_audit_`)
were preserved. Test cleanup removed every schema created by the successful
run; no unknown schema or role was dropped.

Final reference check: all 80 Python/Node canonical files pass. All 243
existing fixture files (including the original 30 frozen outputs and 211
fixtures outside audit testdata) remain byte-identical to b6221f4; production
Go remains unchanged. Final round diff: **685 changed lines**; whole
PR: **2789 changed lines**. The round remains below 800 lines.

Passing stress log SHA-256:
`b1de47010ddfd362ef7c51171498d19a8e328249994605f3625390c394d46e89`.
Full measurements, burner PID/reaping proof and inventories are in
`resume-summary.json`, `burners.json`, `resume-burners-reaped.json`,
`cleanup-summary.json` and `db-final.txt` under the private round cache.
