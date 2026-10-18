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
plus last successful sweeps, remaining budget and webhook health per installation,
and last-delivery age per repository. Repositories
without config get no workers or comments. The read-only `/v1/discovery` endpoint
uses the same authentication as queue status.

### Organization membership permission (Stage A prerequisite)

New `openmerge setup github` manifests request organization **Members: read**
(`members: read`) for resolving `org/team` owners and approver pools. To use
organization teams in approval policy, an existing App must add that organization
permission in App settings, and each affected organization installation must
accept the requested addition in its installed App settings. Updating the App's permission declaration alone does not update
an installation's grant. OpenMerge inspects the permissions on the actual
installation token; renewed tokens (or restarting `serve`) observe acceptance.

**During the ADR 0009 dogfood trial, leave the live App's permissions unchanged,
including `members: read` and its current `actions: write` status.** Exercise
permission adoption only on an isolated sandbox App and installation until
both trial periods drain. Merging these dormant capabilities does not authorize
a deployment; the ADR 0007 differential deployment gates still apply.

`members: read` is needed only for organization team owners or groups in
approval policy; see this operating guide before adopting it. Its absence adds
no server warning or block for queues without such a policy, including personal
and user-only queues. Inspect an installation explicitly with read-only setup
diagnostics (use sandbox credentials during the trial):

```sh
openmerge setup permissions --app-id APP_ID --installation-id INSTALLATION_ID --private-key /path/to/private-key.pem
```

Setup reports the actual missing grant as an optional warning. The dormant
`ValidateTeamPolicyPermissions` hook is reserved for PR 11a to call only when a
queue's effective approval policy references team owners or groups. There is no
runtime caller yet; public approval configuration remains disabled. At Stage A
activation, unresolved named identities will pause only affected queues, and an
unreadable effective owner requirement will block its change rather than imply
empty membership.

Identity-bound grants compare the permission response's stable user ID with the
review or comment author ID. A renamed or reassigned login cannot lend another
account's write permission. A `WriteGrant` HTTP 404 is an unknown observation
(`ErrIdentityUnavailable`), not known-empty permission; PR 11a must report the
lookup as unavailable and discard the grant. A successful identity-bound response
with a non-writing permission is a known denial. Team membership is read by stable organization and
team IDs, includes child-team members exposed by GitHub, and is unknown if any
page fails or the observation exceeds its bound. These reads share installation
budgets and cannot spend settlement's reserve.

A token authenticates an account; it cannot prove that a person rather than an
agent holding that token acted. Use separate bot or App identities for agents
so they do not consume their human owner's approval.

## Webhook setup and health

Polling works without a public endpoint: leave `--webhook-listen` /
`OPENMERGE_WEBHOOK_LISTEN` unset and every queue polls at `--poll-interval` /
`OPENMERGE_POLL_INTERVAL` (default `1m`). Webhooks reduce idle API traffic and
wake affected queues immediately. Trust, CI and landing decisions always use
fresh forge reads; delivery payloads never authorize a transition.

To enable webhooks:

1. Expose a public HTTPS endpoint through your ingress and route it to a private
   listener such as `--webhook-listen 127.0.0.1:8080`. Keep the status API on its
   separate listen address.
2. Set the App's webhook URL and enable delivery with JSON content. New Apps can
   use `openmerge setup github --webhook-url https://HOST/PATH`. For an existing
   polling App, enable **Webhook → Active** in its settings and create a random
   secret, for example with `openssl rand -hex 32`.
3. Set the same secret in GitHub and `OPENMERGE_WEBHOOK_SECRET` (or
   `--webhook-secret`). Manifest setup saves it in the App's private config
   directory as `webhook-secret`; follow the README's export example.
4. Subscribe to `pull_request`, `pull_request_review`, `check_run`, `check_suite`,
   `status`, `push`, `create`, `delete`, and `issue_comment`. The `installation`
   and `installation_repositories` events arrive automatically. Check a GitHub
   delivery's response and `openmerge queue status --discovery` after restart.
   Trigger a real repository event (for example, update a pull request) to verify
   health: GitHub's `ping` has no installation and cannot mark it healthy.

An installation is healthy for `--webhook-health-window` /
`OPENMERGE_WEBHOOK_HEALTH_WINDOW` (default `30m`) after each verified delivery,
including ignored event kinds/actions and redeliveries. The timestamps are
process-local, never journal events: every restart starts unhealthy. Each
repository must also have received a verified delivery in this process within
the health window before its idle queues can use the backstop. Until its first
delivery, and after its own window expires, it keeps `poll_interval` even if
other repositories keep the installation healthy. Eligible idle queues poll at
`--backstop-interval` / `OPENMERGE_BACKSTOP_INTERVAL` (default `10m`). At health expiry they resume the
normal `poll_interval`; a quiet installation therefore falls back to fast
polling. For this purpose, idle includes building and queued-but-not-testing
queues; planner deadlines still apply. Queues in testing or landing, or with an
unsettled attempt, always keep `poll_interval`, whatever the health. Planner,
validation and rules deadlines can
wake workers sooner; webhook wakes do not bypass error or quota backoff. While
waiting on the backstop, each worker checks the database event watermark every
`poll_interval`. Any changed history triggers a normal reconciliation, which
re-plans ready work and recomputes planner deadlines before an idle queue returns
to its backstop. Unchanged checks consume no forge API budget and never write
durable state. Activity checks and webhook wakes both respect error and quota
backoff.

Every attributable verified delivery records an installation and repository
observation and wakes matching queues for a fresh read. Delivery IDs deduplicate
durable refresh hints for typed payloads across restarts. Ignored or incomplete
payloads only set an in-memory wake/refresh hint and write no delivery rows.
Discovery and branch refreshes also deduplicate delivery IDs in a bounded,
listener-local cache, including deliveries before matching workers exist. The
cache resets after 4096 distinct deliveries; an evicted redelivery may request
another coalesced sweep. Startup already runs a full discovery sweep. Health is
renewed only after any required delivery persistence succeeds. Invalid signatures
never update health, request discovery, wake queues or consume forge budget. They produce only a
fixed-cardinality rejection counter in logs, emitted at powers of two with no
request data, so a flood cannot create one log entry per request.

When rotating the secret, update both the App setting and the server secret,
then restart the listener. During a mismatch, signature rejections appear in
logs, health expires, and normal fast polling resumes; the restart also resets
health. Trigger a real repository event after rotation to verify delivery.

Status/API show each installation's health and last verified delivery time, and
each repository's last-delivery age (`never` before a delivery this process).
Inspect the repository ages even while the installation is healthy: deliveries
for one repository do not prove another's webhook is working. See the
[API fields](api.md#endpoints).

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

Before downgrading to a binary that accepts only one queue, remove extra queues
and unsupported config keys. This alone does not make a Phase 1 rollback safe:
new lifecycle/block-source event types require a compatible replaying binary,
or restoration of a database backup from before those events. Health/backstop
scheduling itself adds no durable event types or migration. Keep the backup and
previous binary when upgrading; restoring single-queue YAML does not remove
events from the log. Never reset
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

## Repository CI checks

The repository requires both `check` and `sim-exits` in `.openmerge.yml`'s
`merge_when`. They run in parallel on PRs and queue pushes. Queue policy is read
from the default branch, so a new required check takes effect after the policy
change lands there.

`check` runs `make test`: the Go suite under `-race -short`, with an explicit
15-minute per-package timeout. Simulator safety regressions, mutation-style
checks, first-day adopter/adaptive harnesses, and compact/full fake equivalence
remain in this path. The target for `internal/sim` is under three minutes on CI;
the timeout also accommodates the separate database/server packages.

`sim-exits` runs `make test-sim-exits` without race instrumentation and with a
60-minute package timeout. It runs the entire simulator suite, including all
17 legacy exits, Team and Slow's eight-seed 30-day adopter exits in both
cancellation modes, first-day adaptive harnesses, cancellation savings, serial
capacity comparison, and admission evidence. It omits `-sim-adaptive-full-exit`;
the full adaptive exit remains opt-in. Independent adopter cancellation modes
run in parallel, capped at four tests to match the CI runner's four vCPUs. The
target for this required job is under eight minutes on CI.

The non-required `sim-adaptive-full` job in [sim-full.yml](../.github/workflows/sim-full.yml)
runs `make test-sim-adaptive-full`: the pinned eight-seed 30-day paired
adaptive/fixed-size exit in both cancellation modes, without race instrumentation,
with a 60-minute package timeout and four-test concurrency. It runs on pushes to
`main`, nightly at 06:00 UTC, and via `workflow_dispatch`. It does not run on PRs
or `openmerge/queue/**` pushes and is not in `.openmerge.yml`, so this full
adaptive evidence does not gate landing.

Workload inputs, output records, and assertions are unchanged. Direct
`go test` invocations remain full-length unless passed `-short`, so the mutation,
compatibility, and admission scripts continue to exercise their original tests.

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

Queues may opt into one same-commit retry of failed GitHub Actions checks with
`checks.retry_failed: 1`; the default `0` preserves immediate failure. Non-Actions
check runs and commit statuses report unsupported. Only effective merge-policy
failures are targets; accepted skipped/neutral checks and satisfied alternatives
are excluded. Retry observations bind the producer, workflow/job attempt and
queue test ref, with a separate deadline from the original check run.

```yaml
version: 1
queues:
  - name: default
    merge_when: [check-success = ci]
    checks:
      retry_failed: 1
```

Updating the manifest does not change an existing App. The App owner must add
**Actions: Read and write** in the App's permission settings. GitHub then prompts
each installation to approve the change. Until approval, a retry-enabled queue
has the same outcomes as retry-off while events/status still show retry state,
and logs a WARN. After the App owner approves Actions, retries activate on the
next installation-token renewal (up to about an hour) or immediately on a serve
restart; the WARN persists until then.

`actions: write` is broader than rerun-failed-jobs: it also permits cancellation,
artifact/cache management and deployment approvals. OpenMerge uses only the
rerun-failed-jobs endpoint for retries and the workflow-run cancel endpoint for cancellation.

Each workflow dispatch writes a durable intent before its POST and acceptance
immediately after. A lost response or restart leaves an uncertain intent; the
worker requires an increased workflow `run_attempt` to prove acceptance; status
alone is insufficient. An uncertain intent never authorizes a repeated POST. A
durably recorded definite refusal permits re-dispatch only under a fresh fenced
intent. A crash between intent and POST can consume the retry without starting
CI. Reruns are always bounded by the retry deadline, using one hour when
`check_timeout: 0`. Workflow and job URLs remain in the flaky evidence shown in
status/API output.

If Actions permission is lost while a rerun is outstanding, including during
reconciliation of uncertain acceptance, OpenMerge confirms the failure immediately
with "Actions permission lost during rerun; rerun outcome unknown" in dequeue
details, PR status and `why`. The refused read cannot prove CI success, failure or
timeout. This can dequeue a PR whose rerun passed, keeping landing fail-closed.

Drain older workers before upgrading. After the first retry event is written,
rolling back to an older binary requires a database restore (ADR 0002/0009).

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
The sample uses polling and needs no public listener. For delivery setup and
scheduling defaults, follow [Webhook setup and health](#webhook-setup-and-health).

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
`RulesBlocked`; other missing grants, including `actions: write`, log WARN.
Diagnostics name each permission,
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

## Deploying mid-trial

Follow [ADR 0007's trial deployment gates](adr/0007-changes-not-prs.md#trial-deployment)
and the [dogfood materiality rule](research/phase2-dogfood.md). Check release
eligibility first: passing replay does not waive the hold on PRs 8, 9 or 11.
These are operator commands; the replay tool never connects to Postgres or GitHub.

1. Save a full backup before installing anything. Keep the deployed binary and
   its exact source SHA (for example `a4b173f`), credentials and environment.

   ```bash
   export PGHOST="$HOME/.local/share/openmerge/run" PGDATABASE=openmerge PGUSER=postgres
   stamp=$(date -u +%Y%m%dT%H%M%SZ)
   backups="$HOME/.local/share/openmerge/backups"
   exports="$HOME/.local/share/openmerge/exports"
   mkdir -p "$backups" "$exports"
   # Capture before pg_dump: a conservative lower bound for rollback checks.
   backup_max_event_id=$(PGOPTIONS='-c default_transaction_read_only=on' \
     psql -XAt -v ON_ERROR_STOP=1 -c 'SELECT COALESCE(max(id), 0) FROM events')
   printf '%s\n' "$backup_max_event_id" > "$backups/openmerge-$stamp.max-event-id"
   pg_dump -Fc --file="$backups/openmerge-$stamp.dump"
   ```

2. Export both inputs in one read-only, repeatable-read snapshot. The unusual
   CSV delimiter/quote bytes preserve JSON text literally: ordinary text COPY
   escapes backslashes, and ordinary CSV adds quoting. JSON encodes control
   characters, so these bytes cannot occur literally inside a JSON row.

   ```bash
   PGOPTIONS='-c default_transaction_read_only=on' psql -X -v ON_ERROR_STOP=1 <<SQL
   BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
   \copy (SELECT json_build_object('id', id, 'queue_id', queue_id, 'type', type, 'payload', payload, 'dedupe_key', dedupe_key, 'recorded_at', recorded_at)::text FROM events ORDER BY id) TO '$exports/events-$stamp.jsonl' WITH (FORMAT csv, DELIMITER E'\x01', QUOTE E'\x02', ESCAPE E'\x02')
   \copy (SELECT row_to_json(q)::text FROM queues q ORDER BY id) TO '$exports/queues-$stamp.jsonl' WITH (FORMAT csv, DELIMITER E'\x01', QUOTE E'\x02', ESCAPE E'\x02')
   COMMIT;
   SQL
   ```

   Keep exports private; they contain repository data. Never commit them.

3. In the candidate checkout, compare against a dedicated checkout of the exact
   deployed commit. All historical `ConfigApplied.commit` and legacy `BaseMoved`
   config blobs must already exist in local Git history. No fetch or Go module
   download happens inside the script; provision the Go toolchain/dependencies
   beforehand. The candidate's code comes from its working tree; use a clean
   checkout at the intended release SHA so the reported HEAD identifies it.
   Both checkouts must have empty `git status --porcelain` output, including
   untracked files; the tool refuses dirty trees and records both commits and
   clean flags. Its mandatory startup self-tests must pass before replay can
   produce a PASS report; their result is recorded in that report. The synthetic
   journal also runs through both actual checkouts and any test overlays. Its
   compatibility result is reported separately from the real export: a planner
   branch unvisited in the export can still fail the mandatory synthetic gate.

   ```bash
   git worktree add --detach "$HOME/openmerge-deployed-replay" a4b173f
   python3 scripts/check-live-journal-replay.py \
     --baseline "$HOME/openmerge-deployed-replay" \
     --events "$exports/events-$stamp.jsonl" \
     --queues "$exports/queues-$stamp.jsonl" \
     --config-file .openmerge.yml --out "$exports/replay-$stamp.json"
   ```

   Gate 2 checks each checkpoint after **every event**. `actions_identical: N/N`
   requires byte-identical wire-v1 action payloads, action keys and order, and
   identical planner revision. `first_action_divergence` identifies the first
   event that differs, separately from state comparison.

   State comparison walks the full `queue.State` structurally through reflection,
   including unexported and nested fields, ordered slices, sorted map keys,
   dereferenced pointers with nil distinguished, normalised times and type names.
   Duplicate canonical map-key dumps (including distinct pointers to equal
   pointees) and pointer/map/slice cycles fail closed with the field path.
   Baseline and candidate paths and JSON types must match. Any candidate-only
   field, including a zero-valued field, fails unless its exact path is in the
   script's reviewed exceptions list with a reason and ADR/PR reference; listed
   fields must still be zero-valued. Record the exceptions used in the deploy
   evidence. Snapshot comparison remains an additional diagnostic;
   `--strict-state` also requires original snapshot hash equality.

   At every checkpoint and the final state, the tool strictly compares planner
   current next wake, deadline sets and actions at each union deadline minus 1ns, exactly at
   the deadline, and plus 1ns. It also enumerates `NextWake` from the zero clock
   to include elapsed deadlines still represented by the state: asking only for
   future wakes at a late observation would miss timeout boundaries.
   Boundary instants are deduplicated, and the report
   records their counts. Hitting the per-checkpoint cap fails closed.

   Production restarts rebuild queue state by **full journal replay**. Queue
   snapshots are not persisted or restored by the server. The snapshot codec is
   used by the outside-push audit progress in `internal/store/outside_push.go`;
   decoding/re-encoding an old queue snapshot does not model a server restart.
   The replay gate therefore does not use that conversion as its comparison target.

   `ChangeRevisions` and `BatchRevisions` are the conditional PR 1 derived
   projection allowance. Where a baseline projection is non-empty, the candidate
   must match it exactly. Only an empty baseline allows a new candidate
   projection. The tool reports non-empty checkpoint counts, folds the candidate
   twice, and requires identical derived fields at every checkpoint
   (`derived_determinism`). Every present record must contain its applicable
   ChangeID/Revision/HeadSHA/Basis/LogPosition fields, with Revision > 0 and a
   non-empty HeadSHA. The schema check rejects accountability, classification,
   approvals and unknown fields. Offending paths, values and event IDs are
   retained in the report.

   Exit 0 requires gates 2 and 4 to pass. Exit 1 reports a failed gate; exit 2
   is a tooling/input error and supplies no passing evidence. The report records
   all three full stream paths beside the report, SHA-256 hashes of those canonical
   JSONL files (including trailing newlines and final policy records), export
   hashes, old/new SHAs and live-config content hash. Streams retain original
   snapshot bytes and exact wire action bytes as base64 for the baseline and
   both independent candidate folds. Keep streams private alongside the exports.
   With no `--out`, the report and streams are saved under
   `~/.cache/openmerge-replay/`. Use `--candidate /path/to/checkout` when running
   the tool against a separate clean candidate checkout.

4. Require gate 4 `PASS` too. It is always enabled: `--config-file` selects the
   live config blob from **candidate HEAD**, supplied unchanged to both loaders;
   omission discovers the standard config paths. Both recomputed policy digests
   must match the latest recorded `ConfigApplied` digest and the offline
   reconciler comparison must emit no `ConfigApplied`, including same-digest
   window initialization. Missing policy history fails closed. Gate 4 here means
   **config digest + would-emit-ConfigApplied only**. The ADR's broader “no new
   event types on shadow replay” clause is not proven by this tool.

   Replay uses each event's `recorded_at` as a deterministic planning clock,
   `serve.worker.plannerConfig`, and the historical policy at each transition.
   Before `ConfigApplied`, it uses the config at the observed base tip with an
   empty planner digest. The export cannot recover wall-clock idle cycles or
   historical CLI overrides. Confirm the defaults match the deployment:
   `--poll-interval-seconds 60`, `--default-branch main`, and head cleanup enabled
   (use `--keep-head-branch` for `serve --delete-head-branch=false`). A config
   file's explicit cleanup setting overrides that flag, as on the server.
   Compilation or unsupported input errors fail closed. `API_ADAPTERS` is a
   descriptive label for the probe's API surface, not an enforced compatibility
   registry or proof that all worker behaviour is exercised.

   Attach this report, exact commands, config identity, export watermark and
   the complementary evidence below to the deploy record. The watermark includes
   max event ID, max `recorded_at`, latest `ConfigApplied` digest (per queue),
   event count and queue count. This tool alone is not release authorization.

5. Only after all gates and complementary checks pass, build with `make build`
   and stop the service with `systemctl --user stop openmerge.service`. Before
   installing, re-check the export watermark with this exact read-only query:

   ```bash
   PGOPTIONS='-c default_transaction_read_only=on' psql -X -v ON_ERROR_STOP=1 <<'SQL'
   BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
   SELECT json_build_object(
     'max_event_id', (SELECT max(id) FROM events),
     'max_recorded_at', (SELECT max(recorded_at) FROM events),
     'event_rows', (SELECT count(*) FROM events),
     'queue_rows', (SELECT count(*) FROM queues),
     'latest_config_applied', (
       SELECT COALESCE(json_agg(row_to_json(c) ORDER BY c.queue_id), '[]'::json)
       FROM (
         SELECT DISTINCT ON (queue_id) queue_id, id AS event_id,
           payload->'data'->>'digest' AS digest
         FROM events WHERE type = 'config_applied'
         ORDER BY queue_id, id DESC
       ) c
     )
   );
   COMMIT;
   SQL
   ```

   Compare every watermark value to the replay report. If any moved, re-export
   both inputs using step 2 and rerun the checks before installing; keep the
   service stopped through this final export, verification and install. Once
   fresh evidence passes, run `install -m 0755 bin/openmerge "$HOME/.local/bin/openmerge"`
   and `systemctl --user start openmerge.service`. Do not alter live configuration
   or permissions as part of an inert deployment.
6. Inspect `journalctl --user -u openmerge.service --since '10 minutes ago'` and
   queue status. Confirm real processing continues: look for `enqueued` and
   `test ref built`, not just successful startup. Check for config/replay errors.
7. Add a [dogfood log entry](research/phase2-dogfood.md#log) with the deployed SHA,
   report location, commands/results and maintainer classification **before
   inspecting period metrics**. Tooling/docs-only changes are non-material when
   they do not change workflows/runners, batch planning, landing or check
   observation; log that classification without restarting the periods.

### What a PASS does not show

Replay covers the exported states and policies, event-time plans and bounded
boundary probes. It does not exercise executor/landing mechanics; forge
observation (check-run selection, PR reads or rules); the store layer; PR #53's
outside-push audit loop or audit snapshot decoding; inter-event timing beyond
the boundary probe; CI workflows/runners; or states/policies absent from the
export. Timer paths exercised by neither the export's enumerated deadlines nor
the synthetic journal are not covered by this gate; the synthetic journal
currently covers the checks-timeout family. Equal wire actions do not establish equal execution or external reads.

Require named complementary evidence **for each deployed PR**, recorded before
deploy alongside its SHA and replay report:

- A reviewer/maintainer materiality verdict naming the reviewer, covering
  workflow/runner and executor/forge diffs as well as planning changes.
- `scripts/check-phase2-compatibility.py` results against the deployed baseline,
  and the required `sim-exits` result (`make test-sim-exits`), with commands,
  compared commits and real exit statuses.
- For audit-adjacent changes, `scripts/check-outside-push-isolation.py` results
  and evidence for audit snapshot decoding when affected.
- For build/landing changes, the merge-method commit-ID check: show that each
  supported merge method lands the exact tested commit, recording both commit
  IDs and the check result.

Forward-only events (for example PR #53's `outside_push_recorded`) make binary
rollback unsafe once written: an older decoder cannot read them. Before choosing
binary-only rollback, stop the service and run this read-only query using the
saved backup watermark (add every event type unsupported by the old binary):

```bash
backup_max_event_id=$(cat "$backups/openmerge-$stamp.max-event-id")
PGOPTIONS='-c default_transaction_read_only=on' psql -X -v ON_ERROR_STOP=1 \
  -v backup_max_event_id="$backup_max_event_id" <<'SQL'
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SELECT id, queue_id, type, recorded_at
FROM events
WHERE id > :'backup_max_event_id'::bigint
  AND type IN ('outside_push_recorded')
ORDER BY id;
COMMIT;
SQL
```

The saved watermark precedes `pg_dump`, so this check is conservative if events
arrived during the backup. Any returned unsupported event rules out binary-only
rollback; zero rows permit it only after checking all other decoder/schema
compatibility constraints. Otherwise restore the pre-deploy `pg_dump` backup
into an appropriate empty database before starting the old binary. A restore
loses post-backup journal writes and requires deliberate operator reconciliation;
keep both the backup and evidence.

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

### Speculative testing with an adaptive window

A queue may set `speculation.max_depth` above 1 to test a dependent chain:

```yaml
queues:
  - name: main
    merge_when: [check-success = ci]
    speculation:
      max_depth: 4
      min_depth: 1
    batch:
      max_size: 8
      max_wait: 5m
```

Both depths default to 1; require `1 <= min_depth <= max_depth <= 32`.
A new queue starts with W = `max_depth`. Every landing (including a bisection
half) grows W by one up to that ceiling. A confirmed checks failure or timeout
of an original batch halves W, rounding down with a floor of `min_depth`. Build
conflicts do not measure CI health and leave W unchanged. Bisection failures do
not halve it again. Provisional failures, invalidations, cancellation and
duplicate observations do not change W. Idle
queues retain W; configuration changes clamp it to the new bounds. A shrinking
window blocks new admissions and does not cancel admitted work (a configuration
change independently invalidates unstarted landings as before).

The default stays 1 per ADR 0009 until its dogfooding
criteria are met. Explicit depth-1 defaults retain the pre-speculation config
digest, so upgrading unchanged YAML does not discard active work or pending
bisection groups. Changing either setting invalidates
unstarted landings under the existing configuration protocol.

Every admitted batch occupies a slot until it lands or is invalidated, including
passed batches waiting behind predecessors. Each successor tests on the previous
batch's landing commit. Fast-forward requires strictly descending heads and tests
a unique marker above each landing head. Landing remains serial, leased on the
settled predecessor and fence, with fresh checks and exact-head trust gates.

At depth above 1, the oldest batch automatically gets one same-commit rerun of
failed required checks, even when `checks.retry_failed` is omitted or set to 0.
Depth 1 keeps its opt-in retry policy. Later failures are provisional until all
predecessors settle; an invalidated predecessor discards those results without
blaming their changes. Confirmed failure invalidates the suffix and bisects the
oldest batch, with both halves ahead of unrelated build retries and fresh work.
Admission conservatively stops behind any provisional or failed batch, even
while free window slots remain. This policy is retained: an eight-seed one-day
comparison admitted 35 extra wasted adaptive-profile runs when that block was
removed, with slightly worse median and p95. See the
[planner-backed evidence](research/phase2-chain.md).

Adaptive batch size is separately opt-in:

```yaml
batch:
  max_size: 8
  max_wait: 5m
  target_pass_rate: 0.8
```

`target_pass_rate` must be a number strictly between 0 and 1. The planner counts
checks-failure and timeout dequeues against those dequeues plus landed changes,
using the last 200 such changes in log order. Conflicts and other dequeues are
excluded. Below 20 samples it uses `max_size`; afterward it chooses the largest
k <= `max_size` with `(1 − p̂)^k >= target`, never below one. Equality passes using
exact integer/rational comparisons of the target's canonical decimal; no floating
point exponentiation or logarithm decides admission. A policy digest change
restarts this history and follows the existing invalidation/regrouping protocol.
While policy is unchanged, pending bisection and infrastructure retry groups keep
their sizes as the estimate updates. Leave the target unset to preserve fixed
batch sizing.

At small volumes a 200-change history can be several weeks old. Treat this as a
size ceiling, not a prediction of independent CI outcomes: interacting failures
and runner overload can make the estimate misleading. Inspect its sample count,
CI timeout dequeues and the last window failure before raising depth or changing
the target. A lower target permits larger batches and more failure-isolation work.

Keep `max_depth` at 1 unless the repository has at least `max_depth` CI runners
available for these checks. This guidance is per repository; multiple base queues
multiply concurrent runs and must share that runner capacity. The exit evidence
validates depth 4; depths above 4 are unvalidated. Forge-side runner queueing counts toward
`check_timeout`: with two runners and depth four, queued runs can time out and
cause bisection or dequeues before they finish.

Speculative batches permanently discarded by the planner request best-effort CI
cancellation for their recorded runs. This includes stale tested commits after a
new PR head, dequeue or closure, and suffixes invalidated behind a failure.
Landing and landed batches never request cancellation; becoming the oldest batch
alone never makes a batch a cancellation target. Depth-1 queues keep their
existing bisection/dequeue behavior and do not request cancellation, even with
retries enabled. Late results cannot revive discarded batches. Test refs are
cleaned, including a fresh cleanup action after a delayed builder reports.

GitHub Actions cancellation needs **Actions: Read and write** (`actions: write`),
already requested by `openmerge setup github` for retries. Existing Apps and
installations must approve that permission as described above. The optional
capability cancels only workflows attributed to a recorded job, tested SHA,
queue ref and attempt. Unidentified runs, other CI providers, and newer attempts
that were not recorded are left alone. Missing capability or a permission 403
records `unsupported`; invalidated runs then finish and count as wasted. Quota
and transient errors retry without blocking another batch's landing.

`check_cancellation_requested` records intent before the first call. Restarted
workers repeat the same idempotent cancellation. An accepted cancel POST means
`requested`; only a forge read reporting cancellation or completion means
`confirmed`. Neither state proves landing eligibility. A completed run can
therefore be confirmed even when cancellation arrived too late to save runner
time. Pending reads, repeated 404s, timeouts and other forge errors finish the
current action and persist a delayed successor with its own key. New queue work
remains claimable while it waits. Each cancellation phase gets eight total
attempts, with waits of 30s, 1m, 2m, 4m, 8m, 16m and 30m. Confirmed subsets are
retained across retries and restarts. An unsupported result switches to a separate
read-only follow-up phase with the same budget. Branchless commit statuses use
status provenance; Actions checks must still match the queue ref. These reads
update outcomes while retaining `unsupported`. If either phase exhausts its
budget, status records `abandoned` and the final error or pending reason. Work may
continue at the provider after abandonment; no further cancellation calls or
follow-up reads are scheduled for that intent.

Use `openmerge queue status --repo OWNER/REPO --pr N` (or `--json`) to see the
change's complete recorded CI run ledger: batch, tested SHA, producer/job/workflow
identity and attempt, outcome, cancellation status, and wasted-work reason.
Reruns keep separate rows. A change also appears in successor runs whose tested
commits contain it. Legacy aggregate events have no run identities; status labels
those rows as aggregates instead of inventing IDs. The status API adds
`run_ledger` and `gate` to entries, and current `window` and `batch_size` to queues
when runtime configuration is available. `failure_estimate` contains `failures`,
`samples`, display-only `rate` (p̂), and the optional `target_pass_rate`.
`last_window_failure` identifies the queue-fold `revision`, `batch_id` and `reason`
that last halved W. This revision is an ordinal within the queue log, not a global
database event ID. These inputs also appear in human CLI output. Chain, position, batches ahead and
invalidation reasons remain visible. Gate names explain checks, predecessor
settlement, bisection, `max_wait` and a full window. `landing-preflight` includes
approval: approval and other final merge conditions are evaluated by the executor
at preflight, so that gate does not distinguish approval separately. The existing
PR check summary
includes a separate bounded queue paragraph with W, fresh-batch size, p̂ and
sample count, last window failure and an eight-batch chain excerpt, followed by
up to eight recent ledger rows;
API/CLI retain the full recorded history.
No new summary comment is posted.

Cancellation adds wire-v1 events `batch_runs_observed` (all accepted CI names,
separate from the retry-policy subset), `check_cancellation_requested` and
`check_cancellation_reported` and `check_cancellation_deferred`, and actions
`record_check_cancellation` and
`cancel_checks`, plus the read-only `observe_retired_checks` follow-up and scheduled
`retry_check_cancellation` successors. Empty or unchanged history reads emit no
`batch_runs_observed`; simultaneous historical attempts keep separate ledger rows.
Queues that start at depth 1, with or without retries, use their existing protocol
exclusively. After a downgrade from a higher depth, in-flight speculative intents
keep emitting cancellation events until they drain.
Pending intents survive snapshot encode/decode. Historical run
ledgers are reconstructed from journals, not stored unboundedly in snapshots.
Store retention/compaction remains disabled; enabling it must preserve ledger
history or explicitly define a retention window.

The additive wire-v1 chain types require compatible workers. Drain old workers
before upgrading. Returning to depth 1 does not make an older binary
able to replay those records; rollback across their first use requires a database
restore. Existing depth-1 event/action wire types and fixtures are unchanged.
Journals written by pre-merge heads of PR 4 are intentionally unreadable;
compatibility is claimed only with `origin/main`.

The snapshot codec writes version 3 and reads versions 2 and 3. Production queue
state is rebuilt from journals; queue snapshots are not persisted or restored.
Outside-push audit progress uses the codec separately. The codec retains W,
its bounds and last failure, each batch's once-only failure marker, the ordered
last-200 outcome ring and pending cancellation confirmations. The full encoding
must be a byte fixed point across encode/decode, including retained confirmations.
A configured queue must have a non-null window; unsupported versions are rejected
before validating their schema.
Persistence/compaction and historical ledger retention remain separate work.

Speculative `config_applied` events add optional `window` bounds so replay can
clamp at the exact policy transition; a digest alone cannot recover those bounds.
No new event/action type is needed. Default depth-1 events omit the field. Existing
speculative journals receive one same-digest bounds initialization without
invalidating admitted batches or clearing their outcome history; adaptation starts
from their old fixed ceiling. If policy changes during the upgrade, reconciliation
first reads the policy at the recorded config commit and verifies its digest,
records its fixed ceiling, then clamps W under the new policy. Historical policy
read or digest failures stop that transition. A previously serial queue carries
W = 1 into a higher ceiling. Mixed old/new speculative workers cannot read the added bounds;
drain old workers before upgrading.

## Auditing protection before a policy-authority switch

`openmerge setup import-protection` is a read-only audit usable with today's
`github` policy authority. It makes only GET requests, uses no database, creates
no refs and changes no GitHub settings. Use a **separate repository-admin**
`GITHUB_TOKEN`, as for `setup rulesets`, with Administration read, Contents read,
Checks read and Commit statuses read. Complete inherited bypass lists may require
an organization owner. Team membership reads additionally need organization
members visibility; unreadable memberships remain unacceptable authority blockers.
A 403 or generic 404 on a required protection read is a permission error, never
a clean partial report; only GitHub's explicit `Branch not protected` response establishes
that classic protection is absent.

```sh
openmerge setup import-protection --repo OWNER/REPO
openmerge setup import-protection --repo OWNER/REPO --json > protection-report.json
openmerge setup import-protection --repo OWNER/REPO --base release/1.0 --out /tmp/proposed-openmerge.txt
# After reviewing the report's exact stable IDs, repeat --accept for each gap:
openmerge setup import-protection --repo OWNER/REPO --accept ITEM-ID --out /tmp/proposed-openmerge.txt
```

The report always includes the default branch and every existing base selected
by the default-branch configuration, excluding `openmerge/` refs. Repeat `--base`
to add concrete audit targets; it cannot hide configured bases or make an
unconfigured default branch served. Configuration and governing CODEOWNERS are
read at the default branch's pinned commit. Workflows and comparison CODEOWNERS
are read separately from each served base's pinned tree. Concurrent branch or
protection changes still require a fresh audit before switching.

Each item has a stable ID, its base and source, a proposed configuration fragment,
and an **equivalent**, **stricter**, **weaker** or **unsupported** classification.
JSON format version 1 includes the same items, installation blockers, draft
fragments and contributing-guide snippet. IDs include the base, source,
classification and reported requirement details, including review counts and
check App parameters. Changed details or wording invalidate prior acceptance;
listing order does not. Re-audit after protection edits.
`--out` saves the draft locally even when readiness is false; it does not commit
or upload it. Merge its annotated, reconciled queue fragments into existing
queues; they are not a replacement configuration to deploy verbatim.

The report starts with readiness, blocker and unaccepted-gap counts. Unknown
pull-request, required-status-check and classic protection fields are unsupported
and name the unhandled key and raw value. Unknown ruleset conditions list every
hidden rule and remain unwaivable blockers until their applicability is resolved.
Dynamic producer names, unresolved reusable-workflow callers, path filters and missing
queue-push evidence cannot establish equivalence. CODEOWNERS coverage remains
"to verify at switch" even for plausible owner tokens: this audit does not
resolve owner identities, team visibility or eligibility. Effective write
authority expands readable team and built-in role memberships before intersecting
restrictions; unresolved membership is a blocker that `--accept` cannot waive.
Repository-role bypass IDs are maintain=2, write=4 and admin=5, independently of
privilege ordering. Unknown/custom role IDs stay unresolved. Classic restrictions
include administrators when `enforce_admins` is disabled; unreadable admin
membership blocks authority verification.

**The draft requires the release implementing ADR 0007 Stage A.** It uses that
ADR's `approvals`, `groups` and `changes` keys; current releases reject them.
Review counts use a snapshot of eligible repository writers in
`groups.imported_reviewers`, with a count-bearing group requirement in
`approvals.default` and the same requirement in a `paths: ["**"]` rule.
Keep both: on a known-empty diff, `paths` is unknown and enforced as if matched,
plus the count-bearing default when no rule definitely matches. A definitely
matching `authors: [human] → none` rule suppresses the default even on an empty
diff, but the unknown `paths` rule still enforces the count. Rules add
requirements, so `none` cannot cancel the imported `paths` requirement.
Overlapping classic and ruleset review counts use the maximum in one reconciled fragment per base. Maintain
this group as writer membership changes. Eligibility and blocking requests for
changes must be checked at landing by Stage A.

General code-owner review is **unsupported**: ADR 0007's `{code_owners: true}`
rule requirement exists but is not activated until PR 11b publishes the loader
and enables enforcement. Until then only policy files get mandatory owner
approval, so general code-owner review cannot be translated or enforced by this
draft; a group does not confer path ownership. ADR 0007 defines the future
translation as two independent rules: `paths: ["**"]` for the count, plus
`touches: ["**"]` for `{code_owners: true}`. The owner requirement is rule-only
and rejected as `approvals.default`; this draft does not emit it before 11b.
For example:

```yaml
# Requires the release implementing ADR 0007 Stage A; merge into the relevant queue.
# groups is repository-level, approvals belongs to the queue.
groups:
  imported_reviewers: [alice, bob]
approvals:
  default: {group: imported_reviewers, count: 2}
  rules:
    - when: {paths: ["**"]}
      require: {group: imported_reviewers, count: 2}
# paths is unknown on empty diffs and enforced as if matched, plus the default.
# General code-owner review is unsupported until PR 11b enables enforcement.
```

Review dismissal on is a weaker translation where GitHub invalidates an approval
after a merge-base or diff change without changing the head. Dismissal off makes
head-bound approvals stricter. Last-push approval, conversation resolution and
required deployments have no equivalent. Required checks need a unique observed
producer; App pinning is lost by a name-only condition, and a name shared between
statuses and check runs is ambiguous. Producer observations are bounded to each
pinned base tip, so they cannot prove future producer exclusivity. The report
inspects job names, job `if:` conditions and `on:` triggers in each base's pinned
workflows. Only an absent condition or literal `true` proves unconditional job
coverage; other job conditions remain unknown. PR-only jobs are unsupported,
while dynamic names, reusable workflows, path filters and unproven branch filters
are explicitly unknown. Unresolved producers affect only check names they could
produce; reusable calls with unknown callee names can affect every check.
External CI needs operator verification. OpenMerge treats neutral and skipped as failures, unlike GitHub.
Strict up-to-date checks map to `verification: strict`; `post_merge` relaxes that
guarantee and needs a separate policy review under ADR 0005.

CODEOWNERS selection follows `.github/CODEOWNERS`, `CODEOWNERS`, then
`docs/CODEOWNERS`, with last matching ownership and directory inheritance. The
report lists every differing ordered rule between a served base and the governing
default branch; it does not guess whether arbitrary owner changes are equivalent.
All four accepted OpenMerge configuration paths and all three CODEOWNERS locations
must have user/team coverage. Email-only ownership cannot satisfy Stage A's owner
requirements; team visibility and eligibility also need verification at switch.

Exit **0** means no unresolved item; exit **1** means a read failed or some item
remains unresolved; invalid command-line syntax exits **2**. Repeat `--accept ID`
for every weaker/unsupported policy item you consciously accept. Accepted IDs and
explanations are comments in the draft, so a reviewed configuration PR can retain
the decision. Unknown IDs and attempts to accept installation prerequisites fail.
A served default branch, GitHub accepting App/break-glass bypass, CODEOWNERS
coverage, mandatory policy owner approval and resolution of other writers cannot
be waived. This release still reports the Stage A prerequisite and bypass
acceptance **to verify at switch**, so accepting every policy gap alone does not
produce a successful preflight.

Every ruleset bypass is inventoried, including inherited entries and entries that
do not apply to a served base. An actor bypassing one update restriction is not
reported as an effective writer if another applicable restriction excludes it.
PR-only bypass also counts when it permits merging through GitHub outside the
queue; the App itself needs `always` for its atomic landing push.
Resolve every effective writer as the OpenMerge App, the single declared
break-glass team/admin role, or a writer that will use the queue. Bots cannot be
break-glass. Ordinary writer access is a blocker until exclusive base write
restrictions are installed. Repository administrators and organization owners
remain trusted as described in ADR 0008.

The command also prints this contributing-guide snippet:

```text
This repository merges through OpenMerge. Submit a pull request and use
@openmerge queue; follow its queue state instead of GitHub's merge button.
Disable GitHub auto-merge. CI must run on pushes to openmerge/queue/**.
Creating a branch matching a queue's base selector requires the OpenMerge App
or the declared break-glass team/repository-admin role. Break-glass merges are
recorded as outside-queue landings. Bots cannot be the break-glass actor.
```

Do not remove protection after an audit alone. Follow the coordinated
[ADR 0008 transition](adr/0008-openmerge-policy-authority.md#migrating-protection-into-openmerge):
verify bypass acceptance, install and verify write rulesets under the old policy,
land the reviewed configuration, wait for the new digest and settled attempts,
then remove only reviewed redundant PR rules. Disable GitHub auto-merge and
schedule re-verification of protection after the switch.

## Outside-push observations

`openmerge queue status` and its JSON output expose the last 20 journal-derived
outside-push observations, including their real event IDs. The journal retains
all rows. Auditing runs independently with its own Git cache and credentials;
a stuck history read delays only audits. Outstanding landing attempts must settle
in the shared journal before their observations can be classified. The durable
audit cursor and pending transitions survive restart; no operator action is needed
to retry a delayed audit. Unreadable ancestry or history appears as permanently unknown, without retry.
An attempt that never settles can defer audits indefinitely and grow the persisted
pending list without a bound. History initialization and enrichment make zero
installation-authenticated REST/GraphQL requests. App-JWT token minting is
app-scoped; all history reads use Git smart HTTP, outside installation API quota.

`possible_stale_observation: true` marks only a rewind from a settled landing L
to its attempt's pre-landing build base B. Main's existing multi-server
reconciliation can append a stale base read in precisely this shape, and a real
rollback produces the same journal evidence. The record therefore says that it
may reflect a stale read rather than a real rollback. A subsequent B→L return
is suppressed at most once for that settled attempt,
provided no other nontrivial move has consumed or invalidated its allowance.
L→C→L records both moves; L→B→L→B→L records the second return to L and flags
both rewinds to B; continuing to C retains the flagged rewind and records
subsequent transitions normally. Other rewrites are unflagged.
Use repository history and independent push evidence to verify a flagged record;
ADR 0008's later warnings must treat it as lower-confidence evidence requiring
verification before acknowledgement. This release has no acknowledgement command.
The separate reconciliation fix is deferred until after the ADR 0009 trial.

`outside_push_recorded` is a forward-only additive wire-v1 event. Drain older
workers before upgrading. Once it has been written, rolling back to an older
binary requires restoring a database backup from before its first event.
Clearing the last-20 projection or removing configuration does not remove the
new type from the journal; never edit journal rows to make rollback work.
