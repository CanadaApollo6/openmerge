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

Final required gates and their logs are recorded below after completion.
