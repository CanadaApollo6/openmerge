# ADR 0008 PR 4: source persistence and operator verification

Implementation base: `046de34` (main). **Trial class: Inert** for this PR: the
required legacy comparisons passed with no observed byte changes. PR 3a remains Hold:
an Inert result for this change does not authorize deploying that dependency.
No live database, service unit, live refs/status endpoint, or GitHub writes are
part of this implementation's tests. Organization-sandbox acceptance below is
planned, not executed here.

## Source and concurrency contract

`queue.WriteRestriction` is an internal source-map entry. The three legacy
source event enum values and all wire fixtures remain unchanged. Applied
`write_restriction_observed` and `write_restriction_verified` events reconcile
visibility, findings and the source revision/block in one immutable fold.
Proposed events retain evidence only; they do not activate a pin, change planner
revisions, admission, next actions, or any source block on a GitHub queue.

Findings are indexed by `Finding.Element`. Positives clear only the elements
that the read can see, at the captured source revision and policy binding.
A structural repair can therefore clear while a previously seen unauthorized
actor remains blocked behind a hidden bypass list. Definite negatives apply
even at an older basis and invalidate earlier passes. Pure read failures retain
state and both ages; partial negatives preserve earlier findings and ages.
New governing commits with an unchanged effective binding retain findings.
A changed binding starts unverified and cannot reuse old-policy clears.

The dormant `serve.ObserveWriteRestriction` loads its basis before network I/O.
GitHub's full inventory capability includes repository/inherited rulesets and
classic protection on the concrete expected refs. Classic policy leaves are
normalized with PR 2's injective parameter encoding and retained conservatively
as `classic_inventory`; the push validator treats unsupported classic policy
as incompatible rather than inferring that it is bypassed. Legacy classic
validation remains independent. Activation wiring is reserved for PR 9.

`DeferredByPause` uses an authority-specific in-memory clone to project a clear.
The generic serve emitter explicitly excludes this source; encoding a legacy
`RulesSourceCleared` or `RulesSourceBlocked` for it still fails validation.
Observed unknown actors require a matching `unexpected_bypass` finding for that
exact protection/actor; this is enforced by evidence validation, not only prose.

## Operator verification and the first-emission gate

This command performs admin-token GET reads and an atomic journal append. It
never constructs App credentials or writes GitHub:

```sh
openmerge setup rulesets --repo OWNER/REPO --app-id APP_ID \
  --authority openmerge --verify \
  --database-url "$SANDBOX_OPERATOR_DATABASE_URL" \
  --verification-plan verification-plan.json \
  --emission-receipt pre-emission-receipt.json \
  --operator 'operator name'
```

`GITHUB_TOKEN` is the separate repository-admin token with Administration read;
inherited policy may require organization-owner visibility. `--apply`, `--restore`, `--rollback-preflight` and `--rotate-break-glass`
cannot be combined with verification. Only `--verify` selects verification;
`--authority openmerge --config proposed.yml` continues through the setup
preview/install path, and restore and rollback preflight keep their setup paths. A failed/incomplete seeing read exits 1 and
records no verification. A visible violation is recorded and exits 1.
Successful reads print the source event ID, scope, findings, seeing-read time,
last observation time and last applied verification time. Proposed records do
not advance active ages. Zero timestamps mean never observed/verified.

The reviewed **internal** plan is JSON with `targets`, each containing `queue`
(the existing `store.QueueKey`) and `input` (the existing
`forge.WriteRestrictionInput`). It declares installation/repository/queue IDs,
full effective/coverage digests, declared and resolved actor, merge method,
default branch, the complete `BaseSelectors`, concrete `ExpectedRefs`, and App
ID. Include the default and concrete queue branch in `ExpectedRefs`, and every
target queue. This is an operator artifact, not a new public YAML/schema surface.
Compute the effective digest with `authority.EffectiveDigest` over the resolved
owner-policy/legacy digest and full selector coverage. PR 9 will supply the
resolved governing-policy integration. The verification command checks plan
bindings against queue identities and, for applied scope, the actual pinned
policy; proposed evidence alone cannot authorize activation.

Every invocation generates a cryptographic nonce **before** the seeing reads.
The store locks canonical queue IDs in increasing order and commits all target
events together. The dedupe key hashes scope/kind, queue key and nonce, using
`events_queue_dedupe`. Retry only the same read/records with the same nonce;
changed records under that nonce fail. A new seeing read always gets a new nonce,
even with an identical protection fingerprint. No migrations/indexes/pruning
are added. Proposed-event lookups page 256 rows using `events_queue_watermark`.

**The drain/backup gate applies to `--verify` itself, including proposed
verification on still-`github` queues. During period B, only isolated sandbox
verification may emit these events. Do not point this command at the live DB.**
The gate runs before DB access or GitHub reads. It is an operator-controlled
attestation; it does not automatically discover processes or their capabilities.

1. Inventory every old worker and journal/status reader on every host. Stop and
   drain them, recording host/service identities and UTC stop/drain times. Freeze
   writers while capturing the pre-emission boundary. Record the binary revision.
2. Capture a database backup with the operator's normal `pg_dump --format=custom`
   procedure. Retain its path, SHA-256 and UTC receipt. Restore it into an isolated
   database and compare queue seeds, ordered event IDs/payloads, cursors and refs
   evidence. Retain dump/restore commands, exits, versions and comparison hashes.
3. Write the receipt below. The credential-free connection identity is
   `HOST:PORT/DATABASE?search_path=SCHEMA` (an omitted schema ends in `search_path=`).
   Bind it to the exact operator connection. Keep the backup file available so
   the command can verify its hash. `restore_tested_at` must follow the drain.
4. Run the command only after ensuring that all restarted readers/workers use a
   compatible binary. Retain the receipt and its worker inventory outside the DB.
   There is no flag that skips this receipt, even for a proposed record.

```json
{
  "database_identity": "localhost:54329/isolated_sandbox?search_path=",
  "binary_revision": "REVIEWED_BINARY_COMMIT",
  "drained_workers": ["sandbox-host/worker", "sandbox-host/status-reader"],
  "drained_at": "2026-10-20T12:00:00Z",
  "backup_path": "/secure/evidence/pre-emission.dump",
  "backup_sha256": "ACTUAL_LOWERCASE_SHA256",
  "restore_tested_at": "2026-10-20T12:01:00Z",
  "compatible_readers_only": true
}
```

Legacy state/cursors still encode as snapshot v6. Authority/proposed evidence
requires v7; merely deploying the decoder does not upgrade legacy cursors.
After any new authority event or v7 cursor, switching to GitHub authority is
**not** binary rollback. Use compatible readers or restore the verified
pre-emission backup with all workers stopped. If later landings exist, reconcile
actual refs and recover their evidence before restarting; never resume a stale
backup blindly or delete journal evidence to downgrade.

## Unwired draft adoption

`queue.BindAuthorityDraft` requires exact landed `PolicyBinding` equality and a
fresh complete read with the same entire `Observation.Fingerprint`. This covers
IDs/revisions, enforcement, targets, conditions, rule parameters and visible
actors/modes, as well as actor, merge method, owner-policy/effective digest,
default branch and selectors through the policy binding. Hidden/incomplete or
mismatched reads leave the draft historical and the pin unverified.

`store.AdoptAuthorityPin` authenticates the proposed event ID against that
queue's bounded journal pages. It checks the governing config commit/digest,
pre-read source basis, and that the read began after the governing config event
and completed within the command's two-minute freshness bound. Adoption is
inside the pin event, appended under the queue lock. Repeated per-operation
calls for the same governing pin do not append again. Neither helper is wired
into activation in this PR. An unadopted draft requires applied verification
after activation; it cannot later be revived by a per-operation pin rewrite.

## Organization-sandbox acceptance runbook — not executed

The orchestrator and maintainer run this later on the Smart-Data-Ohio sandbox,
using its isolated DB/cache and reviewed compatible binary. Never use live DB
credentials or change this project's production refs.

1. Retain binary/config commits, effective and coverage digests, resolved actor
   IDs, target queue keys, full sanitized repository/inherited/classic GET bodies,
   fingerprint, UTC timestamps, baseline remote SHAs, and journal watermarks.
   Complete the drain/backup gate above, retaining the restored-backup evidence.
2. Run proposed verification on the GitHub-mode sandbox queue. Confirm findings
   (including a deliberately visible violation) change neither admission/next
   actions nor active authority state. Retain source event IDs and numeric exits.
3. After PR 9 activation, confirm the exact adoption binding against the landed
   config and fresh inventory. Exercise a changed rule ID/parameter, selector,
   merge method or actor, and a hidden read: each must leave adoption unverified.
4. With maintainer authorization, disable one managed write rule **on the
   sandbox only**. Record before/after API bodies. Invoke the dormant observer
   through the reviewed harness. Retain its `write_restriction_observed` event
   ID/basis, new source revision, typed finding and before/after queue state.
   New work must block; an already-started atomic landing must settle.
5. Repair the rule in the sandbox. Observe its visible structural repair while
   hiding the previously exposed unauthorized actor list: only structural
   findings clear. A hidden list and refresh/acknowledgement clear no actor
   finding. Restore exact intended actors; a suitable seeing runtime observation
   or applied admin verification must clear only `write_restriction`. Confirm
   unrelated source blocks/revisions remain intact.
6. Repeat verification with an identical fingerprint on a later day. Retain two
   distinct nonces/event IDs/times, restart/replay state, CLI ages and numeric
   exits. A read failure must leave state/ages unchanged and create no verification.
7. Restore all sandbox settings. Save final ref SHAs and sanitized read bodies,
   source event IDs/revisions, command exits and UTC times, then produce a SHA-256
   artifact manifest. Keep executed/failed/unavailable cases distinct.

## Original offline evidence

Executed on 2026-10-20 UTC. Final production source/binary revision:
`916d8a0fe1548fad9446b5deb0e71db60f904de4`; baseline:
`046de34aa7433d47782eab68da062ff3adc977ce`. The following evidence/test-only
commit changes no production Go source. PostgreSQL was 18.6 at
`localhost:54329`, using disposable schemas/databases; Go was 1.27.2.

All Go work held `/home/riels/.cache/openmerge-heavy.lock`, with
`GOFLAGS=-p=2 GOMEMLIMIT=4GiB` (6GiB for replay). Module/toolchain downloads
were disabled for final compatibility runs. Makefile, CI, test flags, public
schema, SQL migrations/indexes and all deployed wire fixtures are unchanged.

- **Full check, exit 0:** `flock /home/riels/.cache/openmerge-heavy.lock env
  GOFLAGS=-p=2 GOMEMLIMIT=4GiB OPENMERGE_TEST_DATABASE_URL=<isolated localhost:54329 URL>
  GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local make check`. Format, lint,
  `go test -race -short -count=1 -timeout=15m ./...`, and binary build passed.
  Log: `~/.cache/adr0008-4-make-check-verified.log`.
- **Strict offline live-journal replay, exit 0:** unchanged
  `scripts/check-live-journal-replay.py`, baseline checkout
  `~/.cache/adr0008-4-main`, `--strict-state --config-file .openmerge.yml`,
  using the frozen `~/.cache/om-pr72-fix1-input/{events,queues}.jsonl` exports.
  Event export SHA-256:
  `9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`;
  queue export SHA-256:
  `418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`.
  The 2,158 events plus initial state yielded **2,159/2,159** identical actions,
  structural states, snapshot hashes and wake-boundary checkpoints. Gates 2/4
  both PASS. Report: `~/.cache/adr0008-4-replay-receipt.json`. No comparator
  allowances or fixture changes were added. Initial attempts refused reporting
  after an untracked Python cache/source revision change; the accepted final run
  used a clean, fixed `916d8a0` tree.
- **Depth-1/4 contracts, exit 0:** unchanged
  `scripts/check-legacy-plan-action-depths-1-4.py ~/.cache/adr0008-4-main`:
  227,676 short-corpus checkpoints; 5,616,012 required-full-corpus checkpoints;
  196 fixture checkpoints. Exact action/wake hashes, respectively:
  `277bc31efbfd7c139b32069049f2a17fa66eaedf19e0ce5dc9a8ac0359365120`,
  `7aeea830f37d4b3dc0bc4ee69055e20833ed2d6eba8144ce159e58f156508ddb`,
  `6c3bd6e3c5ea4edff094712f6d95babbc949508b5ad9752020801d1ff6641554`.
  Log: `~/.cache/adr0008-4-depths.log`. That comparison began at `b1f8aee`;
  no previously deployed non-test Go file changed between it and `916d8a0`.
  Later changes were confined to new authority endpoints, comments and tests.
- **Simulator differential, both exits 0:** unchanged required
  `make test-sim-exits` in baseline/candidate; **146** normalized result blocks
  identical. SHA-256:
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
  Raw logs/report: `~/.cache/adr0008-4-simulator/`.
- **Legacy reporting, exit 0:** unchanged
  `scripts/check-legacy-reporting.py ~/.cache/adr0008-4-main`: all six checks
  byte-identical; preexisting serialized status fields and their order match
  under the unchanged comparator. Log: `~/.cache/adr0008-4-reporting.log`.
- **First-emission compatibility/backup, exits 0:** old decoder
  `e7000f8cc1df54372f5220617361f21aaca58b0a` preserves newly encoded v6 state
  and cursor wrapper bytes, and rejects v7 plus all three authority event types.
  V6/V7 fixture hashes and commands are in `~/.cache/adr0008-4-boundary/`.
  A custom-format, 21,692-byte pre-emission fixture backup was restored into a
  second isolated DB: one queue, one legacy event, one v6 cursor, migration
  receipts and ordered payloads matched exactly. Backup SHA-256:
  `bca5ce2a8d3428eaeaa2f2d5bd9f870cc47c1b8b93a8efba95e6c946879957fe`;
  restored-state SHA-256:
  `6537b57be21b4275197807f56d3b384b14f0b3d0cd14279f90271a5936c57dce`.
  Both fixture DBs were removed. Commands/exits/timestamps are retained in
  `~/.cache/adr0008-4-backup/report.json`. This is an isolated restore test,
  not a receipt for production workers or a production backup.
- **Paged query evidence, exits 0:** 5,201- and 15,201-event fixtures used
  `events_queue_watermark`, with 256-row pages. Complete lookups took 4.543ms
  and 12.357ms respectively in these compact fixtures; inspected-page execution
  took 0.046ms/0.037ms, returning 256 rows. These are fixture measurements,
  not production latency claims. Full `EXPLAIN (ANALYZE, BUFFERS)` output:
  `~/.cache/adr0008-4-query-plans/{small,large}.log`. The small run uses only
  an external test overlay changing the fixture size/boundary to 5,200 seed
  events; no production query, test flag definition or repository test changed.
- **Real-Git worker, race-enabled, exit 0:**
  `TestStartedRealGitLandingSettlesAcrossAuthorityViolation` used a fresh bare
  remote/cache, one merge batch, actual atomic base/fence landing, a concurrent
  independent permission append inside the read callback, then source violation
  and worker settlement. The append completes within a one-second callback
  deadline, proving no queue lock spans the read. Governing fixture commit:
  `576b768a751c09f6853dd5a21efdf841be67acee`; effective fixture digest:
  `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`;
  coverage digest:
  `05d268603d3ab908c77d154c74f63388f85742115e7a81513773c7c574ac87f9`;
  resolved team ID `200`. Observation event **11**, source revision **1**;
  confirmed landed and remote SHA both
  `9bb213bde1e0ab20cb263477e5607bde41743abb`. The violation remains after
  settlement. Raw trace: `~/.cache/adr0008-4-real-git-verified.log`. These are
  generated local Git fixtures, not GitHub enforcement evidence.
- **Finding/verification cases:** final full/focused checks cover proposed
  admission neutrality; exact/hidden/mismatched adoption; both concurrent append
  orders; hidden-after-violation and mixed repaired/unseen findings; digest,
  coverage and actor changes; same-binding governing commits; source isolation;
  duplicate retries including monotonic-clock metadata; daily identical
  fingerprints with distinct applied event IDs and advancing verification ages;
  restart; hypothetical clearing; generic emitter exclusion; failed reads; and
  mandatory unknown-actor findings. Retry/daily focused log:
  `~/.cache/adr0008-4-retry-daily.log` (exit 0).

[Artifact manifest and summary](adr-0008-4-artifacts.json) retain numeric outcomes,
source revisions, raw-log locations and SHA-256 hashes. The original local manifest hash
is `52f1349e5fd5c7ddef70e853d2f25800d91cd01e2790ce9bb56e40240709e43f`.
The implementation is below the ADR's split threshold, so this is one PR.

Remaining acceptance is the explicitly deferred organization-sandbox run above.
The internal verification plan is an interim operator input until PR 9's policy
resolver is wired. Classic inventory remains conservative for push validation;
this PR does not introduce a new classic content-policy translator. The original evidence predates the PR 3b integration; the rebase results below
record validation on main including PR 3b. No live verification, GitHub protection/ref writes, deployment or merge was
performed. Publishing the review PR uses the requested PR metadata operation.

## Rebase onto PR 3b on main

PR 112 is rebased onto `d4e15c8` (PR 111). Reconciled overlaps:

- `rulesets` registers one `--authority`, owned by `github.RulesOptions`.
  Only `--verify` dispatches to operator verification. Verification requires
  explicit `--authority openmerge` and rejects `--apply`, `--restore`,
  `--rollback-preflight` and `--rotate-break-glass` before credential construction,
  database access or GitHub reads.
- Non-verification commands retain PR 3b's `SetupRules` path, including
  `--config` preview/install, guarded restore, break-glass rotation, and rollback
  preflight's `RuntimeDriver` installation/private-key wiring. The existing
  authority planner, recovery, coverage and permission helpers are reused.
- Classic inventory and rollback preflight share PR 3b's wire validation,
  exact `Branch not protected` 404 handling and separate signatures read through
  `setupAPI.readClassicProtection`. Missing/malformed policy fields and explicit
  null signatures cannot certify verification. Unknown policy fields remain in
  PR 4's fingerprint. No second classic-response validator was introduced.
- Combined help describes setup preview/install/recovery/preflight and the
  explicit verification mode. CLI regressions exercise flag registration,
  a GET-only mocked install preview, verification dispatch/gating, authority
  requirements, incompatible flags, restore routing and preflight credentials.

Rebase verification executed on 2026-10-20 at production source revision
`4ec0c8e98ad9b9f5664e597db94558048dae22aa`. The final evidence-only commit
changes no Go source.
Every Go invocation used the required host lock and `GOFLAGS=-p=2`, with 4GiB
memory (6GiB for replay). Final tests/replay disabled module/toolchain downloads;
PostgreSQL used disposable test schemas at `localhost:54329`.

- `make check`: **exit 0**; formatting, zero lint issues, all short race tests
  including PostgreSQL, real Git and simulator, plus binary build. Log:
  `~/.cache/adr0008-4-rebase-check.log`.
- Focused PR 4 race suites: **exit 0**, no skips, across `cmd/openmerge`,
  `internal/authority`, `internal/forge`, `internal/forge/github`, `internal/queue`,
  `internal/store` and `internal/serve`, with run filter
  `Authority|WriteRestriction|Verification|Verify|Proposed|DailyApplied|StartedRealGit|RulesetsFlag|Emission|Classic`.
  Log: `~/.cache/adr0008-4-rebase-focused.log`.
- PR 3b goldens/fault boundaries: **exit 0**; exactly **32 apply / 30 restore**
  HTTP boundaries passed, all **three** main setup goldens remain byte-identical,
  and permission guidance/runtime HTTP traces passed unchanged. Log:
  `~/.cache/adr0008-4-rebase-fault-golden.log`. The golden files themselves
  also match `d4e15c8` byte-for-byte.
- Strict offline replay: **exit 0**, **2,159/2,159** matching state, structural
  state, actions, snapshot hashes and boundary checkpoints; gates 2/4 **PASS**.
  All **8,676** probes agree, maximum **12**, cap **128** not hit. Repeat
  determinism, all **22** self-tests and **7/7** synthetic checkpoints pass.
  Original `046de34` baseline, comparator, frozen exports and config are unchanged.
  Baseline/candidate/repeat stream SHA-256 remains
  `1fe079295539183b7008e70b940c7f71d45bd4aa4e443c8c1e581fa10d31051a`.
  Report: `~/.cache/adr0008-4-rebase-replay/report.json`.
- Accepted real-Git concurrency fixture: governing commit `e10c9662f7918fddb21ab5a188fd6a0e93314c75`,
  landed/remote SHA `1b6c917b1f694aa96d9d4c1f83e6288926713d79`, event **11**, source revision **1**,
  team ID **200**; effective and coverage digests match the original evidence.
  Generated local commit OIDs differ from the original run and are retained
  with this run's log; no production ref was involved.

The first focused attempt failed the new test fixture's credential expectation;
correcting its mock installation token/API prefix made the complete focused run
pass without changing production behavior. All final log, binary and replay
stream hashes are recorded in `adr-0008-4-artifacts.json` under
`rebase_onto_pr_111`; the original summary remains historical. Updated manifest
SHA-256: `6d0a6464e9a74fcac96694374623f577bbaf3726a4ada31569928197e8aedcb7`.

## Operator implementation refinement (#114)

`openmerge admin acknowledge --queue <queue-id>` uses the numeric durable
`queues.id`, including for retired queues. Read the queue identity with
`openmerge queue status --json` or `GET /v1/queues`
on the operator API, then resolve its numeric ID using the operator SQL lookup in
[the operating guide](../operating.md#acknowledging-outside-pushes). Current status
responses expose identity fields, not `queues.id`.
This refines the implementation notation; the accepted ADR Decision is unchanged.

Verification emission receipts expire after **24 hours** by default. Both
`drained_at` and `restore_tested_at` must be within the bound; future timestamps
are rejected. Set a positive duration with `--emission-receipt-max-age 24h`.
A stale receipt requires a fresh drain inventory, backup and restore test.

Until [Docker/release revision wiring #118](https://github.com/CanadaApollo6/openmerge/issues/118)
lands after ADR 0009 period B, operators must use a binary built with `make build`
from a clean git checkout for `--verify` and `admin acknowledge`. The shipped
Docker build excludes `.git` and does not embed the fallback; it cannot attest.
This is an operator prerequisite for acknowledgement; its two-file gate checks
receipt files rather than binary revision metadata.

`binary_revision` must exactly match the running binary's effective embedded
revision. `openmerge version` prints `commit` and `attestable`: only use a commit
with `attestable: true`. It selects Go build information's `vcs.revision` first,
then `authority.BuildRevision` when VCS revision metadata is absent. Custom
release tooling may embed the reviewed commit with
`-ldflags '-X github.com/CanadaApollo6/openmerge/internal/authority.BuildRevision=COMMIT'`;
`openmerge version` also prints that fallback commit. Dirty builds show their
revision with `attestable: false`; binaries with neither source print
`commit: unknown` and `attestable: false`. Verification rejects either case with
`cannot attest BinaryRevision offline` and directs operators to rebuild with
`make build` from a clean git checkout. Offline matching checks consistency with local build
metadata, not binary provenance, signatures, remote worker versions, worker drain,
or restore success; these remain operator attestations. The gate uses one combined
receipt, so the two-file distinctness rule for `admin acknowledge` does not apply.

If an applied verification basis changes during its seeing read, the command
retains the appended evidence, exits nonzero, and asks the operator to retry
`--verify`. That stale read does not advance the active verification time.
