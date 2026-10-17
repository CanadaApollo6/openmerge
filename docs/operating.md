# Operating OpenMerge

Build with `make build` and complete the [GitHub App and ruleset setup](../README.md#running-openmerge-serve)
before starting a service. The repository's default branch needs a valid
`.openmerge.yml`, and CI must run on pushes to `openmerge/queue/**`. Use one server per installation and a
process-exclusive git cache. Postgres must be available before startup; startup
applies database migrations and ensures the fence without resetting it.

For containers, use the [compose quickstart](../README.md#docker-compose-quickstart).
For recorded queue state and PR explanations, enable `OPENMERGE_API_LISTEN=:8081`
and use `openmerge queue status [--json]`. The [API guide](api.md) documents the
bearer token requirements, response contract, CLI and exit codes. No database or
GitHub credentials are needed by an API client.

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
OPENMERGE_GITHUB_INSTALLATION_ID=456
OPENMERGE_GITHUB_PRIVATE_KEY=/home/you/.config/openmerge/app.pem
OPENMERGE_REPOS=owner/repository
OPENMERGE_BASE=main
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
A writer can post `@openmerge refresh` after fixing a configuration, build or
landing problem to clear a pause and request fresh observations. Invalid
default-branch configuration remains paused until corrected on the default branch.

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
