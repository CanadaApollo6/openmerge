<img src="assets/icon.svg" width="64" height="64" alt="">

# OpenMerge

**An open-source merge queue and pull-request automation engine for the age of coding agents.**

Agents now open pull requests faster than CI and humans can land them. The merge path — queuing, batching, speculative testing, flaky-test handling, and the rules around them — has become the bottleneck, and the best tools for it are closed, paywalled, or tied to a single forge. OpenMerge brings the ideas that Mergify popularized to everyone, under the Apache 2.0 license.

> **Status: pre-alpha.** Phase 1 is in progress: the planner, configuration, forge contract, and simulator are in place; storage, the GitHub driver, and the server are next. It cannot run against a real repository yet. See the [roadmap](ROADMAP.md).

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
openmerge setup github --user --repo CanadaApollo6/openmerge --name OpenMerge-YourName
# For an organization-owned App, use --owner ORG instead of --user.
```

The command opens a local page that submits the manifest to GitHub. Review the
App name and click **Create GitHub App**; GitHub redirects to the one-use
localhost callback. Finish within ten minutes, on the same machine. If browser
opening fails, open the printed URL yourself. Ctrl-C cancels and closes the
listener.

Credentials go into `$XDG_CONFIG_HOME/openmerge/<app-slug>/` (normally
`~/.config/openmerge/<app-slug>/`): `app-id`, `slug`, `client-id`,
`private-key.pem`, and `webhook-secret`. The directory is private (0700), files
are 0600, and an existing directory is refused. `--config-dir DIR` selects a
new directory. Keep the private key and webhook secret safe; neither is printed.
The manifest generates the key automatically. For an existing App, its settings
page's **Private keys → Generate a private key** downloads a replacement PEM.

Open the printed `https://github.com/apps/<app-slug>/installations/new` URL.
Choose the owning account, **Only select repositories**, select your repository,
and install. The private App is available to its owner. The printed command
shows how to supply the App ID, private key path, and installation ID to
`openmerge serve` once that command is available; also supply a Postgres URL.
The App ID and installation ID are different.

Polling is the default: `hook_attributes.active=false` disables deliveries.
For webhooks, pass `--webhook-url https://YOUR-HOST/YOUR-WEBHOOK-PATH`; App
webhooks deliver JSON (`application/json`). Route that URL to the server's
webhook listener and use the saved `webhook-secret` for signature verification.
To enable delivery later, edit **Webhook → Active / Webhook URL** in App
settings and configure the same secret on the server.

Configure repository rules with a **separate repository-admin token** in
`GITHUB_TOKEN`. A fine-grained PAT needs access to the selected repository and
**Administration: read** to inspect, **Administration: write** to apply; approve
organization access/SSO where required. The runtime App deliberately has no
administration or organization-members permission: its collaborator permission
check uses metadata read. It writes checks and reads statuses, so the manifest
uses checks write and statuses read, plus contents/pull requests/issues write.

```bash
openmerge setup rulesets --repo CanadaApollo6/openmerge --config-dir ~/.config/openmerge/APP-SLUG
openmerge setup rulesets --repo CanadaApollo6/openmerge --config-dir ~/.config/openmerge/APP-SLUG --apply
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

Review classic branch protection separately in **Settings → Branches**, including
wildcard rules for queue/fence refs: this command validates rulesets only.
Startup enforcement through `serve` is a follow-up after the server PR merges.

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

## Contributing

The project is just starting, and design feedback is the most useful contribution right now. Read [CONTRIBUTING.md](CONTRIBUTING.md) for setup and the DCO sign-off, and [GOVERNANCE.md](GOVERNANCE.md) for how decisions are made.

## License

[Apache License 2.0](LICENSE). OpenMerge is an independent project and is not affiliated with or endorsed by Mergify SAS. See [NOTICE](NOTICE).
