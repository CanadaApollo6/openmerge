# ADR 0008 PR 10a-2a evidence

Classification: **Hold; shared production store helper changes; deploy only after period B**.
This layer depends on Hold PRs. It has no operator CLI, migration, schema,
index, event/wire/YAML change or forge read. The base is main
`25d309d80e7a8b2944d642bd17c2277c127fe274`.

## Contract and limits

The approved size-gate split is recorded in
[the size-gate note](adr-0008-10a-2-size-gate.md). This layer completes internal
evidence semantics. 10a-2b owns the operator command, real-git command acceptance
and independently derived final complete-export goldens. **v1 remains
PROVISIONAL**. Existing provisional complete-export fixtures are not regenerated
or represented as a final frozen contract.

An evidence absence is a statement about an explicit owner and expected kind /
type identity. It does not invent a source event ID. Required retained evidence
that is missing is an integrity failure. Replay checks both recorded and
not_recorded claims against retained database history, including recorded claims
for nonexistent sources and absence contradicted by a retained matching source.
Claims are evaluated within the saved per-queue watermark; later appended
history does not alter exact replay.

Export verifies closure within its repeatable-read, read-only snapshot by
exhausting a typed resolver worklist and checking references against selected
sources. Source order is the UTF-8 queue tuple (installation, repository, base, lane),
then queue ID and event ID. Queue ID distinguishes malformed duplicate
queue keys and does not alter order for a valid unique queue inventory; identical source identities deduplicate and contradictory copies
fail. Reference order is bytewise canonical JSON order
of the complete typed reference, including owner and source identity;
equal encodings deduplicate. References may point forward in source order.
Spool files belong to the private staging directory under the output
parent. Sorting uses at most 32 records per chunk and stops after MaxBytes
(plus the final bounded record), with fixed 16-way merge fan-in. Frozen per-queue maximum IDs travel with spool records;
resolving each record does not query a moving maximum. Queue inventory also
uses disk sorting, and replay does not build repository-sized queue maps. Replay verifies retained sources and relationships; it does not prove
that a malicious author retained every original queue/event. A fully rehashed
export with dropped events or queues can replay. The external anchor in 10b
detects substitution relative to a previously anchored manifest.

Check evidence is historical: admitted checks are joined to the decision's
batch and tested SHA, then to producer/run retry history. Applicable observations
before the decision boundary sort by run ID, normalized attempt and observation
ID. Generation and dispatch/observation provenance are retained. The first later
observation records an explicit exclusion boundary; subsequent retries remain
outside the governing window. No current forge read repairs missing history.

LandingDecisionEvidence is reused through transaction-local helpers so the
audit snapshot does not open a second transaction. Existing callers retain
their transaction and validation behavior. Their preexisting tests remain
byte-identical and are run unchanged.

## Reproduction

Use the existing shared test PostgreSQL, with isolated test schemas; do not
create a large scratch database. The verifier validates the DSN with
`scripts/adr0008_scratch_db.py` before any subprocess. Its allowlist accepts the
explicit test TCP endpoint `localhost:54329` by default. Port `localhost:5432`
is accepted only when both `GITHUB_ACTIONS=true` and `CI=true`; it
rebuilds the destination URL and refuses libpq routing overrides. The local
run uses only port 54329. This compatibility adjustment to the shared guard and
its connection-free tests is required because the CI Postgres service publishes
5432 while the workstation shared test service publishes 54329. Without explicit
CI detection the ordinary workstation Postgres could be targeted. PG environment
and query routing overrides remain forbidden in both modes.

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
python3 scripts/verify-adr0008-10a-2a.py \
  --baseline /home/riels/.cache/openmerge-10a-2a/main \
  --out /home/riels/.cache/openmerge-10a-2a/acceptance-r3 \
  --baseline-timing-log /home/riels/.cache/openmerge-10a-2a/timing-before/stdout.log
```

The baseline is a detached checkout of the exact base above, never a checkout
change in the main workspace. Both trees must be clean. Every Go invocation,
including Go invoked by make or the replay driver, runs inside
`flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2
GOMEMLIMIT=4GiB`; strict replay uses the requested `GOMEMLIMIT=6GiB` instead.
GOROOT and PATH select the pinned 1.27.3 toolchain. Build/test temporary paths
are under the evidence cache, never `/tmp`.

The verifier writes per-command stdout, stderr and exact command logs, command
exit codes, package and wall timings, a structured summary and a SHA-256
artifact manifest. Error output is sanitized. It never calls a real GitHub API,
live status API or service controller. It compares the fixed offline journal
export rather than querying a live OpenMerge database.

## Initial acceptance results (superseded by PR 147 round 1)

Measured against the exact base above, on code commit
`4a33f8a2c7dfe925ed58e553315eea02a45404be`. The final evidence commit
changes only documentation and the verifier's default test selection; every Go
source and test remains byte-identical to the fully verified commit.

- `make check`: **exit 0**, including formatting, lint, short race suite and build.
- Full non-short audit race suite: **exit 0**, 209.371 seconds.
- Strict replay at 6 GiB: **2,159/2,159 identical** actions, structural states,
  snapshot hashes and boundary checkpoints; 8,676 boundary probes, no new
  non-allowlisted fields, Gate 2 / Gate 4 PASS, self-test exit 0.
- Simulator: **146/146 identical** normalized blocks. Contracts: **90/90 identical**.
- GitHub projection and actual CLI bytes, MixedGithubBytes and Stage A exact
  output checks all pass. All **211 fixtures outside audit testdata** are
  byte-identical. No complete-export golden was regenerated.
- Existing `internal/store/landing_wiring_test.go` is byte-identical. Its original
  LandingDecisionEvidence tests and all existing store callers' tests pass unchanged.
- All seven evidence families, cross-range/shared/forward sources, unknown history,
  missing required evidence, invalid roles `""` / `"Scope"`, DB-status contradictions,
  multi-queue watermark pinning, retry result reversals, producer/run/attempt
  identity mismatches, generation/dispatch provenance and legacy attempt normalization pass.
- All **six focused mutants are killed** by semantic tests, with build/setup
  failures rejected as mutation evidence.
- Main stress: **16 tests × 20 = 320 top-level passes**, `GOMAXPROCS=1 -race`,
  including non-short growth, in 2,515.800 seconds. Eight owned burners each
  consumed positive CPU time (minimum 249,922 ticks); all were reaped.
- The renamed provisional-export semantic test also passed **20 times** under
  identical flags with eight fresh owned burners (minimum 188 ticks); all reaped.
  This gives **17 tests / 340 top-level passes** across the two runs. The verifier
  default now includes that test in the integrated stress selection.
- The shared DSN guard's three connection-free tests pass; both explicit CI
  `localhost:5432` and local `localhost:54329` endpoints are accepted.

Complete export/closure/replay growth (`-race`, non-short, GOMAXPROCS=2):

| Owners | Unique sources | Export + replay (s) | Sampled peak heap (bytes) | Retained heap after GC (bytes) |
| ---: | ---: | ---: | ---: | ---: |
| 128 | 258 | 9.306 | 6,911,304 | 798,592 |
| 1,024 | 2,050 | 77.540 | 8,238,264 | 793,728 |

The eightfold input includes real shared out-of-range batch/build dependencies
and opaque historical payloads. Query-boundary heap sampling supplements the
structural 32-record / fixed-fan-in bound; it is not an instantaneous heap profiler.
Across all 20 stress runs, the large case's sampled peak was 10,144,304–12,111,064
bytes. The existing 20,000-event streaming probe also passes (export 39.225 s,
export/replay sampled peaks 3,759,744 / 3,715,584 bytes).

Artifacts are under `/home/riels/.cache/openmerge-10a-2a/acceptance-r3`:
`summary.json`, per-command logs, `replay.json`, normalized simulator output and
`manifest.json`. The artifact manifest SHA-256 is
`87acb369ec632508a6b2fcb63b011c365703cc8062d9924acbe67b464ff6d831`.
Supplementary stress artifacts are under the sibling `supplemental-stress`
directory; its manifest SHA-256 is
`e1feff9563c61d71d6feb6e759207baa320343a42f2f7a0d74cfe55abe36f202`.
Neither run used a live OpenMerge DB, real forge API, or `/tmp` for spools.

The verifier's focused overlay mutation table requires each mutant to compile
and fail a semantic assertion; a surviving mutant or a build/setup failure
fails acceptance. The checkout itself is never edited by these experiments.

| Mutation | Independent semantic expectation |
| --- | --- |
| Reuse spool decode storage | Heterogeneous immutable source payloads survive sorting and replay. |
| Permit a missing required retained source | Export fails for the explicitly required owner/source identity. |
| Choose oldest instead of newest applicable observation | The newest run ID / normalized attempt at the decision boundary governs. |
| Ignore selected check result mismatch | A result from a different attempt cannot satisfy the decision. |
| Accept arbitrary role strings (remove enum and range/role guards together) | Empty and `Scope` roles fail strict replay. |
| Skip retained closure verification | Rehashed omission of a referenced dependency still fails replay. |

## Paired CI-like package timings

Both exact checkouts completed `GOMAXPROCS=2 go test -race -short -count=1 -p=1`
with exit 0 and 23 tested packages, under the pinned toolchain and shared lock.
Baseline logs/provenance are in `timing-before`; candidate logs are in
`acceptance-r3/timing-after`. Go-reported package seconds exclude compilation
and lock waiting. This single local pair is descriptive, not a causal throughput
benchmark. Audit short cost increased by 2.443 seconds; expensive growth probes
remain outside `-short`.

| Package | Main (s) | 10a-2a (s) | Delta (s) |
| --- | ---: | ---: | ---: |
| `cmd/openmerge` | 68.443 | 69.950 | +1.507 |
| `internal/ascii` | 1.007 | 1.006 | -0.001 |
| `internal/audit` | 2.599 | 5.042 | +2.443 |
| `internal/authority` | 1.111 | 1.102 | -0.009 |
| `internal/brief` | 3.400 | 3.490 | +0.090 |
| `internal/changepolicy` | 1.750 | 1.746 | -0.004 |
| `internal/classification` | 2.265 | 2.274 | +0.009 |
| `internal/config` | 1.652 | 1.617 | -0.035 |
| `internal/config/condition` | 1.020 | 1.020 | +0.000 |
| `internal/diagnostic` | 1.019 | 1.019 | +0.000 |
| `internal/executor` | 1.168 | 1.172 | +0.004 |
| `internal/forge` | 8.306 | 8.232 | -0.074 |
| `internal/forge/fake` | 1.460 | 1.503 | +0.043 |
| `internal/forge/github` | 65.667 | 72.185 | +6.518 |
| `internal/gitops` | 103.400 | 103.247 | -0.153 |
| `internal/queue` | 179.622 | 184.099 | +4.477 |
| `internal/serve` | 337.080 | 334.315 | -2.765 |
| `internal/sim` | 37.558 | 37.468 | -0.090 |
| `internal/sim/capacity` | 1.009 | 1.008 | -0.001 |
| `internal/status` | 11.853 | 11.905 | +0.052 |
| `internal/store` | 74.253 | 74.603 | +0.350 |
| `scripts` | 1.081 | 1.079 | -0.002 |
| `tools/dogfood` | 1.689 | 1.703 | +0.014 |

## PR 147 round 1

Both reviewers requested changes on `8f53303`. Round-1 fixes are commits
`6eb8b29..HEAD`; the acceptance below is measured on the final code commit.

### Requiredness at each decision boundary (P1)

The governing mode is the latest `policy_authority_applied` with an event ID
strictly below the owning decision, which is the store's own
`latestAuthorityPin` at write time: pins are only written validated and paired
with a matching `config_applied`. Only an `openmerge` pin makes the authority
pin and `landing_checks_recorded` required. With no pin, or a `github`
rollback pin, both are explicit absences that carry the owner and expected kind,
with no source ID, and export never fails on them. A retained check row under a
GitHub decision is not attributed to it. The other immutable approval evidence
remains required. An openmerge pin must also match the classification's
governing commit. `TestEvidenceAuthorityRequiredAtDecisionBoundary` covers the
dogfood shape (GitHub mode, Stage A approval history, no authority), a retained
inapplicable check, a range spanning activation, rollback to GitHub followed by
decisions, and a required-but-missing check under openmerge. Each replays
exactly. The always-required and never-required mutants are both killed.

### Replay: exact closure is the single semantic owner (P2-2, P3-5)

Replay rebuilds export's own closure from the selected scope roots, inside
replay's snapshot and at the saved watermarks, then compares complete
selections in source order. This one comparison owns resolved references,
contradicted absences (including a contradicted in-range scope record), missing
retained sources and any record outside the closure, whether a single
unreferenced dependency or a self-contained cycle. The earlier per-record
re-resolution, reference comparison, `validResolution` call and retained-source
merge were strictly subsumed. Their mutants were equivalent, and they doubled
replay's database work, so they were removed. Range, not closure, owns the scope
label: an out-of-range record relabelled as scope is self-consistent under
closure, so the range/role check stays.

The test forger `rewriteSelections` previously rewrote only `selection.jsonl`
and the manifest. Because `events.jsonl` chains each record's role and
references, every forged artifact failed stream integrity before any semantic
guard, so the role cases and the rewritten-absence case passed without
exercising Replay. It now rebuilds the chained events stream, boundaries,
digests, count and terminal hash without `build`'s validation. An
unchanged-selection control must replay.

### Identity, encoding and check attempts (P2-4, P3-6, nits)

Every reference identity is a non-empty JSON string tuple, encoded without HTML
escaping. `validReferences` rejects any other form, so arity and component
boundaries are structural: `["b"]`, `["b","a"]` and `["b/a"]` are distinct, and
bare strings are invalid. References are encoded once while normalizing. An
identity that exceeds the canonical bound (reachable from a ~4 MiB payload of
escaped characters) is a sanitized `errCanonical` rather than a panic.

Check observations are ordered identically in SQL and Go. Within an event, the
greatest run ID wins, then the greatest normalized attempt. Across events, the
greater per-event tuple wins, then the greater event ID. Malformed events
holding several runs of one check (which the codec rejects) are ordered by their
own newest run: when such an event governs the boundary it is selected and
rejected, never silently replaced by an older valid event. Dispatch progress and
results follow the selected request generation, even when a newer retry of
another check in the same batch dispatches later. Member decisions sharing an
attempt each resolve against their own retained item.

### Shared store helper (P3-8)

`LandingDecisionEvidenceTx` takes an explicit `*int64` boundary. `nil` is the
original unbounded path; a pointer to zero selects no rows. Bounded and
unbounded SQL are separate constants, and the unbounded constants are pinned
verbatim. Public invalid-argument rejection still happens before a transaction.
Tests cover zero/exact boundaries, eligibility cardinality 0 and 2, empty batch
and attempt, and the 4 MiB cap at and above the limit for both the decision
and the eligibility read (the unbounded path stays uncapped). Existing store
tests are byte-identical and pass.

### Scaling bound (P3-7)

No migration or index is added. Each retained lookup is a scan over the queue's
`(queue_id, id)` watermark range with JSON filters. The plan below (8,000
unrelated rows, the exact check-observation predicate) removes 8,034 rows by
filter for one result:

```text
Limit (actual rows=1)
  InitPlan 3 -> Limit -> Sort (Sort Key: (SubPlan 1) DESC, e.id DESC)
    -> Seq Scan on events e  Filter: id<8100, queue_id, type, batch_id, tested_sha, EXISTS(run)
       Rows Removed by Filter: 8034
  -> Index Scan using events_queue_watermark (queue_id, id <= 8100, id = InitPlan 3)
```

Work is therefore O(O × L × E): O owners, L lookups per owner (2 + 5 per
admitted check for a check record, about 15 for an approval decision), and E
retained events in the queue. Bounded spools bound memory, not database work.
The partial indexes from migrations 006/007 could serve only the decision,
eligibility and settlement families, so they would not change the bound. A
single-pass per-queue resolver would need a bounded identity spool join
covering every predicate and attempt segment, and it is not attempted here.
Use bounded ranges and queues with hundreds of decision owners; thousands of
owners over large unindexed histories are an offline batch workload.

Measured without `-race` (single runs, descriptive; Sol's snapshot `dd5e52b` vs final):

| Owners | Retained events | Lookups export / replay | Export (s) | Replay before (s) | Replay final (s) |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 64 | 67 | 768 / 768 (was 1,536) | 0.207 | 0.484 | 0.234 |
| 64 | 16,451 | 768 / 768 (was 1,536) | 1.019 | 2.213 | 1.079 |
| 256 | 259 | 3,072 / 3,072 (was 6,144) | 0.820 | 2.033 | 0.891 |
| 256 | 16,643 | 3,072 / 3,072 (was 6,144) | 3.904 | 8.416 | 4.129 |

Lookups are exactly 12 per check-record owner. Export time grows linearly with
owners at fixed history (×4 owners → ×3.8) and with retained events at fixed
owners. The complete pipeline probe with real check history (128 → 1,024
owners) takes 1.689 → 14.816 s for export plus replay (22.0 s before the replay
change). Sampled peak heap is 8.47 → 8.66 MB and retained heap 1.11 → 1.15 MB.
`TestEvidenceCheckHistoryGrowthPlan` (32 owners, 0/2,000/8,000 unrelated rows)
measures 0.357/0.448/0.660 s. `TestScalingProbeOwnerEvent` reproduces the table.

### Mutation table

Every mutant from both reviewers (Astra's six; Sonnet's m1–m3), the verifier's
own and this round's are run against the final commit with overlays over the
complete `-short` audit (and, for the helper, store) suites. Build or setup
failures do not count as kills. The reviewers' mutants of guards that this
round removed are mapped onto exact closure, which now owns those properties.
PENDING_MUTATION_TABLE

### Round-1 acceptance

PENDING_ACCEPTANCE
