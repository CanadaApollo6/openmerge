<img src="assets/icon.svg" width="64" height="64" alt="">

# OpenMerge

**An open-source merge queue and pull-request automation engine for the age of coding agents.**

Agents now open pull requests faster than CI and humans can land them. The merge path — queuing, batching, speculative testing, flaky-test handling, and the rules around them — has become the bottleneck, and the best tools for it are closed, paywalled, or tied to a single forge. OpenMerge brings the ideas that Mergify popularized to everyone, under the Apache 2.0 license.

> **Status: pre-alpha.** Phase 1 is in progress: the planner, Postgres store, GitHub App driver, and polling server are in place. Operator-managed App permissions and branch protection are still required. See the [roadmap](ROADMAP.md).

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

## GitHub App integration

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
`--poll-interval`, `--concurrency`, `--webhook-listen`, and `--webhook-secret`
all have matching `OPENMERGE_` environment variables: uppercase the flag name
and replace hyphens with underscores. Flags override environment variables.
`--repos` is a comma-separated list owned by one installation; the base defaults
to `main`, polling to `60s`, concurrency to four queues, and the git cache to
`.openmerge-cache`. Use a separate cache for each process. Logs are structured
JSON on stderr. Startup applies migrations and ensures each base's fence;
SIGINT/SIGTERM cancels bounded I/O and releases claims before closing Postgres.
Run one server per installation during this Phase 1 rollout.

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
metadata reads are implemented. Ruleset validation/injection, App setup,
REST/CLI status, and Docker Compose remain follow-ups. Operators must grant the
App protected-base bypass and exclusive, nondeletable `openmerge/fence/*`
ownership as described in the [forge contract](docs/forge-driver-contract.md#the-fence).

## Contributing

The project is just starting, and design feedback is the most useful contribution right now. Read [CONTRIBUTING.md](CONTRIBUTING.md) for setup and the DCO sign-off, and [GOVERNANCE.md](GOVERNANCE.md) for how decisions are made.

## License

[Apache License 2.0](LICENSE). OpenMerge is an independent project and is not affiliated with or endorsed by Mergify SAS. See [NOTICE](NOTICE).
