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
accept the requested addition in its installed App settings. Updating the App's
permission declaration alone does not update an installation's grant. The explicit
setup diagnostic inspects the actual installation-token permissions; renewed
tokens (or restarting `serve`) observe acceptance.

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

The explicit `openmerge setup permissions` diagnostic calls
`DiagnoseInstallationPermissions`, which calls `ValidateTeamPolicyPermissions`
and reports a missing organization Members grant as an optional warning.
The runtime Stage A pipeline does not call this grant diagnostic automatically.
Instead, runtime policy resolution reads referenced team identities and membership
through `ResolveTeam` and `TeamMembers`. Definite identity or membership
unavailability creates a durable `ConfigSource` block for the affected queue;
transient or incomplete reads retry without recording a durable configuration
block. An unreadable effective owner requirement blocks its change rather than
implying empty membership. Queues without team references do not acquire a
Members-grant warning or block merely because that grant is absent.

Identity-bound grants compare the permission response's stable user ID with the
review or comment author ID. A renamed or reassigned login cannot lend another
account's write permission. A `WriteGrant` HTTP 404 is an unknown observation
(`ErrIdentityUnavailable`), not known-empty permission; the approval pipeline
reports the lookup as unavailable and discards the grant. A successful
identity-bound response with a non-writing permission is a known denial. Team
membership is read by stable organization and
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

## Queue requests with pending entry conditions

A writer's `@openmerge queue` request is held when `enter_when` is unknown
(for example, a pending or missing sign-off check), and admitted automatically
when the conditions pass. The check and PR detail say
`Waiting for queue entry conditions: <conditions>`. A held request has no queue
position and cannot start a batch.

The existing `enqueue_requested` event pins the request to the latest observed
head. `pending_conditions` evidence on `pr_observed` distinguishes an
unknown result from a definite failure. Journal replay and queue snapshots retain
the held request across restarts; no database migration is needed. Held PRs are
refreshed even when their metadata timestamp has not changed. Admission uses a
fresh PR observation and the existing exact-SHA trust gate: the head must be in
the base repository or have an exact-head approval from a reviewer whose current
write permission is re-read. The command requester's writer permission is checked
only when the command is accepted; it is not re-checked at admission. Demoting
the requester while a request is held does not cancel it, provided the PR still
passes that separate trust gate. This is intended Mergify parity.

A definite failure cancels the request and reports the existing `blocked:
not-eligible` outcome. A later pass requires a new queue command. A new head,
`@openmerge dequeue`, or PR closure also cancels it. Marking the PR draft cancels
it when `-draft` is an entry condition. Changing the queue lifecycle to dormant,
retired, or overlapping drops all held requests immediately, even if their
conditions are still pending, just as that lifecycle drops a fresh queue command.
The PR then has the same status as a dropped fresh command, and reactivation
requires a new command.
These rules apply in GitHub and OpenMerge authority modes; landing, batching,
and CI policies are unchanged.

Held requests have no expiry, matching Mergify. A condition that never reports
can hold a request indefinitely, with one forced PR observation per held PR per
poll even when its metadata timestamp is unchanged. `@openmerge dequeue` clears
it.

This release creates a **database-restore rollback boundary** on the first
persisted `pending_conditions` field or held-request snapshot. The field is
emitted whenever an observation has unknown entry conditions, even before any
queue command: replay needs that evidence to distinguish holding the next
`enqueue_requested` from rejecting it. Restricting emission to already-held
requests would lose that first decision. Older binaries strictly reject the new
field on the existing `pr_observed` event, and snapshots can contain both
`HeldRequests` and `PendingConditions` in cached observations. No queue command
is needed to cross this boundary, so expect it soon after deployment. Once
crossed, a binary-only rollback below this release is unsafe, even after all
holds are cleared. Keep a compatible binary or restore a pre-emission database
backup and reconcile post-backup activity; never edit journal rows or relax
strict decoding. The rollback check below must inspect fields and snapshots as
well as event types.

This behavior change is **Hold**: deploy it to the live dogfood instance only
after ADR 0009 period B.

## Why a PR is waiting

`openmerge queue status` shows a `WAITING` column. Each API entry exposes the
same explanation as `waiting_reason` (`code`, `text`, optional `details` and
`url`). The OpenMerge check includes a `Current state:` line at its existing update
points. These explanations describe recorded state; they do not authorize a
merge or trigger extra observations.

The reason distinguishes approvals, trust, entry conditions, operator action,
queue position, batch construction, CI, conflict, dequeue, paused queues, landing,
and completed outcomes. A landing attempt remains visible while a queue is
paused. CI names and links appear only when retained run evidence identifies
them; superseded attempts do not explain the current wait. Approval wait codes
link to the reviewer brief for the recorded requirements. A conflict does not
identify the other change when that identity was not recorded.

This presentation change is **Hold** during dogfood. Deploy only after period B.

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

To run only a light suite on PRs and the full required suite on queue refs, see
the [queue-only full CI guide](guides/queue-only-ci.md). It covers which check
names a ruleset may require, workflow and ruleset examples, and migration.

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
- For build/landing changes, run the offline merge-method commit-ID check. It
  compiles the same injected `internal/gitops` test into both source trees and
  uses the existing fixture's real local Git remotes, `BuildTestRef`, and
  `Land` paths for squash, merge, rebase, and fast-forward. It pins the fixture
  dates and identities and replaces `crypto/rand.Reader` only inside the test
  process, so the marker and fence nonces are identical without a production
  nonce hook. The test binary and copied source trees live below `~/.cache`,
  not `/tmp`; module and toolchain downloads are disabled.

  ```bash
  (
  set -e
  export DEPLOYED_SHA="<exact deployed source SHA from the deploy record>"
  git worktree add --detach "$HOME/openmerge-deployed-replay" "$DEPLOYED_SHA"
  trap 'git worktree remove "$HOME/openmerge-deployed-replay"' EXIT
  cache_root="$HOME/.cache/openmerge-legacy-commit-ids"
  mkdir -p "$cache_root/go-tmp" "$cache_root/go-cache"
  GOFLAGS='-p=2' GOMAXPROCS=2 GOMEMLIMIT=4GiB \
    GOTMPDIR="$cache_root/go-tmp" GOCACHE="$cache_root/go-cache" \
  python3 scripts/check-legacy-commit-ids.py \
    --old-source "$HOME/openmerge-deployed-replay" \
    --new-source "$PWD" \
    --cache-root "$cache_root"
  )
  ```

  The minimum supported old source is `3f77a88`; the harness also supports
  `c2d9b3a`. The v2 probe uses two commits per PR, titles and multiline bodies,
  forged reserved keys with casing/continuations/whitespace, DCO and coauthors.
  Every invocation first runs mandatory offline self-tests: identical source
  trees must exit 0, a committed nil-metadata reserved-prefix mutant must exit 1
  (squash and rebase differ), and a dirty candidate must exit 2. Its temporary
  clone and build copies are removed. Equal source SHAs produce an explicit
  vacuous-comparison warning; do not use that run as release evidence. Cache
  roots resolving below literal `/tmp` are rejected regardless of `TMPDIR`.

  The check name is `legacy-commit-id-per-method`. It requires both source
  trees to be clean so each reported source SHA is evidence-bearing, and it
  prints the harness SHA-256, compiled test-binary SHA-256, both source SHAs,
  and every exact old/new `tested` and `landed` SHA. The command uses only
  local Git and temporary bare repositories created by the fixture; it does
  not use GitHub, Postgres, systemd, or live OpenMerge refs. Exit 1 means at
  least one method differs; exit 2 means the local input or toolchain could
  not produce evidence. A fast-forward row intentionally reports the marker
  as `tested` and the original head as `landed`; the other three rows must
  have identical tested and landed IDs within each binary.

Forward-only events (for example PR #53's `outside_push_recorded`) and new
fields on existing events make binary rollback unsafe once written: an older
strict decoder cannot read them. Held requests introduce `pending_conditions`
on `pr_observed` and new snapshot fields without changing the event type.
Before choosing binary-only rollback, stop the service and run these read-only
queries using the saved backup watermark. Add every event type, event field,
and snapshot field unsupported by the old binary:

```bash
backup_max_event_id=$(cat "$backups/openmerge-$stamp.max-event-id")
PGOPTIONS='-c default_transaction_read_only=on' psql -X -v ON_ERROR_STOP=1 \
  -v backup_max_event_id="$backup_max_event_id" <<'SQL'
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SELECT id, queue_id, type, recorded_at
FROM events
WHERE id > :'backup_max_event_id'::bigint
  AND (type IN ('outside_push_recorded')
       OR (type = 'pr_observed' AND (payload->'data') ? 'pending_conditions'))
ORDER BY id;

-- These derived snapshots live in bytea JSON cursors, not a snapshots table.
-- Inspect all current cursors: they have no event-ID or backup-time filter.
SELECT queue_id, key
FROM observations
WHERE key LIKE 'outside-push-audit/%'
  AND ((convert_from(value, 'UTF8')::jsonb->'State') ? 'HeldRequests'
       OR EXISTS (
         SELECT 1
         FROM jsonb_each(CASE jsonb_typeof(
           convert_from(value, 'UTF8')::jsonb->'State'->'Observations'
         ) WHEN 'object' THEN
           convert_from(value, 'UTF8')::jsonb->'State'->'Observations'
         ELSE '{}'::jsonb END) AS observation
         WHERE observation.value ? 'PendingConditions'
       ))
ORDER BY queue_id, key;
COMMIT;
SQL
```

The saved watermark precedes `pg_dump`, so this check is conservative if events
arrived during the backup. Any returned unsupported event or snapshot rules out
binary-only rollback. A query/JSON decoding error leaves compatibility unknown
and also rules it out. Zero rows from both queries permit it only after checking
all other decoder/schema compatibility constraints, including unsupported fields
or events already present at or below the watermark. The watermark limits what
changed since backup; it does not certify that the backup suits the old binary.
Otherwise restore the pre-deploy `pg_dump` backup into an appropriate empty
database before starting the old binary. A restore
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
Stage A 9b migration 006 is an unreleased, transactional startup migration.
It builds two partial expression indexes for landing-decision batch/attempt and
exact eligibility-read identity. `CREATE INDEX` is non-concurrent: its ShareLock
blocks journal writes until the migration transaction commits. Apply it during
the planned ADR 0009 period-B drain; trial class remains **Hold**, with no deploy
before that drain. On an isolated synthetic journal containing equal numbers of
decision and eligibility rows, both indexes together took 2.607 ms at 2,600 rows
and 181.702 ms at 260,000 rows. These local measurements exclude lock waiting;
size, payloads and contention affect deployment time. The events heap is not
rewritten. DDL and the migration ledger roll back together on failure. The
compiled deployed `3f77a88` Store can reopen the migrated schema and write/read
legacy events; this does not authorize activating new event types with old workers.
See [Stage A 9b evidence](research/stage-a-9b-evidence.md) for plans and checks.

Migration 007 adds three partial reporting indexes to the same journal table.
It also uses regular, non-concurrent `CREATE INDEX` inside the startup migration
transaction. The table's `ShareLock` blocks journal writes until all three
indexes and the migration record commit; reads remain available. Apply it only
after the ADR 0009 period-B queues have drained. The trial class remains
**Hold**, with no deployment before that drain. `TestReportingIndexesBuildTiming`
measures all three builds against mixed 2,600-row and 260,000-row synthetic
journals and confirms that writes wait for commit. The recorded local builds
took 1.80 ms at 2,600 rows and 65.76 ms at 260,000 rows. These synthetic rows
and local storage do not predict a production duration; the measurement excludes
time waiting to acquire the lock, and production payload size, storage, and
contention will affect duration. Repeat the isolated measurement with
`go test ./internal/store -run '^TestReportingIndexesBuildTiming$' -count=1 -v`
when evaluating a representative deployment database. The **Hold** gate remains
in effect until the period-B drain.

The current source tripwire does not establish a database retention contract.
Before enabling compaction, integration tests must prove retention of
`landing_decision_recorded` and its exact referenced
`approval_eligibility_observed` payload and original read identity.

Store retention/compaction remains disabled; enabling it must preserve ledger
history or explicitly define a retention window. The source-level
`TestDecisionEvidenceRetentionNoPruning` scans SQL literals in production Go
under `internal/` and `cmd/`, plus checked-in SQL in those trees. It rejects
delete/truncate statements, table or index drops, and updates that replace an
event payload. Dynamically assembled SQL, external SQL clients, and direct
database changes can bypass the guard; it recognizes common SQL statement
shapes rather than parsing SQL, so it does not enforce retention at the
database boundary. Any pruning implementation needs integration coverage that
proves `landing_decision_recorded` and its referenced
`approval_eligibility_observed` survive with the original read IDs.

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
ADR's `approvals`, `groups` and `changes` keys, published by PR 11b.
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

The protection-import draft still does not emit general code-owner review.
Stage A enforcement accepts ADR 0007's rule-only `{code_owners: true}`; a group
does not confer path ownership. A reviewed translation uses two independent rules: `paths: ["**"]` for the count, plus
`touches: ["**"]` for `{code_owners: true}`. The owner requirement is rule-only
and rejected as `approvals.default`; this draft does not emit it.
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
# Add a separate touches rule for code-owner review when required.
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
An unusable audit cursor is derived data: the worker rebuilds it from immutable
journal events in bounded passes and replaces the original bytes only after a
successful compare-and-swap. This also overwrites an unsupported future-version
cursor, for example after a binary downgrade. The reset warning is logged only
after that replacement commits; failed repairs and competing CAS attempts do not
repeat it each poll. Existing audit receipts remain deduplicated. This cache
fallback does not waive journal/snapshot compatibility or backup requirements for
a binary downgrade.

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

## Stage A approval activation

Trial class: **Hold**. Merge behind the opt-in; deploy only after ADR 0009
period B drains. Do not add `approvals` to this repository's dogfood queue
until the trial pair closes. Publishing the schema does not authorize that
opt-in. The public loader accepts the Stage A repository keys `classification`,
`groups`, `agents`, and queue keys `changes` and `approvals`.
`changes.pull_requests` accepts only `required`.

A resolved queue with `approvals` installs the complete Stage A pipeline:
identity-bound grants, revision and classification observations, accountability,
native stances and bare receipts, additive approval requirements, fresh owner
eligibility, generated commit metadata, and the atomic landing-decision gate.
There is no production switch for a subset of these checks. A sibling queue
without `approvals` retains the legacy pipeline, digest and commit messages,
even when repository-level policy keys or an opted-in sibling are present.

Before the first use:

1. Coordinate an admission freeze with repository maintainers: stop automation
   posting queue commands and ask contributors not to enqueue new work. Let
   admitted work finish; resolve or explicitly dequeue blocked PRs through the
   existing authenticated `@openmerge dequeue` comment command. Read
   `openmerge queue status --json` for every served partition and require empty
   entries, no active/chain batches and no outstanding landing attempt. Do not
   assume that a paused or blocked queue is drained. Then stop **all** old
   workers, including other hosts and installations serving the
   repository, and verify that none can resume or renew a queue lease. Wait for
   outstanding leases/actions to expire or settle. A binary upgrade on one host
   is insufficient. Existing leases do not advertise binary capabilities, so
   this release cannot reliably detect a legacy worker on another host. Config
   parsing by old binaries fails closed, but an already claimed action is why
   the drain is mandatory.
2. Take and verify a restorable database backup **before** running the new
   binary against an approvals config. Preserve the matching configuration
   commit, repository object cache, binary, and operational settings with it.
   Follow the backup/restore procedure above; do not use a journal edit as a
   substitute for restoration.
3. With admissions and all writers still stopped, run the compatible binary's
   startup migrations against the backed-up database. Migrations **006 and 007**
   build partial expression indexes with ordinary, **non-concurrent**
   `CREATE INDEX` in startup transactions. Their `ShareLock` blocks journal
   writes until commit; reads can continue. Reserve this drained window for
   both migrations and verify startup completes before admitting work. Do not
   change the migration SQL to run concurrent index builds. A failed migration
   rolls back its DDL and migration record; repair the cause before retrying.
   See [006 evidence](research/stage-a-9b-evidence.md) and
   [007 evidence](research/stage-a-12a-evidence.md); synthetic timings exclude lock waits.
4. Start only compatible workers, then apply a reviewed approvals opt-in to the
   intended queue. Verify the resolved queue and its identity diagnostics before
   reopening admissions. Unresolved policy identities pause the affected queue;
   transient identity or tree reads stop reconciliation without using stale
   policy. Legacy siblings remain operational under their own policy.

The first approval-policy `config_applied` (with its optional `policy` pin), or
other Stage A evidence, is a **database-restore rollback boundary**. Removing
`approvals` stops future Stage A work; it does not erase these records or make
an old decoder compatible. To return across this boundary, drain/stop the new
workers and restore the pre-use backup together with its compatible binary and
configuration. Account for external landings since the backup before restarting.
Never delete or rewrite journal rows to make an older binary replay them.

Approval-policy digest composition is versioned and deterministic. The unchanged
legacy `Queue.Digest(delete_head_branch)` supplies the queue component. The
policy component is `changepolicy.PolicyDigest`: canonical selected approvals,
repository classification/groups/agent mappings, static resolved user/team IDs,
selected governing CODEOWNERS content or proven absence, every governing
`.gitattributes` file (including root absence), and classifier version. Current
membership and grants are excluded. `WithPolicyDigest` hashes the JSON object
`{Version, Queue, Policy}` with version `openmerge-effective-policy-v1`. Without
an approval component it returns the legacy digest unchanged.

Approval `ConfigApplied` records retain static identity resolutions, classifier
version and the governing default-branch name in their optional `policy` pin.
These are immutable journal inputs, not reusable eligibility pools. The brief
reader chooses the ConfigApplied record in force for the recorded classification,
loads its configuration and complete governing tree from **local immutable git
objects**, recomputes both component and effective digests, and asserts both.
It never fetches, resolves current names, reads current memberships, or expands
requirements from the worker's current policy. A mismatch reports
`policy_digest_mismatch`; missing objects or identity pins report
`policy_unavailable`. Restore the recorded objects/pins through an operator
managed fetch or backup import. Production journals retain these events in full;
compaction remains disabled and must preserve these pins before it is enabled.

Governing-file diagnostics retain at most eight details per file plus a `+N more`
summary. CODEOWNERS/attributes patterns are limited to 512 bytes and compiled
lazily; unsupported or oversized patterns produce conservative uncertainty.
Decision-time code owners always pass through `ResolveOwnerEligibility` with
fresh identity-bound observations, never a saved membership pool.

### Activation acceptance and handback

Adopt `members: read` on the sandbox App **and its organization installation**
before using team owners. Record the actual installation grant, not just the
App declaration. Recheck after token renewal using `setup permissions`, then
verify team identities are readable and team members have repository write
permission. Apply the same reviewed permission-adoption sequence to production
only after the trial closes and the drain/backup steps above are complete.

Use the [sandbox runbook](research/stage-a-sandbox-evidence.md) to record the
accepted receipt and immutable decision with original event IDs. Sandbox
acceptance is pending maintainer-provided resources; automated fake-forge
acceptance is not evidence of a GitHub installation's grants. The
[Stage A exit report](research/stage-a-acceptance.md) separates those results.

If activation needs rollback, stop admissions, allow outstanding atomic attempts
to settle through their recorded fences, then stop every compatible worker.
Preserve the post-upgrade journal for investigation. Restore the verified
pre-use database backup and matching binary, configuration and Git objects;
reconcile external landed SHAs and fence state since the backup before reopening
admissions. Do not restart an old worker against a journal containing Stage A
or outside-push events. Removing `approvals`, reverting YAML, or downgrading the
binary alone is insufficient. Never reset fences or edit journal rows.

Review exact heads; use
`@openmerge approve <brief-digest>` when intent review requires a receipt.
An accepted receipt is historical evidence; the landing gate rechecks current
eligibility and the receipt's binding. Read `change decisions --json` for the
immutable decision and settlement rather than inferring either from trailers.

Conflict grace is excluded: delivered conflicts remove work as today, without
a grace deadline or retry budget. Stage A does not claim cancellation at depth 1.
Old-worker detection is manual: leases lack binary capability IDs. Keep the
all-host drain checklist and pre-use backup as operational requirements.

## Acknowledging outside pushes

`openmerge admin acknowledge` records that someone with operator database access
saw one particular outside-queue push. The target is the immutable
`outside_push_recorded` **event ID**, its exact `new_sha`, and the exact numeric
`queues.id` supplied as `--queue`. Read the queue identity with
`openmerge queue status --json` or `GET /v1/queues`
on the operator API. These responses currently expose `installation`, `repo`,
`base`, and `lane`, not the database ID. Resolve that exact tuple with operator
DB credentials (isolated sandbox only during period B):

```sql
SELECT id, installation, repository, base, lane
FROM queues
WHERE installation = '173082682' AND repository = 'owner/repo'
  AND base = 'main' AND lane = '';
```

Use the numeric result as `--queue 17`, for example. Retired queues may be absent
from current status; look up their retained exact identity directly in `queues`.
Use the persisted ID even for a retired queue; this command never discovers, registers, or reactivates queues. It uses
`OPENMERGE_DATABASE_URL` (or `--database-url`) with operator credentials and never
uses a forge token, PR authorization, or the status API. Connections do not run
migrations. `--timeout` bounds connection, lock wait and transaction time together
(default 2 minutes; allowed range 1ms through 2 minutes).

Until [Docker/release revision wiring #118](https://github.com/CanadaApollo6/openmerge/issues/118)
lands after ADR 0009 period B, `--verify` and `admin acknowledge` require operators
to use a binary built with `make build` from a clean git checkout. Shipped Docker
images exclude `.git` and have no embedded revision fallback, so they cannot
attest. Run `openmerge version` and retain the exact `commit` with
`attestable: true` in the evidence. The command prefers `vcs.revision`, otherwise
prints the `authority.BuildRevision` ldflags fallback; missing or dirty metadata
is not attestable. Verification enforces revision matching; acknowledgement's
receipt-file checks leave this build prerequisite to the operator.

Before the **first acknowledgement event**, finish the trial period-B drain and
stop every incompatible worker on every host. Save the drained/stopped worker
inventory. Capture a pre-emission database backup, restore it in isolation, and
save a receipt with backup identity/hash, watermark, binary/config revisions,
restore-test result, and timestamp. Retain those records outside the database.
The command requires `--drain-receipt` and `--backup-receipt`: readable, nonempty
regular JSON files of at most 64KiB, with distinct resolved paths, underlying files (inodes), and SHA-256
content hashes. Each document must have its own top-level `kind`: `"drain"` for
`--drain-receipt`, `"backup"` for `--backup-receipt`. Include the corresponding
inventory or backup/restore evidence as additional JSON fields, for example
`{"kind":"drain","workers":["sandbox-host/worker"],"drained_at":"UTC timestamp"}`
and `{"kind":"backup","backup_sha256":"SHA256","restore_test":"passed"}`.
Copies, hard links, aliases and swapped/untyped receipts are rejected. These are
operator attestations; file checks cannot discover other hosts or prove a backup restorable. Use the same retained
receipts on retries. Merely deploying this dormant command writes nothing.

For an isolated/sandbox database whose gate is complete:

```sh
openmerge admin acknowledge \
  --queue 17 --event 442 --sha <original-new-sha> --operator 'On-call name' \
  --drain-receipt /operator-evidence/workers-drained.json \
  --backup-receipt /operator-evidence/pre-emission-restore.json
```

The JSON receipt contains the acknowledgement's journal ID, `source_event_id`,
`sha`, original supplied `operator`, and `at`. Retries, including concurrent
operators, return the original receipt and timestamp. A wrong queue, event type,
or SHA fails before insertion, including on a retry. If output fails after a
commit or a connection loses the commit result, retry the same target to recover
its receipt. A failure/cancellation has no success receipt; it is never permission
to remove journal evidence.

Explicit acknowledgement is allowed in `github` mode, including queues that
have never used authority. It records the same awareness but creates no authority
warning. **It still emits a new wire type and crosses the binary compatibility
boundary.** After the first `outside_push_acknowledged`, use a compatible binary
or restore the verified pre-emission backup with all workers stopped. Mode
rollback does not make an old decoder compatible. Reconcile actual remote refs
and recover evidence of any landings since that backup before restarting; never
resume stale state blindly or delete events to downgrade. This command is not
authorized for the live dogfood database during the trial.

Acknowledgement does not identify the pusher, approve the change, prove checks
ran before landing, clear any rules/suspect/operator block, change ancestry, or
advance green. Original unknown/truncated history,
`possible_stale_observation`, and `verification_before_landing: none` remain in
the audit evidence. A push followed by restoring the old tip between observations
can leave nothing detectable.

The journal-backed warning reader streams 256-event pages at one repeatable-read
snapshot with a two-minute deadline. It folds authority pins in journal order at
the originating `BaseMoved`, then probes the existing `outside-base/<event ID>`
dedupe association and acknowledgement key. No pin means `github`. Audit append
time and event timestamps never select authority. Rollback and retirement retain
outstanding warnings and all receipts, even beyond the 20-item recent projection.
The reader uses O(one page plus one pin) memory and O(queue history) work, plus
indexed dedupe lookups. Consumers must discard partial output if it fails. ADR
0008 PR 6 owns warning rendering in status, CLI, API, and health; this PR adds no
such rendering, schema activation, migration, index, or runtime forge read.

## Authority health alerts (ADR 0008 PR 6, HOLD)

Use `/v1/live` for process restarts and authenticated `/v1/health` for alerts.
Health uses a bounded read-only DB probe and process snapshots. A warning or
unverified authority alone does not fail health. See [the complete API contract](api.md#authority-and-alerting-probes-adr-0008-pr-6-hold).

While waiting, a queue is overdue after its published next-start deadline plus
`--health-overdue-grace` / `OPENMERGE_HEALTH_OVERDUE_GRACE` (30s, range 1–60s).
Immediate journal wakes publish a deadline of now, so slot starvation remains
visible. Slot acquisition starts the cycle and seeds `last_progress_at`.
Completed PR, comment, rules, check-source, identity, configuration and Git reads,
observation writes, check-report writes, actions and turns advance progress.
A unit publishes only after returning; journal traffic alone does not.

During a cycle, overdue means missing start/progress telemetry or no progress for
`--health-progress-stall-limit` / `OPENMERGE_HEALTH_PROGRESS_STALL_LIMIT` (4m,
range 1s–30m, further constrained by the action budget). Startup rejects any limit
below the action timeout + 30s Git cache maintenance + 10s completion write +
configured overdue grace. The CLI and embedded servers enforce the same rule;
embedded servers with longer action timeouts must explicitly extend the limit.
Upgrade note: an explicitly configured `150s` stall limit now fails startup with
standard action/grace settings. Remove that override to use `4m`, or configure a
limit that covers the validated action, maintenance, completion and grace budget.

GitHub progress advances after each successful HTTP response is fully consumed,
including individual pagination pages and PR sub-reads. Failed HTTP responses,
partial bodies and hung requests do not count. Read-unit progress requires
success. A validated wire 304 served from cache counts once its cached body
reaches EOF, including each revalidated pagination page; a 304 that cannot be
served from cache publishes nothing. Cold-cache Git clone/fetch inside
ReadRef and the one-time fence initialization push still have no intermediate
progress notifications; this startup/first-cycle exposure can report overdue
while that single Git operation runs beyond the stall limit.

A 1h action timeout is incompatible with the 30m stall-limit ceiling. The default
2m action timeout and 30s grace require 3m10s, leaving 50s margin under 4m. `--health-max-cycle-duration` / `OPENMERGE_HEALTH_MAX_CYCLE_DURATION`
remains an absolute 3h backstop (range 2m–6h). A progressing cycle may run past
15m or 60m while staying healthy. A hung action becomes overdue after the stall
limit. Degraded means the later of last success and progress while in-cycle is
older than `--health-max-success-age` / `OPENMERGE_HEALTH_MAX_SUCCESS_AGE` (15m,
range 1–60m), using process queue start before either exists. Progress does not
change `last_success_at`. Waiting queues still age from last success, so repeated
failed cycles and backoff cannot hide degradation.

Warning refresh never holds the cache lock during DB I/O. Contention or ledger
anomalies label only that queue's warning section `ledger_unavailable`, retaining
last complete counts. If a background refresh loses to an interrupted newer
foreground fold, its last published counts carry `ledger_refreshing` until the
newer fold or refresh completes. SQL NULL on an audit dedupe key is an orphan anomaly even
if acknowledged. Pin removal clears health warnings; rollback keeps historical
origin-time warning eligibility and refreshes acknowledgements as status does.

## Receipt database binding

The first emission of authority `write_restriction_observed.read_error` creates
a strict reader compatibility boundary. Inventory readers that lack the optional event
and v7 snapshot field, even when they already support authority event types.
Drain those readers before first emission, retain the tested pre-emission backup
and reconciliation receipt, and use a compatible binary after rollback to GitHub.
Removing the config cannot make these retained records safe for older readers.

Before an explicit authority verification or internal activation, drain workers,
back up and restore-test the source as described in the
[first-emission procedure](research/adr-0008-4-evidence.md). Generate its identity
with the same operator connection that verification will use:

```sh
openmerge setup rulesets --print-database-identity --database-url "$OPERATOR_DATABASE_URL"
```

This command performs read-only catalog/journal queries, with no migrations or
GitHub calls. Copy its `openmerge-db-v2:sha256:<64 hex digits>` output into the
receipt's `database_identity`. **Existing hand-written host/port/search_path
receipts must be regenerated**; there is no legacy identity fallback.

The role needs catalog visibility, SELECT on the journal, schema USAGE, and
EXECUTE on `pg_catalog.pg_control_system()`. Where that function is restricted,
a database administrator must explicitly grant
`GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO operator_role;`
to the operator/activation role in the target database. Missing access fails
closed with a privilege diagnostic; there is no endpoint-based fallback. Review
the privilege grant under local operating policy. A nonempty journal is required;
normal GitHub-mode startup does not query this function or require this grant.

The v2 digest covers a JSON-framed sequence of cluster system identifier,
current database name/OID, effective OpenMerge schema name/OID, and the earliest
journal event's ID, UTC microsecond timestamp and canonical JSONB payload text.
All eight persistent store tables (including migration bookkeeping) must resolve
uniquely in the effective search path and share that schema. Shadowed copies,
split schemas, missing tables and an empty journal fail closed. DSN passwords,
hosts and configured search_path strings are not identity inputs or output.
Options, role defaults and alternate endpoints resolving to the same database,
schema and genesis therefore produce the same digest across pool reconnects.

A `pg_dump` restore into a new cluster also changes the system identifier, so
the original receipt will not match. Database drop/recreate changes its OID; a schema replacement changes its OID;
a replacement history with different genesis changes the digest. Regenerate the
receipt after restore/recreation and repeat the drain/backup procedure. This is
a genesis binding, not a full-history checksum or restore counter. A physical
clone/restore preserving the system identifier, database/schema OIDs and genesis,
or a later history fork sharing genesis, cannot be distinguished by these reads.
The receipt remains an operator attestation, not authenticated proof of drainage.
Identity is checked before seeing reads and again before internal adoption;
endpoint replacement during adoption is not transactionally excluded. Public
activation requires the reviewed production opt-in described below and remains
under the deployment Hold.

### Authority governing acquisition (Hold)

Authority setup, verification and import require complete protection visibility:
classic branch protection and every applicable repository or inherited ruleset's
targets, rules and bypass lists. Administration read alone does not guarantee
inherited bypass visibility; the owning organization may need to grant access.
Hidden evidence blocks a new binding and cannot be waived with `--accept`.
Public activation requires the reviewed production opt-in below. Deployment
remains on Hold until dogfood period B drains.

Setup and verification fail closed if the branch inventory exceeds ten pages
of 100 branches, or GitHub truncates its recursive tree response. Reduce the
inventory or wait for a complete supported acquisition; a partial inventory
cannot establish absence of policy. The collector also enforces the shared
256-file and 1 MiB governing-content budgets, including nested attributes.

Discovery reuses a governing acquisition only for the same installation,
case-insensitive repository, default branch, tip and binding set. Successful
acquisitions and definite live owner-eligibility failures expire after
`RulesInterval` (`--rules-interval`, default 10 minutes), measured from acquisition,
without renewal on cache hits. The next scheduled discovery after expiry reads
owners and complete protection visibility again, including unrelated rulesets;
detection can additionally wait for the discovery interval or read-error backoff.
This cache certifies a past acquisition, not current protection or authorization:
activation, recurring observation and admission retain their fresh reads. A
revoked grant blocks admission; restoring grants or team eligibility can clear
that block at the same tip after the retry cadence. Immutable content failures
(coverage, multiple files, invalid configuration/schema) remain blocked until the
governing branch/tip changes. Transport/quota failures retain existing backoff.

Setup rechecks the acquired default branch and tip after each mutation's final
preflight read, immediately before its write. Verification retains that same
acquired identity per repository and rechecks after all target observations,
immediately before committing evidence. A changed or unavailable governing ref
requires a fresh acquisition and records no verification.

### Repository authority propagation and operational gates (Hold)

Each authenticated GET to `/v1/authority-completion` performs outbound App
installation/repository, branch and governing-ref discovery. It returns all
registered partitions for the requested repository across installations,
including partitions excluded by worker allowlists. The bearer token grants
this repository-wide visibility. Discovery is uncached, with a 15-second request
deadline and no separate concurrency semaphore; serialize operator polling to
avoid concurrent discovery exhausting the App's API budget.

Before any authority event or v7 cursor, including **pre-switch**
`setup rulesets --authority openmerge --verify`, freeze admissions, let every
attempt settle, stop every journal writer and incompatible reader on every
host, and prevent automatic restarts. Paused, blocked, dormant and retired
queues are not evidence of drainage. Inventory service processes, spare hosts,
other installations, admin commands and offline readers; include readers lacking
`read_error` even if they recognize authority events and snapshot version 7.
Record each process's stable host/process ID, binary revision and stop timestamp.
Check leases and outstanding actions separately; this is operator-controlled,
not automatic remote capability detection.

The database-bound emission receipt now additionally requires `inventory` and
`reconciliation`; regenerate older receipts. `inventory.complete` attests that
all hosts/readers were reviewed. Its `workers` must match `drained_workers`
exactly, each with `id`, nonempty `binary_revision` and `stopped_at` no later
than `drained_at`. Keep the complete inventory with the matching binary/config
revisions and object cache. The receipt remains an operator attestation.

With all writers stopped, record the source identity and journal watermark,
capture `pg_dump -Fc`, hash its actual bytes, and restore with `pg_restore` into
an **isolated scratch database**, never over the source for the restore test.
Verify restored journal watermark and replay, retain restore logs and compare
actual base/fence refs and any post-backup landings/evidence. A no-change result
still requires an explicit reconciliation record. Record the artifact hash,
source and restored database identities (they must differ), backup hash,
`backup_captured_at`, positive `backup_watermark`, matching `restored_watermark`,
`reconciled_at`, `evidence_sha256`, and
`all_refs_and_evidence_reconciled: true` in `reconciliation`.
Capture time must follow the drain; restore-test time must follow capture;
reconciliation must follow the restore test. Retain the actual evidence whose
hash is recorded. Receipt checks validate these attestations and the backup's
bytes; they cannot independently prove that the restore or reconciliation ran.
Before the first authority-boundary event, the Prepare gate requires live
`max(events.id) == BackupWatermark` across the database. A changed journal
refuses; repeat drain, backup and restore testing. After an authority-boundary
event exists, compatible subsequent verification/adoption may legitimately use
the retained pre-emission backup. This live check supplements the operator's
inventory and restore/reconciliation attestations; it cannot prove that a remote
stopped process will never restart. Keep every writer stopped during first
emission and retain all post-backup evidence.

For example, the additional receipt fields are:

```json
{
  "inventory": {
    "complete": true,
    "workers": [{"id": "host/worker", "binary_revision": "saved-revision", "stopped_at": "2026-10-22T12:00:00Z"}]
  },
  "reconciliation": {
    "source_database_identity": "source identity from print-database-identity",
    "restored_database_identity": "isolated restore identity",
    "backup_sha256": "same SHA-256 as backup_sha256 in the emission receipt",
    "backup_captured_at": "2026-10-22T12:01:00Z",
    "backup_watermark": 123,
    "restored_watermark": 123,
    "reconciled_at": "2026-10-22T12:03:00Z",
    "evidence_sha256": "SHA-256 of retained restore/ref/evidence reconciliation artifact",
    "all_refs_and_evidence_reconciled": true
  }
}
```

Use the same operator connection for `--print-database-identity` and `--verify`.
Before the first authority-boundary event, receipt age, the attested binary,
backup hash and database identity are checked before forge reads. The first
proposed verification is itself a boundary event. Its commit checks the live
global watermark and absence of a prior boundary inside the writing transaction,
serialized against event appends in every queue. Keep the stopped window bounded;
command success cannot prove that a stopped remote process cannot restart.

The authority boundary consists of `write_restriction_verified`,
`policy_authority_applied` and `write_restriction_observed`. An outside-push
acknowledgement retains its separate historical operator receipt contract and
cannot waive this stronger authority gate.

The first-emission transaction first takes the exclusive advisory tenancy lock,
then its queue locks, then `SHARE ROW EXCLUSIVE` on `events`. Every PostgreSQL `INSERT` takes `ROW EXCLUSIVE`, so this lock
blocks appends from all queues without adding work to ordinary append paths.
It also serializes competing first emitters. The runtime database role needs
`UPDATE`, `DELETE`, or `TRUNCATE` privilege on `events` to acquire this lock;
an INSERT-only role fails with permission denied at first emission. Queue
registration takes only a shared advisory tenancy lock, uncontended except
during a gated first emission. Ordinary GitHub registration briefly waits through
that transaction, bounded by the caller context and the drained, no-network
first-emission work; there is no independent lock-wait deadline. Production
supplies no emission gate after the boundary exists. Notification and fence row
lock strengths are unchanged. After any lock wait, the explicit READ COMMITTED
transaction uses a fresh statement snapshot, checks the boundary again and reads `max(id)` through
the existing primary-key index. The boundary query uses
`EXISTS (SELECT 1 ... WHERE type = ANY(...) LIMIT 1)` and never reads JSONB.
There is no type-only index in the current schema. Proving absence before first
emission requires scanning the discriminator; after a boundary exists, the
query stops at its first match. A positive result is cached in-process; negative
results are always re-queried. This is monotonic for a running process because
the restore procedure stops all workers before replacing journal history. This avoids the former whole-journal aggregate
and JSON detoast without introducing a migration for the initial switch.

The receipt window is checked again inside the first-emission transaction after
any lock wait. It can still expire after that last check and before commit;
receipt validation is not a lease guaranteeing freshness through commit.

Once a boundary event exists, steady-state reconciliation, tip-move re-pins,
re-enable and subsequent verification require no emission receipt. The configured
database identity binding remains in force. Rollback to GitHub never requires a
receipt. Before the boundary, a missing, expired or invalid receipt reports
`emission receipt stale/invalid`; repeat drain, backup and restore testing.

Consult the independent authenticated GET endpoint before removing redundant
protection:

```text
/v1/authority-completion?installation=INSTALLATION&repo=owner/repo&digest=EXPECTED_EFFECTIVE_DIGEST
```

or the equivalent CLI, which uses the same `--api`/token settings as `queue status`:

```sh
openmerge admin authority-completion --installation INSTALLATION --repo owner/repo \
  --digest EXPECTED_EFFECTIVE_DIGEST [--targets '{"release/v1":"DIGEST"}']
```

It prints the API response and exits **0 only** when the server reports
`complete: true` with at least one partition for exactly the requested intent
(installation, repository, digest and targets echoed back). An incomplete
answer, an empty or mismatched response, or any API failure exits nonzero;
treat every nonzero exit as "retain redundant protection".

A repository can have different effective digests for different queue policies.
For that case supply `targets`, a URL-encoded JSON object mapping each base to
its reviewed expected digest; `digest` supplies an optional common default.
The response echoes the request and returns `complete` plus each partition's
ID/installation/base/lane/watermark, actual/expected digest, governing commit and reason. HTTP 200 can
mean **incomplete**: require `complete: true`. Missing/unavailable discovery is
503 and must be retried; the API never caches failures or a prior success.

“Served queues” conservatively means **every registered partition of this
repository across installations**, including paused, blocked, dormant, retired,
recovery-only and excluded-by-worker/base-allowlist partitions, plus every
selected base in fresh complete branch discovery and every explicit target.
No per-queue receipt or subset of current worker keys can authorize removal.
Retired queues are deliberately not exempt: if they cannot be brought to the
intended policy, retain redundant protection. Do not delete their histories.
Historical partitions under another installation are included and block completion
when their discovery inventory is unavailable. Multiple live installation scopes
currently fail closed: this endpoint cannot issue a partial success for one of
them. Retain protection until a complete aggregate discovery proof is supported;
repeating per-installation queries is not a substitute.

The API freshly resolves installation/repository access, reads the live default
branch/tip and full branch inventory, folds all registered partitions in a
read-only repeatable-read database snapshot, rechecks branch inventory and
default ref, then takes a second durable repository-wide snapshot. Both
snapshots must match. It requires matching digests, authenticated adoption, verified
current protection evidence and current selected discovery inputs. A newly
selected branch blocks completion before registration and while its siblings
still carry old ExpectedRefs. A changed governing tip or default-branch name blocks completion until
all pins rebind, even when two branches point to the same commit. The API performs no mutation or authority opt-in.

This is a point-in-time completion signal, not a lease or permission surviving
later discovery/policy changes. Freeze configuration/branch changes during the
operator switch, consult it again immediately before protection removal, and
recheck after each resumable partial step. Missing/incomplete propagation means
retain protection and retry normal reconciliation. Public activation requires the reviewed production opt-in below; the final
push guard independently consumes the same fresh repository-wide completion gate
inside the Git session lock. Its final database check follows the last outbound
read and immediately precedes the push. A database change between that check and
the remote Git push remains possible, just as with other guards that recheck
immediately before a write. Freeze changes during the switch; this check is not
a distributed lease.

The final guard's discovery budget per push is one installation listing, one
repository listing per active installation, two branch listings, two default
branch reads and two Git ref reads. It also takes two durable completion
snapshots. The early dispatch check performs no discovery. These are logical
calls; each paginated listing can issue several HTTP requests. The resolver adds
one installation listing, repository listings and one branch listing per
reconcile to detect selected-ref changes even at an unchanged governing tip.
Normal configuration, identity and protection reads remain separate costs.

For a large installation, budget both pagination and the number of active
queues. With `I` active installations and one page per listing, completion alone
costs `I + 5` REST requests and two Git ref reads per push; resolver discovery
adds `I + 2` REST requests per reconcile. Multiply by pushes and reconciles in
the quota window, then include normal protection/identity traffic and API
pagination. Retain redundant protection and reduce active queue concurrency or
reconcile frequency when this exceeds the available App budget. Completion
verdicts are never cached to save quota. Serialize operator completion polling;
each poll repeats the same discovery budget.

### Public authority activation (9c-3, Hold)

This implementation merges under **Hold**. Deploy it only after dogfood period B
drains. The actual organization switch is owner-executed PR 11; offline test
success does not authorize that switch. Activation supports **one installation
only**, with **no lanes**. Freeze the default branch, configuration, selectors,
actor and selected branch inventory for the entire switch. Keep every applicable
GitHub protection until current repository-wide completion succeeds.

Authority mode requires **one dedicated database per client (installation)**.
Every queue counts, including paused, retired, excluded queues and queues in
other repositories. Setup verification, public activation and each final public
authority push check this inventory afresh; more than one installation refuses
with `authority mode requires a dedicated database per installation: this database
serves N installations`. The first-emission transaction takes an exclusive transaction advisory tenancy
lock before queue or event locks, serializing registration, and checks tenancy alongside
the database-wide events lock and watermark. The backup/restore receipt covers
the whole database and is waived once any authority boundary exists in it.
A newly registered second installation therefore stops authority even after the
boundary; rollback to GitHub remains available and ordinary GitHub workers are
unaffected. Shared multi-tenant deployment requires a future ADR defining a
per-installation boundary: scoped watermark, lock, receipt and completion, plus
per-tenant export/restore.

1. Inventory and stop every old worker and strict reader, including admin tools
   that lack `write_restriction_observed.read_error` or v7 cursor support. Retain
   the stopped-host inventory, a pre-emission database backup, its SHA-256 and a
   tested restore/reconciliation receipt bound to the emitting binary revision
   and live database identity. Before any authority-boundary event exists, the
   live repository-independent `max(events.id)` must equal `BackupWatermark`.
   An operator's assertion cannot waive a changed journal: repeat drain, backup
   and restore testing. Verification checks this before reads and atomically in the transaction
   that commits the first proposed verification, after its final governing recheck.
2. Install the write rulesets under existing protection. Complete protection
   visibility means the complete applicable inventory: inherited organization
   rulesets, repository rulesets, classic protection, targets, rules, parameters,
   bypass actors and selected-ref coverage. Permission grants, an empty partial
   listing or one visible managed ruleset do not establish complete visibility.
3. Run the existing `setup rulesets --authority openmerge --verify` with the
   reviewed `--verification-plan`, `--operator`, `--app-id`,
   `--repo` and operator `--database-url`. Supply `--emission-receipt` for the
   first emission. Missing, invalid, expired or wrong-binary
   receipts refuse before seeing reads when no boundary event exists. Retain each
   returned **proposed** event ID.
   Copy the plan to a runtime plan and add `draft_id` to each target, naming that
   target's returned proposed event. Keep `queue` and `input` unchanged. No inline
   proposed payload or per-queue receipt substitutes for that durable event.
4. Enable the production worker with `serve --authority-plan runtime-plan.json
   --emission-receipt receipt.json` (or `OPENMERGE_AUTHORITY_PLAN` and
   `OPENMERGE_EMISSION_RECEIPT`). The receipt flag is optional after a boundary
   event exists. The default maximum receipt age is 24 hours;
   `--emission-receipt-max-age` must be positive. The plan binds each queue,
   installation, App, repository, resolved dedicated team and intended digest.
   Review a replacement plan and restart when changing that intended binding.
   After the first boundary event, receipt age, backup bytes and binary revision
   are no longer runtime liveness requirements. The configured database identity
   binding remains required. An opted-in target whose
   landed configuration remains `github` retains the ordinary GitHub path.
5. Land `policy_authority: openmerge` with its dedicated-team `break_glass` under
   the old protection. Workers acquire governing evidence, adopt authentic fresh
   proposals and propagate pins. Before each authority push the production guard
   requires `AuthorityCompletion.RequireComplete` from actual fresh discovery
   and `Store.RepositoryAuthorityCompletion`, including all registered partitions.
   Continue requiring the operator completion CLI immediately before removing
   only reviewed redundant protection, and after every resumable partial step.

For ExpectedRefs/input drift, retain protection, restore GitHub protection first,
land `policy_authority: github`, wait for rollback pins, run a **fresh proposed**
verification for the current inventory, update the runtime plan's event IDs and
binding, restart and re-enable. Require current completion again. An applied
verification or an old successful completion cannot repair proposal provenance.
A rollback to GitHub never requires an emission receipt. It preserves evidence and does not undo the strict-decoder
boundary. To downgrade across new events, `read_error` or v7, stop all workers,
retain the post-emission journal, restore the tested pre-emission backup and
reconcile subsequent real refs and evidence before resuming. Otherwise keep a
compatible binary. Never delete evidence or rewrite cursors to force a downgrade.

### Authority input drift and binary downgrade recovery

Creating a selected branch (for example `release/v2`) can change ExpectedRefs
without changing the effective policy digest. The worker invalidates the old
adoption before any fallible read, so old frozen push contexts stop working.
Keep redundant protection. Use the compatible binary to roll the repository
back to `github`, wait for rollback pins on all partitions, prepare a **fresh
proposed** `setup --verify` with complete current inputs, then re-enable through the reviewed activation procedure. Consult the
repository completion API again. An applied verification alone cannot re-adopt
an invalidated proposal; do not reuse the prior proposal/event ID. This recovery
remains conservative pending a separately designed provenance contract.

If the live base advances externally between planning and building, a successful
live ref read plus Git's explicit negative ancestry result produces a typed
base-moved refusal. Normal configuration/base observation then replans against
the new base. Retain protection and retry; do not bypass validation or classify
this as a content conflict. Missing history, ref-read failures and ancestry
command failures remain errors and never become a successful base observation.
GitHub mode retains its existing behavior and output.

Rollback to `github` does **not** make retained new events or v7 cursors readable
by an older binary. With all workers stopped, run the read-only, migration-free downgrade check
from this build or later:

```sh
openmerge admin downgrade-check --database-url "$OPERATOR_DATABASE_URL"
```

It refuses the authority journal boundary, new authority enum values, v7/future
cursors and unreadable/unknown journal or cursor snapshot data, including a v7-only cursor
with no authority events. A refusal prints `authority downgrade refused`; a database it
cannot read prints `authority downgrade check unavailable` and decides nothing.
Both exit 1, and neither echoes connection details. Success certifies only absence of this ADR's boundary;
check every other target-binary compatibility requirement separately. Never edit
or delete journal/cursor rows to force success. Keep the post-emission database
and immutable evidence, use a compatible reader, or restore the tested pre-emission
backup with all workers stopped. Before restarting after restoration, reconcile
actual base/fence refs, external landings and immutable decisions/evidence since
the backup, retain the reconciliation artifact/receipt, regenerate the restored
source identity and repeat drain/backup verification. A successful check is
not an automatic restore or an authorization to restart stale state.

The additive `installation_count` field in `/v1/authority-completion` is new in
ADR 0008 PR 9c-3. An older admin CLI using `DisallowUnknownFields` fails closed
against this newer server; upgrade the admin CLI with the server. A restore under
a live process would invalidate the positive boundary cache: stop all workers
before replacing journal history, as required by the restore procedure.

## Offline audit export (ADR 0008 v1)

Deploy this command only after period B drains (**Hold**). Supply the operator
connection through OPENMERGE_DATABASE_URL; a SELECT-only role with schema USAGE
and SELECT on queues/events is sufficient. The command sets read-only transaction
defaults and uses a repeatable-read snapshot, without migrations, worker locks,
journal writes, status API calls or forge access.

```sh
openmerge audit export --repo owner/name --installation 7 \
  --from 2026-10-22T00:00:00Z --until 2026-10-23T00:00:00Z --out ./audit-day
openmerge audit export --manifest ./audit-day/manifest.json --out ./audit-replay
```

All range selectors are required in range mode; manifest mode rejects any of
--repo, --installation, --from or --until, even if empty. Bounds are half-open,
normalized to UTC microseconds by ceiling both endpoints. Equal bounds are
valid. Installation is a positive decimal ID; repository is the exact stored
owner/name, including case. Tenancy must resolve to that installation.
The output directory must not exist. Publication uses private sibling staging
and atomic no-replace rename; never overwrite a previous result. Exit 0 means
success; 2 means usage, selectors or bounds; 1 means a sanitized connection,
source, integrity or publication failure. See audit --help. If publication
fails after rename, a complete directory may exist: preserve it and exact-replay
it to a distinct fresh directory before investigating or retrying.

`governing authority pin rejected by queue fold` means retained pin history
contains a malformed or digest-mismatched transition. It blocks later exports
for that queue even after a valid replacement. Preserve the journal and consult
the authority-transition incident procedure/maintainer; do not delete or edit
retained history to make export pass. A pre-incident range whose decisions
precede the rejection, or a different unaffected queue/repository, can still be
exported where its dependency closure is valid. There is no CLI repair bypass.

V1 is frozen with PR 10a-2b-2. All four streams and the manifest are deterministic;
serialization shape changes after merge require a version bump. Replay verifies
saved selected sources against the database and recomputes exact dependency
closure, but accepts a fully rehashed export with dropped events or whole queues.
Only an externally anchored manifest (10b) detects such omissions. Retain the
original manifest separately; local integrity alone does not establish completeness.

Memory is bounded by paging/spooling, but database work can grow with owners ×
retained events × checks. Long configuration history is folded incrementally per
queue segment within each snapshot. Export and replay can each take minutes on
large journals; the command has a five-minute snapshot timeout. Use modest
intervals and preserve failed outputs as described above. Growth inputs, measured
plans, timings and projected costs are in the 10a-2b-2 evidence note.
Existing migrations 006/007 index decision identity and settlement lookup.
Other lookup families still scan retained events; this release adds no index. Do not infer sublinear database
work from bounded memory or the pin-fold cursor.

Audit publication requires Linux `renameat2(RENAME_NOREPLACE)`; the CLI fails
before opening the database on other platforms. `--timeout 5m` is the default
snapshot deadline; choose a larger Go duration (at least 1ms) for larger journals.
A large `--timeout` can hold the repeatable-read snapshot open for that duration,
holding back vacuum reclamation of old row versions.
Timeouts report `audit: export timed out` (exit 1). Success prints the published
directory and `manifest SHA-256: <hex>` for external anchoring. This printed
value, also printed by `--manifest` replay, is the v1 `manifest_sha256`: the
canonical manifest hashed with that field empty and without LF. It is the
anchor value; `sha256sum manifest.json` hashes the file bytes and is not the
anchor.

After a killed run, first confirm its process has stopped. In the intended
output parent, inspect private `.audit-staging-*` directories and their nested
`.closure-*` spools, then remove only directories belonging to that stopped run.
These are unpublished temporary artifacts; preserve any published target and
verify it by replay before retrying. Do not sweep another active export's spools.

### External anchors and overlapping windows

This procedure is **Inert; deploy the compatible audit binary only after period
B drains**. It does not complete the PR 11 real-journal acceptance. Before use,
the organization must choose and record the anchor custodian/signing identity,
verification and recovery owners, cadence, retention duration, access separation
from database/service administrators, recovery objectives, and maximum commit lag
L. There are no supplied defaults or compliance guarantees. Establish L through
bounded writers or a quiesce/drain barrier; require `until <= custody_time - L`.
Without a defensible bound, label the window provisional. Backdating and late
commits remain indistinguishable; omissions before the first anchor are unknown.

Run exports, fresh crosscheck exports, and replays as a dedicated OS identity
`audit-reader`, using a separate database login with CONNECT, schema USAGE, and
SELECT only on events/queues, without inherited writes or database CREATE/TEMP
(including PUBLIC grants). Backup/restore uses a separately authorized recovery
identity and an isolated database. Do not run migrations or supply forge/service
credentials. Budget snapshot duration, spool/database space, queue inventory
memory and I/O: repeatable-read snapshots can delay vacuum. A timeout fails the
export; partial output is not evidence.

The separate trusted OS custodian `audit-custodian` obtains the five files from
`audit-reader` by a controlled copy/handoff. Publication creates a 0700 directory
and 0600 files **owned by audit-reader**; the custodian cannot simply traverse
these as an unrelated unprivileged user. Use an authorized privileged copier or
have audit-reader copy bytes into a custodian-controlled intake, then have the
custodian freeze its own new copy. Reject symlinks/nonregular files, never share
hard links, and create the destination parent as 0700 while copying. The
custodian sets the frozen parent to 0750, bundle to 0550, files to 0440, granting
only a dedicated reader group traversal/read. audit-reader belongs to that group
but owns none of the frozen paths and cannot write or chmod them. The custodian
must not modify the frozen copy during verification. Root/owners are not
restrained by these modes: custody separation is a procedural precondition.

For a quiesced range, audit-reader runs the existing range export twice, to
separate new paths, and replays the custodian's frozen original to a third new
path. Freeze the fresh crosscheck too. Capture each command's stdout, stderr,
and exit separately; require exit 0 and compare **all five files** of original,
crosscheck and replay with `cmp`, requiring exit 0 for every comparison. Keep
replay outputs under exclusive trusted control and arrange the same controlled
custodian handoff before comparison. Any difference requires investigation.

Successful stdout is exactly two LF-terminated lines: line 1 equals the literal
`--out` argument and line 2 is `manifest SHA-256: <64 lowercase hex>`. Reject any
extra line or trailing content. Retain that printed digest, not `sha256sum
manifest.json`: v1 hashes the canonical manifest with manifest_sha256 empty,
without LF. Receipts are exactly 64 lowercase hex bytes plus one LF, compared
exactly; never use permissive `sha256sum -c`. Independently retain all five files,
count/terminal, repository/installation, bounds, version, binary hash, compatible
configuration, backup identity/hash, required Git objects, custody time and
immutable storage identity. Never retain credentials. Separately administered
versioned/write-once storage and an organization-approved detached signature can
protect custody; preserve signer verification/rotation/revocation records.
Encryption alone does not authenticate the history or prove completeness.

For later verification, retrieve the trusted receipt/bundle, verify its signature
against trusted signer identity, and freeze the candidate as above. As
audit-reader, replay the same frozen candidate against the intended DB or an
isolated restored backup to a new output. Require exit 0, exact stdout syntax,
and all five candidate/replay files equal. A failed replay is never an anchor
comparison pass. With those preconditions, run:

```sh
python3 scripts/verify-audit-anchor.py --anchor "$TRUSTED/digest.txt" \
  --replay-stdout "$REPLAY_STDOUT" --replay-out "$NEW_REPLAY" \
  --earlier "$PRIVATE/A" --later "$PRIVATE/B"
```

`--replay-out` must equal the replay command's literal `--out` value. Omit
`--later` for anchor-only comparison. The offline stdlib tool reads files (or
`--anchor -` / `--replay-stdout -` from stdin), never the DB. Both bundles must
already be replay-validated; replay B to its own fresh output, require exit 0
and compare all five files with B before comparison. Use `-` for at most one
input. The verifier rejects a bundle directory or file whose final path component
is a symlink (symlinked ancestors are not checked; the frozen-copy precondition
covers them), CRLF selection rows, and selection bytes whose SHA-256 differs from
`selection_sha256` in the replay-validated manifest. It hashes the same single
read of the selection stream that it parses, so hash and parse see identical
bytes; this detects post-replay edits, including erasure. This does not replace the
five-file replay comparison or the frozen-copy custody requirement.
Exit 0 says `anchor and overlap match`; 1 reports
anchor/source mismatch, missing source or in-window addition; 2 means malformed,
unreadable or usage input. Preserve originals, outputs and actual exits on
failure; never replace the trusted digest to make a mismatch disappear.

For anchored A and later B, first replay A into C on the later DB and compare
all five files to A: B cannot substitute for this retention proof. The verifier
joins exact integer queue/source IDs, comparing queue keys, type, recorded_at
and payload digest. `context differs …` reports contextual role/reference
changes but still exits 0; an exit-code-only gate misses these notices. Review
them before anchoring B.
A scope source in the common half-open interval must exist in B; any B source
inside A's anchored interval absent from A is an in-window addition requiring
investigation, even with an adopted lag bound. `--require-full-coverage` also
requires B's interval to contain A's entire interval and every A source,
including closure dependencies. Ordinary partial overlaps do not require A's
closure-only sources. Chains restart at zero, so prefix/terminal comparisons
are unsuitable. Anchor B separately only after discrepancies are resolved and
recorded, retaining A and its receipt.

Replay rebuilds from retained DB sources; it does not walk supplied
previous_sha256 links. Unchanged stream hashes detect archive edits; a fully
rehashed valid subset or corresponding DB edit may replay successfully. The
independent earlier manifest anchor detects the changed commitment, including
watermark changes that leave the terminal unchanged. This establishes agreement
with a prior commitment, not authenticity or completeness before anchoring.
Keep originals before upgrades/first new-event emission; mode rollback does not
waive v6/v7 snapshot or unknown-event downgrade limits. Restore in isolation and
reconcile remote history and post-backup landings before service recovery.


#### Daily anchoring

Start only after dogfood period B drains. The chosen private fingerprint store
is `CanadaApollo6/openmerge-audit-anchors`, which has not yet been created.
The operator also acts as custodian for this trial, so the identity separation
above is not achieved. A work rollout must move the anchor repository to a
different owner. This routine does not establish independent custody.

1. Create the private repository with an initial commit. Apply a ruleset that
   forbids force-push and deletion, including for the automation identity.
   Provision a dedicated write deploy key for append-only pushes and a private
   checkout with a tracking branch. GitHub deploy keys do not themselves enforce
   append-only file content; protect access to the key and review receipt diffs.
2. Provision the `audit-reader` OS identity and read-only database login described
   above. Remove inherited write, CREATE and TEMP privileges. Keep the database
   URL in a protected environment file, with no service or forge credentials.
   Keep the deploy key separate from the database credentials.
3. Install a compatible revision-stamped binary, Bash, GNU date/find, flock,
   Python 3, and Git. Establish and record maximum commit lag L before scheduling.
   Set `AUDIT_LAG_SECONDS` to L in seconds; 08:00 local must be late enough in UTC.
4. Set `OPENMERGE_DATABASE_URL`, `AUDIT_REPO`, `AUDIT_INSTALLATION`,
   `AUDIT_OUTPUT_BASE` and `AUDIT_ANCHOR_CHECKOUT`. Optionally set `OPENMERGE_BIN`.
   Use a dedicated private output base and one clean anchor checkout per runner.
   Run `scripts/audit-anchor-daily.sh --dry-run` first. It exports the previous
   UTC day, replays into a fresh directory and compares both digests and all five
   files. It appends a local receipt but skips commit, push and pruning. Inspect
   and remove that uncommitted test receipt before the first scheduled run.
5. After B, schedule the script daily at 08:00 local. Each TSV receipt records
   inclusive from, exclusive until, repository, installation, manifest digest,
   binary revision and local bundle directory name. Keep receipts forever;
   successful runs prune local bundles older than 90 days. Preserve failures and
   investigate before retrying; a failed push leaves a local commit to reconcile.
6. Once a week, after the daily run, invoke the same script with `--weekly`.
   It exports and replays the previous seven UTC days, freshly replays the last
   seven daily bundles, then runs the offline verifier with full coverage against
   each daily receipt. Missing days or mismatches fail. Review any `context
   differs` notices as described above. Weekly mode appends no receipt.
7. Each quarter, restore a backup into an isolated scratch database using the
   recovery identity. Sample retained anchors, retrieve their corresponding
   bundles from backup if older than 90 days, replay, compare all five files and
   run the offline verifier. Record the sample, backup identity and outcome.

Keep frozen bundles and captured stdout/stderr private as described above.
No repository, key, schedule or live service is created by this change.

## Shared reader retention recovery

The shared reader and writer foundations (ADR 0010 / ADR 0011) are **Hold**:
deploy only after ADR 0009 period B drains. No production path calls the new
`Store.AppendSide` API. Before enabling any writer, deploy these readers and
[audit v2 (#181)](https://github.com/CanadaApollo6/openmerge/pull/181) everywhere,
drain incompatible readers/workers, verify snapshot-plus-tail replay, and verify
a whole-database pre-emission backup. Public activation, dependency namespace
initialization/recovery and owner opt-in remain gated.

**Slice 4b activation boundary (Hold):** the private provenance pass can now
co-commit fresh per-member inventory/T evidence with decisions, check evidence
and `LandingStarted` in the shared transaction. The public loader continues to
reject `provenance.requirements`; public queues install no pass and emit no side
evidence. Approval policy alone never activates provenance. The internal/test
installer supplies stable already-acquired local objects and trusted composer
observations; it must also use the existing 4a publication filter. This is not a
public policy switch.

Activated workers run `RecoverProvenance` before new work and immediately after
an accepted `Landed`. It calls `RecoverLanded` using retained immutable tested
inventories/mappings, accepted decisions and the actual outcome, with no network
or current-ref reads. Supported outcomes are the exact T composition or the
unmodified fast-forward terminal head. It refuses an unexpected changed result.
A superseded historical inventory uses a new recovery generation with the exact
current predecessor; only that attempt's consumed inventories may accompany L.
All #188 reserve/cap checks remain enforced. A permanent refusal stops the
activated worker before admission; follow the recovery procedure below rather
than clearing evidence or disabling policy to make it proceed.

Deployment remains after ADR 0009 period B, compatible 3c readers/audit v2 on every
worker, the older-worker drain, a verified pre-emission backup and explicit
slice-5 owner opt-in. **High-volume activation remains blocked on a structural
fence bound or explicit lifetime/rollover policy.** Keep the existing capacity
gauge and 70% alert. The [4b proof map](research/adr-0010-4b-evidence.md) includes
the isolated real Postgres backup/restore downgrade rehearsal.

**Writer prerequisites delivered under Hold for ADR 0010 slice 4b and ADR 0011
slices 4a/4b (production integration remains disabled):**

- **Shared transaction.** `appendSideTx` accepts the existing queue-locked
  transaction. The `startLandingWithSide` foundation seam supplies exact inserted
  decision IDs and commits decisions, companions, v9 cursor upgrade, dedupe
  manifest and append checkpoint together. A failure after decision insertion
  rolls everything back. The ordinary production start supplies no companion
  builder. Acquire the queue advisory before action/observation locks or event
  INSERTs; FK `KEY SHARE` on queues is part of this order. Never call the public
  wrapper in a second transaction while holding the first transaction's lock.
- **Landed recovery.** The `RecoverLanded` helper checks an immutable
  `Landed` row against the immediately preceding queue state, rejecting stale
  outcomes and mismatched attempts/decisions. It validates exact historical batch
  membership, revisions and heads, every member's complete accepted decision
  (including classification, policy/brief identity and source evidence), and
  the terminal member's final landed composition (intermediate results remain
  valid). Close, dequeue and head-change events preserve a pending landed
  companion until recovery completes. V9 checkpoints optionally retain bounded
  `Awaiting` attempt/outcome markers, including alongside `FenceTable`; deploy
  this reader before writing those checkpoints. Original event fields do not
  change. An existing companion under another key
  returns typed `LandedAlreadyCompleteError`. Its deterministic outcome key
  compares complete canonical bundles on retry. The caller must reconstruct the
  actual landed composition from immutable inputs: a `Landed` row alone cannot
  supply source-to-build mappings. Recovery appends the missing companion once,
  allowing tested evidence to retire. The activated coordinator invokes it
  before starting new work; public activation remains disabled.
  A recovery bundle may contain only the landed companion and inventory chunks
  consumed by its exact member generations. Reconstructed inventories must match
  the historical tested inventory after rebinding the generation and exact
  predecessor; unrelated side records are refused. Recovery preserves the dynamic
  retirement reserve for every remaining active inventory and attempt, even when
  bypassing the configurable admission high water.
  Recovery bypasses admission high water only when it settles the pending attempt
  and strictly reduces retained projection bytes; all hard caps still apply.
- **Lifetime fences.** Exact arbitrary string identities and original journal
  anchors are retained in a lossless, sorted, front-coded compressed fence table.
  A watermark/range is unsound because these IDs have no monotonic allocation
  contract. The strict `FenceTable` v9 checkpoint shape reads both earlier v9
  shapes; legacy-only queues retain their full byte identity. Replay measures
  compressed capacity geometrically; bundle preflight measures once and final
  checkpoint encoding reuses the unchanged pack. The 32 MiB projection ceiling
  still applies,
  including base64 framing. Decoding also bounds expansion to 128 MiB and
  2,097,152 fences; preflight and encoding enforce these same memory bounds.
  This reduces lifetime metadata substantially without
  pretending that arbitrary, unbounded histories fit a finite cap. The scale test
  folds 200,001 real landings outside short mode (400 in short mode), asserts
  linear capacity-measurement work, and checks restart, suffix equivalence and
  exact generation/attempt reuse rejection. Original audit rows never change.

- **Capacity and queue-event reserve.** Side append preflights once per complete
  bundle. It caches the exact fence pack for the final cursor encode, rather
  than packing per record. `Store.WithSideHighWaterPercent` configures the
  admission threshold (default 80% of the 32 MiB provenance projection cap;
  values must be 1–99). The same fraction reserves expanded fence bytes and
  entry capacity. Admission also reserves the exact expanded generation-fence
  charge for every active inventory and the maximum-ID attempt-fence charge for
  every active companion. A percentage in range still refuses the bundle if
  that inventory could later exceed either hard fence cap on retirement.
  Queue-driven retirement and recovery replay retain the
  hard cap, reserving space for terminal fence growth. Queue status JSON exposes
  `provenance_capacity.fence_count`, `projected_bytes`, `limit_bytes` and
  `high_water_bytes` for queues with retained side evidence. Alert at 70% of
  `limit_bytes`, before the default 80% admission refusal. Track fence count and
  growth rate to plan rollover well before refusal; raising the threshold cannot
  spend the required active-inventory retirement reserve. Projected bytes include
  active canonical evidence,
  compressed/base64 fences and framing, but exclude the queue/dependency base;
  final snapshot encoding independently enforces the complete snapshot cap.

**Still BLOCKING before enabling writers on a high-volume queue:** a structural
fence bound (e.g., deriving attempt/generation IDs from monotonic journal positions
so a watermark can replace per-ID fences), or an explicit lifetime/rollover
policy. Lossless compaction only delays the cliff: the reviewed identity
distribution reaches the hard projection cap at about 281k lifetime landings;
the default high-water mark refuses new side evidence earlier. This estimate
is workload-dependent, not a supported lifetime guarantee. No structural bound
or rollover policy is implemented here. Production integration, reader-first
rollout, authority/start fencing, namespace recovery and owner opt-in remain
gated; no production writer is enabled.

### Permanently failing landed recovery

An accepted `Landed` without its companion pins its tested evidence and an
`Awaiting` marker. Close, dequeue, head changes and queue retirement cannot clear
that marker. Up to **32** unfinished attempts can remain pinned indefinitely;
when that budget is exhausted, new tested landings are refused. Successful
recovery is the only exit for an accepted pending landing; there is no
force-forget or abandon-pending API. Raising high water cannot fix inconsistent
evidence, and recovery may drain markers above high water within the hard caps.

Keep side emission stopped while diagnosing a permanent refusal. On a restored
copy, inspect the immutable `Landed` outcome and immediately preceding accepted
batch/attempt, every member's revision/head and full accepted decision, tested
companion, inventory chunks/generation predecessors, and original layer targets.
Reconstruct the actual landed composition from immutable build inputs, including
intermediate member results and the terminal `NewBaseSHA`. Check the strict v9
reader version, namespace binding, missing originals, superseded inventories,
and capacity error separately. If an inventory was superseded, recovery needs a
fresh valid generation for the same historical inputs with the exact current
predecessor; an old generation must not be reused.

Escalate an irreconstructible composition, missing original, contradictory
historical tested evidence or persistent hard-cap failure to the maintainers
with the queue/attempt/outcome IDs, exact error, reader version and a protected
copy of the original journal and checkpoint. Validate a reviewed compatible
reader/retention fix or restore on that copy before resuming emission. Never
delete pending markers, alter original events or invent a successful companion
to release capacity. Public activation of these helpers remains disabled under Hold.

### Retention decision and audit closure

**Retain all original journal evidence, without a time window.** ADR 0008 10b's
retain-all rule remains in force. Compaction affects only the derived provenance
projection and v9 audit cursor; it never deletes or rewrites events or snapshots
in the database. No migration or index is added; the high-water option controls only future side admission.

The projection retires a tested/landed companion pair after validating the landed
companion. Existing terminal queue journal events also retire non-landed work:
`LandingAbandoned` and discarded/rebuilt batches release their tested companions;
accepted dequeue acknowledgements, PR closure, head/revision supersession and
queue retirement release the change's unfinished generations and companions.
An uncertain fenced landing keeps its evidence until its definitive outcome.
Retirement is deterministic on full replay and snapshot-plus-tail replay; no
new event kinds or changes to existing event fields are needed. Settlement releases the pair's inventories unless another unfinished tested
attempt still needs them. A superseded inventory is released when no unfinished
attempt pins it. Current and partial inventories and unfinished tested companions stay retained
only while the work is live. Small generation/chunk-zero and completed-attempt fences survive
compaction: stale CAS predecessors, duplicate generations and reused completed
attempt IDs remain rejected across restart. The latest generation fence remains
even after its inventory is retired. Further work after completed evidence must
acquire a fresh generation with that exact predecessor, never reuse the retired
inventory for authorization.

This is projection retention, not audit retention. #181 closure follows the
original IDs for every chunk sibling, previous generation, tested companion,
decision and layer reference, transitively and across the selected date range.
Those rows remain exportable after settlement, supersession, policy disablement
and retirement. A missing required original still fails export. A compact cursor
is never a replacement for source evidence or an external anchor.

The active provenance budget is **4256 records / 32 MiB**, now counting
compact fence metadata within the byte budget as well as canonical payloads.
There are at most **32 unfinished tested companions**, **385 distinct reference
targets per companion**, and **12,320 targets in aggregate** per queue. The
record bound reserves `32*(128+1) + 128` records so 4096 distinct inventories,
32 companions and a batch of headroom fit. Reference bounds derive from the loader/planner maxima of 128 batch members
and speculation depth 32: two references per member plus the decision anchor, with another 128
references of headroom per companion. The aggregate matches the exact-ID lookup
cache scope and caps compact-v9 reloads at 12,320 reference queries. This bounds v9
reference revalidation by active work rather than lifetime completed attempts;
completed history requires no reference queries on reload. Inventories pinned by
unfinished attempts are deliberately not evicted to admit more work. These are
resource ceilings, not authorization or scheduling policy. Future writers must
handle refused capacity without publishing effects or incomplete bundles. Compact
fences also consume a finite budget; there is no promise of unlimited history in
RAM. Original journal history remains unbounded by these projection limits.

`AppendSide` reserves actual sequence IDs and builds a complete bundle under the
queue advisory lock. It applies the exact composed replay validator, including
references, namespace binding, chunk order, member agreement and retention, before
**any** event INSERT. Each intermediate projection and final snapshot must fit;
writer preflight stays strictly below the record and byte ceilings. A rejection
leaves both journal and cursor unchanged. Reserved sequence gaps are harmless.
The API admits exactly provenance observations/companions and the six existing
dependency journal kinds. It refuses every queue event, including landing
decisions. Decisions remain on the existing policy/phase-authorized
`StartLanding` path; future writers must co-transact companions there.
A matching dedupe key succeeds only when bundle length, reserved IDs, independent
binding and all canonical stored payload bytes match; a changed bundle fails.
Unverified dependency evidence stays quarantined, and a caller-supplied binding does not make currently unconfigured runtime readers accept dependency
references in provenance layers.

### Cursor prefix invariant and rollback

A legacy v6/v7 cursor certifies a side-evidence-free prefix. `AppendSide` upgrades
the cursor to **v9 in the same transaction as the first side append**, retaining
its existing scanned watermark and outside-move bookkeeping. A missing cursor is
created at watermark zero; no unaudited moves are skipped. The queue lock and
existing observation-byte CAS prevent a stale audit from undoing the upgrade.
Invalid existing cursors cause append refusal rather than guessed progress.

`Load`, `Plan` and `StartLanding` treat checkpoints as disposable caches. On a
cursor-envelope unmarshal error or checkpoint `LoadSnapshot` error, they log
`journal checkpoint unusable; replaying from seed`, with the queue ID and reason,
and replay from the queue seed. They do not change the cursor. Invalid journal
records still fail replay; this fallback cannot make an incompatible side event
readable by an older binary.

`AppendSide` remains strict because it must not append on state it cannot verify.
The next outside-push audit pass reports `CursorResetReason`, rebuilds from the
journal in bounded pages and commits the replacement cursor using the original
bytes for CAS. Appends recover after that reset commits, provided journal replay
and the other checkpoints are valid. A read alone does not repair the cache.
An incompatible append checkpoint also falls back for reads, but requires a
compatible reader or reviewed cache repair before appends can resume.

The owned provenance replay fold updates lifetime rejection fences in place,
with rollback on validation failure. It copies only bounded active state per
event; the immutable `journal.Apply` adapter remains available for callers that
retain old states. Checkpoint encoding, decoding and sorting still process compressed lifetime fence metadata,
with that cost paid at checkpoint boundaries rather than on every folded event.
`AppendSide` atomically saves its own derived checkpoint in observations.
`AppendSide` and `Store.Load` resume the newest eligible append/audit checkpoint
and stream only its suffix in 512-row pages; the first checkpoint still requires
bootstrap replay. This checkpoint never advances outside-push audit bookkeeping.
Dedupe manifests are also derived observations; immutable journal rows remain
the evidence source. No SQL migration, index or snapshot-table writer is added.

Outside-push audits still read only the next **256 events per poll**. V9 resumes
the bounded suffix and retained state, with exact-ID lookups only for unfinished
companions. Tests cover a 268,000-row history, idle/restart and nonempty suffixes,
20 consecutive checkpoint-resuming appends, 10,000 serial abandonments, and
10,000 dropped changes per terminal path. Active payload retention stays at one
record for serial abandonments and zero for dropped changes. Lossless fence-table
compaction now reduces lifetime metadata, while finite fence/checkpoint budgets
and future writer activation remain explicit constraints.
Legacy and no-side-evidence cursors retain their complete existing envelope/snapshot bytes,
including #178's full cursor goldens.

The original strict v9 shape remains readable. A distinct strict v9 shape with a
`Compaction` checkpoint carries retired fences after projection compaction; it is
not emitted for legacy-only queues. Every checkpoint field is required and
validated. The strict `FenceTable` shape losslessly compresses the same exact
fences and is another reader compatibility boundary. Deploy this reader before
emitting that shape. **The first side event or compact v9 cursor is a future binary rollback
boundary.** Once writers are enabled, rollback requires a compatible reader, or
stopped workers, retained post-backup evidence, restoration of the verified
pre-emission backup, token rotation and reconciliation of remote refs/subsequent
landings. Removing policy/configuration does not erase that boundary. An older
binary rewriting a side-era cursor as legacy is unsupported.

If a retention limit occurs during preflight, the complete bundle is refused;
keep serving the already valid journal and stop that producer's new emission.
If replay encounters an old over-budget history, stop emission and retain the
journal, cursors, logs and verified backup. Use an isolated restored copy to
validate a reviewed compatible retention/budget change before resuming. Restart,
cache deletion, policy disablement and deletion/skipping of evidence are not
recovery. `journal.RetentionLimitError` includes counts and this recovery link.

Dependency projections decoded without independently verified database identity
and incarnation are quarantined. They can be replayed for compatibility but the
shared reader's `Dependencies` accessor refuses them. A snapshot cannot supply
its own trust binding. The future startup/writer integration must supply the
operator-verified binding before dependency satisfaction is exposed; no live
policy or satisfaction path is enabled here.

### Invalid records and unresolved references

An invalid record or unresolved/mismatched reference stops `Load` for its queue.
`/status` and `Journals` also fail for **every queue in the same requested key
set**: they do not publish partial results or quarantine just the offending row.
This is a real availability risk comparable to the **2026-10-16 wedge**. Every
future writer slice must validate complete candidate records, reference targets,
batch-wide identities and capacity before append, in the transaction/snapshot
that fences its write. Reader rejection is not an acceptable write-time check.

On failure, stop emission for the affected queue and record the queue ID, event
position and error. Keep immutable events, snapshots, logs and a verified
whole-database backup. Reproduce replay against an isolated restored copy and
identify whether the producer wrote invalid evidence or the reader has a bug.
Recovery requires a reviewed compatible **code fix or restore**; immutable events
cannot be edited in place. Test the fix with full replay and snapshot-plus-tail
reads, including the combined reporting key set, before deployment and resuming
emission. A restore requires stopped workers, retaining post-backup evidence,
token rotation and reconciliation of actual remote refs/subsequent landings under
ADRs 0010/0011. Restarting, clearing caches, disabling configuration, skipping or
deleting a bad record does not repair history.

The shared-reader AST writer guard is a **tripwire**, not proof of writer absence.
It checks production Go imports/calls and individual raw string literals for new
kind names; it does not catch string concatenation, `.sql` files or `_test.go`
files. Only `journalreader/snapshot.go` may compose v8/v9 for derived audit
cursors. `store/side_append.go` is the sole allowlisted production adapter for encoding
and inserting side events, and the guard rejects production references to its
unsanctioned entry points (including function-value captures).
Transaction-local append is
allowlisted only in the adapter and landing-start seam. Ordinary starts must
pass a literal nil companion builder. The private activated gate is the only
sanctioned start caller, and the activated startup/result coordinator is the
only recovery caller. Function-value captures and ungated callers are refused.
Review every future persistence path independently.
