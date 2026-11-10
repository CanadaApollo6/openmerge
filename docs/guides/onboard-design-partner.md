# How to onboard a design partner

This guide is for the person who operates a concierge alpha instance. In the
concierge alpha, the owner runs one OpenMerge instance by hand for each design
partner. [ADR 0012's owner decisions](../adr/0012-hosted-service-architecture.md#owner-decisions-2026-11-09)
set the rules, and at most three partners may run at once.

Each partner gets one `serve` process, one dedicated Postgres database, one Git
cache, and a GitHub App that the partner's organization owns. In the commands
below, `PARTNER` is a short lowercase name for the partner, such as `acme`.
`ORG/REPO` is one of the partner's repositories.

## Before you start

Send the partner a written notice before anything is installed. ADR 0012
requires the notice to say four things:

- The service is pre-alpha.
- One person operates it, and no backup person exists.
- The App private key is a file on the serving host, and the service user can
  read it.
- No recovery-time promise exists.

Agree the flat pilot fee in the same message. The fee buys no SLA.

Confirm that the partner has:

- An organization owner, who creates the GitHub App.
- A repository admin, who edits the rulesets.
- A required CI check that can also run on pushes to `openmerge/queue/**`.

Confirm that the host has:

- Linux with systemd, and `git`.
- Postgres. CI tests OpenMerge against Postgres 18.6.
- Outbound HTTPS to GitHub. Polling mode needs no inbound listener.
- Power and network all day. The queue stops when the host sleeps, and the
  partner's pull requests wait until the host returns.

## Create the GitHub App

An organization owner runs these steps on their own machine, because the App
must belong to the partner's organization. The setup command gives the owner
ten minutes to finish in the browser.

1. Download a release archive, as the [README](../../README.md#install) shows.
2. Create the App:

   ```sh
   ./openmerge setup github --owner ORG --repo ORG/REPO --name OpenMerge-ORG
   ```

   The command opens a GitHub page. Click **Create GitHub App**. The command
   saves the App ID and the private key in `~/.config/openmerge/APP-SLUG/` and
   prints the next steps. This App uses polling, so it has no webhook.
3. Open the printed installation URL. Choose **Only select repositories**,
   select the repositories that OpenMerge will serve, and install the App.
4. Create a fine-grained token for a repository admin with
   **Administration: Read and write** on those repositories. Export the token
   as `GITHUB_TOKEN`.
5. Preview the ruleset changes, then apply them:

   ```sh
   ./openmerge setup rulesets --repo ORG/REPO --config-dir ~/.config/openmerge/APP-SLUG
   ./openmerge setup rulesets --repo ORG/REPO --config-dir ~/.config/openmerge/APP-SLUG --apply
   ```

   The preview prints every problem that it finds. Fix each problem before you
   apply. The README section
   [Setting up the GitHub App](../../README.md#setting-up-the-github-app)
   explains what the command changes.

## Prepare the repository

The partner makes two changes on the default branch of each repository.

1. Run the required CI check on queue branches. For GitHub Actions, add the
   branch pattern to the workflow's `push` trigger:

   ```yaml
   on:
     pull_request:
     push:
       branches: [main, "openmerge/queue/**"]
   ```

   A check counts only when it ran on the queue branch. See
   [Queue CI provenance](../operating.md#queue-ci-provenance).
2. Commit `.openmerge.yml`. Replace `ci` with the name of the required check:

   ```yaml
   version: 1
   queues:
     - name: default
       merge_when:
         - check-success = ci
   ```

   This queue squashes and tests one pull request at a time. Keep those
   defaults until the first landings look right. Then raise
   `speculation.max_depth`.

If the full suite is slow, the
[queue-only full CI guide](queue-only-ci.md) shows how to run a light suite on
pull requests and the full suite only on queue branches.

## Move the App credentials to the host

The host needs two files from `~/.config/openmerge/APP-SLUG/` on the partner's
machine: `app-id` and `private-key.pem`.

The private key lets its holder push to the selected repositories. Do not send
the key by email or chat. Use a channel that encrypts to the operator alone.
For example, the operator sends an [age](https://github.com/FiloSottile/age)
public key, and the partner runs:

```sh
tar -C ~/.config/openmerge -cz APP-SLUG | age -r OPERATOR_PUBLIC_KEY > openmerge-app.tar.gz.age
```

After the first landing succeeds, the partner deletes their local copy of
`private-key.pem`.

## Create the database

Give the instance its own role and its own database. Never point two instances
at one database.

```sh
createuser --pwprompt openmerge_PARTNER
createdb --owner openmerge_PARTNER openmerge_PARTNER
```

`serve` applies the migrations when it starts.

## Install the instance

Each instance has its own binary, so that you can upgrade one instance at a
time.

1. Create the directories and install the binary and the key:

   ```sh
   mkdir -p ~/.local/share/openmerge-PARTNER ~/.local/state/openmerge-PARTNER
   mkdir -m 700 -p ~/.config/openmerge-PARTNER
   install -m 755 openmerge_0.1.0_linux_amd64/openmerge ~/.local/share/openmerge-PARTNER/openmerge
   install -m 600 APP-SLUG/private-key.pem ~/.config/openmerge-PARTNER/app.pem
   ```

2. Save this file as `~/.config/openmerge-PARTNER/environment` with mode 600.
   Use absolute paths. Choose an API port that no other instance on the host
   uses:

   ```ini
   OPENMERGE_DATABASE_URL=postgres://openmerge_PARTNER:PASSWORD@127.0.0.1:5432/openmerge_PARTNER?sslmode=disable
   OPENMERGE_GITHUB_APP_ID=APP_ID
   OPENMERGE_GITHUB_PRIVATE_KEY=/home/you/.config/openmerge-PARTNER/app.pem
   OPENMERGE_GIT_CACHE_DIR=/home/you/.local/state/openmerge-PARTNER/git-cache
   OPENMERGE_API_LISTEN=127.0.0.1:8082
   OPENMERGE_POLL_INTERVAL=60s
   OPENMERGE_LOG_LEVEL=info
   ```

3. Save this file as `~/.config/systemd/user/openmerge-PARTNER.service`:

   ```ini
   [Unit]
   Description=OpenMerge merge queue for PARTNER

   [Service]
   Type=simple
   EnvironmentFile=%h/.config/openmerge-PARTNER/environment
   WorkingDirectory=%h/.local/state/openmerge-PARTNER
   ExecStart=%h/.local/share/openmerge-PARTNER/openmerge serve
   Restart=on-failure
   RestartSec=5s
   TimeoutStopSec=180s
   UMask=0077

   [Install]
   WantedBy=default.target
   ```

4. Start the service and read its log:

   ```sh
   systemctl --user daemon-reload
   systemctl --user enable --now openmerge-PARTNER.service
   journalctl --user -u openmerge-PARTNER.service -f
   ```

5. If the service must run after you log out, run
   `loginctl enable-linger "$USER"` once.

[Run as a systemd user service](../operating.md#run-as-a-systemd-user-service)
explains each setting.

## Check the first landing

1. List what the instance discovered:

   ```sh
   ~/.local/share/openmerge-PARTNER/openmerge queue status --discovery --api http://127.0.0.1:8082
   ```

   Each prepared repository shows `served`. A repository that shows
   `not configured` has no `.openmerge.yml` on its default branch. Discovery
   runs every ten minutes, so a new repository can take that long to appear.
2. Tell the partner that a landed pull request shows as **Closed**, not
   **Merged**, when the queue squashes.
   [How landed PRs appear on GitHub](../operating.md#how-landed-prs-appear-on-github)
   explains why.
3. Ask someone with write access to open a small pull request and post a
   comment that contains only `@openmerge queue`.
4. Watch the pull request. The App replies that it accepted the request, CI
   runs on an `openmerge/queue/` branch, and the App then comments
   **Landed in** with the commit and closes the pull request.

If a pull request waits, see
[Why a PR is waiting](../operating.md#why-a-pr-is-waiting).

## Run the instance

- Back up the partner's database on a schedule that you tell the partner.
- After the first weeks, run `openmerge report savings` and read the result
  with the partner. See the [CI savings report](../savings-report.md).
- Write down each thing that the partner asks for or trips over. That list
  decides what gets built next.

### Upgrade the instance

Upgrade your own instances first. ADR 0012 then requires a backup and a passing
replay of the partner's journal, as
[Deploying outside a trial](../operating.md#deploying-outside-a-trial)
describes.

The replay tool cannot do that yet. `scripts/check-live-journal-replay.py`
reads every queue policy from the OpenMerge checkout, so it cannot replay a
journal from a partner's repository
([#236](https://github.com/CanadaApollo6/openmerge/issues/236)). Do not upgrade
a partner instance until that issue is closed.

## Remove a partner

1. Ask the partner to uninstall the App, or to delete it. Either action ends
   the private key's access to the repositories.
2. Stop and disable the service:

   ```sh
   systemctl --user disable --now openmerge-PARTNER.service
   ```

3. Delete `~/.config/openmerge-PARTNER/` and
   `~/.local/state/openmerge-PARTNER/`.
4. Dump the database for the partner if the partner wants the journal. Then
   drop the database and the role.
