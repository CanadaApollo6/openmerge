# ADR 0008 PR 10a-1 evidence

Classification: **Inert by its own behavior; deploy after B**. Depends on Hold
PR 8 and transitively 3a–4. No deployment before period B drains. The package has
no daemon/command caller and adds no advertised `openmerge audit` CLI.

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

## Acceptance

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
