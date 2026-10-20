# ADR 0008 PR 5 acceptance evidence

Recorded 2026-10-20. Trial class: **Inert** for this change. No live deployment
is authorized: main contains held ADR 0008 dependencies, and first emission has
a separate operator-controlled drain/backup gate even for github-mode queues.

Implementation revision: `4631fb8dc0c8f4e7ef4a0017e978b47dee0db3cf`.
Baseline: `d4e15c89fa9a1e68d8cf9afe55d0f3a25318ae91` (main after PR 3b merged). PR 4 is not used or stacked.

## Ledger and receipts

`internal/store/outside_push_ledger.go` streams a repeatable-read queue journal
in pages of 256, folding only ConfigApplied and PolicyAuthorityApplied through
the existing queue fold. At each BaseMoved under an openmerge pin it seeks the
same queue's existing `outside-base/<BaseMoved ID>` dedupe association, verifies
the audit type/SHA and source order, and looks for a receipt. No pin means github.
The page selects `dedupe_key` into the new EventRecord.DedupeKey field; the
existing EventsAfter logging SQL/behavior is unchanged.

The scan is O(queue events) with indexed dedupe seeks per authority base move,
O(one page plus one pin) memory, and a two-minute deadline. It never accumulates
all moves, warnings, or receipts in a map. A visitor must discard partial results
on failure. Retirement and rollback never delete evidence. No snapshot shape,
codec, migration, index, runtime forge read, public config, or warning rendering
changes are introduced. PR 6 owns rendering.

The new wire-v1 outside_push_acknowledged event has exactly one registry fixture.
The source event ID/SHA is checked in the exact numeric database queue ID before
receipt lookup/insertion under the existing queue lock. The deterministic dedupe
key is `outside-push-acknowledged/<source event ID>`, independent of operator.
Concurrent/restarted retries return the original event ID, supplied name, and
JSON timestamp. Wrong targets are rejected even after an acknowledgement exists.
Github-mode acknowledgement is intentionally permitted as an explicit awareness
receipt; it neither creates a warning nor changes any queue state.

The command requires saved drain and backup receipt files before opening its
operator DB connection. This enforces an explicit attestation, not automatic
host discovery or proof that a backup is restorable. Connections do not run
migrations. Old decoders reject the new event in either mode. See the operating
guide for restore/ref reconciliation rather than evidence deletion on downgrade.

## Behavioral acceptance

`adr0008-5-final-focused.log` retains passing tests for:

- Activation and rollback, BaseMoved immediately before/after the pin, audits
  held across settlement and restart, append under the new mode or after another
  mode reversal, history enrichment, replay, and unchanged source associations.
- Twelve concurrent acknowledgers; retries and restart; wrong queue/type/event/
  SHA; wrong SHA after a receipt exists; pre-cancel and cancellation waiting for
  the queue lock; a deferred transaction failure after insert but before commit.
- Twenty-seven outstanding pushes despite the recent projection's 20-item cap;
  rollback, retired-queue lookup/acknowledgement, github-mode receipts, stale pin
  rejection, read-snapshot consistency, and caller mutation isolation.
- Strict missing/unknown/case-folded/duplicate/invalid wire-field rejection;
  immutable queue state, snapshot-plus-tail equality and next actions at each
  approval fixture checkpoint; independent rules and suspect blocks remain.
- Real Git bare remote and independent observer cache: own landing suppressed
  only after settlement, exact landed-to-build-base rewind marked possibly
  stale, outside forward push and rewind enriched from real objects, and all
  remote refs plus queue snapshots unchanged by acknowledgement.
- Legacy persisted outside-push cursors continue using version 6.

Unknown/truncated history, possible_stale_observation and verification-before-
landing none remain original evidence. Acknowledgement does not identify the
pusher, approve changes, grant reviews, clear blocks, change ancestry, or move
green. Push-and-restore between observations is still a detection limit.

## Growth and query plans

On PostgreSQL 18.6 at localhost:54329, isolated per-test schemas, 30 outside pushes
plus 5,000/50,000 filler events: full warning scans took 27.895371 ms / 219.45457 ms;
one acknowledgement took 310.178 us / 565.993 us. These are single local samples,
not service latency claims. Pages were bounded at 256; all 30 warnings survived.
The retained EXPLAIN ANALYZE BUFFERS plans used events_queue_dedupe for point
lookups. The single-queue fixture planner chose events_pkey for ordered pages;
a separate mixed-queue fixture (5,000 selected plus 50,000 sibling filler
events) selected **events_queue_watermark**, returning 256 rows in 0.145 ms.
This is a query-plan sample, not a complete ledger latency. No new index or
indexed JSON predicate is needed. Raw plans and commands are retained locally
and identified by the artifact manifest.

## Legacy equivalence and backup boundary

`make check` exited **0** with PostgreSQL enabled: formatting, zero lint issues,
all short race tests, and the CLI build. Baseline binary SHA-256:
`98c2b560888205b5d3e4e445d2e2c0b0d8598d8554225b59876cf33e1d7af1bc`.
Candidate binary SHA-256:
`83e82301be1a5d4de01eedadc66da93f2903ced84e8e5b7c6f20e3964bb740c8`.

Strict offline replay passed **2,159/2,159** checkpoints for actions, structural
state, snapshot hashes, and deadline boundaries (8,676 boundary probes). Gates 2
and 4 passed, with no comparator allowances or first difference. The input has
2,158 events, IDs 1..2158, and one queue. Its exported SHA-256 identities are:

- events: `9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`
- queues: `418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`

All **90** approval/legacy-block contract checkpoints at depths 1 and 4 match.
Six legacy reporting fixtures have byte-identical complete serialized status and
check output, with no field stripping. All **102** existing wire fixtures are
byte-identical; exactly one acknowledgement registry fixture is added. Paired
simulator exits are **0/0**, with **146 identical logged result blocks** after
removing only harness timing/interleaving.

The isolated backup exercise ran at 2026-10-20T22:15:39Z. Source event **1**
(`outside_push_recorded`, SHA `outside`) produced receipt event **2** at
`2026-10-20T22:15:39.94795184Z`. Different-operator retries returned identical JSON;
a wrong-SHA retry exited 1 with no receipt. The pre-emission dump restored exact
journal bytes into a separate disposable database. The restore has no new event;
the emitting source has exactly one. The source audit bytes remain unchanged.
Dump SHA-256:
`80627285dd99eff24c097fea1426cf79c817fa75b102c6e6c78b77e2e0f9eea5`.
Both isolated databases were dropped after evidence capture.

Logging-only overlays retain journal IDs and real Git SHAs without changing test
behavior. In each independent boundary fixture, BaseMoved **9**, authority pin
**10**, and BaseMoved **11** are consecutive. Activation warns only for origin
11 (audit 14, or 15 after a second mode change); rollback warns only for origin
9 (audit 13, or 14 after a second mode change). Reopening the cursor and replaying
the ledger retains these same source/receipt associations.

The real Git run observed main at
`558b3a15eaba2e83aac8ae1502717a550481541f`, settled landing
`a3e2ecbeae15d395bac9acb1155e15a8b8f8fad8`, and outside tip
`8fff2d72268a7550dabcb468525858fde7126635`. BaseMoved IDs 11, 12, 13 yielded
audit IDs 15, 16, 17 and acknowledgement IDs 18, 19, 20. The exact rewind retains
possible_stale_observation; all three retain verification_before_landing none.
Acknowledgement left both the actual refs and encoded queue state byte-identical.

No live database, status endpoint, systemd unit, production ref, or real GitHub
call in code/tests was used. The
organization break-glass acknowledgement on GitHub remains PR 11 acceptance.

## Reproduction and retained artifacts

The [equivalence summary](adr0008-5-artifacts/equivalence.json) records exact
commands, numeric exits, fixture identities, policy digests, and stream hashes.
The [artifact manifest](adr0008-5-artifacts/manifest.json) hashes retained local
raw logs, streams, binaries, and the pre-emission backup. The identical replay
stream SHA-256 for baseline, candidate, and candidate-repeat is
`1fe079295539183b7008e70b940c7f71d45bd4aa4e443c8c1e581fa10d31051a`.
The final recorded governing config is event 1287, commit
`6fa7d082a9888f86fdec9f4f80473070c7fb4be6`, digest
`e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`.
The export has zero authority pins; unchanged config emits no new ConfigApplied.

[Source/receipt IDs and real Git SHAs](adr0008-5-artifacts/source-evidence.txt),
[before](adr0008-5-artifacts/backup-before.jsonl) and
[after](adr0008-5-artifacts/backup-after.jsonl) backup-boundary journal samples,
the [backup command report](adr0008-5-artifacts/backup-report.json), and the
[mixed-queue page plan](adr0008-5-artifacts/mixed-queue-page-plan.txt) are committed
alongside this report. The raw backup and full replay streams remain local at
the paths named in the manifest. No production backup or GitHub API body is used.

With clean baseline and implementation checkouts plus the existing offline export:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/check-adr0008-5-inert.py \
  --baseline /home/riels/.cache/adr0008-5-main \
  --events /home/riels/.cache/om-pr72-fix1-input/events.jsonl \
  --queues /home/riels/.cache/om-pr72-fix1-input/queues.jsonl \
  --out /home/riels/.cache/adr0008-5-final-inert

flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB \
  OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' \
  make check
```

The runner wraps Go work in the shared load lock (`-p=2`, 4GiB; 6GiB for replay).
It adds no comparator allowances, CI/Makefile changes, or test-flag changes.
The otherwise unspecified CLI queue selector is the immutable numeric queues.id,
so retired and case-variant histories are unambiguous. No scope deviations;
real GitHub break-glass acceptance remains the dependent PR 11 exercise.

## Operator implementation refinement (#114)

`openmerge admin acknowledge --queue <queue-id>` uses the numeric durable
`queues.id`, including for retired queues. Read the queue identity with
`openmerge queue status --json` or `GET /v1/queues`
on the operator API, then resolve its numeric ID using the operator SQL lookup in
[the operating guide](../operating.md#acknowledging-outside-pushes). Current status
responses expose identity fields, not `queues.id`.
This refines the implementation notation; the accepted ADR Decision is unchanged.

The acknowledgement gate now requires separate JSON documents with `kind: drain`
and `kind: backup`, distinct resolved paths and distinct SHA-256 content hashes;
see [the operating guide](../operating.md#acknowledging-outside-pushes).
The ledger validates every `outside_push_recorded` origin, including rows without
a dedupe key. An orphan is an explicit ledger anomaly error, even if an
acknowledgement exists; callers must discard partial results on error. The visitor
signature and warning ordering stay unchanged. `EventRecord.DedupeKey` is optional:
`nil` means not loaded by `EventsAfter`, while a pointer to an empty string means
loaded with no association. The operational logging SQL is unchanged.
