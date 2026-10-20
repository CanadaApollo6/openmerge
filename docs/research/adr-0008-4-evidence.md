# ADR 0008 PR 4: source persistence and operator verification

Implementation base: `046de34` (main). Trial class and measured acceptance
results are recorded below after offline verification. PR 3a remains Hold:
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
inherited policy may require organization-owner visibility. `--apply` cannot
be combined with verification. A failed/incomplete seeing read exits 1 and
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

## Offline evidence

Results and artifact hashes are added here after the required checks complete.
