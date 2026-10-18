# Stage A 9b: landing-decision follow-ups

Trial class: **Hold**. Merge only; do not deploy until ADR 0009 period B drains.
Base: origin/main `15aa69929e89970fb49381493398814b8793567a` (#75).
Worktree: `/home/riels/openmerge-sa-9b`, branch
`stage-a/9b-decision-followups`. Baseline checkout:
`/home/riels/openmerge-sa-9b-main` at that exact commit.

No live DB, status API, systemd unit or live `openmerge/*` ref was accessed.
Tests use the fake forge and isolated schemas in the supplied shared test DB.
Repository fetch used only `refs/heads/main:refs/remotes/origin/main`.

## Obligations and coverage

- S1: `TestLandingDecisionRetentionAndReplay/{landed,failed,cancelled,superseded}`
  retains decisions through landing, removes them at settlement, preserves prior
  State values, round-trips live snapshots, and compares encoded-event replay
  with incremental state. `TestLandingDecisionOldSnapshotPrunesHistory` reads
  the old snapshot format while dropping historical projection entries.
  `TestLandingDecisionJournalReadbackAfterSettlement` recovers settled evidence
  and its exact EligibilityReadID through `Store.Journals`, despite a newer
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
- S3: `TestParseChangeTrailersAuthoritativeBlock` covers preserved forged body
  text, duplicate pairs, non-final blocks, casing/whitespace variants, missing
  metadata and the fast-forward marker. `TestChangeTrailersBeforeCI` now uses
  the immutable object reader and shared parser against the real PR 10 writer
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
