# Writer foundations verification

Fix round 1 starts at PR #183 head `7776802`, following the Astra and Sonnet
requests for changes. The PR remains **Hold**. No production writer is enabled,
and no event schema, SQL migration/index, Makefile, CI, Dockerfile, live database,
service or forge call is changed. Loader/planner maxima are now shared constants;
the accepted configuration envelope is unchanged.

## Retention and cost

All original journal evidence remains in the database for audit export. Existing
terminal queue events retire abandoned/discarded tested companions and dropped
change generations. Dequeue acknowledgements must actually remove the admission;
stale acknowledgements are no-ops. Close, head/revision changes and queue
retirement preserve uncertain fenced landing evidence until its definitive
outcome. Completed pairs and unpinned superseded inventories also compact.
Generation/chunk-zero and terminal-attempt fences reject identity reuse after
reload. This solves active projection accumulation, not unlimited history:
rejection fences and checkpoint bytes still grow with lifetime identities and
remain subject to finite resource budgets.

The payload/metadata budget is 4256 records / 32 MiB. The reference budget
uses the same maxima as the loader/planner: 128 members and speculation depth 32.
Each companion admits `128*2 + 1 + 128 = 385` distinct targets, with a separate
aggregate bound of `385*32 = 12,320`. The maximum-envelope fixture retains 4096
distinct inventories and 32 companions (4128 records), with 8,224 distinct targets counted
per companion, and round-trips its checkpoint.

The owned fold transfers state ownership on successful application and rolls back
lifetime fence mutations on rejection. It copies bounded active state, never the
lifetime maps. The immutable `Apply` adapter remains for branching callers.
Checkpoint sorting, encoding and restoration still process lifetime metadata;
that work is explicit at checkpoint boundaries.

`AppendSide` and `Store.Load` restore the newest eligible append/audit checkpoint
and page its suffix in 512-row pages. The initial bootstrap still replays the
prefix. Append checkpoints use an observation key independent of outside-push
audit progress; they do not skip unaudited base movement. Each successful side
append saves its checkpoint and dedupe manifest in the same transaction.

## Behavioral and scale proofs

- `TestTenThousandAbandonedAttemptsRetireDurably`: 10,000 real batch/build/start/
  abandon sequences; at most one active inventory record; reload every 1,000;
  retired attempt reuse rejected.
- `TestTenThousandDroppedChangesRetireDurably`: 10,000 distinct changes for each
  close, accepted dequeue, legacy head change, explicit revision change and queue
  retirement path; zero active records; reload every 1,000; generation reuse
  rejected. Assertions precede cleanup events so cleanup cannot mask a mutant.
- `TestUncertainLandingRetainsEvidenceUntilDefinitiveOutcome`: close, head change
  and retirement cannot erase the evidence needed by a successful fenced landing.
- `TestCloseRetiresPinnedAndPartialGenerationsAndTheirAttempt`: close releases
  both pinned and partial inventories and their tested companion, while rejecting
  generation/attempt reuse against a fresh valid inventory after reload.
- `TestStaleDequeueCannotRetireNewInventoryWithoutAdmission`: stale/no-op queue
  outcomes cannot retire newly acquired evidence.
- `TestOwnedFoldScalePreservesFenceMaps`: 2,000 / 8,000 / 20,000 completed attempts
  have exactly N generation fences and N settled fences, zero active records and
  zero lifetime-map copies. Counts, not latency, are assertions.
- `TestOwnedRejectionRollsBackLifetimeFenceUpdates`: a late capacity failure
  restores generation, latest-CAS and fence state byte-for-byte.
- `TestSideAppendAndLoadResumeCheckpointWithOnlySuffixRows`: after a 2,000-row
  prefix, 20 consecutive appends each issue one dedupe query plus one empty
  suffix query; each Load issues one empty suffix query. Zero historical rows
  are returned. Query counts/positions are asserted.
- `TestSideEvidenceLargeHistoryBoundedAuditAndReloadCost`: the existing 268,000-row
  fixture still produces a 394,710-byte cursor, zero reload reference queries
  and one empty audit suffix query per idle poll. Its idle query plan uses three
  shared buffers, below the asserted maximum of 16.
- `TestSidePendingV9ReloadHasFixedReferenceBudget`: 32 unfinished companions
  produce exactly 32 exact-ID queries and one empty suffix query.
- `TestMaximumBatchAtMaximumSpeculationDepthFitsReferenceBudget`: maximum member
  and attempt counts, reference headroom, preflight and restart.

Elapsed measurements are diagnostic only; `packages.log` records each run.
Original legacy cursor goldens and deployed no-side-evidence byte encodings are
unchanged. The original #181 compatibility proof used head
`f8fbe12b041fd6c1eeaede2de5de0a608b8338f8` in a separate archive; original journal
rows remain the sole source for its transitive export closure.

## Append admission and atomicity

`AppendSide` admits exactly the two provenance and six dependency journal kinds.
Every queue event, including `LandingDecisionRecorded`, is refused. Decisions
remain on the existing policy/phase-authorized StartLanding path; future writers
must co-transact companions there. No production caller exists.

All candidate records validate before any INSERT under the queue advisory lock.
The cursor upgrade remains atomic at the audit's existing watermark, including
rollback and stale-audit CAS refusal. A dedupe retry must match bundle length,
reserved IDs, independent binding and complete canonical payload bytes. JSONB's
formatting is normalized through the owning strict codec before comparison.

## Mutation proofs

Isolated scratch copies compile and fail their intended behavioral assertion when
any of these 18 checks is removed or regressed:

1. Abandoned/discarded batch retirement.
2. PR-close retirement.
3. Accepted-dequeue retirement.
4. Legacy-head retirement.
5. Explicit-revision retirement.
6. Queue-lifecycle retirement.
7. Writer-supplied dependency binding verification (both identities valid).
8. Settled-attempt reuse rejection with a fresh valid inventory.
9. Checkpoint generation predecessor-chain integrity.
10. Checkpoint pending-companion temporal binding.
11. Retired checkpoint watermark versus containing cursor.
12. Append/Load checkpoint resume.
13. Dedupe payload-byte comparison.
14. Owned fold's absence of lifetime-map copies.
15. Maximum-configuration aggregate reference allowance.
16. Maximum-configuration record allowance.
17. Maximum-configuration pending-attempt allowance.
18. Dropped-change companion retirement alongside pinned/partial generations.

The earlier before-insert, cursor-upgrade, compaction, CAS, budget and production
writer/encoder/raw-kind tripwires remain in the package suites. Source mutations
are confined to `/home/riels/.cache/pr183-fix/mutation-wt`; JSON results and
individual logs are next to that directory. Build failures are not counted as
killed mutants.

## Commands

All Go invocations use:

```sh
export TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
export OPENMERGE_TEST_DATABASE_URL="$TEST_DATABASE_URL"
go_safe() {
  flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB \
    GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3 \
    PATH=/home/riels/.local/share/mise/installs/go/1.27.3/bin:$PATH go "$@"
}
go_safe test ./internal/journalreader ./internal/provenance/journal ./internal/dependency ./internal/queue ./internal/status ./internal/audit ./internal/config ./internal/brief ./internal/changepolicy ./internal/provenanceeval ./internal/provenance ./internal/gitops -count=1 -v
go_safe test -race ./internal/store ./internal/serve -count=1
go_safe vet ./...
```

`golangci-lint run` uses the same flock, toolchain and resource environment.
Toolchain gofmt and `git diff --check` verify formatting. Database tests use
isolated test schemas and synchronization points. Scratch command output lives
under `/home/riels/.cache/pr183-fix/`: `post-merge-packages.log`,
`post-merge-race.log`, `post-merge-vet.log`, `post-merge-lint.log`,
`post-merge-status.log`, `post-merge-mutations.log`, `mutations.json`,
`audit-compat.log`, and individual mutant logs.

## Post-merge results

Main advanced during verification. Commit `54b7bf3` (#182 and #184) was merged
without rewriting the PR's history. The post-merge package matrix above passed,
including the packages changed upstream. Store and serve run their complete
package suites under race instrumentation: **PASS**, 170.431 s and 540.456 s.
The clean pre-merge full core rerun is also retained in `final-core-green.log`.

Post-merge scale diagnostics were 1.81 / 7.73 / 19.11 s for 2,000 / 8,000 /
20,000 attempts. Assertions pin exactly N generation fences, N attempt fences,
zero retained payload records and zero lifetime-map copies; elapsed times are
not performance thresholds. Ten thousand abandonments retain one payload
record (2,632,922 checkpoint bytes); each 10,000-change drop path retains zero
payload records (6,576,901–6,926,524 checkpoint bytes, including queue state).
Lifetime metadata growth remains explicit despite bounded active payloads.

Post-merge **vet PASS**, **lint PASS (0 issues)**, **18/18 compiling mutants
killed**, gofmt clean, and `git diff --check` clean. All requested checks passed.
The additional dropped-companion mutant fails the pinned/partial-generation
close assertion. No production writer was enabled; the PR remains Hold.


## Fix round 2

Merged main at `cea664a68bbdc9577d4e1fe5599effd7661e9571` without rewriting
history. The merge preserves #181's audit decoder allowance and the unwired
side-append allowance in the production writer tripwire.

`Load`, `Plan` and `StartLanding` now log an unusable checkpoint's queue ID and
reason, then replay from the queue seed. Database query and journal-validation
errors still propagate. `AppendSide` keeps strict checkpoint admission; an
outside-push audit reset must commit before a new append can recover.

`TestSharedReaderInvalidAuditCursorRecovery` failed before the fix for garbage
snapshot data, non-JSON cursor bytes and snapshot version 999. After the fix,
all three cases match full replay, pass planning and landing-start validation,
refuse an append without changing journal/cursor bytes, and append successfully
after the audit's reset commits. A separate invalid-journal case proves fallback
does not suppress authoritative event errors.

`TestCheckpointRejectsPendingBeforeFinalInventoryChunk` accepts a valid
multi-chunk compact checkpoint and rejects a forged checkpoint whose final
inventory chunk follows its pending companion. In an isolated archive, removing
`g.ids[len(g.ids)-1] >= r.ID` from `restoreCheckpoint` compiles and fails that
assertion with an accepted forged checkpoint. This kills the surviving P3 mutant.

Operating guidance and both internal READMEs now mark lifetime fence growth,
transaction sharing for decision/companion writes, and idempotent landed-companion
recovery as BLOCKING prerequisites for ADR 0010 4b and ADR 0011 4a/4b. The root
README links those gates. Review capacity and decode-time estimates are labeled
as estimates rather than supported limits.

Commands for this round use the `go_safe` wrapper and database URL exports above:

```sh
go_safe test ./internal/store ./internal/provenance/journal -run 'TestSharedReaderInvalid|TestCheckpointRejectsPendingBeforeFinalInventoryChunk' -count=1
go_safe test ./internal/store ./internal/journalreader ./internal/provenance/journal ./internal/dependency ./internal/queue ./internal/serve ./internal/status ./internal/audit ./cmd/openmerge -count=1
go_safe test -race ./internal/store ./internal/serve -count=1
go_safe vet ./...
# Same flock, Go toolchain and resource environment as go_safe:
golangci-lint run
python3 -I scripts/test_verify_audit_anchor.py
node scripts/verify-audit-v1-node.cjs
python3 -I /home/riels/.cache/pr183-fix-r2/check-oracle.py
```

The independent Node v1 oracle reproduces all 100 frozen files. The v2 oracle
redirects derivation into temporary scratch storage and reproduces all 20 frozen
files without changing repository fixtures. The Python anchor contract passes.
Focused regression checks pass, and the isolated compiling ordering mutant is
killed. Logs and the verification script are under
`/home/riels/.cache/pr183-fix-r2/`.

The complete normal package matrix passes, including `internal/store`,
`internal/journalreader`, `internal/provenance/journal`, `internal/dependency`,
`internal/queue`, `internal/serve`, `internal/status`, `internal/audit` and
`cmd/openmerge`. This includes the full legacy cursor goldens and #181's audit
export/replay, scratch limits, provenance closure and CLI integration coverage
on the merged tree.

The complete store and serve suites also pass under `-race`. `go vet ./...`
passes, `golangci-lint run` reports **0 issues**, and gofmt and `git diff --check`
are clean. All required round-2 validation passes. No production writer is
enabled; the PR remains Hold.
