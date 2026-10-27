<img src="assets/icon.svg" width="64" height="64" alt="">

# OpenMerge

**An open-source merge queue and pull-request automation engine for the age of coding agents.**

Agents now open pull requests faster than CI and humans can land them. The merge path — queuing, batching, speculative testing, flaky-test handling, and the rules around them — has become the bottleneck, and the best tools for it are closed, paywalled, or tied to a single forge. OpenMerge brings the ideas that Mergify popularized to everyone, under the Apache 2.0 license.

> **Status: pre-alpha.** Phase 1 is complete, and OpenMerge lands its own PRs: the planner, Postgres store, GitHub App driver, polling server, read-only API/CLI, and Compose quickstart are available. All core conditions have runtime observations, with classic protection and installed App grant checks alongside ruleset validation. The version-1 JSON Schema and fast-forward mode are available. Phase 2.1 adds multi-base workers, App-wide installation discovery, and webhook health with backstop polling; throughput and post-merge verification follow. See the [roadmap](ROADMAP.md).

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
`./bin/openmerge serve`, with the App ID, private key
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
Check run, Check suite, Status, Push, Create, Delete and Issue comment events,
and give the same secret to serve as `OPENMERGE_WEBHOOK_SECRET`.

Configure repository rules with a **separate repository-admin token** in
`GITHUB_TOKEN`. A fine-grained PAT needs access to the selected repository and
**Administration: read** to inspect, **Administration: write** to apply. Create it
under **Settings → Developer settings → Personal access tokens → Fine-grained
tokens**: choose the repository's **Resource owner**, **Only select repositories**
with just the target repo, and **Repository permissions → Administration: Read
and write**. Use a fine-grained token; avoid classic tokens. Approve organization
access/SSO where required, then export it as `GITHUB_TOKEN` for the commands below.
The runtime App deliberately has no administration permission: its collaborator
permission check uses metadata read. The manifest uses contents, workflows, pull
requests, checks and Actions write, plus statuses, issues, metadata and organization
members read. Members read is a dormant Stage A prerequisite for team resolution;
see [permission adoption and the trial freeze](docs/operating.md#organization-membership-permission-stage-a-prerequisite)
before changing an existing App or installation. Actions write
permits same-commit retries of GitHub Actions checks only; foreign-App check
runs and commit statuses are unsupported. Updating the manifest does not change
an existing App. The App owner must add **Actions: Read and write** in the App's
permission settings, prompting each installation to approve. Until approved, a
retry-enabled queue behaves exactly like retry-off and logs a WARN.

`actions: write` also grants cancellation, artifact/cache management and deployment
approvals. OpenMerge calls only the rerun-failed-jobs endpoint for retries (and,
after ADR 0009 PR 4, cancel). Enable the bounded single retry per queue:

```yaml
version: 1
queues:
  - name: default
    merge_when: [check-success = ci]
    checks:
      retry_failed: 1 # default: 0
```

Reruns get one `check_timeout` from the recorded request. If `check_timeout: 0`
disables the original check timer, requested reruns still have the default
one-hour deadline. A dispatch that never receives acceptance times out with
“rerun dispatch never confirmed”; it cannot wait indefinitely. The permanent
record of flaky-run identities, workflow/job links and retry outcomes is `why`,
including after the PR lands; the landed comment does not repeat that evidence.

Drain older workers before upgrading. After the first retry event is written,
rolling back to an older binary requires a database restore, per
[ADR 0002](docs/adr/0002-storage-and-state-model.md) and
[ADR 0009](docs/adr/0009-speculative-batching.md).

Workflows write lets
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
`serve` validates visible rulesets and reads classic protection at startup and periodically; run
`setup rulesets` first to audit the full bypass lists with an admin token.
Classic protection reads need installation Administration read; unreadable
protection produces WARN and retains only its own previous block state. The default App
manifest does not request Administration. Audit it manually when unreadable.

For subsequent repositories, install the App, run the ruleset setup with the
separate repo-admin token, enable CI on `openmerge/queue/**`, and commit
`.openmerge.yml` to the default branch. No server edit is needed. A private App
belongs to its owning account; organizations need their own App
(`openmerge setup github --owner ORG`) and `serve` process. Processes can share
Postgres but need separate git caches and listen addresses. See
[onboarding](docs/operating.md#installation-discovery-and-onboarding) and
[Phase 1 migration](docs/operating.md#migrating-a-phase-1-deployment) for the
allowlist removal, non-default `--base` caveat, and rollback constraints.

## GitHub integration details

The [GitHub driver setup guide](internal/forge/github/README.md#app-setup-and-troubleshooting)
lists permissions and setup details. Subscribe to `pull_request`,
`pull_request_review`, `check_run`, `check_suite`, `status`, `push`, `create`, `delete`, and
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
Commit a valid `.openmerge.yml` on the repository's default branch and configure
CI to run on pushes to `openmerge/queue/**`. This repository includes both; its required
batch check is `check`, with PR DCO sign-off required at admission.

```bash
export OPENMERGE_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
export OPENMERGE_GITHUB_APP_ID=123
export OPENMERGE_GITHUB_PRIVATE_KEY=/absolute/path/openmerge.pem
bin/openmerge serve \
  --git-cache-dir /absolute/path/openmerge-cache --poll-interval 60s
```

`--database-url`, `--github-app-id`, `--github-private-key`,
`--github-installation-id`, `--repos`, `--base`, `--git-cache-dir`,
`--poll-interval`, `--backstop-interval`, `--webhook-health-window`, `--concurrency`, `--webhook-listen`, `--webhook-secret`,
`--api-listen`, `--api-token-file`, `--rules-interval`, `--discovery-interval`, `--log-level`,
`--git-committer-name`, `--git-committer-email`, and `--delete-head-branch`
all have matching `OPENMERGE_` environment variables: uppercase the flag name
and replace hyphens with underscores. Flags override environment variables.
`--github-installation-id`, `--repos`, and `--base` are optional deprecated
allowlists; remove their `OPENMERGE_*` environment values too when migrating.
`--repos` accepts comma-separated case-insensitive owner/name globs. With no
allowlists, one process serves every configured repository visible to its App.
The default branch's policy expands into independent base workers. Polling
defaults to `60s` for active queues or unhealthy/missing webhooks, and `10m` for
idle queues with healthy webhooks. The health window defaults to `30m`,
installation/repository discovery to `10m`, concurrency to four
queues, and the git cache to `.openmerge-cache`. Each process needs its own cache
and listen addresses. Logs are structured JSON on stderr. Startup applies
migrations and recovers stored landings before discovery; SIGINT/SIGTERM cancels
bounded I/O and releases claims before closing Postgres. See the
[operating guide](docs/operating.md) for a systemd user unit, environment file,
committer attribution, logs, upgrades, and GitHub's Closed badge after squash/rebase.

See [landed head cleanup](docs/operating.md#landed-head-cleanup) for the
default behavior, safety checks, configuration, and upgrade semantics.

Enable read-only queue inspection with `--api-listen :8081` (binds localhost;
default off), then run `bin/openmerge queue status --json` or
`bin/openmerge queue status --repo CanadaApollo6/openmerge --pr 42`.
Bearer authentication uses `OPENMERGE_API_TOKEN` or
`OPENMERGE_API_TOKEN_FILE` and is required for any non-loopback bind. See
[API v1 and CLI](docs/api.md) for the JSON contract, PR event explanations, token handling and stable exit codes.

Read a recorded change brief with
`bin/openmerge change brief <change-id> --repo owner/repo --base main --audience reviewer`.
Use `--audience operator` for blockers and recovery, `--audience agent` for
structured next steps, or `--json` for the public DTO. Historical missing
evidence says "not recorded". Policy reads use only pinned local objects and
verify the effective config digest for legacy queues. A matching legacy pin
shows empty obligations, a brief digest, and “approvals not required by the
recorded policy”. Reconstructed disagreement (including older planner JSON)
yields `policy_digest_mismatch`; missing pinned objects yield `policy_unavailable`.
Candidate approval policies remain unavailable: **11b must define and verify the
full recorded-policy digest composition**, including effective queue settings and
`WithPolicyDigest`, before displaying their requirements or digest. Busy local
locks yield transient `policy_busy` with retry advice. A later config never
substitutes for the recorded pin. JSON uses `claimed_intent` and string Truth
values. Structured recovery includes `wait_for_approval`, `fix_check`, `refresh`,
and `wait_for_recovery`. Complete display inputs are redacted before bounding;
`display_redacted` signals credential redaction. Hunks preserve line breaks,
tabs and indentation. Machine digests, IDs, SHAs and enums remain verbatim.
Recorded facts and their digest remain untouched. Separate display budgets protect requirements,
provenance and tests from large hunks. See [brief API and CLI](docs/api.md#recorded-change-briefs-stage-a-7b).

Startup and every ten minutes (`--rules-interval`) validate repository/inherited
rulesets, classic protection on the base and its fence, and the installation
token's actual permission grants. Missing `contents: write` or `workflows: write`
logs ERROR and blocks new work as `repository-rules`, before initial fence
creation. Other missing required grants log WARN with the exact permission.
Definite policy problems also block. Rulesets, classic protection, and permissions
track independent blocks and revisions; each passing source clears only its own block.
Read failures log WARN, retain that source’s previous state, and retry after one minute
with exponential backoff capped at five minutes (or the configured rules interval
if shorter). Classic protection the installation is not permitted to read (HTTP 403 or a
generic 404) is unavailable rather than failed: it logs WARN, retains its previous state,
does not delay activation, and is read again after the normal rules interval.
Other pauses and action poison remain independent.
Hidden bypass lists produce WARN logs and require the separate admin-token setup
audit. See [runtime rules validation](docs/operating.md#runtime-repository-rules).

Polling requires no public URL. Optional webhooks use
`--webhook-listen 127.0.0.1:8080 --webhook-secret "$OPENMERGE_WEBHOOK_SECRET"`
behind your HTTPS ingress. Verified deliveries record process-local health and
wake affected queues for fresh reads; delivery IDs deduplicate refresh work.
Testing, landing and unsettled attempts keep the fast polling interval. Idle
queues use backstop polling only while both their installation and their own
repository have received a verified delivery within the health window; restart
or expiry returns to fast polling.
Invalid signatures change neither health nor scheduling. Status shows installation
health and each repository's delivery age. See [webhook setup and health](docs/operating.md#webhook-setup-and-health)
for events, secrets and fallback behavior. Unchanged PR summaries skip detail and
comment reads; active test SHAs still refresh CI. Each installation reserves the
last 10% of its primary budget for settlement and honors primary/secondary-limit
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
`.openmerge.yml` on the default branch, and enable CI on `openmerge/queue/**` first.
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
or invalid default-branch configuration pauses work, and a default-branch move
reloads it. CI producer ambiguity stays blocked until an unambiguous refresh succeeds.
`landing-not-on-base` pauses new work while fence resolution continues; restore
the tested commit on the base before resuming after an external rollback.
Pending closure work is retained while paused and runs after refresh resumes it.
Every queue's `merge_when` must require a positive `check-success = <name>`
term in its conjunction (nested `and` groups are flattened); `or`, `not`, and
counts cannot supply that requirement. Extra terms may still use `or`/`not`.
Loading also rejects conditions that pass with missing or failed checks.
Pending or missing check alternatives keep an OR condition pending until the
whole condition passes or is definitely false.

The driver supplies all core conditions: `base`, `label`, `author`,
`#approved-reviews-by`, `check-success/failure/pending`, `files`, `draft`, and
`conflict`, plus `head` and `title`. Missing observations remain unknown even
under negation. File lists are fetched only when a condition uses `files`. Renames include both
old and new paths; lists are paginated and capped at 1,000 distinct paths. Larger PRs
leave every `files` predicate unknown, including counts and exclusions.
Uncomputed mergeability is unknown and retried on polls. See the
[condition reference](internal/config/README.md#conditions).

Phase 1 is complete, including the [version-1 JSON Schema](schema/openmerge.v1.json)
and fast-forward mode. The Mergify importer is
planned for Phase 5; the native grammar already accepts the core attribute names.

Phase 2.1 adds multiple named queues with branch/glob `base` selectors,
repository-level `delete_head_branch`, and independent workers for every concrete
base, under the default branch's policy. Branch deletion/recreation and queue
retirement preserve logs and fences; settlement runs before config and admission,
including recovery outside current allowlists. Status lists every queue and its
lifecycle state. Installation discovery now serves configured repositories across
the App, preserving canonical keys, settlement on removal/suspension, and
per-installation budgets with discovery status. Verified webhook health reduces
idle polling while active queues retain their fast safety net. See the [configuration reference](internal/config/README.md).

Operators must grant the App protected-base bypass and exclusive, nondeletable `openmerge/fence/*`
ownership as described in the [forge contract](docs/forge-driver-contract.md#the-fence).

## Repository schema and fast-forward landing

The [configuration reference](internal/config/README.md) documents every key and
condition. `openmerge config schema` prints the embedded
[version-1 JSON Schema](schema/openmerge.v1.json); `openmerge config validate`
checks YAML and loader semantics as well. For editor completion, use the YAML
language-server comment (a literal `$schema` YAML key is not accepted):

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/CanadaApollo6/openmerge/main/schema/openmerge.v1.json
version: 1
queues:
  - name: default
    merge_when: [check-success = ci]
    merge_method: fast-forward
    batch: {max_size: 8, max_wait: 30s}
```

Fast-forward tests the exact head tree and history via a unique marker commit;
it lands the head itself. The marker is one empty commit with the head as its
only parent, the same tree, the configured App committer, and an authoritative
`OpenMerge-Batch` trailer. A random nonce makes each build unique, so existing
checks on the head cannot authorize landing. For a batch, each head must strictly
descend from the previous head, starting at the base; the marker tests the last
head, which is what lands. The executor selects the longest consecutive linear
prefix in queue order; unrelated heads fall back to a singleton. A head equal to
or diverged from the current base is dequeued as `fast-forward-required`: the
author must update the branch and queue it again. OpenMerge never rebases it.
Base movement invalidates the test batch, and a rebuild checks ancestry again.
Landing still uses one atomic push leased on both base and fence. Original
commits, authors, signatures and trailers are preserved; head cleanup follows
the same safety checks as other methods.

## Contributing

The project is just starting, and design feedback is the most useful contribution right now. Read [CONTRIBUTING.md](CONTRIBUTING.md) for setup and the DCO sign-off, and [GOVERNANCE.md](GOVERNANCE.md) for how decisions are made.

## License

[Apache License 2.0](LICENSE). OpenMerge is an independent project and is not affiliated with or endorsed by Mergify SAS. See [NOTICE](NOTICE).

### Stage A 10 activation obligations (PRs 11b and 9)

PR 11b must fail approvals queues closed when the build metadata loader is nil.
`ChangeMetadata` carries no classification Generation: 11b must invalidate every
not-yet-landing batch on a generation advance, or PR 9 must compare its pinned
labels against the current classification before allowing landing. PR 9 should
reuse `changepolicy.ClassificationLabels` for the canonical sorted true labels.

Legacy nil-metadata builds preserve forged `OpenMerge-Change` and
`OpenMerge-Classification` text in contributor bodies for byte identity. Future
trailer consumers must trust only commits reached via a recorded tested/landed
SHA of an activated build, never arbitrary history scans.

The audit export format is defined by the normative
[v1 frozen specification](docs/research/adr-0008-10a-design.md#v1-frozen-specification).

ADR 0010/0011 writer foundations remain **Hold**, with no production writers.
The derived provenance projection retires abandoned and dropped work using
existing queue journal outcomes, while retaining original audit evidence and
rejection fences. Lifetime fences still consume finite checkpoint capacity;
unlimited-history pruning remains unresolved. See the
[retention and recovery contract](docs/operating.md#shared-reader-retention-recovery).

Writer activation has **BLOCKING** prerequisites for ADR 0010 4b and ADR 0011
4a/4b: address lifetime fence growth with compaction or at least a metric and
alert, factor append validation/insertion into the landing transaction, and
recover missing landed companions idempotently. See the
[writer prerequisites](docs/operating.md#shared-reader-retention-recovery).

Provenance audit exports use bounded scratch storage on the output filesystem.
The defaults are 256 MiB of accounted index/fold bytes and 65,536 files/directories;
`--scratch-max-bytes` and `--scratch-max-files` override them. Exhaustion fails the
export without publishing a bundle. Provision additional space for filesystem
metadata, selection/merge spools and the final output. See the
[v2 scratch requirements](docs/research/adr-0010-slice3b-audit-v2.md#provenance-closure).
