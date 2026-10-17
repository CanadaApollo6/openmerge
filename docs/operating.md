# Operating OpenMerge

Build with `make build` and complete the [GitHub App and ruleset setup](../README.md#running-openmerge-serve)
before starting a service. The repository's default branch needs a valid
`.openmerge.yml`, and CI must run on pushes to `openmerge/queue/**`. Use one server per App and a
process-exclusive git cache. Postgres must be available before startup; startup
applies database migrations and ensures the fence without resetting it.

For containers, use the [compose quickstart](../README.md#docker-compose-quickstart).
For recorded queue state and PR explanations, enable `OPENMERGE_API_LISTEN=:8081`
and use `openmerge queue status [--json]`. The [API guide](api.md) documents the
bearer token requirements, response contract, CLI and exit codes. No database or
GitHub credentials are needed by an API client.

## Installation discovery and onboarding

One `serve` process serves one GitHub App, across its installations. At startup,
every `--discovery-interval` / `OPENMERGE_DISCOVERY_INTERVAL` (default `10m`),
and after a verified `installation` or `installation_repositories` webhook, it
lists installations and their repositories. Webhook payload lists only request
a listing; they never grant or revoke repository access.

To onboard a repository:

1. Install the App on it. An organization owner may need to approve access.
2. Run `openmerge setup rulesets --repo OWNER/REPO` with a repository-admin token,
   following the setup preview/apply instructions in the README. Protect every
   selected base and its fence.
3. Enable CI on pushes to `openmerge/queue/**`.
4. Commit `.openmerge.yml` to the default branch. The next discovery sweep serves
   it without a service configuration change.

A private App can only be installed on its owner's account. An organization
needs its own App (`openmerge setup github --owner ORG`) and its own `serve`
process. Processes may share Postgres, but each needs a distinct
`--git-cache-dir` / `OPENMERGE_GIT_CACHE_DIR`, `--webhook-listen` /
`OPENMERGE_WEBHOOK_LISTEN`, and `--api-listen` / `OPENMERGE_API_LISTEN`.

`--github-installation-id` / `OPENMERGE_GITHUB_INSTALLATION_ID`, `--repos` /
`OPENMERGE_REPOS`, and `--base` / `OPENMERGE_BASE` are optional, deprecated
**narrowing allowlists**. The warnings name the replacement: App installation
access; App repository access plus default-branch config; and `queues[].base`,
respectively. With none given, every discovered configured repository is served.
Repository selectors are comma-separated, case-insensitive `owner/name` globs
(e.g. `--repos 'MyOrg/*,me/project?'`); `*` and `?` stay within a path segment.
An allowlist cannot add a repository or base absent from App access/config.

Only a successful complete listing can establish removal or suspension. A
confirmed config-file deletion or allowlist exclusion also stops admission and
new landing starts. The independent `repository-removed` block reports a paused
status where credentials permit it. A landing that has started always settles
through its recorded fence; losing access leaves it unsettled until access
returns. Transient listing/config reads preserve durable state. Unreadable
configuration stops the affected worker in memory and is retried; invalid
configuration has its own durable block. Reinstatement never clears operator,
landing, config, or rules blocks owned by another observer.

Each installation has its own driver, token and atomic request budget. Settlement
has the last 10% of the observed window reserved. Active testing/landing gets
capacity before idle polling, and reporting yields first. Discovery and rules
each retain a 1% minimum share. A validated 304 returns its local reservation;
uncertain requests conservatively consume capacity. Hard GitHub cooldowns apply
to all requests. Unknown budgets remain explicitly unknown until headers arrive.

`openmerge queue status` includes discovery; `openmerge queue status --discovery`
(or `--discovery --json`) shows every known repository's state: `served`,
`validating`, `unavailable`, `not configured`, `invalid config`, `removed`,
`suspended`, or `dormant base`,
plus last successful sweeps and remaining budget per installation. Repositories
without config get no workers or comments. The read-only `/v1/discovery` endpoint
uses the same authentication as queue status.

## Migrating a Phase 1 deployment

1. Upgrade using the existing flags and environment. They now narrow discovery.
   Confirm in `openmerge queue status --repo OWNER/REPO` that the existing queue,
   history and fence remain present. Installation IDs remain decimal strings;
   repository casing is compared case-insensitively and the stored spelling is
   retained. Queue names never change partition identity.
2. Remove `--github-installation-id`, `--repos` and `--base`, **and unset**
   `OPENMERGE_GITHUB_INSTALLATION_ID`, `OPENMERGE_REPOS` and `OPENMERGE_BASE`
   in the environment file. A remaining environment value keeps the allowlist
   in effect even when its flag is removed.
3. Restart and inspect `openmerge queue status --discovery` and queue history.
   Dropping `--base` is safe when it names the default branch. Otherwise first
   add a queue with that `base` to the default-branch config; without that
   selector the old queue becomes dormant/retired and admits no new work.

Keep a compatible binary for rollback once new block sources are recorded;
restoring single-queue YAML does not remove events from the log. Never reset
fences, edit stored events, or run two processes against the same git cache.

## Multiple bases and branch lifecycle

Repositories are discovered from every installation of the configured App. Put all queue policies in each repository's
**default-branch** `.openmerge.yml`; a release branch's file cannot override it.
Use `queues[].base` for literals or globs, for example `release/*`. Omitted `base`
selects the observed default branch. Every matching concrete base gets its own
serial worker, protection/rules checks and `openmerge/fence/<base>` fence.
Protect every selected base and its fence before enabling the selector.

At startup and every `--discovery-interval` / `OPENMERGE_DISCOVERY_INTERVAL`
(default `10m`), the server lists all App installations, their repositories, and configured branches using paginated conditional requests.
Verified branch create/delete notifications also request a fresh listing.
Subscribe the App to branch `create` and `delete` events when using webhooks; deliveries only wake
reconciliation and never establish branch existence. Polling repairs missed
deliveries. OpenMerge's internal `openmerge/*` refs are excluded from expansion.
Failed listings keep the previous expansion, log a warning and retry with
backoff. They never pause or resume a queue. Branch refresh honors the rules
cache; a transient rules-read failure keeps the previous rules and lifecycle.

`--base` / `OPENMERGE_BASE` is now an optional, deprecated comma-separated
branch/glob allowlist. Existing `--base main` deployments keep their exact
installation/repository/main/empty-lane key, history and fence. To serve release
queues, add their selectors to the default-branch config and remove or widen
both the flag and its environment value. The allowlist cannot create a queue
absent from config. A selector overlap blocks only the involved queue entries;
other entries continue serving. Fix the selectors and let branch refresh retry.

A deleted base becomes **dormant**. A removed queue entry or narrowed selector
makes its old bases **retired**. Neither admits PRs nor starts landings; logs and
fences are retained. Recreating a base first settles any previous attempt through
its fence and repeats overlap/protection validation before activation. Do not
reset the fence or remove its stored history to reactivate a queue.

Every worker cycle settles in-flight landings before notifications, config,
rules, PR reads or admission. Config errors, operator pauses and read failures
cannot bypass settlement. Startup scans historical queues before discovery, including repositories/bases excluded by current `--github-installation-id`, `--repos`,
`--base` or config, and recovers their attempts with settlement-only workers.
Unavailable credentials or a missing base can leave an attempt unsettled;
access returning permits a retry, and never implies an abandoned push.

`openmerge queue status --repo owner/repo` lists all its served bases; add
`--base release/v1` for one queue. The API list is dynamic as branches appear.
The `state` field / CLI `STATE` column is `active`, `dormant`, `retired`, or
`overlapping`. `active` may still have independent pauses. Startup settlement
and validation are in-memory gates and do not append lifecycle events. An
always-active queue, including an existing single `main` queue, writes no
lifecycle events on upgrade or restart.

Once a real lifecycle transition is recorded, its `lifecycle_changed` event
requires a binary that understands it. Reactivation does not remove that event:
an older binary cannot replay that queue, even after restoring single-queue
configuration. Keep a compatible binary when rolling back. Downgrading to an
incompatible binary requires restoring a database backup from before those
events were recorded; do not edit the log.

Follow-up: prune workers for long-dormant queues to bound worker and polling
costs. Retain their logs and fences, settle recorded landings first, and restore
the same queue identity when a matching branch returns.

## Queue CI provenance

For every merge method, CI must run on `push` events for
`openmerge/queue/**`. A check run counts toward `merge_when` only when its check
suite's `head_branch` exactly equals the batch's test ref name, for example
`openmerge/queue/main/b7`, without `refs/heads/`. GitHub Actions suites triggered
by that push report this branch name; the repository's
[CI workflow](../.github/workflows/ci.yml) enables these pushes.

Each build publishes a unique tested SHA: a composed commit for merge, squash,
or rebase, or an empty marker above the pinned head for fast-forward. The SHA
and suite branch together bind check runs to that queue execution. A contributor
can copy a published commit into a fork and open another PR; even a newer pass
from the same App on that SHA must not replace pending queue CI. Suites with
null, missing, or other `head_branch` values are ignored, rather than counted as
failed. Filtering happens before newest-run selection and source ambiguity
checks, both during refresh and immediately before landing. Reruns of a matching
suite remain valid; a pending rerun revokes its older result.

Commit statuses have no suite or branch provenance and remain accepted on the
tested SHA. GitHub Actions jobs for fork PRs cannot post statuses with their
read-only token. External CI services that post commit statuses must not build
arbitrary fork SHAs and report those results in the base repository: otherwise
a copied queue commit could authorize landing. Prefer check runs with matching
queue push suites for external CI.

### Remedy for `unbound-check-provenance`

This diagnostic means a check named in `merge_when` has runs on the tested SHA,
but none belongs to the queue push suite. The PR check, status API `why`, and
logs name the check, App source, observed `head_branch` (including `null`), and
expected test ref. Configure the provider to create check suites for the queue
**push**; GitHub sets `head_branch` on push-created suites. Alternatively, report
commit statuses on the tested SHA, subject to the fork-trust caveat above.
The batch keeps waiting for bound CI; a timeout retains reason `checks-timeout`
and includes the provenance diagnostic in its detail.

The additive wire-v1 `check_provenance_observed` event records diagnostic changes
and recovery so explanations survive restarts. Existing event/action payloads
and fixtures remain unchanged. Drain older workers before upgrading, because
older binaries reject the new event type.

## Run as a systemd user service

Create the installation directory and install the binary:

```sh
mkdir -p ~/.local/bin ~/.config/openmerge
install -m 755 bin/openmerge ~/.local/bin/openmerge
```

Create
`~/.config/openmerge/environment` with absolute paths appropriate to your account:

```ini
OPENMERGE_DATABASE_URL=postgres://openmerge:password@127.0.0.1:5432/openmerge?sslmode=disable
OPENMERGE_GITHUB_APP_ID=123
OPENMERGE_GITHUB_PRIVATE_KEY=/home/you/.config/openmerge/app.pem
OPENMERGE_GIT_CACHE_DIR=/home/you/.local/state/openmerge/git-cache
OPENMERGE_POLL_INTERVAL=60s
OPENMERGE_LOG_LEVEL=info
```

Set the database host, port, credentials and database name to match your
Postgres instance. The example uses port `5432`; `make db` uses `54329`.

At startup, OpenMerge reads `/app` with its App JWT for the slug, then
`/users/<app-slug>[bot]` for the numeric **bot user ID**, which differs from the
App ID and installation ID. Generated commits use `<app-slug>[bot]` and
`<bot-user-id>+<app-slug>[bot]@users.noreply.github.com`. Explicit
`--git-committer-name` / `--git-committer-email` flags or
`OPENMERGE_GIT_COMMITTER_NAME` / `OPENMERGE_GIT_COMMITTER_EMAIL` environment
variables override discovery. For example:

```ini
OPENMERGE_GIT_COMMITTER_NAME=my-openmerge[bot]
OPENMERGE_GIT_COMMITTER_EMAIL=789+my-openmerge[bot]@users.noreply.github.com
```

If bot user discovery fails, serving continues with
`<app-slug>[bot]@users.noreply.github.com`; a WARN log gives the manual settings
and lookup needed for numeric account attribution. Squash and merge
commit authors use the PR author's login and numeric GitHub no-reply email.
Rebase preserves the original author name, email and author date. All generated
commits, including fences and replayed commits, use the configured committer.

Protect the environment file and App key:

```sh
chmod 600 ~/.config/openmerge/environment ~/.config/openmerge/app.pem
mkdir -p ~/.config/systemd/user ~/.local/state/openmerge
```

Save this as `~/.config/systemd/user/openmerge.service`:

```ini
[Unit]
Description=OpenMerge merge queue

[Service]
Type=simple
EnvironmentFile=%h/.config/openmerge/environment
WorkingDirectory=%h/.local/state/openmerge
ExecStart=%h/.local/bin/openmerge serve
Restart=on-failure
RestartSec=5s
TimeoutStopSec=180s
UMask=0077

[Install]
WantedBy=default.target
```

Start and inspect the service:

```sh
systemctl --user daemon-reload
systemctl --user enable --now openmerge.service
systemctl --user status openmerge.service
journalctl --user -u openmerge.service -f
```

For operation after logout, enable lingering for the service account with
`loginctl enable-linger "$USER"` (your system may require an administrator).
The sample uses polling and needs no public listener. To enable webhooks, add
`OPENMERGE_WEBHOOK_LISTEN=127.0.0.1:8080` and `OPENMERGE_WEBHOOK_SECRET` to the
environment file, and configure the same secret and HTTPS ingress in the App.

## Logs and recovery

Logs are JSON on stderr, captured by the journal. `--log-level` or
`OPENMERGE_LOG_LEVEL` accepts `debug`, `info` (default), `warn`, or `error`; explicit
flags override the environment. INFO records committed admissions and dequeues
with reasons, built test refs, check results, landing starts, landings,
abandonments, pauses/resumes, and configuration errors. Each transition carries
`repository`, `base`, `pr`, `batch`, `sha`, and `reason` where applicable; queue-wide
events use PR zero, and unavailable batch/SHA fields are empty. DEBUG adds
reconciliation cycles and quota backoff deadlines. Unchanged polls and repeated
check results do not emit duplicate INFO transitions.

Logs are best effort, not the durable event journal. A process crash can leave a
committed transition unlogged. On restart, OpenMerge resumes from Postgres,
refreshes observations, and resolves any uncertain landing through its fence.
Do not reset or delete `openmerge/fence/*` refs to recover a service.
Every active block is listed with its source and detail in status, why, and the
CLI. Source set/clear observations are logged independently, even when another
source already blocks the queue. A writer can post `@openmerge refresh` to clear
`operator`, `action-poisoned`, and `build-failed` blocks, reset build-retry backoff,
and request fresh observations. It clears a `landing-not-on-base` block only
after a fresh ancestry check confirms the durable
landing commit is on the base. Config, ambiguous check sources, repository rules,
and queue lifecycle clear through their own recovery observations; refresh never
clears them. Build retries also reset on base or effective config changes and
successful builds. Legacy shared pauses appear under `operator` and can still be
cleared by refresh; no production path currently emits new `operator` blocks.

| Source | What sets it | How it clears |
| --- | --- | --- |
| `config` | Invalid or unreadable default-branch configuration | Automatically after a successful configuration read and validation |
| `check-sources` | Ambiguous providers for a named admission or batch check | Automatically after a complete recheck finds unambiguous sources across the queue |
| `rulesets` | Definite repository or inherited ruleset problems | Automatically after a complete passing inspection of that source |
| `classic_protection` | Definite base or fence classic-protection problems | Automatically after a complete passing inspection of that source |
| `permissions` | Missing required installation permissions | Automatically after a complete passing inspection of refreshed grants |
| `lifecycle` | A dormant, retired, or overlapping queue | Automatically when the base/configuration is eligible again, after settlement and validation |
| `operator` | Legacy shared pause events; no current production emitter | A writer's `@openmerge refresh` |
| `action-poisoned` | A durable action reaches three decode/validation incidents | A writer's `@openmerge refresh`, which also resets poison incident counts |
| `build-failed` | Non-conflict build failures reach the retry limit | A writer's `@openmerge refresh`, which resets the streak and backoff even below the limit; base or effective config changes also reset retries |
| `landing-not-on-base` | Fresh verification cannot find the durable landing commit on the base | A writer's `@openmerge refresh`, only after fresh ancestry verification confirms that commit is on the base |

Every new definite failure advances its source's revision even if already
blocked, so a clear prepared before that failure cannot remove it. Each clear
affects only its own source.

The additive wire-v1 `block_source_blocked` and `block_source_cleared` events
require a compatible binary once recorded. Clearing blocks does not remove these
events: an older binary cannot replay that queue. As with `lifecycle_changed`,
downgrading to an incompatible binary requires restoring a database backup from
before those events were recorded. Do not edit the event log to downgrade.

## Case-variant history recovery

`conflicting case-variant queue histories` means an older deployment created
separate rows for different spellings of the same repository. Admission fails
closed. In discovery mode, each persisted queue ID still settles its own landing.
In configured-target (flag) mode, startup fails fast on
`ErrFenceOwnershipRequired` or a fence-read failure; use discovery mode to keep
settlement running during reconciliation. Do not rename
or delete rows, reset fences, merge event logs, or copy command receipts between
histories.

In the SQL below, replace `LANE` with the exact lane (empty for the default
lane), including in the advisory-lock key. Escape single quotes in every SQL
string substitution by doubling them: a base named `release/it's-ready` becomes
`'release/it''s-ready'`.

1. Back up the database. Identify every row for the installation/base/lane,
   including its exact repository spelling:

   ```sql
   SELECT id, installation, repository, base, lane, initial_fence
   FROM queues
   WHERE installation = 'INSTALLATION' AND lower(repository) = lower('OWNER/REPO')
     AND base = 'BASE' AND lane = 'LANE' ORDER BY id;
   SELECT queue_id, id, recorded_at, payload FROM events
   WHERE queue_id IN (FIRST_ID, SECOND_ID) ORDER BY id;
   SELECT queue_id, key, convert_from(value, 'UTF8') FROM observations
   WHERE queue_id IN (FIRST_ID, SECOND_ID)
     AND (key LIKE 'cursor/%' OR key LIKE 'comment/%') ORDER BY queue_id, key;
   ```

   Compare `openmerge queue status --json`, the landing-start/result events,
   current entries and batches, every block source/revision, and command cursors
   and receipts. Replay with `Store.Load` rather than inferring current state
   from the last event alone.
2. Leave the recovering server running until **every** recorded landing has a
   real result. Restore missing installation access first if necessary. Its
   workers resolve each attempt against the actual base and fence, even while
   canonical admission is blocked. A timeout or missing credentials is not
   abandonment. If the fence cannot establish an outcome, retain the block and
   investigate the recorded push; do not manufacture `Landed` or abandonment.
3. Stop every server that can write these partitions before maintenance. Choose
   the history whose replayed `Fence` matches the actual
   `openmerge/fence/BASE` token and whose command receipts belong to the last
   serving worker. With no usable cached choice and multiple fenced histories,
   discovery reads that ref and selects only the unique history whose replayed
   `Fence` equals its SHA (the last initialized, landed, or abandoned fence
   commit). A failed read, no match, or multiple matches blocks admission and
   writes no canonical mapping. Event watermarks and lifecycle/access events
   never establish ownership. If fence and receipt evidence disagree, investigate
   the original worker's logs/backup before selecting. The selected row's ID,
   spelling, receipts and cursors stay intact. A live unfenced variant alongside
   any fenced history is a conflict, even if the fenced history has drained;
   it cannot adopt the fence and bypass the fenced history's command receipts.
4. Drain entries from each unselected history using a small maintenance program
   **inside this repository** (so it can import `internal/store` and
   `internal/queue`). Connect with `store.Open`, inspect with `db.Load`, and
   append the following existing events for each entry, using its current
   admission ID. Refuse to run while any loaded batch is `queue.Landing`:

   ```go
   for _, entry := range state.Entries {
       _, err := db.Append(ctx, discardedID, []queue.Event{
           queue.DequeueRequested{PR: entry.PR},
           queue.Dequeued{PR: entry.PR, AdmissionID: entry.AdmissionID,
               Reason: queue.Requested},
       }, "")
       if err != nil { return err }
   }
   ```

   Run the reviewed program with `mise exec -- go run ./PATH`, using an explicit
   maintenance database URL. This cancels non-landing work through the reducer;
   it does not edit history. Re-enqueue any still-wanted PRs with fresh commands
   after service recovery. Inspect `state.Blocks(queue.Config{})` on **every**
   variant. Repair each independent safety problem before clearing its own
   source with `db.Append`: `queue.BlockSourceCleared{Source: source, Basis:
   state.BlockSourceRevision(source)}` or `queue.RulesSourceCleared{Source:
   source, Basis: state.RulesSourceRevision(source)}` for a rules source. Reload
   before each clear. Do not clear unrelated holds to make selection succeed;
   if a hold must remain, keep its history selected. Multiple histories with
   unresolved holds must remain blocked until those causes are reconciled.
5. Record the chosen ID explicitly, including when correcting a **wrong cached
   selection**. Keep all servers stopped. Reload every variant with `db.Load`;
   refuse this step if any is landing, or any unselected history has entries,
   an active batch, or an unresolved independent safety hold. Confirm the
   selected history's replayed `Fence` still equals the actual fence ref SHA and
   inspect its receipts/cursors. Do not use `initial_fence` alone: subsequent
   landing/abandonment events advance the fence. If ownership cannot be proven,
   leave admission blocked and investigate; changing the mapping cannot repair
   a mismatched fence.

   Run this SQL against the explicit maintenance database, substituting the
   inspected partition and `SELECTED_ID`. It changes only the canonical mapping,
   preserving all histories and receipts. The partition predicates prevent
   selecting an unrelated queue ID. Apply the quoting and lane substitutions
   above consistently. Require exactly one returned row:

   ```sql
   BEGIN;
   SELECT pg_advisory_xact_lock(1330464082,
     hashtext('INSTALLATION' || '/' || lower('OWNER/REPO') || '/' || 'BASE' || '/' || 'LANE'));
   INSERT INTO canonical_queues
       (installation, repository, base, lane, queue_id, variant_ids)
   SELECT q.installation, lower(q.repository), q.base, q.lane, q.id,
       ARRAY(SELECT v.id FROM queues v
             WHERE v.installation=q.installation
               AND lower(v.repository)=lower(q.repository)
               AND v.base=q.base AND v.lane=q.lane ORDER BY v.id)
   FROM queues q
   WHERE q.id=SELECTED_ID AND q.installation='INSTALLATION'
     AND lower(q.repository)=lower('OWNER/REPO') AND q.base='BASE' AND q.lane='LANE'
   ON CONFLICT (installation, repository, base, lane) DO UPDATE
     SET queue_id=EXCLUDED.queue_id, variant_ids=EXCLUDED.variant_ids
   RETURNING queue_id, variant_ids;
   COMMIT;
   ```

   Call `db.CanonicalQueue(ctx, selectedKey)` and require the inspected ID. Reload
   and verify its fence/receipts again. A cached ID still present in the variant
   set remains selected when drained or when another legacy row is imported;
   an imported variant with live state, a landing, or an independent hold
   blocks admission instead of flipping that selection. Resolve that conflict
   with the same procedure. Never depend on the order of maintenance appends.
6. Restart serving and inspect status. The selected queue validates normally;
   the others remain settlement-only. Confirm one new PR lands and historical
   commands receive no new acceptance replies. Keep the backup and the inspected
   IDs/events as the recovery record.

## Runtime repository rules

Serve reads the repository's actual default branch, lists **all pages** of
repository and inherited rulesets, and fetches each detail at startup. It repeats
this every ten minutes (`--rules-interval` / `OPENMERGE_RULES_INTERVAL`), at the
next worker cycle. Reads share the driver's conditional cache and quota budget.
The same cycle checks classic protection on the base and its exact
`openmerge/fence/<base>` ref, plus the granted permissions from the installation
token response. Each source (`rulesets`, `classic_protection`, `permissions`)
owns its block and revision. Definite findings block even when another part of
that source's read fails; partial reads retain its previous findings too. A
complete passing inspection clears only that source's block. An unreadable
classic protection endpoint cannot veto ruleset or permission recovery.
A read failure (including network, 5xx, quota/rate limit or authentication errors)
logs WARN and preserves only that source's previous state: healthy queues continue,
and an existing block for that source stays blocked. Failed reads retry after one minute,
then two, four, and at most five minutes, capped by `--rules-interval` if shorter.
The worker wakes for that deadline even if its normal poll interval is longer;
the driver's shared quota cooldown can defer the read further.
Classic protection the installation is not permitted to read (HTTP 403 or a
generic 404, typically because the App has no Administration permission) is
unavailable, not failed: it logs WARN, keeps that source's previous state, does
not hold back activation of a new or restarted queue, and is read again after the
normal `--rules-interval`, so granting the permission takes effect at the next
interval rather than after one minute.

Rules blocks appear in the API, PR check, journal and logs. Every definite
failure advances its source's revision, including unchanged problems, so an older
passing read cannot clear a newer confirmed failure for that source. Passing
checks on healthy sources append nothing. A later complete passing check appends
`RulesSourceCleared` with that source's current basis; generic pauses, action poison,
build failures and `landing-not-on-base` remain independent. An operator
`@openmerge refresh` cannot clear a known rules block. All active reasons are
shown by status/API, including `rules_sources` with per-source problems and revisions.
Blocks still permit `ResolveLanding`: a push already in
flight must settle truthfully through its fence.

The new `rules_source_blocked` and `rules_source_cleared` events extend wire v1.
Stored `rules_blocked` and `rules_cleared` events retain their payloads and basis
semantics and fold into the `rulesets` source. Existing event/action fixtures
remain unchanged. A legacy `PauseRequested` with
reason `repository-rules` remains a generic pause on replay; after repairing its
cause, a writer must clear it with `@openmerge refresh`. A passing rules check
never emits a generic resume during upgrades.

The runtime uses only the App installation token. GitHub documents
[ruleset list/detail reads](https://docs.github.com/en/rest/repos/rules#get-a-repository-ruleset)
as requiring Metadata read, which the manifest already grants. It also documents
that bypass actors are returned only to callers with write access to the ruleset.
The App has no Administration permission, so bypass lists can be hidden.

Policy: enforce everything visible, including ref conditions and the fence's
creation/update/deletion/non-fast-forward structure. A **hidden** bypass list
produces a WARN rather than a pause caused solely by that missing information.
Validation assumes an App-only always bypass for that ruleset while checking its
visible structure. A visible empty/incorrect bypass list, malformed conditions,
missing rule details, or structurally unprotected fence still pauses. This avoids
wedging an otherwise working installation but does **not** prove exclusive fence
ownership or base/queue push permission when bypass actors are hidden.

Run `openmerge setup rulesets` with the separate repository-admin token before
deployment and after policy changes to audit full bypass ownership. That command
continues to fail closed on hidden lists; never give its admin token to serve.
Classic protection reads use the installation token, never the setup admin
token. GitHub requires [Administration read](https://docs.github.com/en/rest/branches/branch-protection#get-branch-protection)
for this endpoint. The manifest does not request it; without that grant, the
read can be unavailable. Only a 404 explicitly saying `Branch not protected`
establishes no classic protection. Generic 403/404 responses log WARN and keep
state. Continue the manual protection audit whenever these reads are unavailable.

Visible classic required PR reviews without an App bypass, required status
checks, signed commits, locks, or push restrictions excluding the App are
reported as problems. Linear history blocks `merge` mode on the base; squash,
rebase, and the single-parent fence are compatible. Method-dependent validation
is repeated after base configuration loads or changes. Both base and fence
updates are fast-forwards from their leased tips, so disallowing force pushes
does not block these operations; `--force-with-lease` supplies compare-and-swap.
Classic status requirements are conservatively rejected in favor of an App-bypass
ruleset and explicit `merge_when` CI, rather than assuming GitHub will accept the push.

Installed permissions are compared to the [required set](../internal/forge/github/README.md#setup-and-ruleset-validation).
Missing `contents: write` or `workflows: write` logs ERROR and blocks through
`RulesBlocked`; other missing grants log WARN. Diagnostics name each permission,
required access, and actual access. Grants refresh with the token (about 55
minutes), or after restarting serve. A new blocked queue defers fence creation
and records `fence_initialized` after recovery; this additive wire-v1 event only
fills an absent token and never replaces a recorded fence. Changed PR condition
inputs use the new `pr_metadata_observed` event. Both have golden fixtures;
existing event/action types and fixtures are unchanged.

Protection checks are periodic observations, not an atomic policy lease; do not weaken fence ownership while
OpenMerge is running. Runtime validation does not modify repository policy.

## Upgrade or restart

1. Build the desired revision with `make build` and run its documented checks.
2. Stop intake with `systemctl --user stop openmerge.service`. SIGTERM cancels
   bounded work and releases claims; uncertain landing attempts remain durable.
3. Back up Postgres before an upgrade that includes migrations. Keep the previous
   binary and check migration compatibility before attempting a downgrade.
4. Install the new binary as `~/.local/bin/openmerge`. Keep the same database,
   App credentials and git cache. If changing the unit, run
   `systemctl --user daemon-reload`.
5. Run `systemctl --user start openmerge.service` and inspect `status` and the
   journal for startup/configuration errors and queue progress.

For an environment-only change or a routine restart, use
`systemctl --user restart openmerge.service`; systemd rereads the EnvironmentFile.
Do not run another process against the same git cache during replacement.

## How landed PRs appear on GitHub

OpenMerge pushes exactly the tested commit to the base branch. Merge commits
retain the original PR head, so GitHub recognizes the merge. Squash and rebase
replace that history: GitHub shows those PRs as **Closed**, rather than **Merged**.
OpenMerge posts an idempotent **Landed in [commit]** comment linking the tested,
landed SHA and then closes each PR. This is expected; no second GitHub merge is
performed to change the badge. PR title/body edits after the test-ref build do
not alter the commit that lands.

## Landed head cleanup

`serve --delete-head-branch` / `OPENMERGE_DELETE_HEAD_BRANCH` defaults to true.
Set it to false to retain heads by default. A top-level `delete_head_branch`
boolean in the default branch's `.openmerge.yml` overrides this default for that
repository; omission inherits the flag. See the
[configuration reference](../internal/config/README.md).

Immediately after `LandingStarted`, before pushing, the executor records
`LandingCleanupPolicyRecorded{BatchID, AttemptID, DeleteHeadBranch}` with the
effective cleanup choice. The first decision for an attempt is immutable.
After freshly verifying a successful landing, the executor records
`HeadCleanupRequested{BatchID, LandedSHA}` before `Landed`, including during fence
recovery. The intent refers to the original PR heads already pinned by the
batch's stored events. Only an enabled attempt policy and a matching intent
followed by `Landed` schedule `DeleteHeadBranch`, with an idempotency key pinned
to PR, original head, and landed SHA. A failed append leaves the landing
recoverable; retries can repeat the intent without duplicating cleanup. For squash/rebase, cleanup also waits
until `MarkLanded` is acknowledged.

Before deletion, the executor rechecks that the landed commit remains on the
base. `Driver.HeadBranchForDeletion` checks the closed PR's repository, original
head, base/default branch, protection, open PRs targeting the head, and open PRs
sharing the repository-qualified head (`owner:branch`) across every base. Read
errors fail closed and retry. Forks, protected heads, OpenMerge refs,
base/default branches, and heads used by other open PRs are retained. Missing or
changed heads are no-ops. `Git.DeleteBranch` uses an exact-head SHA lease to
preserve concurrent pushes. Eligibility reads and deletion are not atomic:
GitHub provides no transaction covering PR usage or protection changes and a git
push. Protect long-lived integration branches or disable cleanup if that
transaction guarantee is required.

Configuration changes affect cleanup choices for future attempts. Started
attempts and pending cleanup actions keep their recorded choice across restarts
and retries. On upgrade, attempts without a cleanup-policy record retain their
heads, even if recovery records a cleanup intent. A crash between the start and
policy records also retains the heads. The `landing_cleanup_policy_recorded`
event adds a wire v1 type and golden fixture; existing event/action payloads and
fixtures are unchanged. Drain old workers before upgrading, because older
binaries reject the new type.

The durable pending-CI writes remain unchanged. Skipping a reopen based on an
unlocked projection would race a concurrent passing `Land` preflight: a delayed
success could restore success without invalidating the old generation. The why
view instead groups identical consecutive reopen/refresh pairs and explains
whether the fold actually revoked recorded success. Stored evidence is retained.
