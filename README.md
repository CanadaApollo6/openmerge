<img src="assets/icon.svg" width="64" height="64" alt="">

# OpenMerge

**An open-source merge queue and pull-request automation engine for the age of coding agents.**

Agents now open pull requests faster than CI and humans can land them. The merge path — queuing, batching, speculative testing, flaky-test handling, and the rules around them — has become the bottleneck, and the best tools for it are closed, paywalled, or tied to a single forge. OpenMerge brings the ideas that Mergify popularized to everyone, under the Apache 2.0 license.

> **Status: pre-alpha.** Phase 1 is in progress: the planner, Postgres store, GitHub App driver, and polling server are in place. Read-only API/CLI, runtime ruleset validation and a Compose quickstart are available. Classic branch protection checks, installed App permission validation, and label/file conditions remain Phase 1 follow-ups. See the [roadmap](ROADMAP.md).

## What it will do

- **Merge queue that never lies:** what was tested is exactly what lands.
- **Throughput:** batching with bisection, speculative chains with an adaptive window, priorities, multiple queues, two-step CI.
- **Monorepo lanes:** independent scopes merge in parallel (path globs, Bazel, Nx, Turborepo, Pants).
- **Flaky-test handling:** ingest JUnit/CTRF, score flakiness, quarantine, and never evict a PR for a known flake.
- **Agent-native:** JSON API, CLI with `--json`, an MCP server, change identity across re-pushes, per-agent budgets, and machine-readable failure reasons.
- **PR automation:** labels, reviews, backports, merge protections, stacks.
- **Runs anywhere:** one binary plus Postgres. GitHub first, then GitLab and Forgejo/Gitea.

## Docs

- [Roadmap](ROADMAP.md): principles, architecture, phases, and decisions
- [Architecture decisions](docs/adr/README.md): the ADRs behind the design
- [Research](docs/research/README.md): how Mergify works, and the wider merge-queue landscape

## Try the simulator

The simulator runs the real planner and executor against an in-memory forge on a virtual clock, and checks after every step that main only ever contains tested, passing commits.

```bash
go run ./cmd/openmerge sim --seed 42            # a 500-PR day: 5% broken changes, 2-minute CI
go run ./cmd/openmerge sim --faults --json      # add per-method errors, lost acknowledgements, late/duplicate pushes
```

Flags: `--prs`, `--broken-rate`, `--flake-rate`, `--ci`, `--ci-max`, `--seed`, `--faults`, `--drop-webhooks`, `--merge-method`, `--window`, `--repush-rate`, `--conflict-rate`, `--json`. `--ci-max` uses `--ci` as the minimum of a duration range. JSON reports the full effective configuration and mean, median, and p95 merge times in seconds. See the [`internal/sim` package docs](internal/sim) for the full model.

## Setting up the GitHub App

Create a private App from a [GitHub App manifest](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest):

```bash
make build
./bin/openmerge setup github --user --repo CanadaApollo6/openmerge --name OpenMerge-YourName
# For an organization-owned App, use --owner ORG instead of --user.
```

The command opens a local page that submits the manifest to GitHub. Review the
App name and click **Create GitHub App**; GitHub redirects to the one-use
localhost callback. Finish within ten minutes, on the same machine. If browser
opening fails, open the printed URL yourself. Ctrl-C cancels and closes the
listener.

Credentials go into `$XDG_CONFIG_HOME/openmerge/<app-slug>/` (normally
`~/.config/openmerge/<app-slug>/`): `app-id`, `slug`, `client-id`,
`private-key.pem`, and, in webhook mode only, `webhook-secret`. The directory is private (0700), files
are 0600, and an existing directory is refused. `--config-dir DIR` selects a
new directory. The manifest generates and auto-saves the private key at
`<config-dir>/private-key.pem`; you do not need to download one. Keep the key and
webhook secret safe; neither is printed.

Open the printed `https://github.com/apps/<app-slug>/installations/new` URL.
Choose the owning account, **Only select repositories**, select your repository,
and install. The private App is available to its owner. `--repo owner/name`
targets the printed repository selection, ruleset commands and serve command.
The printed next steps show installation, ruleset preview, ruleset apply, then
`./bin/openmerge serve` once that command is available, with the App ID, private key
path and installation ID; also supply a Postgres URL.
Find the **installation ID** under **Settings → Applications → Installed GitHub
Apps → Configure**: it is the number at the end of the URL, not the App ID.

Polling is the default: the manifest then has no webhook and no event
subscriptions, because GitHub rejects loopback hook URLs even when inactive.
For webhooks, pass `--webhook-url https://YOUR-HOST/YOUR-WEBHOOK-PATH`; App
webhooks deliver JSON (`application/json`). Route that URL to the server's
webhook listener and use the saved `webhook-secret` for signature verification.
When webhooks are enabled, setup prints this environment assignment for serve
(with your actual, shell-quoted path); the secret value itself is never printed:

```bash
export OPENMERGE_WEBHOOK_SECRET="$(cat ~/.config/openmerge/APP-SLUG/webhook-secret)"
```

A polling-mode App has no webhook or secret. To enable delivery later, open the
App settings, set **Webhook → Active** and the **Webhook URL**, generate a random
**Webhook secret** (for example `openssl rand -hex 32`), subscribe to Pull request, Pull request review,
Check run, Check suite, Status, Push and Issue comment events,
and give the same secret to serve as `OPENMERGE_WEBHOOK_SECRET`.

Configure repository rules with a **separate repository-admin token** in
`GITHUB_TOKEN`. A fine-grained PAT needs access to the selected repository and
**Administration: read** to inspect, **Administration: write** to apply. Create it
under **Settings → Developer settings → Personal access tokens → Fine-grained
tokens**: choose the repository's **Resource owner**, **Only select repositories**
with just the target repo, and **Repository permissions → Administration: Read
and write**. Use a fine-grained token; avoid classic tokens. Approve organization
access/SSO where required, then export it as `GITHUB_TOKEN` for the commands below.
The runtime App deliberately has no
administration or organization-members permission: its collaborator permission
check uses metadata read. The manifest uses contents, workflows, pull requests
and checks write, plus statuses, issues and metadata read. Workflows write lets
test-ref builds and landings include changes to `.github/workflows/`; PR-thread
comments use pull requests write. Issues read permits command reads and the
`issue_comment` subscription. See the [permission table](internal/forge/github/README.md#setup-and-ruleset-validation).

```bash
./bin/openmerge setup rulesets --repo CanadaApollo6/openmerge --config-dir ~/.config/openmerge/APP-SLUG
./bin/openmerge setup rulesets --repo CanadaApollo6/openmerge --config-dir ~/.config/openmerge/APP-SLUG --apply
# Alternatively supply --app-id NUMERIC_APP_ID.
```

Both commands print problems and a change preview; unresolved problems exit
nonzero. `--apply` adds an **always** Integration bypass for the App to active
repository rulesets targeting the default branch, preserving other actors and
rules. It creates/repairs only `openmerge-fence`: creation, update, deletion and
force-push restrictions, with only this App allowed to bypass. Both direct and
nested `openmerge/fence/*` names are covered. It never edits inherited rulesets;
organization/enterprise owners must fix reported blockers. Rules that may block
creation or force-push in `openmerge/queue/*` are reported for manual correction.
An unprotected base needs no bypass. Apply is idempotent; serialize policy edits
while it runs (GitHub ruleset updates have no compare-and-swap).

Install the App on the selected repository before applying rulesets. If GitHub
rejects an `Integration` bypass actor, setup prints sanitized API diagnostics and
installation guidance. Personal-account repositories may still reject that actor
after installation. If so, keep protection enabled: use an organization-owned
repository that accepts the App bypass, or defer OpenMerge until GitHub supports
it. Do not substitute a broader bypass that breaks App-only fence ownership.

Review classic branch protection separately in **Settings → Branches**, including
wildcard rules for queue/fence refs: this command validates rulesets only.
`serve` validates visible rulesets at startup and periodically; run
`setup rulesets` first to audit the full bypass lists with an admin token.

## GitHub integration details

The [GitHub driver setup guide](internal/forge/github/README.md#app-setup-and-troubleshooting)
lists permissions and setup details. Subscribe to `pull_request`,
`pull_request_review`, `check_run`, `check_suite`, `status`, `push`, and
`issue_comment`; `installation` and `installation_repositories` arrive
automatically. Rerun notifications trigger a fresh check/suite observation.

Find the numeric installation ID at the end of the installed App's **Configure**
URL under your account/organization settings, or in signed webhook
`installation.id`. For 401s, verify the App ID/key pair and installation access,
and synchronize the host clock: JWTs tolerate only the configured 60-second
backdating margin. See the [troubleshooting guide](internal/forge/github/README.md#app-setup-and-troubleshooting).

The bounded ETag cache revalidates every GET and replays validated 304 bodies
inside the transport. A full open fork PR refresh with one approver and one
active CI SHA costs about **6 REST requests per poll**, or **7** with command
comment discovery (one page per endpoint). Unchanged responses usually return
304 and consume no primary rate-limit quota; changed 200 responses do. Webhooks
plus periodic reconciliation reduce traffic. See [polling costs and cache limits](internal/forge/github/README.md#polling-cost-and-conditional-requests).

## Running `openmerge serve`

Build with `make build`, start local Postgres with `make db`, and install a
GitHub App using the [driver setup guide](internal/forge/github/README.md#app-setup-and-troubleshooting).
Commit a valid `.openmerge.yml` on the base branch and configure CI to run on
pushes to `openmerge/queue/**`. This repository includes both; its required
batch check is `check`, with PR DCO sign-off required at admission.

```bash
export OPENMERGE_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
export OPENMERGE_GITHUB_APP_ID=123
export OPENMERGE_GITHUB_PRIVATE_KEY=/absolute/path/openmerge.pem
export OPENMERGE_GITHUB_INSTALLATION_ID=456
bin/openmerge serve --repos CanadaApollo6/openmerge --base main \
  --git-cache-dir /absolute/path/openmerge-cache --poll-interval 60s
```

`--database-url`, `--github-app-id`, `--github-private-key`,
`--github-installation-id`, `--repos`, `--base`, `--git-cache-dir`,
`--poll-interval`, `--concurrency`, `--webhook-listen`, `--webhook-secret`,
`--api-listen`, `--api-token-file`, `--rules-interval`, `--log-level`,
`--git-committer-name`, `--git-committer-email`, and `--delete-head-branch`
all have matching `OPENMERGE_` environment variables: uppercase the flag name
and replace hyphens with underscores. Flags override environment variables.
`--repos` is a comma-separated list owned by one installation; the base defaults
to `main`, polling to `60s`, concurrency to four queues, and the git cache to
`.openmerge-cache`. Use a separate cache for each process. Logs are structured
JSON on stderr. Startup applies migrations and ensures each base's fence;
SIGINT/SIGTERM cancels bounded I/O and releases claims before closing Postgres.
Run one server per installation during this Phase 1 rollout. See the
[operating guide](docs/operating.md) for a systemd user unit, environment file,
committer attribution, logs, upgrades, and GitHub's Closed badge after squash/rebase.

After a confirmed landing, head branches in the base repository are deleted by
default after PR closure, provided they are not the base/default branch,
protected, or targeted by another open PR. Cleanup leases the original head SHA
so newer work is preserved. Set `--delete-head-branch=false` or
`OPENMERGE_DELETE_HEAD_BRANCH=false` to retain heads. This is a serve-wide option;
per-repository `delete_head_branch` configuration is a follow-up.

Enable read-only queue inspection with `--api-listen :8081` (binds localhost;
default off), then run `bin/openmerge queue status --json` or
`bin/openmerge queue status --repo CanadaApollo6/openmerge --pr 42`.
Bearer authentication uses `OPENMERGE_API_TOKEN` or
`OPENMERGE_API_TOKEN_FILE` and is required for any non-loopback bind. See
[API v1 and CLI](docs/api.md) for the JSON contract, PR event explanations, token handling and stable exit codes.

Startup and every ten minutes (`--rules-interval`) validate repository and
inherited rulesets with the App installation. Definite problems from a
successful read block new work as `repository-rules`; a later passing check clears only that rules block.
Read failures log WARN, retain the previous state, and retry after one minute
with exponential backoff capped at five minutes (or the configured rules interval
if shorter). Other pauses and action poison remain independent.
Hidden bypass lists produce WARN logs and require the separate admin-token setup
audit. See [runtime rules validation](docs/operating.md#runtime-repository-rules).

Polling requires no public URL. Optional webhooks use
`--webhook-listen 127.0.0.1:8080 --webhook-secret "$OPENMERGE_WEBHOOK_SECRET"`
behind your HTTPS ingress. Signed delivery IDs are persisted and deduplicated;
a webhook only schedules a fresh read, including early CI refreshes. Polling
continues to recover missed deliveries. Unchanged PR summaries skip detail and
comment reads; active test SHAs still refresh CI. The shared driver reserves the
last 100 primary API requests until reset and honors primary/secondary-limit
cooldowns. Each extra PR, CI SHA, page, and permission check increases the
[request budget](internal/forge/github/README.md#polling-cost-and-conditional-requests);
use a longer polling interval for larger repositories.

Writers can post `@openmerge queue [sha]`, `@openmerge dequeue`, or
`@openmerge refresh`. The comment must contain only the command: after trimming
surrounding ASCII whitespace and normalizing CRLF, it must be one ASCII line, with
the exact lowercase mention and command name and a single space or tab between
tokens. Other content,
including prose, multiple lines, code fences, HTML, and quotes, is silently ignored.
An optional queue SHA must be the full commit SHA of the current head
(abbreviations are refused because they can be ambiguous); a mismatch gets a
refusal reply. Without a SHA, the command queues the head current when the
server processes it. Acceptance replies name that exact SHA; a later push
dequeues it. Entry conditions and fresh trust checks still apply.
An unpinned command grants nothing beyond the head's own trust: same-repository
heads were pushed by someone with write access, and fork heads still require a
writer's exact-head approval at the trust gate. Queueing whatever is current is
therefore equivalent; use the SHA form when intent must bind to a specific head.
Refresh rechecks eligibility, requests fresh CI, and
resumes an operator pause after its cause is fixed; it does not enqueue a new
entry. Fork heads need an approval of that exact SHA from a current writer.

## Docker compose quickstart

Complete the [App and ruleset setup](#setting-up-the-github-app), put
`.openmerge.yml` on the base branch, and enable CI on `openmerge/queue/**` first.
Then configure and start the single binary plus its own Postgres:

```sh
cp deploy/.env.example deploy/.env
chmod 600 deploy/.env
# Edit deploy/.env: App/installation IDs, repo, absolute key path.
# Generate separate DB password and API token: openssl rand -hex 32 (twice).
docker compose --env-file deploy/.env -f deploy/compose.yaml config --quiet
docker compose --env-file deploy/.env -f deploy/compose.yaml up --build -d
docker compose --env-file deploy/.env -f deploy/compose.yaml logs -f openmerge
```

The image runs as UID/GID 1000 and includes git and CA certificates. The existing
private key is mounted read-only; ensure it is readable by UID 1000 (a 0600 file
owned by that UID works). Keep the key outside the checkout. The build context
includes only Go sources/module files. Postgres has no published host port;
the status API is published on **127.0.0.1:8081**. Polling needs no webhook URL.
Compose requires an API token because the listener also binds the container
network. Set a random token in the env file and use the same token in your CLI:

```sh
OPENMERGE_API_TOKEN=YOUR_API_TOKEN bin/openmerge queue status --json
# Or use the container's binary and configured environment:
docker compose --env-file deploy/.env -f deploy/compose.yaml exec openmerge openmerge queue status --json
```

Named volumes retain Postgres data and the process-exclusive git cache across
container replacements. Back up the database before upgrades. `docker compose
--env-file deploy/.env -f deploy/compose.yaml down` stops this deployment while
retaining volumes; adding `--volumes` deletes them. Do not run this alongside an
existing server for the same installation. See the [operating guide](docs/operating.md).

CI validates the Compose configuration, builds the image, and checks its
non-root user, git and binary. It runs the Go suite with Postgres and fake/local
forge fixtures; credentialed GitHub deployment is outside that CI coverage.
The App ignores its own commands and replies once to unauthorized commands.
The existing queue check shows blocked and paused reasons. Missing, ambiguous,
or invalid base configuration pauses work, and a base move reloads it. CI
producer ambiguity stays blocked until an unambiguous refresh succeeds.
`landing-not-on-base` pauses new work while fence resolution continues; restore
the tested commit on the base before resuming after an external rollback.
Pending closure work is retained while paused and runs after refresh resumes it.
Every queue's `merge_when` must require a positive `check-success = <name>`
term in its conjunction (nested `and` groups are flattened); `or`, `not`, and
counts cannot supply that requirement. Extra terms may still use `or`/`not`.
Loading also rejects conditions that pass with missing or failed checks.
Pending or missing check alternatives keep an OR condition pending until the
whole condition passes or is definitely false.

The current driver supplies conditions on `base`, `draft`, `author`,
`approved-reviews-by`, and checks. Configurations referencing `head`, `title`,
`label`, `files`, or `conflict` pause with an explicit diagnostic until those
metadata reads are implemented. Phase 1 remains in progress. Classic branch
protection checks, installed App permission validation, and the remaining metadata conditions (including labels
and files) are follow-ups. Operators must grant the App protected-base bypass and exclusive, nondeletable `openmerge/fence/*`
ownership as described in the [forge contract](docs/forge-driver-contract.md#the-fence).

## Contributing

The project is just starting, and design feedback is the most useful contribution right now. Read [CONTRIBUTING.md](CONTRIBUTING.md) for setup and the DCO sign-off, and [GOVERNANCE.md](GOVERNANCE.md) for how decisions are made.

## License

[Apache License 2.0](LICENSE). OpenMerge is an independent project and is not affiliated with or endorsed by Mergify SAS. See [NOTICE](NOTICE).
