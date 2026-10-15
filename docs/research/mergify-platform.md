# Mergify beyond the merge queue: research report for OpenMerge (as of 2026-10-15)

Most of this comes from Mergify's own docs. They publish Markdown versions of every page (append `.md`), an `llms.txt` index, an OpenAPI spec and a JSON Schema, so most claims below are checked against primary sources. Anything I could not confirm is marked **unverified**.

---

## 0. Key findings for OpenMerge

1. **Mergify relicensed its client tooling to proprietary a week ago.**
   - On 2026-10-06 the `mergify-cli` repo was cut down to an install script, agent skills and plugin manifests. The commit says the Rust CLI is now "developed, built and released from Mergify's private monorepo".
   - On 2026-10-08 the LICENSE went from Apache-2.0 to a proprietary licence. It forbids modification and reverse engineering, and forbids using the software "to build or operate a product or service that competes with Mergify". It is governed by French law.
   - PyPI shows the same switch: `mergify-cli` and `pytest-mergify` 2026.10.3 are Apache-2.0, and 2026.10.4 onward are `LicenseRef-Mergify-Proprietary`.
2. **Apache-2.0 snapshots still exist and can legally be forked.**
   - **Engine:** the `Mergifyio/mergify-engine` repo (it now redirects to `Mergifyio/mergify`) was Apache-2.0 from 2018 until commit `3f0cf5476c` on 2022-07-11. That commit deleted about 747k lines. The last full-source commit is around `9d872fe872` (2022-07-06).
   - **CLI:** the Rust CLI was Apache-2.0 up to commit `96d684450a` (2026-10-06). It includes the Stacks, CI/JUnit, scopes-detect, freeze, queue and config-validate/simulate crates.
   - Neither snapshot can use the "Mergify" trademark. Do not use any binary released after 2026.10.3.
3. **Mergify supports GitHub only.** `llms.txt` says so outright: no GitLab, Bitbucket or other code host. GitLab CI is accepted only as a source of CI results. The CEO argued against GitLab support publicly in 2024 ("There's Almost No GitLab").
4. **There is no Mergify MCP server.** Their agent strategy is a set of CLI-driving "Agent Skills" for Claude Code, Codex and skills.sh agents. Their own Graphite comparison table admits Graphite ships `gt mcp`.

---

## 1. Workflow Automation (`pull_request_rules`)

**Model.** Each rule has `name`, `conditions` and `actions`, plus optional `description` and `disabled: {reason}`.
- Rules evaluate the PR's *state*, not individual events. Every matching rule runs, not just the first one.
- Most actions are **edge-triggered**: they run once per match→unmatch→match cycle.
- These actions instead run on every evaluation to hold the PR in a state: `assign`, `dismiss_reviews`, `edit`, `label`, `post_check`, `rebase`, `request_reviews`, `review`, `squash`, `update`.
- These actions refuse to run twice in one evaluation, so the first rule in the file wins: `close`, `delete_head_branch`, `merge`, `queue`, `rebase`, `squash`, `update`.
- `label` has loop protection. If a label is flipped back and forth by different rules 10 times within an hour, Mergify stops changing it.

**Conditions.** The grammar is `[-][#]attribute [op value]`.
- Operators: `=`, `!=`, `~=` (regex), `glob` (added Oct 2025), `<`, `<=`, `>`, `>=`.
- Logical grouping: `and`, `or`, `not`.
- There are about 75 attributes. Highlights:
  - PR state: `files`, `added-lines`, `#approved-reviews-by`, `check-success/failure/pending/neutral/skipped/stale/timed-out/cancelled`, `conflict`, `draft`, `linear-history`, `#commits-behind`, `review-threads-unresolved`.
  - Scheduling and dependencies: `schedule` (e.g. `Mon-Fri 09:00-17:30[Europe/Paris]`), `depends-on`, `current-datetime`.
  - Commands and forks: `sender`, `sender-permission`, `from-fork` (new Aug 2026).
  - Monorepo and queue: `scope`, `queue-position`, `queue-dequeue-reason`.
  - Dependabot metadata: `dependabot-update-type` and related.
  - Injected from GitHub branch protection and rulesets: `github-review-approved`, `github-code-owner-review-satisfied`, `github-require-last-push-approval`.

**Actions (18 listed; 3 deprecated):**

| Action | What it does / notable options |
|---|---|
| `assign` | `add_users` / `remove_users` (`users` is the legacy form). Supports `{{ author }}`. Skips `[bot]` logins. Max 10 assignees. |
| `backport` | Cherry-picks a **merged** PR onto `branches` / `regexes` (regexes are checked against at most 1,000 branches). Options: `assignees`, `labels`, `title`/`body` templates (`{{ cherry_picked_commits }}`), `ignore_conflicts` (default true, opens a PR with the conflicts), `label_conflicts`, `merge_conflict_style` (merge/diff3), `bot_account`, `report_mode` (check/comment). Head branch is `mergify/bp/<dest>/pr-<n>`. |
| `copy` | Same options as backport, but runs on any non-conflicting PR, merged or not. Head branch is `mergify/copy/...`. |
| `close` | Closes the PR and posts a templated `message`. |
| `comment` | `message` template, optional `bot_account` impersonation. |
| `delete_head_branch` | **Deprecated (Mar 2026)**; use GitHub's auto-delete setting. Option `force`. Never touches forks. |
| `dismiss_reviews` | `approved` / `changes_requested` (true, a list of logins, or `from_requested_reviewers`), `message`, `when: synchronize\|always`. Pushes made by Mergify itself do not trigger it. |
| `edit` | Toggles `draft: true/false`. |
| `github_actions` | **Deprecated (Jul 2026)**. Dispatched `workflow_dispatch` workflows with `inputs` and `ref`. |
| `label` | `add`, `remove`, `remove_all`, `toggle`. Creates missing labels with a random color. |
| `merge` | `method` (merge/rebase/squash/fast-forward), `merge_bot_account`, and `commit_message_format` (`title: inherit\|pr-title`, `body: inherit\|pr-body\|empty`, `trailers: [co-authored-by, approved-by, merged-by]`), which replaces the deprecated Jinja `commit_message_template`. Mergify adds its own conditions: branch protection and rulesets, Merge Protections, `Depends-On:`, `Merge-After:`, and a valid config. |
| `post_check` | **Deprecated (Mar 2026)** in favour of Merge Protections. Mergify opens an auto-migration PR. |
| `queue` | Adds the PR to the merge queue (`name` optional). |
| `rebase` | Always acts as a GitHub user (the author or `bot_account`), who must have logged in to the dashboard once. It cannot impersonate bots. Fork PRs can no longer be rebased (Sep 2026). |
| `request_reviews` | `users`, `teams`, `users_from_teams`, `random_count` with weights (1–65535). Never requests the author or already-involved reviewers. Respects GitHub's 15-request cap. |
| `review` | `type: APPROVE\|REQUEST_CHANGES\|COMMENT`, `message`, `bot_account`. |
| `squash` | `commit_message: all-commits\|first-commit\|title+body`. Force-pushes as a user. |
| `update` | Merges the base branch into the PR. Works on forks only if "Allow edits from maintainers" is ticked. |

Example rule:
```yaml
pull_request_rules:
  - name: label + comment on conflict
    conditions: [conflict]
    actions:
      comment: { message: "@{{ author }} this PR is in conflict" }
      label: { toggle: [conflict] }
  - name: backport on label
    conditions: [label = backport]
    actions:
      backport: { branches: [release-1.0], assignees: ["{{ author }}"] }
```

**Templating change.** Jinja2 templating **stops working on 2026-10-31**. Only plain `{{ variable }}` substitution remains. Accounts created recently never had Jinja at all. Self-hosted installs keep Jinja.

---

## 2. Comment commands

Syntax is `@Mergifyio <cmd>`; `@Mergify` also works and the prefix is case-insensitive. The command must start the comment. Mergify acknowledges it with a 👍 reaction.

- **`backport <branch…>`**: waits until the PR is merged, then backports.
- **`copy <branch…>`**: runs immediately on non-conflicting PRs.
- **`queue [queue-name]`**: also re-queues a dequeued PR. A pending queue command is replaced if a different queue name is given. There is also a checkbox UI, controlled by `merge_queue.queue_controls_comment`.
- **`dequeue`**: also cancels a pending queue command. It cascades to stacked PRs above it.
- **`rebase`**: refused on fork PRs.
- **`update`**
- **`refresh`**: re-evaluates the PR against the config.
- **`squash [all-commits|first-commit|title+body]`**

Renamed or deprecated commands:
- **`unqueue`**: alias removed (May 2026); use `dequeue`.
- **`requeue`**: deprecated (Feb 2026); use `queue`. It still appears in the restrictions schema.

**`commands_restrictions`.** A per-command `conditions:` list is evaluated against the commenter, e.g. `sender = @team`, `sender-permission >= write`, `base = main`.
- Defaults: `sender-permission >= write` for everything.
- `update`, `rebase`, `squash` and `refresh` also allow the PR author, but only on **fork** PRs (changed Aug 2026). This blocks a read-only user from getting Mergify to rewrite a branch in the main repo.
- `conditions: true` removes the restriction entirely.

---

## 3. Merge Protections, Stacks, Monorepo

### Merge Protections

These have replaced `post_check`. They are configured as `merge_protections: [{name, description, if: [...], success_conditions: [...]}]`.
- A rule is "active" when its `if` matches.
- All active rules roll up into a single **`Mergify Merge Protections`** check run. It can also be reported as a GitHub Deployment via `merge_protections_settings.reporting_method`.
- Mergify enforces the protections on its own merges. To block merges made in the GitHub UI, users must mark the check as required.

**Built-in protections:**
- **`Depends-On:`** headers in the PR body. They work across repos owned by the same owner, and circular chains block forever.
- **`Merge-After:`** with a timestamp.
- **Configuration Changed**: a PR that edits `.mergify.yml` must have a valid config.

**`merge_protections_settings`:**
- `auto_merge_conditions` (May 2026) replaces `queue_rules[].autoqueue`. Set it to `true` or to a list of conditions; matching PRs are auto-merged, or auto-queued if the queue is on. Mergify opens auto-migration PRs.
- `post_comment` (default true) posts a status comment.
- Merge Protections now honor GitHub's "require approval of most recent push" (Jun 2026).

**Scheduled freezes.** These are created in the dashboard, the CLI (`mergify freeze create --reason … --start … --end … -c base=main -e label=hotfix`) or the API.
- A freeze has a start, an optional end and a timezone, plus scope conditions and **exclude** conditions (ANDed together).
- "Freeze Now" starts one immediately.
- Recurring windows use a `schedule` condition in a protection rule instead.

The dashboard rule builder edits the YAML by opening a PR.

### Stacks (`mergify stack`)

Stacks follow a Gerrit-style "one branch, many PRs" model.
- **`mergify stack setup`** installs `commit-msg`, `prepare-commit-msg`, `post-commit` and `pre-push` hooks.
  - Each commit gets a **`Change-Id: I<40 hex>` trailer**, which persists across amend and rebase.
  - The `pre-push` hook blocks a plain `git push` on stack branches.
  - Setup also configures git-notes refs so `mergify stack note` can record amend reasons.
- **`mergify stack push`** works like this:
  - It rebases onto the base branch, unless a PR in the stack is approved and the rebase would dismiss the review. A real conflict overrides this; `--force-rebase` / `--skip-rebase` control it.
  - It creates one remote branch per commit, named `stack/{user}/{branch}/{slug}--{changeid8}`.
  - It opens chained PRs, each based on the previous one's head.
  - It only force-pushes changed commits and deletes orphaned branches.
- **Chain representation:**
  - By default (since Sep 2026) it registers the stack with **GitHub's own native stacking API**. **Unverified** beyond Mergify's own docs; I did not research GitHub's feature itself.
  - Otherwise it writes `Depends-On: #N` into PR bodies and posts a stack comment. The merge queue uses these to land stacks bottom-up as a unit.
- **Other commands:** `new`, `list`, `open`, `checkout`, `sync`, `reorder`, `move`, `fixup`, `squash`, `drop`, `edit`, `reword`, `note`, `hooks`.
- **Git config keys:** `mergify-cli.stack-branch-prefix`, `-create-as-draft`, `-github-native`, `-keep-pr-title-body`, `-revision-history`.
- **Auth and cost:** Stacks needs only a GitHub token, never the Mergify API. Their own comparison page says Stacks is free without a subscription.
- **Comparisons:** the docs compare Stacks with plain git, gh-stack and Graphite. Strengths they concede to Graphite: `gt absorb`, `gt undo`, frozen branches, a VS Code extension and MCP.
- **License:** proprietary since 2026-10-08. The Rust implementation, including `change_id.rs`, `slug.rs`, `native_stack.rs` and the hook scripts, is Apache-2.0 at `96d684450a`.

### Monorepo: scopes and Job Selection

A top-level `scopes:` block can come from three sources:
- `source.files` with include/exclude globs, which Mergify evaluates server-side;
- a `manual` source;
- uploads from Nx, Bazel (bazel-diff), Pants or Turborepo via the `gha-mergify-ci` action, `mergify ci scopes-send`, or `PUT /pulls/{n}/scopes`.

Scopes feed two consumers:
- **Merge queue:** scope-aware batching, `parallel` mode lanes, `isolated` mode, per-scope check limits, "barrier" files, and **Direct Merge** for scope-unaffected PRs. The merge queue itself is the other researcher's area.
- **CI Insights Job Selection:** a detect job outputs per-scope booleans, and a `ci-gate` job aggregates the results. A special `merge-queue` scope lets suites run only on queue drafts.

The older "Monorepo CI" blog branding (Nov 2025) now appears as Job Selection.

---

## 4. CI Insights and Test Engine

Names have changed over time: "CI Issues" became CI Insights (the old name survives in repo descriptions and a 2024 HN quote describing AI issue detection), and "Test Insights" became **Test Engine**.

**CI Insights (job level):**
- **Jobs page:** health per job (Healthy/Flaky/Broken), duration, failure rate, **cost**, and individual executions. It can be grouped by branch. I did not verify how cost is computed.
- **Flaky job definition:** the same job in the same pipeline on the same SHA ends with different conclusions. Cancelled runs are ignored.
- **Auto-Retry** (configured in the dashboard per repository):
  - Conditions: job name, pipeline, branch, conclusion (`failure`/`timed_out`), flaky flag, and log-line regex that also matches check-run annotations. A regex tester is built in.
  - 1–10 retries per rule; mode `job` (failed jobs only) or `pipeline`.
  - **GitHub Actions only**, and only at workflow-run granularity.
- **Self-hosted runners** (Jun–Jul 2026): fleet and queue metrics such as wait time vs run time, per-label demand, per-runner health and cost. It auto-detects persistent runners (seen twice). GitHub-hosted runners are excluded.
- **Merge Queue Stats** now show CI runs per merged PR and why wasted runs were discarded (Oct 2026).
- **Notifications:** Slack only, with filters on repo, branch, pipeline, job and conclusion. Datadog receives merge-queue metrics.

**Test Engine (test level):**
- **Health state machine:** healthy, flaky and broken.
  - "Flaky evidence" is any of: the same commit giving different results, the framework marking the run flaky, or an intermittent failure on new commits.
  - Flaky tests need a longer clean streak to recover than broken ones.
  - Tests are tracked per repo, branch, pipeline, job and test name.
- **Prevention** (Jul 2026): framework plugins **rerun tests on PRs** within a dashboard-set CI budget. Reports cover caught flaky tests, new tests with a confidence level, and CI budget spent.
- **Detection:** default-branch health and an impact score.
- **Mitigation / quarantine:**
  - Quarantine is set in the dashboard, the CLI (`mergify tests quarantines add|remove|list`) or the API.
  - Auto-quarantine waits for evidence and suppresses "CI incident" mass failures.
  - Since Oct 2026, tests leave quarantine automatically when they recover.
  - Plugins apply quarantine in-process; pytest marks the test as non-strict `xfail`.
  - "Fix quarantined tests with AI" (Oct 2026) hands Claude Code or Cursor a ready-made prompt.

**Ingestion:**
- **Job data:** collected through the GitHub App, starting from the next CI run after CI Insights is enabled per repository. The mechanism is presumably `workflow_job` webhooks and the Actions API (**unverified**).
- **Buildkite:** plugin and webhook (Apr 2026).
- **Jenkins:** an MIT-licensed plugin, `jenkinsci/mergify-plugin`.
- **CircleCI, GitLab CI, TeamCity:** via `mergify ci junit-process` from the CLI.
- **Test data:** JUnit XML upload via `mergify ci junit-process` or the `gha-mergify-ci` action (`report_path`, `job_name`, `test_step_outcome`). It needs an application key with `ci` scope in `MERGIFY_TOKEN`.
- **Native framework plugins:** pytest, RSpec, Vitest/Jest, Playwright, Cypress, Go, Rust, JUnit/TestNG, NUnit/MSTest, PHPUnit/Pest, Minitest.
  - **pytest-mergify sends OpenTelemetry traces over OTLP/HTTP** for each test (it depends on `opentelemetry-exporter-otlp-proto-http`).
  - It also does reruns, quarantine, and "test selection" (rerunning only previously failed tests in the queue).
- **Flaky-detection recipe they recommend:** a scheduled workflow that loops the suite N times on the default branch.

**API:** test search, test detail, executions, failures, health transitions, per-test and aggregate stats time series, runner stats, CI queue stats, and quarantine CRUD.

**Retention:** jobs 24 h (Free/OSS) vs 30 days (Max); tests 7 days vs 30 days.

---

## 5. AI and agent features

**What exists:**
- **Agent Skills** (proprietary), with six skills: `mergify-merge-queue`, `mergify-events`, `mergify-config`, `mergify-merge-protections`, `mergify-ci`, `mergify-stack`.
  - Distribution: the Claude Code official marketplace (`/plugin install mergify@claude-plugins-official`), the OpenAI/Codex plugin directory, and skills.sh (`npx skills add Mergifyio/mergify-cli`).
  - They shell out to the CLI.
- **`docs.mergify.com/stacks/agents`**: a bootstrap page written for agents to read ("You are an AI coding agent…").
- **Agent-friendly docs:** `llms.txt`, a "For agents" section, Markdown for every page, OpenAPI and JSON Schema.
- **CLI built for agents:** `mergify auth login` uses a device-code flow, stores the credential in the OS keychain "outside the agent's reach", and offers `--json` output and stable exit codes.
- **"Fix quarantined tests with AI"** prompt handoff (Oct 2026).

**What does not exist:**
- No MCP server (confirmed against docs and search).
- No AI code review.
- Earlier "AI" CI-issue detection appears to have been folded into CI Insights (**unverified**).
- An archived repo, `gha-ai-inference`, suggests an abandoned experiment (**unverified**).

**How they market to agent-heavy teams:**
- **The framing is a throughput and "release problem", not an AI review problem.** Homepage: "no matter how fast AI ships code". Blog post "Merge queues and AI coding agents" (Julien Danjou, 2026-07-13) argues for speculative lanes, batching with bisection, scope graphs and two-step CI. It also says Mergify is working toward **change-based identity** (logical change rather than SHA) so rebases keep reviews and test results.
- **A data report, "State of Merge Queues 2026"**: AI-assisted PRs broke main 1.9% of the time vs 4.4% for others. The sample was 200k PRs across 477 orgs; AI usage was detected from commit signatures.
- **Stacks pitch:** "AI can write 1,000 lines… nobody wants to review a 1,000-line PR."

---

## 6. Config sharing, validation, dashboard, API, integrations, hosting

**Config file.** Mergify reads the first of these it finds on the default branch: `.mergify.yml`, `.mergify.yaml`, `.mergify/config.y(a)ml`, `.github/mergify.y(a)ml`. Having two is an error.

Top-level keys: `pull_request_rules`, `merge_protections`, `merge_protections_settings`, `queue_rules`, `merge_queue`, `priority_rules`, `scopes`, `commands_restrictions`, `defaults`, `shared`, `extends`.

- **`defaults`**: `defaults.actions.<action>.<opt>` and `defaults.queue_rule.<opt>` act as fallbacks.
- **`shared`**: a free-form home for YAML anchors. Anchors and aliases work anywhere.
- **`extends: <repo-name>`**:
  - The source must be in the same org, have the app installed, and be at least as visible as the extending repo.
  - Only one level is supported; no chaining.
  - Merge semantics:
    - Same-named rules: local wins.
    - `defaults`, `commands_restrictions`, `merge_queue` and `merge_protections_settings` are deep-merged.
    - `shared` and `scopes` are **not** merged.
  - A community request for local-path extends is open (Discussion #5176).

**Validation and simulation:**
- A "Configuration changed" check runs on any PR that edits the config. It also reports sections that will never be used because the relevant product is disabled.
- The JSON Schema is listed in SchemaStore.
- There is a `mergify-pre-commit` hook.
- CLI: `mergify config validate` and `mergify config simulate <PR-URL>`.
- API: `POST .../pulls/{n}/simulator`, `POST .../simulator`, `POST .../configuration-simulator`.

**Dashboard (dashboard.mergify.com):**
- Products are enabled per repository (Merge Queue, Merge Protections, Workflow Automation, CI Insights), with defaults for new repos.
- Merge-queue status board and stats.
- Activity Log (Jun 2026).
- Configuration editor; it cannot save configs that use YAML anchors (Discussion #5152).
- Merge Protections rule builder and scheduled freezes.
- CI Insights: jobs, auto-retry and runners. Test Engine: prevention, detection and mitigation.
- Stacks adoption page.
- Application keys (`admin` or `ci` scope; neither is read-only).
- Delegated roles (Billing, CI, Integrations, Delegation Admin), **Enterprise only**.
- Slack and Datadog setup.
- Browser extensions for Chrome and Firefox (Apache-2.0) add a queue/dequeue panel, stack revision history and local-timezone timestamps inside GitHub.

**REST API** (`https://api.mergify.com/v1`, Bearer application key or GitHub PAT):
- Queues: status, configuration, branches, per-PR detail, **dequeue-impact preview**, dequeue, pause/unpause.
- Scheduled-freeze CRUD.
- Simulators.
- Activity log and aggregates.
- About 20 queue statistics endpoints: time to merge, checks outcome, batch bisection, failure rate, CI runs, idle time, PR exit reasons, and more.
- Scope upload per PR or commit.
- CI/Test Insights (listed in §4).
- Product enablement.
- Slack notification configuration.
- Removed: `/v1/badges/*` (Aug 2026).
- There is also an official **Terraform provider** (MPL-2.0, May 2026).

**Notifications.**
- **Slack:** merge-queue events (enter, checks end, leave, pause, freeze), CI job outcomes, and Test Engine quarantine events.
- **Datadog:** merge-queue metrics plus a tile.
- I found **no generic outbound webhooks** in the docs or the API (absence not 100% confirmed).

**Hosting:**
- **GitHub.com:** Mergify Cloud.
- **GitHub Enterprise Server:** only through **Mergify Enterprise Server**, the on-prem edition on the Enterprise plan. It ships as container images for Kubernetes or VMs, requires GHES 3.14+, and keeps data in databases the customer owns. Air-gap support is **unverified**.
- **GitLab, Bitbucket, Gitea:** not supported. A Gitea request is open (Discussion #5170). The 2019 GitLab issue #447 is closed.
- **Sign-in:** GitHub OAuth only; SSO is inherited from GitHub SAML.

---

## 7. Business

**Pricing** (mergify.com/pricing; all products in every plan):

| | Open Source | Free | Max | Enterprise |
|---|---|---|---|---|
| Price | $0 | $0 | **$21 per active seat/month** (15% off yearly; EUR available; 14-day trial) | Custom; volume and purchase order |
| Users | Unlimited, **public repos only** | ≤5, private repos | ≤100 | Unlimited |
| Job / test retention | 24 h / 7 d | 24 h / 7 d | 30 d / 30 d | Custom |
| Gated (✗ on OSS) | Batch, Scopes, Pause, Priorities, Two-step CI, Monitoring, Scheduled Freeze, Quarantine, Auto-Retry, Slack | All ✓ | All ✓ + SOC 2 report + email support | + Delegated Roles, on-prem, 24/7 support (optional), Slack Connect, quarterly roadmap reviews |

Notes on the table:
- Parallel checks, stacks, PR dependencies, scheduled merge and SSO are available on every tier.
- **Workflow Automation and Merge Protections are not listed** in the pricing table, so their gating is **unverified**.
- **Seats count only "active contributors"**: users who opened or pushed to a PR in a rolling 30-day window, de-duplicated across repos, prorated per day, with bots excluded.
- There is a startup programme worth up to $12k in credit.
- History: in 2019 they moved from org-size pricing to counting collaborators, after "pricing was the main obstacle".

**Company:**
- Founded in 2018 (first merges May 2018) by **Julien Danjou (CEO)** and **Mehdi Abaakouk (CTO)**, both from OpenStack and Debian. The idea came from missing OpenStack/Zuul-style gating on GitHub.
- Show HN was in Nov 2018.
- The About page says: remote team in France and the Netherlands, **7 people**, **$0 raised** (bootstrapped), 2,000+ organizations, 75k+ PRs merged per month. Named customers include Cerebras, Back Market, Productboard, PayFit and Zama.
- They wrote a TLA+ spec of the queue.
- Compliance: SOC 2 Type II, a HackerOne bug bounty, and AES-256 at rest. Repo contents are not stored; clones are temporary.

**License history:**
- **mergify-engine:** Apache-2.0 from 2018-04 to 2022-07-11. The code then moved private and the repo became the "Community Issue Tracker".
  - Danjou's stated reasons: about six external contributors in four years and none regular; functional tests needed undocumented GitHub setup; nobody self-hosted; competitors could read the code.
  - A user asked in Discussion #5035 for the history to be archived publicly. The git history is in fact still present in `Mergifyio/mergify`.
- **mergify-cli:** Apache-2.0 from 2021-05 to 2026-10-08, then proprietary as described in §0. It was originally Python and rewritten in Rust in 2026 (timing **unverified**).
- **pytest-mergify:** the archived GitHub repo says GPL-3.0. PyPI releases up to 2026.10.3 say Apache-2.0, and 2026.10.4 onward are proprietary. Development moved to `Mergifyio/mergify-ci-integrations`, which is private (404).

**What is open today:**

| Component | License |
|---|---|
| browser-extension | Apache-2.0 |
| mergify-ci-buildkite-plugin | Apache-2.0 |
| homebrew-tap | Apache-2.0 |
| daiquiri (logging library) | Apache-2.0 |
| terraform-provider-mergify | **MPL-2.0** |
| jenkinsci/mergify-plugin | **MIT** |
| gha-mergify-ci, setup-cli, mergify-pre-commit, merge-queue-academy | **No LICENSE file** (all rights reserved by default) |
| mergify-cli binaries, Agent Skills, pytest-mergify, @mergifyio/vitest | **Proprietary** |

---

## 8. Pain points, and where OpenMerge can do better

Public complaint volume is thin: no substantive Reddit threads, and only scattered HN comments. These come from primary evidence:

1. **Noise from draft PRs and bot spam.** NixOS/nixpkgs adopted Mergify in Dec 2024 and disabled it almost at once because "it was opening a Pull request **per minute**". It formally dropped Mergify in July 2025: "the mergify experiment failed and we're on our way towards GitHub merge queues". Users also ask how to turn off Merge Protections and CI Insights comments (#5129, #5154).
2. **Impersonation complexity.** Rebase and squash must act as a human who once logged in to the dashboard. Bots cannot be impersonated, forks are refused, and commits appear to come from "random users" (#5038, #5117, #5125). This is a structural weakness of a hosted GitHub App.
3. **Stuck state and opacity.** "Summary" check runs stay permanently `in_progress` with no way to recover (#5159). There are "GitHub permissions prevent queueing" errors (#5142). `assign` fails silently.
4. **Reliability tied to GitHub.** Status-page incidents include a 47-minute queue slowdown (2026-07-14) and webhook-delay incidents (2026-08-17). A third-party tracker counts about 18 outages over two years, but its counts vary (**treat as approximate**).
5. **Pricing and lock-in.**
   - $21 per active seat is reportedly poor value at low PR volume (aicoolies).
   - The OSS tier removes batching, scopes, freeze and quarantine.
   - Retention is 24 hours on free tiers.
   - Delegated roles and on-prem are Enterprise-only.
   - The client tooling (the CLI used in CI, the test plugins, the stacks hooks) is now proprietary and bans competing use. That is new lock-in that OpenMerge can directly counter.
   - Third-party pages note a "learning curve" and that the product is GitHub-only.
6. **Config ergonomics.**
   - `extends` is limited: one level, same org, no local files, no merging of `shared` or `scopes`.
   - The dashboard editor fails on YAML anchors.
   - Frequent breaking deprecations: Jinja removal on 2026-10-31, `autosquash` removed, queue `conditions` and `routing_conditions` deprecated, `allow_queue_branch_edit` going away after 2026-12-31, the badge API removed. Each one forces config churn, even when auto-migration PRs are offered.
7. **Platform gaps.** No GitLab, Gitea/Forgejo or Bitbucket support. GHES requires the Enterprise plan. No MCP server. No generic webhooks.

**Where OpenMerge can differentiate:**
- Truly open licensing: core, CLI and test plugins all Apache-2.0.
- Self-hosting by default, including GHES, and multi-forge support via a forge abstraction.
- An MCP server alongside the CLI.
- Quiet-by-default reporting.
- Impersonation-free update strategies.
- A stable, versioned config format with compatibility for Mergify's schema. The schema is a public interface; reimplement from facts, and do not copy doc prose.
- Generic webhooks and OpenTelemetry as first-class ingestion and export.

---

## Sources

- https://docs.mergify.com/llms.txt (and the `.md` source of each page listed there: workflow/*, workflow/actions/*, commands/*, configuration/file-format, configuration/sharing, configuration/conditions, configuration/data-types, merge-protections/*, stacks/*, cli/agents, cli/usage, ci-insights/*, test-engine/*, integrations/*, security, billing, support, browser-extensions)
- https://docs.mergify.com/openapi.json
- https://docs.mergify.com/changelog/ and https://docs.mergify.com/changelog/rss.xml
- https://docs.mergify.com/stacks/agents
- https://mergify.com/ ; https://mergify.com/pricing ; https://mergify.com/about ; https://mergify.com/solutions/enterprise-server
- https://mergify.com/blog/merge-queues-and-ai-coding-agents
- https://mergify.com/reports/state-of-merge-queues-2026
- https://github.com/Mergifyio (org listing via GitHub API)
- https://github.com/Mergifyio/mergify-cli (README, LICENSE; commits f9cac3c8ce, ed45bdfce8, 96d684450a, da3caa7e1d)
- https://github.com/Mergifyio/mergify (formerly mergify-engine; commit 3f0cf5476c; LICENSE at 9d872fe872)
- https://github.com/Mergifyio/mergify/discussions/5035
- https://github.com/Mergifyio/mergify/discussions (#5152, #5159, #5142, #5170, #5176, #5129, #5154, #5117, #5125)
- https://github.com/Mergifyio/pytest-mergify ; https://pypi.org/project/pytest-mergify/ ; https://pypi.org/project/mergify-cli/
- https://github.com/Mergifyio/gha-mergify-ci ; https://github.com/Mergifyio/terraform-provider-mergify ; https://github.com/jenkinsci/mergify-plugin
- https://articles.mergify.com/why-mergify-codebase-isnt-open-source-anymore-a-tale-of-growth-change-and-adaptation/
- https://articles.mergify.com/new-pricing/
- https://julien.danjou.info/p/theres-almost-no-gitlab and HN discussion https://news.ycombinator.com/item?id=42084571
- https://news.ycombinator.com/item?id=18492639 (Show HN 2018) ; https://news.ycombinator.com/item?id=42293221
- https://github.com/NixOS/nixpkgs/pull/360340 ; https://github.com/NixOS/nixpkgs/pull/423506
- https://isdown.app/status/mergify/incidents/622245-merge-queue-processing-delays ; https://isdown.app/status/mergify/incidents/639776-merge-queue-delays-caused-by-a-github-incident ; https://pingoru.io/providers/mergify/outage-history
- https://aicoolies.com/reviews/mergify-review ; https://stackpick.net/compare/mergify-vs-aviator/ ; https://mergify.com/compare-mergify
- https://www.caplight.com/company/mergify ; https://console.dev/interviews/mergify-julien-danjou
