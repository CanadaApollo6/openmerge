# ADR 0011 slice 4b-2 evidence (Hold)

Base: `cbf94f6a5fd9e06b2ea0e063b530013f7bbff137` (`origin/main`, #199).
Read the ADR slice table, ordering/cancellation and real-git/review-push acceptance,
[4b-1 evidence](adr-0011-4b1-evidence.md), and #199's complete PR body including
Round 1 fixes and Hold limits.

The remaining 4b behavior is already implemented by the composition overlay,
scoped observer and ADR 0009 retirement protocol on this base. This slice closes
its acceptance matrix with tests; **no production Go code changes**. Public
activation remains rejected. This does not enable Stage A, wire slice 6, or waive
any 4b-1 activation prerequisites.

## Item-to-test map

| Item | Proof |
| --- | --- |
| Exact suffix order; unaffected prefix; retained enqueue order; fresh rebuild ID and oldest retired entry first | `store.TestDependencyOrderingRestartLateResultsAndCancellation/supported`: four admitted batches, middle declaration edit, exact planner/store suffix, prefix settlement and PR 8 rebuild |
| Cold restart and late build/check success cannot revive retired CI | Same matrix deletes disposable checkpoints, loads the journal, applies late events through the raw fold and store; `TestDependencyLateBuilderReissuesCleanupAfterRestart` covers a builder still building at retirement and cleanup after late publication |
| Cancellation capability present | Ordering matrix `/supported`: exact recorded run IDs, attempts, batch/tested SHA and reason; durable request, claimed action, fake executor confirmation and completion |
| Cancellation capability absent | Ordering matrix `/absent`: same retirement, executor without `CheckCanceller`, durable unsupported report and read-only follow-up; queue entries and retired IDs remain intact |
| Depth one has no new cancellation | Ordering matrix `/depth-one`: source wait retires the single batch but creates no cancellation intent |
| Planner, raw fold and every-cut replay agree | Ordering matrix `/supported` and `/depth-one`, late builder and real-git tests use `assertCompositionReplay`; replay helper now accepts the actual depth config; the admission helper skips claims correctly cancelled as stale after prefix settlement. The four-batch matrix sweeps every snapshot/tail cut from its completed setup onward; the other new sweep callers and existing 4b-1 proofs retain every history cut. Each cut compares next actions at every subsequent revision and final cursor bytes. `/absent` uses cold full replay and the store fold without another quadratic cut sweep |
| Unlanded review pushes preserve revision-less waits and stale only explicit pins; later landing never repins | `dependency.TestReviewPushPreservesWaitAndNeverRepins`: fake forge through production `ReconcileEdges`/`ObserveEdge`, unchanged declaration basis, pushed revision 7→8, snapshot restart, rejected delayed success, later landing, and a still-present matching historical landing |
| Two-repository real-git ordered landing and explicit-pin sibling | `store.TestDependencyRealGitReviewPushOrderedLanding`: two `t.TempDir()` bare repositories; review push with no B CI; A's durable settlement and production `gitops.IsAncestor`; revision-less B builds and lands; pinned sibling remains queued and gated. Actual source revision 2/head enters the start evidence |
| Trim 1 clock skip (#196) | `store.TestDependencyTrimOneSkipsClockPreparation`: `BatchMaxSize=1`, positive `BatchMaxWait`, skip key present and no prepared clock |
| Round 1 guards remain live | Existing `TestDependencyFreshSettlementChangesComposition`, `TestDependencyInvalidationRequiresExactSuffix`, `TestDependencyAppendOfBadInvalidationCompanion`, clock-membership and writer tests; mutations below disable the final simulated-state check, exact suffix and supplied-companion refusal |
| Legacy bytes, loader gate, downgrade and writer guard | Unchanged production tree; paired seeded differential; existing wire/cursor goldens, public loader tests, real-event downgrade refusal and `journalreader.TestNoProductionNewEventOrSnapshotWriters` |

No new event kind, payload field or snapshot version is introduced. Existing
new kinds retain their downgrade inventory and real-row refusal tests. No new
production writer, lock, network read or formation-clock acquisition is added.
All forge calls are fake; Git fixtures use plumbing to create objects and the
production ancestry reader, with no hand-parsed history or real GitHub calls.

## Mutations

**12/12 compiled mutants killed by behavioral assertions; all restored.**
Each mutation runs in an isolated temporary copy, uses
`-race -short -count=1`, must compile and fail a behavioral assertion, and is
restored before the next mutation. Compilation failures do not count.

| Disabled guard | Detecting test |
| --- | --- |
| Suffix invalidation latch | Ordering matrix `/supported` |
| Depth-one exclusion in check retirement | Ordering matrix `/depth-one` |
| Durable cancellation intent | Ordering matrix `/supported` |
| Explicit-pin review-push stale check | Review-push `/explicit-pin` |
| Exact pin match before accepting a landing | Review-push `/explicit-pin` |
| Trim 1 clock skip | Trim 1 test |
| Final post-observation composition validity | Existing fresh-settlement regression |
| Supplied invalidation companion rejection | Existing bad-companion regression |
| Exact insert-time suffix fence | Existing exact-suffix regression |
| Cleanup after late builder publication | New late-builder restart test |
| Observation prior/sequence fence | Review-push delayed-success assertion |
| Unrelated production new-kind literal | Existing AST writer tripwire |

Reproduction: disable the named guard, run the listed focused test with the
wrapper below, require an assertion failure, then restore it. Session scripts
and complete logs are under `/tmp/openmerge-4b2-evidence/`, outside the PR.

## Validation and back-to-back timings

Every Go command uses:

```sh
flock /home/riels/.cache/openmerge-heavy.lock env \
  GOFLAGS=-p=2 GOMEMLIMIT=4GiB \
  GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3 \
  PATH=/home/riels/.local/share/mise/installs/go/1.27.3/bin:$PATH \
  TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' \
  OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' \
  go test -race -short -count=1 -timeout=580s ./internal/PACKAGE
```

Packages run individually. Each main/head pair runs consecutively; main is an
isolated `git archive origin/main` copy, never the protected main checkout.
Commands return asynchronously; each package has a less-than-ten-minute timeout.
Postgres fixtures use disposable test schemas.

All five packages pass individually with `-race -short -count=1`.
The first table reports Go's test-package times:

| Package | Main | Final | Delta |
| --- | ---: | ---: | ---: |
| serve | 311.676s | 314.162s | +2.486s |
| store | 224.419s | 229.102s | +4.683s |
| queue | 138.864s | 139.643s | +0.779s |
| dependency | 7.842s | 7.869s | +0.027s |
| journalreader | 19.787s | 19.702s | -0.085s |

Whole-command elapsed times, including compilation and the wrapper:

| Package | Main | Final | Delta |
| --- | ---: | ---: | ---: |
| serve | 321.633s | 321.103s | -0.530s |
| store | 224.817s | 229.480s | +4.663s |
| queue | 142.109s | 143.049s | +0.940s |
| dependency | 9.010s | 8.175s | -0.835s |
| journalreader | 21.677s | 21.563s | -0.114s |

`golangci-lint run ./...`: **0 issues**.
Gofmt and `git diff --check` are clean.

The legacy differential uses seeds 17/42/101, merge/squash/rebase/fast-forward
and depths 1/4: **24 scenarios / 6,335 serialized queue events plus results**
match main byte for byte. Both SHA-256 hashes are
`17558d35f02727f5c504863b88d7532e0f8ff5d744456d327a8021e46904a77b`.
The temporary harness lives only in archived copies; no production source differs.

The first full branch store run caught a test-helper assumption: an old
speculative build plan is correctly cancelled after prefix settlement, but the
helper stopped at that cancelled claim. It now skips cancelled claims and reaches
the fresh plan. The corrected focused store matrix passes. A first successful
pair then measured +6.497s test time, above the target.
The four-batch matrix now starts its quadratic cut sweep at the completed setup,
retaining all invalidation/retirement/late-result cuts and every later revision;
existing admission/initialization sweeps remain unchanged. Final uninterrupted
main/head store timings above supersede those intermediate samples.

No serve fixtures, cycles or production changes are added. These are single
local paired samples, not statistical estimates of CI duration. The serve delta
measures suite variation with identical production and serve test inputs; the
store delta includes the added acceptance fixtures as well as suite variation.

## Interpretations and Hold limits

1. Stage A consumes confirmed landings, not review heads. A revision-less edge
   waits across review pushes without declaration edits or re-approval. An
   explicit pin never automatically follows a later revision's landing.
2. A matching historical landing still on base remains terminal after later
   review pushes. Start evidence records the actual satisfying revision/head,
   rather than silently replacing it with the current review revision.
3. Retirement preserves queue positions and leaves an unaffected prefix to
   settle. Re-eligibility requires a fresh composition and fresh CI; late build
   or check success cannot revive the retired ID. Late ref publication reissues
   cleanup even after the earlier deletion completed.
4. Cancellation is best effort for permanently discarded speculative work.
   With the capability absent, the existing read-only follow-up remains; neither
   an unsupported response nor cancellation confirmation grants landing authority.
   Depth-one cancellation is not introduced. Started/uncertain pushes retain
   the existing settlement boundary, covered by 4b-1's landing-boundary proof.
5. Final simulated-state authorization, exact suffix/membership fencing and
   store-computed companion ownership remain unchanged and mutation-tested.
   No new authorization path bypasses #199's Round 1 fixes.
6. The inherited 32,768-record/32-MiB formation-clock cap and the remaining
   activated O(history) locked loads described in #199 remain **activation gates**.
   This test-only slice neither fixes nor expands them. Public schema/loader and
   production dependency acquisition remain gated; slice 5/6 work is still required.

No Makefile, CI, test-flag, Dockerfile, migration, index, status API, protected
`openmerge/*` ref, main checkout, live DB or installation changes. Gofmt fixes
are isolated in a style commit. No temporary harness is committed.
