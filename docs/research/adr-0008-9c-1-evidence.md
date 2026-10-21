# ADR 0008 PR 9c-1 evidence

Trial class: **Hold**. Baseline: local main `051798c98e4bbaac235771be3b1d7d85e8cbbcc9`.
Verified implementation: `26b85a2aaceccfa5984a495d2ab1541d64aba0e8`.
Public OpenMerge activation remains rejected and production constructors remain
opted out. Deployment waits for dogfood period B to drain. This run implements
9c-1 and stops at its PR; it neither queues nor merges.

## Requirements and proof

| Plan / issue item | Implementation and regression |
| --- | --- |
| 9c split | [Plan](adr-0008-9-plan.md#pr-9c-review-split-2026-10-21): 9c-1 owns #135; 9c-2a/2b/2c/2d separately own acquisition, push gates/ordering, operational gates and integrated real-git acceptance; 9c-3 is public schema/loader/CLI acceptance and production opt-in only. Every original row has exactly one owner. 9c-3 requires the repository-wide completion signal and drain/backup/v7 gate first. The owner-executed PR 11 sandbox switch remains the final release acceptance gate. |
| #135.1 admission timeout | `internal/serve/write_restriction.go:193` caps admission reads by ActionTimeout/default and the remaining action deadline. This is not an independent read budget; late reads can time out despite a healthy forge. Forge I/O keeps RulesRequest and action cancellation; Load and Append each use a completionWriteTimeout derived from the worker parent. `worker.go:145`, `stage_a_gate.go:318`, `authority_admission.go:32` carry that parent through the actual landing path and schedule retry on read failure even when persistence also fails (unless shutting down). `write_restriction_worker_test.go:134` covers reader-only expiry and earlier action expiry. `authority_admission_test.go:186` drives `s.execute`, proves the block/retry survives action expiry and rejects any decision/check/start record. |
| #135.2 persisted variant / snapshot / drain | New `internal/queue/testdata/wire/v1/variants/write_restriction_observed_read_error.json:1`; strict variant byte round-trip in `authority_test.go:33`; snapshot block/recovery in `authority_read_error_test.go:72`. ADR `docs/adr/0008-openmerge-policy-authority.md:702`, `docs/operating.md:1827` and the plan's first-emission notes include strict readers lacking the optional event/v7 field in inventory/drain and backup/reconciliation/downgrade obligations. No existing fixture changed. |
| #135.3 cross-process schemas | `internal/serve/serve_test.go:74` uses 96 bits from crypto/rand, independent of the bubble clock and process counter. Existing same-instant schema-isolation regression remains exercised. Scratch schemas retain cleanup. |
| #135.4 real backoff | `internal/serve/write_restriction_worker_test.go:185` uses a ten-minute RulesInterval, asserts exact 1/2/4/5-minute deadlines, no read one nanosecond before each, a read exactly at each, and quota RetryAfter. All waits advance virtual time. |
| #135.5 reasons / rebind comment | `internal/serve/write_restriction.go:206` records only timeout/quota/canceled/unavailable classes, with wrapped-error and secret-text tests at `write_restriction_worker_test.go:295`. Untyped permission failures deliberately remain unavailable; no transport-text inference or secret-bearing text is persisted. `internal/store/landing_start.go:102` explains why ValidateLandingChecks compares evidence with the current pin's governing commit when ConfigApplied retains the first commit for an unchanged digest. |

The parent-context mutation changes only `worker.go` to pass `actionCtx` into
the landing gate. `TestAuthorityAdmissionWorkerTimeout` then fails with
`worker timeout skipped recovery schedule` (expected exit 1); unchanged source
passes. This checks the production context wiring, beyond the helper test.

## Verification

The clean-tree driver returned **PASS**.

- `make check`: **exit 0**, formatting, zero lint issues, complete short race
  suite and build. Focused worker/lifecycle/barrier/store/queue/config race
  checks: **exit 0**. Parent-context mutation: **expected exit 1**.
- Strict frozen live-journal replay: **2,159/2,159 identical** actions,
  structural states, snapshot hashes and checkpoints; **8,676** matching boundary
  probes. Gates 2/4 and all **22** mandatory self-tests pass, no differing fields.
- Depth-1/4 contracts: **90/90 byte-identical**; simulator: both exits **0**,
  **146/146 normalized blocks byte-identical**. Hashes are retained in the summary.
- GitHub status/API/health/author and actual CLI bytes: **identical**.
- Existing wire fixtures: **104/104 unchanged**, including **90** registry
  fixtures. One new strict read_error variant; no new event type or wire field.
- Isolated local serve race/short suite: **262.873s before**, **254.835s after**.
  Serve inside full `make check`: **287.217s**, with other packages running.
- Test Postgres serve scratch namespaces: **95 during baseline, 94 after**,
  no additions. The baseline's then-active time-named scratch schema was removed
  by its cleanup; the 94 pre-existing namespaces remain. No random serve schema
  from this implementation remains.

Reproduce from clean checkouts:

```sh
export GOROOT="${GO_1273_ROOT:?set to your Go 1.27.3 installation}"
export PATH="$GOROOT/bin:$PATH"
: "${OPENMERGE_TEST_DATABASE_URL:?set shared test Postgres credentials for localhost:54329}"
python3 scripts/verify-adr0008-9c-1.py \
  --baseline "$HOME/.cache/adr0008-9c-main-051798c" \
  --out "$HOME/.cache/adr0008-9c-verification-rerun"
```

Exact timing command in each checkout:
`go test -race -short -count=1 -timeout=15m ./internal/serve`, wrapped with
`flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB`.
Every Go invocation exports both toolchain variables. Replay uses 6 GiB.
Timing is one successful isolated run per side on the same local machine;
no speedup/regression or GitHub-runner prediction is claimed. Initial load
averages were 0.19/0.19/0.42 on 16 cores. Tests actually ran with `-count=1`,
shared test Postgres and race enabled. The new tests use fixtures inside
`testSynctest` and add no real-time sleeps or short-mode skips. No slow real-git
acceptance is added in 9c-1; complete real-git activation acceptance belongs to 9c-2d.

Compact receipts live in [9c-1 artifacts](adr-0008-9c-1-artifacts/summary.json).
Raw command logs and replay streams remain in
`/home/riels/.cache/adr0008-9c-verification`; retained hashes identify them.
An initial focused compile found an unused import after replacing schema names.
The first worker regression replaced only the transient reader, which trust
refresh correctly rebuilt; installing its resolver reader fixed the test setup.
Neither development failure is represented as a passing check.

## Reviewer flags

- The public barrier and production opt-out remain. No new forge capability,
  SQL migration/index/table, Makefile/CI/Dockerfile/test-flag change or existing
  fixture edit is included. Wire-v1 stays strict and additive.
- The parent is the worker operation context, not a detached background context.
  Worker shutdown still cancels persistence; a persistence failure remains an
  error and cannot claim durable recovery. No landing is authorized after a
  timed-out read.
- First read_error emission is incompatible with readers lacking its optional
  event/snapshot field. Rollback to GitHub does not erase that boundary.
- Actual governing acquisition/import gates, complete generated-object guards,
  first-fence ordering, ExpectedRefs/VerificationInput drift, propagation signal,
  operational gates and real-git acceptance remain 9c-2a/2b/2c/2d. Public enablement is
  exclusively 9c-3; PR 11 requires the owner and is outside this task.

Only the shared test Postgres on localhost:54329 and the frozen offline journal
export were used. No live DB/status API, service units, live OpenMerge refs,
application GitHub API or Smart-Data-Ohio organization was accessed. Git push and
PR creation are the explicitly requested publication operations.

## Round 2 corrections

Persistence Load and Append each have a `completionWriteTimeout` derived from the
worker parent. The append timer starts after forge I/O so the read cannot consume
its write budget. Shutdown still cancels both. Joined append failures retain read
classification for retry but remain errors: no durable block is claimed when the
queue lock prevents its write. The action completion row is returned to `planned`,
the in-memory authority cadence backs off, and no decision/check/start is recorded.

`TestAuthorityAdmissionWorkerTimeout` drives actual `execute` through ordinary
expiry and parent cancellation; `TestAuthorityAdmissionWorkerLockedAppend` holds
the queue advisory lock in a competing transaction. The real lock case uses wall time and a watchdog because real socket
I/O prevents synctest clock advancement; the other cases use `testSynctest` with
fixtures inside its bubble. `TestAuthorityMissingReaderAndLogsAreClassified`
checks missing-reader persistence and token-bearing URL redaction in logs.

The read retains the action deadline. Giving it a fresh parent-derived ActionTimeout
would extend an almost-expired action by a full action budget and undermine the
health bound. A timeout means budget exhaustion, not proof the forge was unhealthy;
the fail-closed block/backoff is intentional. The code and this evidence now state
that limit instead of claiming an independent production read budget.

The plan gives each remaining 9c row/carry-over exactly one owner among 2a/2b/2c/2d,
keeps public removal exclusively in 9c-3, and assigns the owner-run organization
release gate exclusively to PR 11 outside the slices. 9c-3 must test consultation
of the completion API and verify/drain refusal, not merely their presence.

The driver accepts toolchain, lock and frozen-export paths; credentials come only
from `OPENMERGE_TEST_DATABASE_URL`, validated as localhost:54329 without a password
literal or password comparison. Earlier artifact paths are machine-local historical
receipts. Its inherited PR 1 helper still uses this machine's Go build cache path;
that helper is unchanged. No credential is published by the driver.
