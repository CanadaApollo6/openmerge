# ADR 0008 PR 10a-1 evidence

Classification: **Inert by its own behavior; deploy after B**. Depends on Hold
PR 8 and transitively 3a–4. No deployment before period B drains. The package has
no daemon/command caller and adds no advertised `openmerge audit` CLI.

The earlier sections record historical pre-review and round-1 acceptance.
The round-2 section below records the current provisional v1 contract and results.

Base main: `7aa71a3f20b7c7b1b7538aaaa5caf1fdf77dff3d`. Tested implementation:
`4e652cc0f5a3e5052d1f6ff5d57d107d4a3c3c56`. The final evidence commit changes
documentation only;
runtime, tests, new golden fixtures and verifier remain identical to that SHA.

The normative v1 format and 10a-1/10a-2 boundary are in
[the approved design](adr-0008-10a-design.md#10a-1-implementation-contract).
10a-2 owns dependency closure, check/retry joins, historical-versus-missing
verdicts, CLI and real-git evidence acceptance. It can reuse the snapshot-local
producer/builder and existing role/reference fields. Existing v1 input bytes
stay fixed; a different closure selection creates its own chain.

## Pre-review acceptance (archived)

The committed verifier returned **PASS** on the tested implementation.

- `make check`: **0**, including lint (zero issues), short race suite and build.
- Full new-package race suite: **0**, **44.075 s**.
- Every one of the **11** new top-level tests passed **20/20** iterations with
  `GOMAXPROCS=1 -race -count=20`, including non-short growth/replay cases.
  Package elapsed time: **1173.527 s**.
- Eight owned CPU burners remained active throughout stress. An independent
  `/proc` observer confirmed all eight alive with positive CPU-time increments;
  the verifier terminated/reaped only those processes. Independent inspection
  confirmed their PIDs absent before timing runs.
- Strict replay at `GOMEMLIMIT=6GiB`: **2159/2159** identical
  actions, structural states, snapshot hashes and boundary checkpoints;
  **8676** probes, no non-allowlisted differences,
  gate 2/4 and mandatory self-tests pass.
- Simulator: **146/146** normalized
  blocks identical, SHA-256 `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Depth-1/4 contracts: **90/90**
  identical. GitHub status/API/health/author projections and actual CLI bytes
  match main. Both Stage A exact-output tests pass in both trees.
- MixedGithubBytes is unchanged:
  `cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee`.
- **211/211** existing
  fixtures remain byte-identical, including **105** wire
  fixtures and **90** registry fixtures.
  All changes are additions under the audit package plus the two requested
  documents and verifier; no existing production source or fixture changed.

## Source/read-only/publication proof

Tests use an isolated schema on shared test Postgres and a temporary NOLOGIN,
NOSUPERUSER role granted only schema USAGE and table SELECT. They assert no
INSERT/schema-CREATE privilege, `transaction_read_only=on` and
`transaction_isolation=repeatable read`. Query tracing asserts explicit BEGIN
and rejects migrations, writes, advisory locks, FOR UPDATE or LOCK TABLE.
Normal PostgreSQL read relation locks are not application locks.

A writer obtains a lower event ID before snapshot acquisition, while a higher
ID is committed before the snapshot. A query-end barrier then commits the lower
ID after snapshot acquisition. Exact replay reproduces all four artifact files
byte-for-byte; a fresh export includes the newly visible event. No sleep orders
this test. Retired queues absent from the live inventory, bytewise queue order,
unknown historical kinds, half-open microsecond and submicrosecond boundaries,
empty output and multiple queue/event pages are covered.

Strict replay rejects changed payload/metadata, missing sources, unknown
versions/fields, unsafe relative/absolute paths, symlinks, truncation, wrong count
and terminal digest. Golden fixtures independently fix full bytes and hashes
for a two-record chain and empty output, plus Unicode, HTML, large integers,
equivalent decimals, timestamps and zero with a large exponent. Numbers never
pass through float64. Unicode, duplicate-key, depth and expansion failures are
explicit and sanitized.

Publication tests exercise real Linux no-replace directory publication against
an empty target created during staging, checking that the winner's inode is
retained. They check 0700 staging under the output parent, failed-stage removal
and no valid manifest from a truncated stream. Stream files are 0600; flush,
sync and close succeed before the manifest is written last. The acceptance run
keeps export staging under its cache output parents;
no disk spool is used.

## Growth and memory

Non-short tests export and exactly replay 5,000 then 20,000 source events with a
1 KiB payload field. Queue/event/replay pages contain at most 32 records; files
stream to private staging. There is no repository-sized event slice or map.
Measurements sample Go heap at query boundaries and force GC for retained heap;
they are not a claim that total allocations or OS file cache stay constant.

```text
events=5000 wall=2.78615339s heap_before=454568 heap_after_GC=529616 total_alloc_delta=316456056 page_size=32 payload_limit=4194304 sampled_peak_heap=3686224
events=5000 replay_sampled_peak_heap=3651920
events=20000 wall=14.709701734s heap_before=531200 heap_after_GC=532824 total_alloc_delta=1263664568 page_size=32 payload_limit=4194304 sampled_peak_heap=3694320
events=20000 replay_sampled_peak_heap=3759888
```

Quadrupling events kept sampled export/replay heap below 4 MB; retained heap
changed by only a few KB. Assertions bound both peak and retained growth.
A separate whole-race-process observer sampled roughly 133–135 MiB resident
memory and a 164–165 MiB high-water mark during loaded stress; this includes
race-detector/harness and expansion-rejection work, not only the pipeline.

The actual scanner SQL on a sparse 32-event queue amongst 20,000 retained
source events used `events_queue_watermark`, returned 32 rows, touched three
shared buffers, and reported **0.025 ms** execution in this local plan sample.
A one-queue table may legitimately prefer its primary-key index. This is access
path evidence, not a throughput benchmark. Oversized DB payloads fail before
transfer into client pages and leave no published target or failed staging.

## CI-like package timing

Both trees ran `GOMAXPROCS=2 go test -race -short -count=1 -p=1
-timeout=15m ./...`. Values are Go-reported package elapsed seconds, excluding
compilation and shared-lock waiting. These are single local measurements on a
shared host, not causal performance estimates or daemon throughput. The new
short package excludes the costly growth case.

| Package | Main (s) | Implementation (s) | Delta (s) |
| --- | ---: | ---: | ---: |
| `cmd/openmerge` | 68.625 | 68.217 | -0.408 |
| `internal/ascii` | 1.005 | 1.006 | +0.001 |
| `internal/audit` | — | 1.338 | new |
| `internal/authority` | 1.110 | 1.118 | +0.008 |
| `internal/brief` | 3.423 | 3.412 | -0.011 |
| `internal/changepolicy` | 1.756 | 1.733 | -0.023 |
| `internal/classification` | 2.233 | 2.218 | -0.015 |
| `internal/config` | 1.664 | 1.642 | -0.022 |
| `internal/config/condition` | 1.023 | 1.018 | -0.005 |
| `internal/diagnostic` | 1.017 | 1.017 | +0.000 |
| `internal/executor` | 1.179 | 1.163 | -0.016 |
| `internal/forge` | 8.134 | 8.176 | +0.042 |
| `internal/forge/fake` | 1.469 | 1.440 | -0.029 |
| `internal/forge/github` | 71.410 | 69.968 | -1.442 |
| `internal/gitops` | 102.876 | 102.784 | -0.092 |
| `internal/queue` | 184.149 | 185.427 | +1.278 |
| `internal/serve` | 335.874 | 334.482 | -1.392 |
| `internal/sim` | 37.648 | 37.776 | +0.128 |
| `internal/sim/capacity` | 1.009 | 1.008 | -0.001 |
| `internal/status` | 11.797 | 11.614 | -0.183 |
| `internal/store` | 73.645 | 74.468 | +0.823 |
| `scripts` | 1.079 | 1.079 | +0.000 |
| `tools/dogfood` | 1.655 | 1.679 | +0.024 |

## Reproduce and inspect

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
python3 scripts/verify-adr0008-10a-1.py \
  --baseline /home/riels/.cache/openmerge-10a-1/main \
  --out /home/riels/.cache/openmerge-10a-1/acceptance-rerun
```

The baseline must be clean main `7aa71a3`. The verifier applies
`scripts/adr0008_scratch_db.py`'s DSN guard before any subprocess. It creates no
database and never initializes Postgres; tests create small isolated schemas.
Every Go command runs under `/home/riels/.cache/openmerge-heavy.lock` with
GOFLAGS=-p=2 and 4 GiB memory (6 GiB strict replay). Both GOROOT and PATH are
pinned to Go 1.27.3. Build/test temporary files and logs stay under the cache.

Full logs, overlays, replay report and independent observers are retained at
`/home/riels/.cache/openmerge-10a-1/acceptance/`.
Summary SHA-256: `1f6cb37045b913f24802adbdb3b8eae2638e2cd052e9375bcc899210bd2d3b15`.
Artifact manifest SHA-256: `229ad2caa2315167c8af5fef482bdc22c7adfb6bb38c1433f1d9cf6e739dd72b`.

Earlier development failures and the acceptance attempt stopped to fix exact
submicrosecond bounds are excluded. The final complete verifier ran from the
clean tested commit above. No migrations/indexes, Makefile/CI/test flags,
Dockerfile, queue event/wire/YAML/schema/forge behavior, shared GitHub formatter
or Stage A retry behavior changed. The live OpenMerge DB, systemd units, live
refs/status API and Smart-Data-Ohio were untouched. The implementation and
verifier made no real forge API calls. Publication is limited to the requested
branch and PR; no deployment, merge, queue or GitHub comment.

## Round-1 review fixes (supersedes pre-review contract)

Merged test-only main `59194fd` normally. Classification remains **Inert; deploy
after B**. The round-1 speculative shape was removed in round 2 (see the current
contract and results below). V1 remains provisional until 10a-2 merges.

The clean full verifier returned **PASS** on production revision
`45218fe1e8c6e798545b882d21a7930674dab792`, compared with clean main `59194fd`.
The final follow-up changes only test assertions, the verifier's completeness
mutation, and documentation; production Go files remain identical to that
revision. The strengthened timestamp-spelling and empty-queue watermark tests
were separately rerun 20/20 under the same race/GOMAXPROCS/burner conditions.

### Round-1 acceptance

- `make check`: **0**, lint **0 issues**, short race suite and build pass.
- Full audit race suite (including non-short cases): **0**, **70.438 s**.
- All **25** new or changed top-level tests passed **20/20** with
  `GOMAXPROCS=1 -race -count=20`; elapsed **692.802 s**. This includes queue-growth
  and large-payload non-short tests. The unchanged 5,000/20,000-event growth test
  ran in the full race suite.
- The two strengthened tests passed a further **20/20**, **3.973 s**, with eight
  freshly owned burners. Both runs verify all eight burners alive with positive
  CPU deltas and terminate/reap only their own processes. Original and
  supplemental burner PIDs are absent after their runs.
- All **15/15** final overlay mutations are killed by behavioral test failures,
  with no build failures counted as kills. The clean primary run killed 14;
  the supplemental run reran those 14 and added the empty-queue completeness
  mutation. The committed verifier reproduces all 15.
- Independent Python Decimal verification: **11/11** canonical vectors and both
  complete four-stream export fixtures reproduce exact bytes, hashes, chains,
  and manifest self-hashes. The supplement reran this check and lint (**0**).
- Strict replay at `GOMEMLIMIT=6GiB`: **2,159/2,159** identical actions,
  structural states, snapshot hashes and boundary checkpoints; **8,676** probes;
  no non-allowlisted differences; gates 2/4 and mandatory self-tests pass.
- Simulator **146/146** identical blocks; normalized SHA-256
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Contracts **90/90** identical. GitHub projections and CLI goldens are identical;
  Stage A exact-output and legacy line-zero diagnostics pass on both trees.
- MixedGithubBytes remains
  `cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee`.
- All **211/211** existing fixtures are byte-identical, including **105** wire
  fixtures and **90** registry fixtures. Only this package's unreleased v1
  goldens were finalized. No Makefile/CI/Dockerfile/project-test-flag, existing
  schema/index, queue event/wire/YAML/forge, shared formatter, or Stage A retry
  behavior changes were made.

### Per-item resolution and regression evidence

1. **CI DSN guard (P1):** test helper accepts localhost at any port, database path and options; Python verifier retains the stricter sandbox guard. `TestSourceDatabaseURLGuardAllowsLocalhostPorts` kills the fixed-port mutation. All four originally failing DB tests run with a `:5432` authority and `port=54329` sandbox route override.
2. **Queue discovery growth (P2):** one index-eligible ID/key-only query, then Go UTF-8 byte sorting; no repeated collation/page scan. `TestSourceQueueIdentityQueryOnce` kills restoration of the original rescanning implementation; `TestSourceQueueGrowthPlan` records EXPLAIN ANALYZE/BUFFERS at 32/512/2048/8192 matching queues.
3. **Forged watermarks (P2):** validates every ID/key mapping against snapshot inventory, unique bytewise order and frozen queue coverage for selected sources, retained max, positive watermark existence in its own queue, and each selected event's ID bound. `TestSourceReplayRejectsForgedWatermarks` rejects below-selected, unknown queue, swapped IDs, forged keys, duplicates, order and missing queues, The round-2 contract allows omitted unselected queues and tests this explicitly. `TestReplayRejectsWatermarkIDFromAnotherQueue` also rejects an event ID belonging only to a different queue. The validation-bypass mutant is killed.
4. **Pagination/collation proof (P2):** ICU-collated base/lane columns have a demonstrably different order from byte order; every fixture queue has events straddling event pages, and queue keys vary both components. `TestSourcePagingAcrossQueuesAndCollation` kills duplicate/skipped queue-boundary, lane-before-base, database-order and inclusive-event-keyset mutants. The old queue keyset code is removed; duplicate/skip mutations target equivalent inventory boundary mistakes.
5. **Superseded round-1 v1 proposal:** the dependency/absence/boundary model and its tests were removed in round 2. V1 is provisional until 10a-2 merges; 10a-2 defines and freezes that model. See the current design and round-2 results below.
6. **Line safety:** `TestCanonicalV1EscapesLineSeparators` and canonical goldens fix lowercase escapes for U+0085/U+2028/U+2029. The independent Python Decimal encoder checks all 11 vectors, exact four-stream files, chains and manifest self-hashes. Reproduce via `independent_goldens()` in the committed verifier (no Go serializer used to derive expected bytes).
7. **Canonical request timestamps:** resolve normalizes nonempty bounds to UTC with exactly six fractional digits and `Z`, after the microsecond ceiling. `TestSourceEquivalentMicrosecondBoundsHaveIdenticalManifest` proves identical bytes for equivalent effective bounds, UTC-offset spellings and extra fractional zeroes; `TestSourceSubmicrosecondBoundaries` also covers offset spellings. Equal normalized bounds represent an empty interval.
8. **Submicrosecond ceiling:** added `from=.000000001Z, until=.000002Z`, expected one event. `TestSourceSubmicrosecondBoundaries` kills truncation (two events).
9. **Input-file hash proof:** `TestSourceReplayRejectsEventCorruptionWithoutManifestRehash` changes events.jsonl alone, preserving manifest bytes; removing the input-file comparison is killed.
10. **Durability:** publication syncs files and staging, atomically renames without replacement, then syncs the parent before returning success. The design now states local crash durability and the visible-target possibility if parent sync fails; no backup/anchoring claim is added.
11. **Memory:** both export/replay retain at most 32 events and 4 MiB cumulative payload bytes per page, plus at most one bounded lookahead payload and per-record canonical copies. `TestSourceLargePayloadPagesBounded` streams/replays 33 payloads of 1,398,131 bytes, requiring at least 17 reads in both directions. Removing SQL and Go byte bounds is killed. `TestSourcePageRejectsOversizedBeforeTransfer` proves SQL returns a rejection marker instead of transferring an oversized payload; its transfer-guard mutation is killed. `TestSourcePageQueriesBoundCandidateBeforeSizing` also asserts the limited candidate precedes sizing and only one payload::text conversion.
12. **Nits:** unsupported rename EINVAL/ENOSYS/EOPNOTSUPP maps to a distinct sanitized error (`TestPublicationSanitizedUnsupportedFilesystem`); `sourceLess` delegates queue comparison to `keyLess`; materialized candidate projections compute payload::text once per row.

### Queue-count growth and payload budget

`TestSourceQueueGrowthPlan` measures the one identity-query access path, with
seven noise queues per matching queue. Full EXPLAIN (ANALYZE, BUFFERS) output is
in `foundation-full/stdout.log` of the primary artifact directory.

```text
matching / total queues   SQL execution    access path
32 / 256                 0.073 ms         one small-table sequential scan
512 / 4,096              0.071 ms         bitmap identity-index scan
2,048 / 16,384            0.236 ms         bitmap identity-index scan
8,192 / 65,536            0.970 ms         bitmap identity-index scan
```

Each plan has one scan loop; the larger plans have one identity-index search.
The query returns only matching ID/base/lane identities, then Go sorts by UTF-8
bytes. Discovery never repeats that query per page. The one-query regression
kills restoration of the reviewer's original repeated COLLATE scan. These are
single-run identity SQL execution times; indexed per-queue watermark lookups
and event serialization are separate work.

`TestSourceLargePayloadPagesBounded` exports and replays **33** payloads of
**1,398,131 bytes** each. A retained page can hold only **two** such payloads
within the **4,194,304-byte** budget, so both directions require at least **17**
reads. Removing the SQL and Go byte checks fails this test. A query may return
one bounded lookahead payload, which is discarded and reread on the next page;
canonical copies are limited per record. Oversized values are replaced with a
rejection marker before client transfer. The count cap remains 32.

The original streaming growth check remains bounded: 5,000 / 20,000 events have
post-GC heaps **537,008 / 540,832 bytes**, sampled export peaks
**3,761,008 / 3,779,856 bytes**, and sampled replay peaks
**3,696,320 / 3,710,912 bytes**. Export times were **2.752 / 15.050 s**.

### Main-versus-branch package timings

With pinned Go, the shared flock, `GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`,
`GOMAXPROCS=2`, no owned burners, and `go test -race -short -count=1`:

- `internal/audit`: absent on main; **2.442 s** on the branch.
- `internal/serve`: **336.945 s** on main; **335.749 s** on the branch.

The additional serialized all-package race-short passes both exit **0**:
`internal/serve` **336.438 / 333.721 s** (main/branch), and branch audit
**2.403 s**. Go-reported package elapsed times exclude compilation and waiting
for the flock. These are single-run measurements; no speedup is inferred.

### Reproduce and inspect round 1

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
python3 scripts/verify-adr0008-10a-1.py \
  --baseline /home/riels/.cache/openmerge-146-r1-main \
  --out /home/riels/.cache/openmerge-146-r1-rerun
```

The baseline must be clean main `59194fd`. The verifier retains the stricter
Python sandbox DSN guard before any subprocess, while Go test helpers accept
localhost ports and options like the store tests. Its CI-shaped probe uses a
5432 authority with the fixed 54329 sandbox route override and runs all four
originally failing source tests. It creates no database, initializes no server,
and uses only isolated schemas on shared test Postgres.

Primary artifacts:
`/home/riels/.cache/openmerge-146-r1-acceptance-final/`.
Supplementary test/mutation/lint proof:
`/home/riels/.cache/openmerge-146-r1-supplement/`.
Both contain summaries and content-hash manifests; the primary also contains
raw plans, package logs, strict replay, normalized simulator output and the
independent burner observer. The supplemental summary hashes the exact final
test and verifier files and confirms the production diff is empty.

Primary summary SHA-256: `274e4781f5f0178db7f3578067daec6f4a432e643a1b0e9d50c9daab347b0a46`.
Primary artifact manifest SHA-256: `f73a8bcc5769ab0d72a8171cda8b441e54c12a45cd60bbeb6382d47189acfbf6`.
Supplement summary SHA-256: `33d243486359962dfc36a859b4eb0b6c90356465fe8d1d5770964d2fe1ed08a1`.
Supplement artifact manifest SHA-256: `d0132869fb485c9c675f1c9ea7d33a780e5ce5c1159bf8a91d4078515106f33f`.

The live OpenMerge DB, systemd units, `openmerge/*` refs, live status API and
Smart-Data-Ohio were untouched. Tests and verification made no real GitHub API
calls. Publication is limited to the requested branch and PR body; no deployment,
PR comment, review request, merge or queue action.

## Round-2 fixes (current contract; supersedes round-1 shape and inventory rules)

Classification remains **Inert; deploy after B**. V1 is provisional until
10a-2 merges; 10a-2 defines and freezes the dependency/absence model. The
speculative dependency/reference fields and boundary stream were removed.
Scope exports retain only `scope` role and non-null empty references. The
three source streams and manifest remain deterministic; only this package's
unreleased manifest goldens changed, and the two empty boundary fixtures were
removed. Other audit golden bytes and every pre-existing external fixture remain
unchanged.

### Resolution by review item

- **P2-1 frozen inventory:** `TestSourceReplayAfterNewQueue/empty` and `/events`
  prove untouched exact replay after a new lane is created, with and without
  events. `TestSourceReplayAllowsUnselectedQueueOmission` permits omission of an
  event-free queue. The `missing` case of
  `TestSourceReplayRejectsForgedWatermarks` now removes a selected queue's
  watermark. Unknown IDs, key mismatches, duplicates, ordering, and selected-ID
  bounds remain rejected; `TestReplayRejectsWatermarkIDFromAnotherQueue` remains.
- **P2-2 provisional model:** removed the speculative dependency/absence/boundary
  records, boundary stream, populated reference goldens and extension replay
  test. `TestExportV1Golden` fixes the remaining scope-only output.
  `TestSourceReplayRejectsScopeShapeAndCrossQueueOrder` rejects bad/dependency
  roles and null/nonempty references using internally coherent rehashed exports.
  The design lists absent-evidence identity without invented IDs, required-but-
  missing failure, owning selection/decision, all seven evidence kinds/expected
  types, governing check attempt/excluded later retries, and DB status validation
  as requirements for 10a-2.
- **P3 semantic gaps:** `TestSourceReplayRejectsForgedWatermarks/event-free-key`
  isolates a forged key on an event-free queue.
  `TestSourceReplayRejectsScopeShapeAndCrossQueueOrder/cross-queue-order` reverses
  selected queues and rebuilds every metadata/file/chain/manifest hash. The
  remaining shape cases exercise role and reference validation directly.
- **P3 inventory cost:** one set SQL statement retrieves IDs, keys and watermarks
  using a lateral indexed top-one lookup. It avoids per-queue Go SQL round trips
  and full history aggregation. `TestSourceQueueIdentityQueryOnce` checks query
  count and rejects the old max-ID loop; `TestSourceQueueGrowthPlan` now includes
  populated events and verifies both existing indexes at the largest size.
  No new index or migration. Queue identities still use uncapped
  O(total queue-key bytes) memory; replay holds current and frozen maps. Expected
  scale is thousands of queues with ordinary short names; 8,192 matching queues
  are measured. Large keys/inventories require separate memory budgeting or a
  future spool design, rather than a constant-memory claim.
- **Publication nit:** the design gives the operator remedy after parent fsync
  failure: preserve the visible target, verify it through exact replay into a
  fresh path, investigate storage failure and remove only the failed export
  target before retrying; never overwrite blindly.

### Mutation accounting

All **17/17** applicable mutations are killed by behavioral test failures, with
no build failures counted. These include all **12** still-applicable round-1
mutations and **5** new mutations (event-free forged key, cross-queue selection
order, invalid role, invalid references, restored current-inventory completeness).
The old completeness-removal mutant is retired because omission of unselected
queues is now correct. The old dependency-role and historical-absence rejection
mutants are retired with the speculative shapes, rather than counted as killed.
A development ordering mutant initially produced an unused-variable compile
error; the verifier was corrected to preserve the variable reference, and the
final run proves the behavioral failure specifically in `/cross-queue-order`.

### Round-2 acceptance

Tested candidate: `980eaf4a9d45eda178b11c6c53e101c62ae3cc8a`; baseline main: `59194fd46c06526d6dbd7cd4a8d8e9b350e51410`.
The subsequent evidence commit changes documentation only; production and test
Go files and the verifier retain their tested bytes.

- `make-check`: exit **0**, wall including lock **541.102 s**; package seconds: `github.com/CanadaApollo6/openmerge/internal/audit` **2.770 s**, `github.com/CanadaApollo6/openmerge/internal/serve` **356.784 s**.
- `foundation-full`: exit **0**, wall including lock **71.244 s**; package seconds: `github.com/CanadaApollo6/openmerge/internal/audit` **70.482 s**.
- `foundation-stress`: exit **0**, wall including lock **698.104 s**; package seconds: `github.com/CanadaApollo6/openmerge/internal/audit` **697.636 s**.
- `ci-packages-before`: exit **0**, wall including lock **340.397 s**; package seconds: `github.com/CanadaApollo6/openmerge/internal/serve` **339.206 s**.
- `timing-before`: exit **0**, wall including lock **921.775 s**; package seconds: `github.com/CanadaApollo6/openmerge/internal/serve` **335.694 s**.
- `ci-packages-after`: exit **0**, wall including lock **334.058 s**; package seconds: `github.com/CanadaApollo6/openmerge/internal/audit` **2.655 s**, `github.com/CanadaApollo6/openmerge/internal/serve` **333.349 s**.
- `timing-after`: exit **0**, wall including lock **921.196 s**; package seconds: `github.com/CanadaApollo6/openmerge/internal/audit` **2.582 s**, `github.com/CanadaApollo6/openmerge/internal/serve` **335.403 s**.

- **25** selected top-level tests passed **20/20**, including all new/changed
  tests and non-short queue-growth/large-payload cases, at `GOMAXPROCS=1 -race`.
  An independent observer confirmed eight owned burners accumulated positive
  CPU deltas; all eight were terminated/reaped and their PIDs are absent.
- Independent canonical comparison: **0 mismatches / 6,128 cases**. The supplied
  reviewer corpus contains 6,118 entries; the scratch rerun adds ten explicit
  zero-exponent, Unicode, duplicate-key, decimal and surrogate edge cases.
- Independent Python Decimal goldens: **11/11** vectors and both complete
  scope-only exports reproduce bytes, file hashes, chains and manifest self-hashes.
- Strict replay (`GOMEMLIMIT=6GiB`): **2,159/2,159** identical actions, structural
  states, snapshot hashes and boundary checkpoints, **8,676** probes, zero
  non-allowlisted differences; gates 2/4 and self-tests pass.
- Simulator **146/146** identical; contracts **90/90** identical; GitHub
  projections and CLI goldens identical; Stage A exact-output and legacy
  line-zero diagnostics pass on both trees. MixedGithubBytes remains
  `cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee`.
- All **211/211** existing external fixtures remain byte-identical. No fixture
  outside `internal/audit/testdata` changed.

Populated inventory growth (one event per queue, seven noise queues per matching
queue), from the full race run:

| Matching / total queues | Combined identity + watermark SQL execution |
| --- | ---: |
| 32 / 256 | 0.345 ms |
| 512 / 4,096 | 0.467 ms |
| 2,048 / 16,384 | 1.588 ms |
| 8,192 / 65,536 | 7.004 ms |

The largest plan uses `queues_installation_repository_base_lane_key` and backward
index-only `events_queue_watermark` lookups. These local single-run SQL samples
include watermark work, excluding client sorting/serialization. No speedup is
inferred. The full growth test records export/replay sampled heaps below 4 MB at
5,000 and 20,000 events; queue-key inventory remains a separate uncapped cost.

Reproduce the complete verifier with pinned GOROOT/PATH and the permitted test
DSN as above, using baseline `/home/riels/.cache/openmerge-146-r1-main` and a fresh
`--out` directory. The verifier enforces the shared flock, memory limits,
isolated test schemas and clean candidate/baseline checks.

Primary artifacts: `/home/riels/.cache/openmerge-146-r2-acceptance-final/`.
Canonical differential: `/home/riels/.cache/openmerge-146-r2-differential/`.
Burner observer: `/home/riels/.cache/openmerge-146-r2-burners.json`.

Primary summary.json SHA-256: `a8d5f592b7bbfb06688dd45d0e02c04c45c264ce9fe5e1b41530afd37f823f99`.

Primary manifest.json SHA-256: `8c0918eb8da00801bc868269856658c49d9f243015a25b3605da94118278a454`.

The live DB, systemd, `openmerge/*` refs and live status API were untouched.
Verification used no real forge API calls. No deployment, comment, merge,
review request or queue action; only the requested branch push and PR body edit.
