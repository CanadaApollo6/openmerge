# ADR 0010 slice 4b evidence (Hold)

Base: main through #192, including the independent #191 test-only fix. This slice adds no migration, index, public configuration,
event fields, test flags, CI or deployment changes. Public queues cannot activate
the private provenance pass. Deployment waits for ADR 0009 period B and the
reader-first/drain/verified-backup gates. High-volume activation is still blocked
on a structural fence bound or an explicit lifetime/rollover policy; the capacity
gauge and alert are unchanged.

## Proof map

| ADR proof | Tests |
| --- | --- |
| Fake forge plus real Git merge, squash, rebase, fast-forward | serve `TestProvenanceFreshGateFakeForgeRealGitAllMethods` builds/pushes and lands real objects through gitops, reads checks from the fake forge, drives the activated Stage A read/prepare/commit callbacks, validates tested metadata, persists T/L and rejects changed title/check inputs. Store `TestProvenanceWritersRealGitTestedLandedAndReplay` commits the corresponding real-object inventories and companions. |
| Batches and audit closure | Audit `TestProvenanceBatchIntermediateExportAndSecondaryDecisionClosure` exports/replays intermediate results and follows an out-of-range companion from a secondary member's decision. Store `TestProvenanceBatchEveryMemberAndIntermediateCompositions`, for all four methods: exact per-member decisions, original sets, intermediate results and terminal L; a secondary member cannot borrow approvals. |
| Speculative rebuild | Store `TestProvenanceSpeculativeRebuildUsesNewGeneration`: an abandoned speculative attempt retires T; a rebuilt speculative batch keeps declaration/receipt identity, uses a new mapping/generation and exact retired predecessor, and commits another T. |
| Re-push and base rewrite | Store `TestProvenanceRepushAndBaseRewriteInvalidatePreparedStart`: old starts leave no side effects; a re-push replaces originals, while a rewritten build base adds the newly exposed original without losing the stable inventory. |
| Restart and offline replay | Store `TestProvenanceWritersRealGitTestedLandedAndReplay`: delete the object directory after `Landed`, recover exactly once without forge/Git/ref access, and compare every snapshot-plus-tail cut to full replay. `TestProvenanceRecoveryRebindsOnlySupersededHistoricalInventories` reconstructs only the changed member from immutable T. |
| Concurrent generations | Store `TestProvenanceGenerationCASRace`: channel-controlled competing append wins between the prepared read and transaction; the stale start rolls back. #188's shared-transaction races remain exercised. |
| Backup/restore downgrade | Store `TestProvenanceBackupRestoreDowngradeRehearsal`: actual `pg_dump`/`pg_restore` on localhost:54329, retain post-backup evidence, old decoders refuse side-era rows/v9, restore into a separate disposable database, verify exact pre-emission event count/state/cursor bytes and legacy readability. No live refs, services or APIs are used. |
| Atomic refusal | Store `TestProvenanceStartMutationRefusalsAreAtomic`: generation, approval, parser identity, mapping, decision, namespace, missing activation and forced SQL-insert failures leave no partial decisions/start/evidence/checkpoint. #188's failure-after-decision tests remain intact. |
| Bounded canonical chunks | Store `TestProvenanceChunksCanonicalAndBounded`; writer canonicalizes detached declarations and commits, starts at 128 commits/chunk and lowers framing until the strict encoder accepts each complete chunk. No truncation. |
| Activation boundary | serve `TestProvenanceRequiresCompleteInternalActivation`, public-loader rejection tests and `TestProvenanceSeamTripwire`; journalreader `TestActivatedProvenanceWriterGuard` and existing writer guards prohibit unsanctioned callers/captures. |
| Legacy bytes | Existing queue `TestWireV1Golden`, `TestLandingDecisionGolden`, receipt goldens; store legacy snapshot/full-cursor goldens and legacy landing SQL proof; provenanceeval baseline brief/canonical receipt goldens; serve GitHub compatibility/status comparisons. None of their fixtures changes. |

## Write and recovery boundary

`prepareStart` runs after fresh checks/eligibility and before SQL. The activated
store start repeats the evaluator against the queue-locked state and checks
exact predecessors. It uses #188's shared transaction; neither fetches nor
external effects run under the lock. Both canonical declaration digests are
retained: the inventory's no-rule digest and the receipt's potential-rule digest.
The effective policy binds Git version, parser/classifier settings, identity
rules and all requirements. Typed unknown inputs keep their compiled types;
partial CI knowledge also retains observed/unsettled names in typed inputs.

The current executor lands T exactly for merge/squash/rebase, and the original
terminal head for fast-forward. L recovery reconstructs those compositions from
immutable T plus the accepted decision/outcome, removes the fast-forward test
marker and gives L its own stage/identity. Unexpected changed compositions fail
closed. Recovery cannot append unrelated side records or consume retirement
reserve; #188's dynamic reserve, hard caps and shrinking-recovery requirement
remain authoritative. No structural fence bound is claimed here.

The private installer remains responsible for stable object custody, trusted
composer observations and applying 4a's pre-CI publication filter. Public policy,
schema, diagnostics and owner activation remain slice 5.

## Validation

Source mutations were isolated and reverted after each run: removing declaration
identity binding, skipping the atomic T append, skipping restart recovery, and
bypassing both generation CAS checks each failed its focused race-mode proof
(9.185s, 3.627s, 4.732s and 3.542s command wall time respectively). Reinstating
the old audit requirement that every member use the final composition also
failed the batch export/replay proof (1.638s). All five mutants were restored.

All Go invocations use the requested flock,
GOFLAGS, GOMEMLIMIT and Go 1.27.3 environment; all database tests use the isolated
localhost:54329 endpoint and disposable schemas/databases.

The backup rehearsal selects an installed `pg_dump`/`pg_restore` client at least
as new as the configured server. Only that external-tool rehearsal skips if no
compatible client is installed. The local proof used PostgreSQL 18.6 and performed
the actual restore; all writer/replay proofs run independently of those clients.

## Final command results

All affected packages passed with `-race -short -count=1 -timeout=15m`:

| Package | CI-mode elapsed |
| --- | --- |
| `internal/store` | 227.024s |
| `internal/serve` | 290.690s |
| `internal/audit` | 102.931s |
| `internal/gitops` | 107.673s |
| `internal/journalreader` | 20.872s |
| `internal/provenance` | 1.065s |
| `internal/provenance/journal` | 72.004s |
| `internal/provenanceeval` | 1.309s |
| `internal/queue` | 145.002s |
| `internal/executor` | 1.153s |
| `internal/config/condition` | 1.020s |
| `internal/status` | 10.327s |

`go test -race -short -count=1 -timeout=15m ./...` passed every package except
serve, whose process was killed at 299.813s without an assertion failure. The
same serve command run alone passed in 290.690s. After the rebase, audit and
journal were rerun as complete packages; the strengthened real-Git runtime proof
also passed separately in 3.594s (Git 2.56) and 3.552s (Git 2.55).

The Git 2.55 provenance subset passed: gitops 20.698s, store 19.390s, serve 2.547s
and condition 1.007s. `go vet ./...` passed; `golangci-lint run` passed with zero
issues. Gofmt and `git diff --check` are clean. No test flags or resource settings
were changed to obtain the isolated serve result.
