# Stage A 9b: landing-decision follow-ups

Trial class: **Hold**. Merge only; do not deploy until ADR 0009 period B drains.
Base: origin/main `15aa69929e89970fb49381493398814b8793567a` (#75).
Worktree: `/home/riels/openmerge-sa-9b`, branch
`stage-a/9b-decision-followups`. Baseline checkout:
`/home/riels/openmerge-sa-9b-main` at that exact commit.

No live DB, status API, systemd unit or live `openmerge/*` ref was accessed.
Tests use the fake forge and isolated schemas in the supplied shared test DB.
Repository fetch used only explicit main and feature-branch refspecs.

## Obligations and coverage

- S1: `TestLandingDecisionRetentionAndReplay/{landed,failed,cancelled,superseded}`
  retains decisions through landing, removes them at settlement, preserves prior
  State values, round-trips live snapshots, and compares encoded-event replay
  with incremental state. `TestLandingDecisionOldSnapshotPrunesHistory` reads
  the old snapshot format while dropping historical projection entries.
  `TestLandingDecisionJournalReadbackAfterSettlement` recovers settled evidence
  and its exact EligibilityReadID through bounded `Store.LandingDecisionEvidence`, despite a newer
  eligibility read in State. Existing `TestLandingDecisionStaleBasisAndLegacyAbsence`
  and the legacy replay gates pin the absent legacy map/snapshot bytes.
  Store settlement/crash tests now assert removal from State, while preserving
  the journal's immutable decision.
- S2: `TestAtomicLandingRejectsWholeMemberSet/legacy-bypass` requires the typed
  `LandingDecisionRequiredError`, explicitly excluding ErrStale.
  `TestLandingGatePolicyMiswiring/{store-only,executor-only}` exercises executor
  and store policy mismatches, with no start/evidence insert or push.
  `TestLandingDecisionErrorsFailWorkerAction` injects both that typed error and
  ErrLandingApprovals at the executor boundary and proves the real worker's
  Failed completion leaves the action retryable.
- S3: `TestParseChangeTrailersAuthoritativeBlock` rejects reserved forged body
  text, duplicate pairs, non-final blocks, casing/whitespace variants, missing
  metadata and the fast-forward marker. `TestChangeTrailersBeforeCI` now uses
  the cache-only immutable object reader and shared parser against the real PR 10 writer
  for squash, merge, rebase and fast-forward. The gate's
  `TestAtomicDecisionExecutorCrashWindows/trailers-mismatch` rejects unequal
  labels. Real writer behavior remains unchanged; metadata-enabled fake commits
  now have its blank separator. Legacy fake messages remain unchanged.
- S4: [landing-decisions.md](../../internal/queue/landing-decisions.md) documents
  generic worker failure, the named PR 11a exit
  `approval-rejection-worker-replan`, latest-only eligibility versus journal
  recovery, pre-read InputFingerprint versus post-read wait fingerprint, and
  ADR 0008's separate per-check event keyed by batch/attempt/change.
- Astra legacy path: `TestLegacyExecutorEmitSQL` runs the production executor
  through `Store.Emit`, with no decision gate, and traces every SQL statement
  including BEGIN/COMMIT and lock/claim order. The identical harness is run in
  both checkouts by `scripts/check-legacy-landing-sql.py`; it does not substitute
  a nil-policy StartLanding call for the real legacy path.

## Host load and environment

Every heavy command starts only after no other agent's `go test`, `*.test` or
replay process is running. Jobs run sequentially, using:

```sh
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
export GOTMPDIR=/home/riels/.cache/sa9b-tmp
export GOCACHE=/home/riels/.cache/go-build
export GOFLAGS=-p=2
export GOMEMLIMIT=4GiB # 6GiB for replay
```

The load-gated runner is `/home/riels/.cache/sa9b-run.py`. It redirects both
streams to each named log, records the actual subprocess return code and prints
`exit=<code>`. It waits for other jobs; it never kills them. No initdb is used.

## Commands and exit statuses

This section records the original implementation run. The Round 1 section below
records the current fixes and their complete gate rerun.

Early focused attempts (logs `/home/riels/.cache/sa9b-focused-{1,2,3}.log`):

1. `go test -count=1 ./internal/queue ./internal/forge ./internal/gitops ./internal/store ./internal/executor`
   — **exit 1**: test adaptation compile errors (unused/missing local variables)
   and incremental/replay nil-versus-empty fixture normalization.
2. `go test -count=1 ./internal/queue ./internal/forge ./internal/gitops ./internal/store ./internal/executor -run 'LandingDecision|Atomic.*Landing|AtomicDecision|LegacyExecutorEmitSQL|ChangeTrailers|ParseChange'`
   — **exit 1**: fixture normalization and fake messages lacking the writer's
   blank separator before trailers.
3. Same focused command — **exit 1**: fake message issue resolved; only fixture
   nil/empty normalization remained. Incremental test input now uses decoded
   journal events, matching production's journal boundary.

Final implementation tested: `1eb749bf2dc8d20b065e256a97ece0e362d23595`.
The final evidence commit changes documentation and one SQL-trace comment only.

4. Final focused command adds `./internal/serve` and uses
   `-run 'LandingDecision|LandingGatePolicyMiswiring|Atomic.*Landing|AtomicDecision|LegacyExecutorEmitSQL|ChangeTrailers|ParseChange'`
   — **exit 0**, log `sa9b-focused-4.log`.
5. First `make check` — **exit 2**, log `sa9b-check-1.log`: staticcheck SA4006
   caught an overwritten setup-state assignment in the new replay test. Removed
   that unused assignment; no behavior change.

All logs below are under `/home/riels/.cache/`. Every command uses the environment
and load gate above. `BASE=/home/riels/openmerge-sa-9b-main` (the fetched
origin/main SHA). Commands ran from `/home/riels/openmerge-sa-9b`.

| Command | Actual exit | Log |
| --- | ---: | --- |
| `make check` (final) | 0 | `sa9b-check-2.log` |
| `make test-sim-exits` | 0 | `sa9b-sim.log` |
| `python3 scripts/check-phase2-compatibility.py "$BASE"` | 0 | `sa9b-compat.log` |
| `python3 scripts/check-legacy-landing-sql.py "$BASE"` | 0 | `sa9b-sql.log` |
| `python3 scripts/check-legacy-plan-action-depths-1-4.py "$BASE"` | 0 | `sa9b-depths.log` |
| `python3 scripts/check-legacy-commit-ids.py --old-source "$BASE" --new-source /home/riels/openmerge-sa-9b` | 0 | `sa9b-ids.log` |
| Live-journal replay versus main, strict (command below) | 0 | `sa9b-replay-main.log` |
| Deployment-pair replay versus a4b173f (command below) | 1 (pre-existing classifications difference) | `sa9b-replay-deployed.log` |

`make check` passed fmt/lint (zero issues), every race/short package and the
binary build. Queue tests: 283.046s; server: 236.341s; store: 22.018s.
Full required simulator exits: 38.377s, exit 0.

Compatibility preserves all **17** legacy exit records (SHA-256
`bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`),
**1,055** retry/default-digest records (SHA-256
`07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`),
and all **84** existing wire fixtures byte-for-byte.

The production executor/Emit SQL differential passed in each checkout with
**exit 0**, exactly **seven** identical statements in identical order:
BEGIN, queue advisory lock, action claim/lease FOR UPDATE, queue seed read,
ordered journal read, unchanged LandingStarted INSERT, COMMIT. There are no
additional legacy reads or transactions. The harness separately checks the
unchanged LandingStarted event and absent decision evidence.

Depth-1/4 exact action bytes/keys/order, nil plans, first actions and wakes:

| Corpus | Comparisons | SHA-256 | Exit |
| --- | ---: | --- | ---: |
| Short simulator | 227,662 | `71f1ae2108d98685b675aa5a158834bbd96595ce3c9e5f3ede6cb9c575aec96a` | 0 |
| Required full simulator | 5,615,998 | `05c71f7394b7a59e84b6e86cab4c219d39d3e60c6c82ebd336d3c3157b3dc4ed` | 0 |
| Fixture journals | 188 | `0ee08b70cb6685c634c25e904bc461cc46988655e189bd1e903204708029e939` | 0 |

Each baseline and candidate subprocess exited 0 (214 short, 446 full, 188 fixture
records apiece). No plans/actions/wakes exceptions. Commit-ID preflight:
identical case **exit 0**, intentional mutant **exit 1**, dirty-source case
**exit 2**, all expected; aggregate preflight and four-method comparison
**exit 0**. Tested and landed IDs match baseline exactly:

| Method | Tested | Landed |
| --- | --- | --- |
| Squash | `ff69dbec39d51f58388ecb40939b116639245145` | same |
| Merge | `bcc5548afcfa623368902566ffc0baa8b9cdc752` | same |
| Rebase | `fb81d35ef3ab6f59feb8cd76b09047d01637c9fb` | same |
| Fast-forward | `fc5c1bf85c90345690a94321d2afe65d7007018c` | `ccfc498745b4ed1f770d8e20c0c37e585564669a` |

## Saved-journal replay

Both runs are offline, use `GOMEMLIMIT=6GiB`, and read only already-exported
files. No live DB or status API access occurs.

```sh
python3 scripts/check-live-journal-replay.py \
  --candidate /home/riels/openmerge-sa-9b \
  --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl \
  --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl \
  --baseline /home/riels/openmerge-sa-9b-main --strict-state \
  --out /home/riels/.cache/sa9b-replay-main.json
# actual exit=0

python3 scripts/check-live-journal-replay.py \
  --candidate /home/riels/openmerge-sa-9b \
  --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl \
  --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl \
  --baseline /home/riels/openmerge-stage-a-9-deployed \
  --out /home/riels/.cache/sa9b-replay-deployed.json
# actual exit=1
```

Against main: verdict, gates 2/4 and mandatory self-test **PASS**. All 22 preflight
tests pass (exit 0). All **2,159/2,159** checkpoints from 2,158 events have
identical actions, structural state, legacy state and snapshot hashes. All
**8,676** timer probes agree; no cap hit. Candidate repeat is deterministic.
No first difference or non-allowlisted field. Synthetic pair: 7/7 checkpoints,
15 timer probes, identical snapshots/state/actions. This pins unchanged legacy
snapshot bytes, not just plans.

Against `a4b173f9c7a5fd5f7eb5b1dec69fffcf884ae4a7`: actual **exit 1**,
Gate 2 **FAIL**, Gate 4 **PASS**. All 2,159 action checkpoints and boundary
checkpoints agree, all 8,676 probes agree, and derived determinism passes.
Every structural difference at every checkpoint is exactly **classifications
absent → strictly nil `queue.classifications`**. This is the pre-existing
exception reported in PR 9 (and already present between a4b173f and main),
not a PR 9b regression. The deployment-pair synthetic self-test has the same
classification-only failure: **21/22 pass, self-test exit 1**. Its seven action
and boundary checkpoints and 15 timer probes still agree. No allowlist was
loosened. `landingDecisions` absent → nil remains the existing reviewed narrow
exception; populated or non-nil empty evidence is not permitted.

An auxiliary report-shape inspection initially exited **1** because it assumed
`non_allowlisted_fields` was a list of field names, rather than per-checkpoint
objects. Corrected `/home/riels/.cache/sa9b-inspect-replay.py` exited **0** and
asserted the exact nil-classifications shape for every real and synthetic
checkpoint; log `sa9b-inspect-replay.log`. This did not rerun or alter either gate.

The effective policy digest stays
`e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`;
`would_emit_config_applied=false`. Export SHA-256:

- Events: `9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`.
- Queues: `418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`.

The exact command/return-code index is `/home/riels/.cache/sa9b-statuses.tsv`.
No deployment was performed or authorized.

Final documentation checks: `make fmt` **exit 0** (`sa9b-final-fmt.log`);
`git diff --check` initially **exit 2** for an extra EOF blank line in this
evidence file, then **exit 0** after removing it.

## Round 1 review fixes

Trial class remains **Hold**. The base was fetched explicitly and remains
`15aa69929e89970fb49381493398814b8793567a`; no rebase was needed.

- **Writer/parser compatibility:** normalize CRLF before rejecting lone CR.
  Reserved keys are checked across the entire message; only canonical adjacent
  member pairs in the final paragraph, canonical replay provenance immediately
  before a pair, and the final batch trailer are accepted. Distinct pairs in a
  fast-forward marker remain supported. Contributor prose, trailer-like lines,
  folded continuations and empty values before the reserved suffix are ignored.
  `TestChangeTrailersWriterParagraphShapes` drives the unchanged real writer for
  squash/merge/rebase/fast-forward with six shapes (24 subtests), including both
  CRLF body and commit messages. `TestChangeTrailersBeforeCI` additionally checks
  two-member markers, provenance, contributor-byte preservation and immutable
  landing IDs. `TestParseChangeTrailersAuthoritativeBlock` retains forged,
  duplicate, non-final, case/space and continuation-injection rejection cases.
- **Complete SQL tracing:** `TestLegacyExecutorEmitSQL` now implements pgx
  BatchTracer and CopyFromTracer as well as QueryTracer. The differential script
  commits five overlay negative controls: extra authorizeStart statement,
  removed FOR UPDATE, nil-policy StartLanding in the legacy path, state load
  before claim check, and SELECT 1 via Emit/SendBatch. Each mutant must complete
  the real executor test successfully and differ from main's SQL trace; a
  compilation/test failure cannot count as detection.
- **Immutable local reads:** `Repo.ReadCommitMessage` opens the existing cache
  directly, under its cache lock, with full-SHA validation and no remote/auth
  resolution, fetch or maintenance. Read mode disables lazy fetch, replacements,
  grafts and every transport. Acquisition remains separate.
  `TestReadCommitMessageCacheOnly` proves cached success with an unavailable
  resolver, rejection of refs/abbreviated IDs, and missing-object failure even
  with a configured promisor remote. Writer/gate round-trips use this reader.
- **Bounded historical evidence:** `Store.LandingDecisionEvidence` filters by
  queue/type/batch/attempt before decoding, reads at most 129 decision rows to
  enforce the 128-member limit, and fetches each exact eligibility reference
  with LIMIT 2 (detecting missing/ambiguous evidence). All reads share a read-only
  repeatable-read transaction. Migration 006 indexes `(queue_id,type,id)`;
  `TestMigrationsConcurrentOpenAndReopen` covers migration/reopen. The settled
  readback test checks exact old eligibility despite a newer read, unrelated
  batch/attempt exclusion, unrelated future-version payload exclusion, and the
  member bound. Both event types and referenced eligibility identities/payloads
  must survive future compaction; this is documented in the API, migration and
  `internal/queue/landing-decisions.md`.
- **Wiring and retention nits:** reverse policy mismatch now returns
  `LandingDecisionRequiredError` before starting a transaction, rather than a
  stale/retry error; rejection has the same guard. Tests:
  `TestLandingGatePolicyMiswiring`, `TestLandingRejectionPolicyMiswiring`.
  `retainLandingDecisions` allocates only when pruning is required and preserves
  prior maps. `TestLandingDecisionPrunesInvalidTesting` directly covers both
  InvalidationReason and ProvisionalFailure. The cancelled/superseded branches
  in `TestLandingDecisionRetentionAndReplay` now include LandingStarted,
  settlement and a replacement batch with its own tested SHA/attempt/start.

Round 1 logs are under `/home/riels/.cache/sa9b-r1-*`; the existing load-gated
runner records real subprocess statuses in `sa9b-statuses.tsv`. All jobs retain
GOFLAGS=-p=2, 4 GiB memory (6 GiB replay), shared test Postgres and cache-backed
Go directories. No other agent's process was killed.

Initial focused run: **exit 1**, `sa9b-r1-focused.log`: the replacement fixture
reused the old tested SHA, which production correctly rejects. The fixture now
uses a new tested SHA. Second focused run: **exit 0**,
`sa9b-r1-focused-2.log`, covering forge/gitops/queue/store and the test families
above, including both policy-miswiring paths.

Round 1 implementation tested: `5503ada89d07f578abeaab903dccbe52c0d19d82`.
The final evidence commit changes this document only. An explicit final fetch
of `refs/heads/main:refs/remotes/origin/main` still returned `15aa699`; no rebase
or gate repetition was needed. The code and comparison source were clean.

| Round 1 command | Actual exit | Log (under `~/.cache/`) |
| --- | ---: | --- |
| `make check` | 0 | `sa9b-r1-check.log` |
| `make test-sim-exits` | 0 | `sa9b-r1-sim.log` |
| `python3 scripts/check-phase2-compatibility.py "$BASE"` | 0 | `sa9b-r1-compat.log` |
| `python3 scripts/check-legacy-landing-sql.py "$BASE"` | 0 | `sa9b-r1-sql.log` |
| `python3 scripts/check-legacy-plan-action-depths-1-4.py "$BASE"` | 0 | `sa9b-r1-depths.log` |
| `python3 scripts/check-legacy-commit-ids.py --old-source "$BASE" --new-source /home/riels/openmerge-sa-9b` | 0 | `sa9b-r1-ids.log` |
| Strict saved-journal replay versus main, same exports/command as above, output `sa9b-r1-replay-main.json` | 0 | `sa9b-r1-replay-main.log` |

`BASE=/home/riels/openmerge-sa-9b-main`. `make check` reports zero lint issues,
all race/short packages pass, and build exits 0. Queue: 282.712s; server:
223.345s; store: 22.645s. Required full simulator: 38.431s, exit 0.

Compatibility preserves 17 legacy exit records, 1,055 retry/digest records and
84 wire fixtures, with exactly the original hashes above. SQL baseline and
candidate tests both exit 0 and produce seven identical ordered statements.
Each of the **five** SQL-mutant tests also exits **0**, and each is rejected by
an actual SQL trace difference; the negative-control suite exits **0**.

Depth-1/4 baseline and candidate subprocesses all exit **0** (214 short, 446
full, 188 fixture streams each). Short/full/fixture comparisons remain
227,662 / 5,615,998 / 188, with exactly the original three hashes above. No
plans/actions/wakes exception. All four methods' tested/landed IDs match the
original table exactly. Commit-ID preflight statuses: identical **0**, mutant
**1**, dirty source **2**, all expected; preflight/comparison aggregate **0**.

Strict main replay: verdict/Gates 2 and 4/self-test **PASS**, actual exit **0**;
22/22 preflight tests pass with self-test exit **0**. All **2,159/2,159** action,
state, structural-state, legacy-state, snapshot and boundary checkpoints match.
All **8,676** probes match, no cap is hit, repeat determinism passes, no first
difference and no non-allowlisted field. The synthetic self-test also passes.
Exports and policy digest remain exactly as documented above. Round 1 does not
rerun the historical deployment-pair comparison; its earlier classifications
exception remains recorded in the original-run section.

Final `make fmt` and `git diff --check`: **exit 0** each. No deployment performed;
**Hold** remains in force. Only the feature branch is authorized for push.


## Round 2 review fixes

Trial class remains **Hold**; no deployment. Initial explicit fetch of main and
this branch found main unchanged at `15aa699`. The strict parser is unchanged.

- Metadata-enabled rebase now appends its provenance, canonical adjacent pair
  and optional batch trailer as the true final paragraph, after all contributor
  comments/prose. Retained DCO/coauthor trailers move into that paragraph before
  provenance. The nil-metadata path still calls the original Git trailer writer.
  `FuzzChangeTrailersWriterRoundTrip` drives two-member batches through each of
  squash/merge/rebase/fast-forward and the immutable message reader plus strict
  parser. Ten seeds cover final comments (including separated comment paragraphs),
  trailing blanks, CRLF, trailer-like prose, folding, reserved-key attempts,
  patch dividers and empty text. Mutated supported contributor messages exercise
  the same property; NUL/lone-CR inputs are excluded as unsupported commit text.
  `TestChangeTrailersBeforeCI` retains attribution, provenance, object immutability
  and multi-commit replay coverage; adversarial parser tests remain unchanged.
- Unreleased migration 006 replaces the queue/type index in place with partial
  expression indexes `events_landing_decision_identity` (queue/batch/attempt/id)
  and `events_eligibility_read_identity` (queue/change/read/revision/head/id).
  Production SQL uses those exact JSON expressions, with EligibilityReadID
  rather than the latest read; limits/ambiguity checks remain unchanged.
  `TestLandingEvidenceIdentityIndexes` EXPLAINs the exact production statements
  on 2,600 and 260,000 synthetic journal rows, half of each evidence type, after
  ANALYZE. Both realistic and `enable_seqscan=off` plans use an Index Scan;
  each returns one row in one loop with zero rows removed by filtering. All
  identity predicates appear in Index Cond, with no history-sized filter scan.
  Normal decision/eligibility execution times were 0.016/0.018 ms at 2,600 and
  0.023/0.020 ms at 260,000; forced times were 0.007/0.007 and 0.012/0.008 ms.
  These are local synthetic measurements, not production latency predictions.
- **Deploy note:** 006 runs non-concurrent CREATE INDEX inside the migration
  transaction/advisory lock. Both revised indexes together built in **2.846 ms
  at 2,600 rows** and **171.029 ms at 260,000 rows**. The test verifies ShareLock
  remains until commit, an independent insert fails with 100 ms lock_timeout,
  and relfilenode stays unchanged (no heap rewrite). Lock waiting is excluded
  from build timings. DDL and ledger are transactional; apply during the planned
  ADR 0009 period-B drain, retaining Hold. `TestMigrationsConcurrentOpenAndReopen`
  stays mandatory. Retention of landing_decision_recorded and exact referenced
  approval_eligibility_observed payload/read identity remains documentation-only;
  12a/later must enforce it before compaction is enabled. The same deployment
  and retention notes are in docs/operating.md.

All Round 2 logs are under `~/.cache/sa9b-r2-*`, and actual subprocess statuses
are appended to `~/.cache/sa9b-statuses.tsv`. The existing runner checks host
load before each heavy job and applies the supplied DB URL, cache Go directories,
GOFLAGS=-p=2 and 4 GiB memory (6 GiB replay). No initdb, live DB/status/systemd
or live OpenMerge refs were used. Only explicitly named main/branch were fetched.

Initial focused run **exit 1** (`sa9b-r2-focused.log`): appending a separate
metadata block initially left existing attribution outside Git's trailer block;
credits now move into the final block. EXPLAIN JSON uses floating-point row
counts, fixed in the test decoder. A failed plan assertion skipped transaction
cleanup and stalled the following test; only our own Store test process was sent
SIGQUIT (recorded exit 1), then its isolated schema was removed. Transaction
cleanup is now deferred on every path. Second focused run **exit 0**
(`sa9b-r2-focused-2.log`) includes the fuzz seeds, parser rejection cases,
identity-index scale/lock/heap assertions, settled readback and concurrent reopen.

Fuzz campaign: `go test ./internal/gitops -run '^$' -fuzz
'^FuzzChangeTrailersWriterRoundTrip$' -fuzztime=30s -parallel=1` **exit 0**,
`sa9b-r2-fuzz.log` (10 seeds plus mutations, 12 completed executions; no claim
of exhaustive coverage). The compiled deployed `3f77a88` Store test
`TestRound2DeployedReopensMigratedSchema` **exit 0**, `sa9b-r2-rollback.log`,
uses a detached checkout and external overlay at `~/.cache/sa9b-r2-*`. It applies
the exact revised 006 DDL plus ledger entry in a transaction on an isolated test
schema, closes the original connection, then twice reopens, appends and loads
legacy events with both new indexes still present. This verifies schema rollback
compatibility, not old-worker decoding of newly activated event types.
