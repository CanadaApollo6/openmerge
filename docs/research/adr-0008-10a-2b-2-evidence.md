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

Final foreground verifier results and artifact hashes will be recorded here
once the committed implementation completes all requested gates.
