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
The independent Python reference derives 15 complete files for full, empty and
typed-closure exports from documented journal-v1.json literals, with no Go output
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
measure before deployment. `(queue_id,type,id)` and batch-ID expression indexes
are post-trial candidates only. No migration or index is part of this PR.

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

**87 targets: 86 assertion kills, 1 equivalent, 0 unreachable, 0 unexpected
survivors, 0 build/setup failures, 0 infrastructure failures.** Only compiled
semantic assertion failures count as kills. The declared equivalent removes
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
