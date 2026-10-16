# Operating OpenMerge

Build with `make build` and complete the [GitHub App and ruleset setup](../README.md#running-openmerge-serve)
before starting a service. The base branch needs a valid `.openmerge.yml`, and CI
must run on pushes to `openmerge/queue/**`. Use one server per installation and a
process-exclusive git cache. Postgres must be available before startup; startup
applies database migrations and ensures the fence without resetting it.

For containers, use the [compose quickstart](../README.md#docker-compose-quickstart).
For recorded queue state and PR explanations, enable `OPENMERGE_API_LISTEN=:8081`
and use `openmerge queue status [--json]`. The [API guide](api.md) documents the
optional bearer token, response contract, CLI and exit codes. No database or
GitHub credentials are needed by an API client.

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
base-branch configuration remains paused until corrected on the base branch.

## Runtime repository rules

Serve reads the repository's actual default branch, lists **all pages** of
repository and inherited rulesets, and fetches each detail at startup. It repeats
this every ten minutes (`--rules-interval` / `OPENMERGE_RULES_INTERVAL`), at the
next worker cycle. Reads share the driver's conditional cache and quota budget.
A read failure also pauses new work until the next successful check. Pauses use
`repository-rules` and appear in the API, PR check, journal and logs. Identical
results do not append duplicate pause events. A later passing check automatically
resumes that rules pause, preserving unrelated external pauses; an operator
`@openmerge refresh` cannot bypass a known rules failure. A pause still permits
`ResolveLanding`: a push already in flight must settle truthfully through its fence.

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
Classic branch protection and validation of installed App permission grants are
still operator responsibilities and follow-ups. Ruleset checks are periodic
observations, not an atomic policy lease; do not weaken fence ownership while
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
