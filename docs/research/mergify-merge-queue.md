# Mergify merge queue: technical research report (as of 2026-10-15)

## 0. Summary and method

- **Sources.** This is based mainly on the current docs at docs.mergify.com, which publishes raw Markdown for every page (append `.md`), plus the docs changelog, the OpenAPI spec, and the self-hosted ("Enterprise") requirements. For internals I read the last public source snapshot of the old engine through the GitHub API (`Mergifyio/mergify`, formerly `mergify-engine`, commit `fe6e586630`, 2022-07-08), and Mergify's 2020 engineering post on Redis Streams.
- **Big changes since 2024–2025:**
  - `partition_rules` were removed and replaced by `scopes`.
  - `speculative_checks` became `merge_queue.max_parallel_checks`, which now defaults to 5.
  - `priority_rules` moved to the top level. The implicit priority that came from queue order was removed.
  - `conditions` was renamed `merge_conditions`, and `routing_conditions` was renamed `queue_conditions`.
  - `autoqueue` was replaced by `merge_protections_settings.auto_merge_conditions`.
  - `allow_inplace_checks` was deprecated; Mergify now decides it automatically.
  - The `requeue` command became an alias of `queue`, and the `unqueue` alias was removed.
  - New in 2026: queue modes (`serial`, `parallel`, `isolated`), dynamic batch size, `merge-batch`, Direct Merge, automatic `checks_timeout`, CI retries, and a single "Mergify Merge Queue" check run.
- **Licensing finding.** The engine source was Apache-2.0 for its whole public history. The `LICENSE` file was only ever touched by the initial commit and by the commit that removed the code. The code was taken out of the public repo on 2022-07-11; I found no license change. **The 2022 snapshot is still usable Apache-2.0 code** for OpenMerge, with attribution, and with care around the Mergify trademark.

---

## 1. Configuration file

### Locations and precedence (current)
Mergify reads the **first** file it finds, from the repository's **default branch**, in this order:

1. `.mergify.yml`
2. `.mergify.yaml`
3. `.mergify/config.yml`
4. `.mergify/config.yaml`
5. `.github/mergify.yml`
6. `.github/mergify.yaml`

If a PR against the default branch would leave two or more of these files, Mergify does not evaluate that PR. It reports a failure naming the files it found and the one it will read.

The 2022 engine supported only `.mergify.yml`, `.mergify/config.yml` and `.github/mergify.yml` (`constants.MERGIFY_CONFIG_FILENAMES`). There is a JSON Schema at `https://docs.mergify.com/mergify-configuration-schema.json`.

### Top-level keys (current)

| Key | Purpose |
|---|---|
| `pull_request_rules` | Workflow Automation: conditions → actions (`queue`, `label`, `comment`, …) |
| `queue_rules` | Per-queue definitions: conditions, batching, merge method, … |
| `merge_queue` | Global queue options: `mode`, `max_parallel_checks`, labels, comments, `reset_on_external_merge`, `skip_intermediate_results` |
| `priority_rules` | Global priority assignment (top-level since 2024-08) |
| `scopes` | Monorepo scopes: `source.files` or `source.manual`, `capacities`, `default_capacity`, `barrier_files`, `merge_queue_scope` |
| `merge_protections` | Rules (`if` / `success_conditions`) that gate merging |
| `merge_protections_settings` | `auto_merge_conditions`, `post_comment`, `reporting_method` (`check-runs` or `deployments`) |
| `commands_restrictions` | Per-command condition gates |
| `defaults` | Defaults for `actions.*` and `queue_rule` |
| `shared` | Free-form storage for YAML anchors |
| `extends` | Repository name (same org) to inherit config from |

- **Removed:** `partition_rules`. It was deprecated 2025-07-28 and removal was announced for 2025-10-31.
- **Unused sections:** a section is only read if its product (Merge Queue, Merge Protections, Workflow Automation) is enabled for the repo in the dashboard. The exception is `queue_rules`, which the `queue` action reads whenever Workflow Automation is enabled.

### `extends` semantics
- The value is a bare repository name, resolved in the same org. Only one level is allowed.
- The source repo must have the app installed and be at least as visible as the extending repo.
- How sections merge:
  - `defaults`, `commands_restrictions`, `merge_queue` and `merge_protections_settings` are merged, with local values winning.
  - Rule lists (`pull_request_rules`, `queue_rules`, `priority_rules`, `merge_protections`) are merged by rule `name`, with local winning.
  - `shared` and `scopes` are **not** merged.

```yaml
defaults:
  actions:
    comment:
      bot_account: ReleaseBot
  queue_rule:
    batch_size: 10

queue_rules:
  - name: default
  - name: small batches
    batch_size: 2
```

```yaml
shared:
  my_ci: &common_checks
    - check-success = ci-one
    - check-success = ci-two
queue_rules:
  - name: hotfix
    queue_conditions:
      - and: *common_checks
  - name: default
    queue_conditions:
      - and: *common_checks
      - schedule = Mon-Fri 09:00-17:30[Europe/Paris]
```

---

## 2. Queue mechanics end-to-end

### 2.1 Entering the queue
There are four ways in:

1. **The `@mergifyio queue [queue-name]` command.** `@Mergify` also works, and the prefix is case-insensitive.
   - With no name, the PR goes to the **first queue whose `queue_conditions` match**.
   - Running the command again with a different name cancels the pending one and moves the PR.
   - Running it on a dequeued PR resets it and requeues it.
2. **The "Queue this pull request" checkbox** in a Mergify comment (added 2026-06). It is controlled by `merge_queue.queue_controls_comment`, which defaults to `true`.
3. **Auto-merge** via `merge_protections_settings.auto_merge_conditions`, set to `true` or to a list of conditions. This replaced `queue_rules[].autoqueue`, and Mergify opens automatic migration PRs for it.
4. **The `queue` action** in `pull_request_rules`. All of its options except `name` were removed in 2024–2025.

```yaml
merge_protections_settings:
  auto_merge_conditions:
    - author = dependabot[bot]
```

```yaml
pull_request_rules:
  - name: put PRs in queue
    conditions:
      - label = queue-me
    actions:
      queue:
```

The default command restriction for `queue` and `dequeue` is `sender-permission >= write` (see §4.6).

### 2.2 `queue_conditions` vs `merge_conditions`
- **`queue_conditions`** gate entry, and the PR must keep matching them to stay in the queue. If it stops matching (for example, a label is removed), it is dequeued.
- **`merge_conditions`** are checked at validation time, just before the merge. For checks run on a draft PR, `check-*` conditions in `merge_conditions` are **evaluated against the temporary draft PR, not the original**.
- Splitting the two is what makes "two-step CI" possible (§3.6).
- State machine from the docs: Dequeued → Queueing → Queued (matches `queue_conditions`) → Validating (reaches the top) → Merged (matches `merge_conditions`). From Queued or Validating, an unmatch, a failure, or `dequeue` sends the PR back to Dequeued.
- **Branch protection injection.** GitHub branch protections and rulesets are injected as conditions, controlled by `branch_protection_injection_mode`:
  - `queue` (default): injected into both queue and merge conditions.
  - `merge`: injected into merge conditions only.
  - `none`: not injected; this requires `merge_bot_account`.

```yaml
queue_rules:
  - name: urgent
    queue_conditions:
      - label = urgent
    merge_conditions:
      - check-success = myci
  - name: default
    queue_conditions:
      - check-success = myci
```

### 2.3 How it tests: in-place vs draft ("batch") PR

**In-place checks.** CI runs on the original PR branch, after Mergify updates it with the base branch by `merge` or `rebase`. This happens **only when all of these hold**:
- `batch_size = 1` on every queue
- `max_parallel_checks = 1`
- `mode = serial`
- `max_checks_retries = 0`
- no two-step CI (`merge_conditions` is empty or identical to `queue_conditions`)

`allow_inplace_checks` is deprecated (2025-09-30) because this is now computed automatically.

**Draft / batch PR (the default in practice, since `max_parallel_checks` defaults to 5):**
- **Branch name:** `<queue_branch_prefix><10 hex chars>`, for example `mergify/merge-queue/a76b1f3d25`. The prefix defaults to `mergify/merge-queue/`.
  - Mergify periodically deletes any branch matching *exactly* that pattern that is not in use, matching by name only. Don't create your own branches in that shape.
  - In the 2022 engine the name was `mergify/merge-queue/<base-ref>/<pr1>-<pr2>-…`.
- **Contents:** the base branch tip, plus every PR queued ahead (the "parent" cars), plus this batch's PRs, each merged in order. The cars are cumulative: (PR1), (PR1+PR2), (PR1+PR2+PR3), …
- **How the 2022 engine built it:**
  1. Create a ref at the base SHA (`POST /git/refs`).
  2. For each parent PR and each embarked PR, call `POST /repos/{o}/{r}/merges` with `head: refs/pull/N/head` and commit message `"Merge of #N"`.
  3. Open a **draft** PR with `"draft": true`, titled `merge-queue: embarking #1, #2 together`, whose body is the queue summary.
  4. If a merge conflicted, report "conflict with at least one pull request ahead in queue".
  5. If the App got a 403 "Resource not accessible by integration" (typically a PR touching workflows), postpone.
- **How it is built today:** Mergify clones the repo to scratch disk to compose the branch. The self-hosted requirements say "Mergify clones repositories on disk when the merge queue builds a draft pull request". The exact commit messages it uses now are **unverified**.
- **Commit signing — the docs disagree:**
  - The rulesets page says hosted Mergify signs its queue merge commits as `Mergify <commit-signer@mergify.com>`, with the public key at `github.com/mergify-commit-signer.gpg`, and that self-hosted installs don't sign.
  - The 2026-08-25 changelog says Mergify "has no signing key".
  - Treat this as unresolved. The rulesets page appears to be the newer of the two.
- **Draft PR description (rewritten 2026-10-13):**
  - a note at the top that Mergify opened it and nobody needs to act on it
  - the batch and queue it was opened for, and a warning that pushing to or closing it dequeues the PRs
  - what it tests, the current state and the checks still pending, what happens next, and how the PRs will be merged
  - the merge conditions and queue graph, folded away below
- **Draft PR label:** the `merge-queue` label (configurable via `scopes.merge_queue_scope`).
- **Git note:** Mergify attaches a **git note** to the batch head commit. `mergify ci queue-info` reads it and returns JSON with:
  - `scopes`, `all_scopes`
  - `pull_requests[]`
  - `previous_failed_batches[]` (each with `batch_pr_number`, `batch_pr_head_sha`, `checked_pull_requests`)
  - `previous_check_retries[]`
  - `checking_base_sha`
- **Bot accounts:** `draft_bot_account` lets a user account create the draft PR. `update_bot_account` is used for in-place updates.
- **Edits to the batch branch:** an external push to the batch branch dequeues its PRs. `allow_queue_branch_edit` relaxes this, but it is deprecated and will be removed after 2026-12-31.

### 2.4 Status reporting
- **The "Mergify Merge Queue" check run** (unified 2026-04-21). It appears on every PR in a queue-enabled repo, with titles such as "In merge queue" or "Running merge queue checks".
  - Historically there was a separate "Queue: Embarked in merge train" check (2022 constant `MERGE_QUEUE_SUMMARY_NAME`) plus a "Summary" check.
  - Merge Protections reports a separate "Mergify Merge Protections" check, or a deployment if `reporting_method: deployments`.
- **Labels:**
  - `queued_label`, default `queued`
  - `checking_label`, no default; applied while the PR is embarked in a batch
  - `dequeued_label`, default `dequeued`
- **Status comments:** `status_comments: all | outcomes | none`.
  - A "Nothing landed" section appears when a rebase produced empty commits.
  - A "Requeue this pull request" checkbox appears after a dequeue.
- **Other surfaces:** the dashboard, the CLI, and the REST API (`/repos/{o}/{r}/merge-queue/status`, `/merge-queue/pull/{n}`, and `/dequeue-impact`).

```yaml
merge_queue:
  queued_label: queued
  checking_label: checking
  dequeued_label: dequeued
  status_comments: outcomes
```

### 2.5 Merge methods and update methods

**`merge_method`:**
- `merge`, `squash`, `rebase`: the **original PRs** are merged through GitHub's merge API after the batch passes.
- `fast-forward`: the base ref is moved directly.
  - *Inplace mode:* the PR is rebased, CI runs on it, then the base is fast-forwarded to its head.
  - *Batch-PR mode:* the base is fast-forwarded to the batch head, which is the exact SHA that was tested.
  - Serial mode only. It needs a bypass on rulesets/branch protection.
- `merge-batch` (new 2026-04-07): merges the batch PR itself through the standard PR merge API with a merge commit that lists every PR. Requires `batch_size > 1` and needs no bypass.
- `null` (default): the first method the repository allows.

`queue_branch_merge_method: fast-forward` is the deprecated spelling of `merge_method: fast-forward`.

**`update_method`** (used for in-place updates): `merge` or `rebase`. When unset, it defaults to `merge`, or to `rebase` with fast-forward.

Fork caveat: rebasing a fork PR **as an `update_bot_account`** was removed on 2026-09-29. Such PRs are now refused at queue entry.

```yaml
queue_rules:
  - name: default
    merge_method: fast-forward
    batch_size: 5
    merge_conditions:
      - check-success = ci
```

### 2.6 Failure, ejection, requeue
- **Dequeue reasons** are exposed as the `queue-dequeue-reason` attribute, for example `checks-failed`, `checks-timeout` and `checks-interrupted`. The full enum is rendered by a component and wasn't in the Markdown I fetched (unverified).
- **Conflict with a PR ahead:** the PR is **held**, not ejected.
  - If the PR ahead leaves without merging, the held PR resumes.
  - If the PR ahead merges, the held PR is dequeued as conflicting.
  - A conflict with the base branch, or with a PR that already left the queue, dequeues immediately.
- **Interrupted checks** (a check cancelled before any conclusion) give `checks-interrupted`. Requeue without changes.
- **`checks_timeout`:**
  - `auto` (default since 2026-06): the p95 of successful queue CI runtimes plus a margin, per queue and per base branch, over 7 days. It needs about 20 qualifying runs before it applies; retries and bisection runs are excluded.
  - A fixed duration (minimum 60s), or `null` to disable.
- **`max_checks_retries`** (2026-03): recreates the draft PR for a fresh CI run. It forces draft mode and is incompatible with fast-forward when `max_parallel_checks > 1`.
- **`reset_on_external_merge: always`** (default) restarts the queue when someone merges or pushes to the base outside the queue.
- **Requeue:** `@mergifyio queue` (or the checkbox). `@mergifyio requeue` was deprecated 2026-02-18 and is now an alias of `queue`.

---

## 3. Speculative checks, batching, bisection, two-step CI

### 3.1 Parallel ("speculative") checks
`merge_queue.max_parallel_checks` (default **5**, since 2025-06-02) caps how many cumulative cars are tested at once. Setting it to 1 disables speculation. It replaced per-queue `speculative_checks` (2024-09-23).

If a car fails, all cars after it are cancelled and requeued.

### 3.2 Batching
- `batch_size` is 1–128, or `{min, max}` for **dynamic batching** (2026-06-19). Dynamic batches stay at `min` while parallel-check slots are free and grow toward `max` when the queue backs up.
  - Example: with `max_parallel_checks: 5` and `{min:1, max:2}`, 3 queued PRs become 3 batches of 1, and 10 queued PRs become 5 batches of 2.
- `batch_max_wait_time` (default 30s) is how long to wait for a batch to fill. The timer starts when the first PR enters.
- **How a batch is composed:**
  1. Priority and queue order come first; they are never overridden.
  2. Stacks stay together.
  3. Remaining slots are filled by **similarity**: shared-scope ratio, then changed directories, then FIFO.
  - Similarity only looks at a window near the front of the queue, slightly wider than `batch_size`.

```yaml
merge_queue:
  max_parallel_checks: 2
queue_rules:
  - name: default
    batch_size: 3
    batch_max_wait_time: 5 min
```

### 3.3 Batch failure handling (bisection)
1. A batch fails or times out. All cars after it are cancelled and requeued.
2. The failed batch is split into `max(2, max_parallel_checks)` parts, and the splits are **cumulative prefixes**. The docs' example: 6 PRs with `max_parallel_checks: 3` gives `[PR1+PR2]`, `[PR1..PR4]`, `[PR1..PR5]`. The full set is not retested because it is already known to fail.
3. Splits are tested in parallel. A passing prefix is merged. A failing one is split again.
4. A single-PR batch that still fails identifies the culprit, which is dequeued.

Related controls and signals:
- `batch_max_failure_resolution_attempts` caps the number of splits. `0` dequeues the whole batch.
- Batch statuses during resolution include `bisecting`, `waiting_for_previous_batches` and `waiting_for_requeue`.
- The docs give recipes for re-running only the tests or jobs that failed in the parent batch, using `previous_failed_batches[-1].batch_pr_head_sha`.

### 3.4 `skip_intermediate_results`
`merge_queue.skip_intermediate_results: true`: if a *later, larger* passing batch contains a PR, an earlier failing batch containing that PR is treated as flaky, and the PR merges.

It is supported in serial and parallel modes (parallel since 2026-07-08), but not in isolated mode.

### 3.5 Deprecated or obsolete knobs
- `allow_inplace_checks`: deprecated, computed automatically.
- `allow_queue_branch_edit`: deprecated, removal after 2026-12-31.
- `speculative_checks`: replaced by `max_parallel_checks`.
- `commit_message_template`: use `commit_message_format`.

### 3.6 Two-step CI, and how CI knows it's a queue run
- Fast checks gate `queue_conditions`. The heavy suite gates `merge_conditions` and runs only on queue branches.
- **Detection is by branch name.** The batch is a real (draft) pull request whose head is `mergify/merge-queue/…`, so CI fires on the ordinary **`pull_request`** event.
  - Mergify does **not** use GitHub's `merge_group` event or GitHub's native merge queue.
  - The App does subscribe to "Merge group" events and has "Merge queues: read", described as "Used to receive GitHub merge queue events". What it does with them isn't documented; probably migration or analytics (unverified).

```yaml
# .github/workflows/ci.yml
on:
  pull_request:
    branches: [main]
jobs:
  preliminary-tests:
    runs-on: ubuntu-latest
    steps: [ { uses: actions/checkout@v4 }, { run: make lint test-unit } ]
  pre-merge-tests:
    if: startsWith(github.head_ref, 'mergify/merge-queue/')
    runs-on: ubuntu-latest
    steps: [ { uses: actions/checkout@v4 }, { run: make test-integration test-e2e } ]
```

```yaml
# .mergify.yml
queue_rules:
  - name: default
    queue_conditions:
      - check-success=preliminary-tests
    merge_conditions:
      - check-success=pre-merge-tests
```

Other ways CI can read queue context:
- `mergify ci queue-info`, from the git note.
- Scope-aware job selection through `Mergifyio/gha-mergify-ci`.
- The org also publishes `gha-mergify-merge-queue-labels-copier`, which copies PR labels onto draft PRs.

---

## 4. Priorities, multiple queues, scopes/partitions, freeze, pause, commands

### 4.1 Priorities
- `priority_rules` is top-level, with up to 50 rules. The first matching rule wins. With no match, the priority is `medium`.
- Values: `low` = 1000, `medium` = 2000, `high` = 3000, or any integer from 1 to 10000.
- `allow_checks_interruption` defaults to `false` since 2026-04-21.
  - `false`: a higher-priority PR is inserted just behind the cars that are already running.
  - `true`: running checks are preempted.

```yaml
priority_rules:
  - name: urgent
    conditions: [label = urgent]
    priority: high
    allow_checks_interruption: true
  - name: critical
    conditions: [label = critical]
    priority: 5000
```

### 4.2 Multiple queues and ordering
- A PR is routed to the first queue whose `queue_conditions` match, or to the queue named in the command.
- The implicit priority that came from queue declaration order was **removed 2025-06-30**. Only `priority_rules` set test and merge order.
- Batches are filled with PRs of the same queue rule, after priority is applied.

### 4.3 Queue modes and scopes (the monorepo story)
- **`merge_queue.mode`:**
  - `serial` (default): one cumulative pipeline.
  - `parallel` (2026-04-13): needs scopes. Batches that share a scope form a parent/child DAG; batches in disjoint scopes merge independently.
  - `isolated` (2026-06-02): every batch is independent and scopes are optional. Semantic conflicts between batches are possible.
  - Fast-forward is rejected in `parallel` and `isolated`.
- **`scopes.source`:**
  - `files`: named scopes with `include`/`exclude` globs.
  - `manual`: scopes pushed by external tooling. Options are `PUT /v1/repos/{o}/{r}/commits/{head_sha}/scopes` (application key with the `ci` scope), `mergify ci scopes-send`, or `gha-mergify-ci`. There are guides for Nx, Bazel, Pants and Turborepo.
  - Upload limits: 10,000 scopes per upload and 1024 characters per name.
- **`barrier_files`**, or `all_scopes: true` on a manual upload, turns a PR into a barrier. It is never batched or parallelized with others, and everything behind it waits.
- **Concurrency limits:** `capacities` sets per-scope caps (glob keys allowed) and `default_capacity` covers the rest. Both are sub-limits of `max_parallel_checks`, and both are rejected in serial mode.
- **Direct Merge** (2026-07-20):
  - A PR that is alone at the front of its lane merges with **no queue CI** if it is already up to date with the base, or if the base moved only by queue-recorded merges in scopes the PR doesn't touch.
  - It is not available with in-place checks, two-step CI, or `merge-batch`.
- **The `scope` condition attribute** (2026-04-09).

```yaml
merge_queue:
  mode: parallel
  max_parallel_checks: 5
scopes:
  source:
    files:
      api:      { include: [services/api/**/*] }
      frontend: { include: [apps/web/**/*] }
  barrier_files:
    include: [.github/workflows/**, Makefile]
  capacities:
    frontend: 2
queue_rules:
  - name: default
    batch_size: 3
    queue_conditions: [check-success = ci]
```

**Historical `partition_rules`** (added 2023-03-23, deprecated 2025-07-28, removed about 2025-10-31): independent "partitions" processed in parallel, each covering all queues, with membership by conditions such as `files~=^apps/projectA/` and a `queue-partition-name` attribute. A PR in several partitions had to satisfy all of them. My reconstruction of the exact syntax is **unverified**; the current docs no longer carry it.

### 4.4 Freeze and pause
- **Scheduled freezes** (part of Merge Protections):
  - Created from the dashboard, the CLI (`mergify freeze create --reason … --timezone … -c "base=main" -e "label=hotfix"`), or the API (`/repos/{o}/{r}/scheduled_freeze`).
  - They take a scope and exclusion conditions. Exclusions are ANDed.
  - With the queue enabled, the queue holds merges until the freeze ends. Without the queue, the "Mergify Merge Protections" check stays pending.
  - Recurring windows use a `schedule` condition in `merge_protections` instead.
  - **History:** the original "queue freeze" API (2022) was deprecated on 2024-11-21 in favor of Pause.
- **Pause:**
  - Dashboard button, `mergify queue pause --reason …` / `unpause`, or `PUT`/`DELETE /repos/{o}/{r}/merge-queue/pause` with body `{reason}`.
  - PRs can still enter the queue, but nothing is scheduled or merged, and running checks are cancelled where possible. In-place checks can't be cancelled.
  - The 2024 changelog mentions an `allow_checks_to_run` option, but the current OpenAPI request body has only `reason` (unverified / possibly removed).

```yaml
merge_protections:
  - name: Working hours only
    if: [base = main]
    success_conditions:
      - schedule = Mon-Fri 09:00-17:00[America/Chicago]
```

### 4.5 Commands
Current commands: `queue`, `dequeue`, `refresh`, `rebase`, `update`, `squash`, `backport`, `copy`.

- `requeue` is now an alias of `queue`.
- `unqueue` was renamed `dequeue` on 2023-11-13, and the alias was removed on 2026-05-05.
- The command must start the comment. Mergify acknowledges it with a 👍 reaction.

### 4.6 `commands_restrictions`
- Most commands default to `sender-permission >= write`.
- `update`, `rebase`, `squash` and `refresh` also allow the author, but only on fork PRs:

```yaml
commands_restrictions:
  rebase:
    conditions:
      - or:
          - sender-permission >= write
          - sender = {{author}}
  refresh:
    conditions: true   # anyone who can comment
```

---

## 5. Conditions grammar

**Form:** `[-][#]attribute [operator value]`.
- `-` negates the condition. Time-based attributes can't be negated.
- `#` takes the length of a list attribute.
- An attribute with no operator is a boolean test, for example `draft`.

**Operators:**

| Operator | Symbols |
|---|---|
| Equal | `=` or `:` |
| Not equal | `!=` or `≠` |
| Regex match | `~=` |
| Glob | `*=` (Unix-style path pattern) |
| Greater than | `>`, `>=` / `≥` |
| Less than | `<`, `<=` / `≤` |

- On list attributes, an operator is true if **any** element matches. The exception is `!=`, which is true only if no element matches.
- `schedule` supports only `=` and `!=`, for example `schedule = Mon-Fri 09:00-19:00[America/Vancouver]`. Overnight windows must be split with `or`.

**Combinators:** separate lines are an implicit AND. You can nest `and:`, `or:` and `not:` blocks.

```yaml
success_conditions:
  - not:
      or:
        - label = do-not-merge
        - draft
```

**Attributes (current, abridged):**
- **People and review:** `author`, `assignee`, `approved-reviews-by` (counts only reviewers with write+), `changes-requested-reviews-by`, `commented-reviews-by`, `dismissed-reviews-by`, `review-requested`, `review-threads-resolved` / `review-threads-unresolved`, `co-authors`, `sender`, `sender-permission`.
- **Branches and PR metadata:** `base`, `head`, `head-repo-full-name`, `from-fork`, `title`, `body`, `body-raw`, `label`, `milestone`, `number`, `draft`, `closed`, `merged`, `merged-by`, `merged-at`, `locked`, `conflict`, `linear-history`.
- **Repository:** `repository-full-name`, `repository-name`.
- **Files and lines:** `files`, `added-files`, `modified-files`, `removed-files`, `added-lines`, `deleted-lines`, `modified-lines`.
- **Commits:** `commits`, `#commits-behind`, `commits-unverified`, `updates`.
- **CI:** `check`, `check-success`, `check-failure` (which also includes cancelled, timed-out and action-required), `check-pending`, `check-neutral`, `check-skipped`, `check-stale`, `check-cancelled`, `check-timed-out`. These match both check runs and commit statuses. Since 2026-06, check conditions can be scoped to a specific GitHub App.
- **Deployments:** `deployment-success`, `deployment-failure`.
- **Queue state:** `queue-name`, `queue-position` (-1 if not queued), `queued-at`, `queue-merge-started-at`, `queue-dequeue-reason`, `scope`.
- **Time:** `created-at`, `updated-at`, `closed-at`, `current-datetime`, `schedule`.
- **Dependabot:** `dependabot-dependency-name`, `dependabot-dependency-type`, `dependabot-update-type`.
- **Other:** `depends-on`, `mergify-configuration-changed`.

**Injected-only attributes:** `github-review-approved`, `github-code-owner-review-satisfied`, `github-require-last-push-approval` and `github-require-review-from-specific-teams` are generated from rulesets. User configs may only reference `github-review-decision`.

**Evaluation (2022 engine; current behaviour is unverified):**
- A hand-written parser (`rules/parser.py`, operator tables, no external grammar library) builds a filter tree (`rules/filter.py`).
- The tree is evaluated against a lazily-loaded PR `Context`, with "live resolvers" that expand `@team` names to members.
- Time-based conditions are handled by a `delayed-refresh` sorted set in Redis. The engine re-evaluates a PR at the next time a schedule or timestamp boundary could flip the result.

---

## 6. Interaction with GitHub

**GitHub App permissions (current):**
- **Repository:**
  - Actions RW
  - Administration R (reads branch protection and rulesets)
  - Checks RW
  - Commit statuses R
  - Contents RW
  - Deployments RW
  - Issues RW (labels, comments, reactions)
  - Metadata R
  - Merge queues R
  - Pages RW (so Pages builds trigger after Mergify merges)
  - Pull requests RW
  - Workflows RW (needed to merge or update PRs that touch `.github/workflows`)
- **Organization:** Administration R (org rulesets), Members R (team resolution).
- **Account:** Email R.

**Webhook events subscribed** (from the self-hosted manual-install guide):
- branch protection configuration, branch protection rule
- check run, check suite
- deployment status
- issue comment
- member, membership
- merge group
- organization
- pull request, pull request review, pull request review comment, pull request review thread
- push
- repository, repository ruleset
- status
- team, team add
- workflow job, workflow run

The webhook endpoint is `POST /event`, verified with an HMAC secret.

**Branch protection and rulesets:**
- Supported rules are injected as conditions; a required review becomes `#approved-reviews-by >= 1`, for example.
- The recommended setup is Mergify as a **bypass actor with `exempt`** mode on every ruleset covering queued branches:
  - `pull_requests_only` doesn't cover the raw ref operations on queue branches.
  - `exempt` is required for GitHub-native stacks.
- Leave GitHub's native `merge_queue` rule **off**; it is incompatible.
- **"Require branches to be up to date"** is incompatible with batch-PR checks, because GitHub enforces it on the original PRs. To resolve it, disable the setting, use an exempt bypass, use fast-forward, or use in-place checks.
- Rules that apply to queue branches: `creation`, `update` and `branch_name_pattern` are checked when the queue branch is created, and `deletion` when it is cleaned up. Ignored rule types include `required_deployments`, `required_linear_history`, `non_fast_forward`, the pattern and file rules, `workflows` and `code_scanning`.
- **Reviews:** Mergify uses GraphQL `reviewDecision` when the required approval count is ≥ 1. At 0 it rebuilds the gate itself. Last-push approval is **not** enforced at 0 approvals.
- **Native merge queue:** Mergify does **not** use GitHub's native merge queue. It is entirely its own draft-PR system.

**How required checks are satisfied for temp branches.** Mergify doesn't make the batch PR's checks count for the original PR. Instead:
1. With injection mode `queue`, the required checks must already pass on the original PR to queue it.
2. The batch PR must satisfy `merge_conditions`.
3. The original is then merged through the GitHub API, which GitHub accepts because its own required checks are green and "up to date" is off.

With `merge-batch` or `fast-forward`, the tested head is what lands. With injection mode `merge`, GitHub refuses the merge unless Mergify is a bypass actor.

**Rate limits:**
- **2022 engine:** if `X-RateLimit-Remaining < 20`, it raises `RateLimited` and reschedules the whole org bucket at `X-RateLimit-Reset`. It honours `Retry-After`. Other failures get exponential backoff of `2**min(attempts,3) * backoff`. Per-org serialization exists specifically to avoid secondary or abuse limits.
- **Current approach:** not documented (unverified).

---

## 7. Internal architecture and open-source history

**Repository history:**
- `Mergifyio/mergify-engine` was created 2018-04-18 and now redirects to `Mergifyio/mergify`, the "Mergify Community Issue Tracker".
- Engine code was public until commit `fe6e586630` (2022-07-08). Commit `3f0cf5476c` (2022-07-11, "explain this is the community issue tracker") removed about 300 files.
- `LICENSE` was Apache-2.0 from the first commit until that removal, and the pyproject classifier says Apache, so the code went closed simply by being removed.
- I found no evidence of an AGPL or other relicensing (unverified rumors aside).
- Today's public org repos are peripheral: `mergify-cli`, `gha-mergify-ci`, `setup-cli`, the Terraform provider, `merge-queue-academy`, and others.

**Stack in 2022:**
- Python 3.10, asyncio.
- FastAPI + uvicorn/gunicorn web process (webhook intake, REST API, badges, simulator).
- `mergify-engine-worker` processes.
- Redis (hiredis, Lua scripts, msgpack), httpx with HTTP/2, voluptuous config schemas, tenacity retries, Datadog/ddtrace, Sentry.
- Procfile: `web` and `worker`.
- **Event pipeline:**
  - Each event is slimmed and appended to a Redis **stream per (repo, PR)**: `bucket-sources~{repo_id}~{pull_number|0}`.
  - That stream is indexed by a sorted set per org (`bucket~{owner_id}`), which is in turn listed in a global `streams` sorted set scored by timestamp.
  - Org buckets are sharded to worker tasks by `blake2s(owner_id) % (processes × tasks_per_process)`, so one org is processed by one task at a time.
  - Paying orgs could get "dedicated workers".
  - Events for a PR are processed in batches and only removed after success, with retries and per-bucket attempt counters.
  - This replaced Flask + Celery in June 2020, which cut API calls by about 4x (about 20x for large orgs).
- **Queue state:** a "merge train" per (repo, base branch), stored as a Redis hash `merge-trains~{owner_id}` keyed by `{repo_id}~{ref}`. A train holds waiting `EmbarkedPull`s and `TrainCar`s, where each car is one speculative batch and records its parent PRs, still-queued PRs, draft PR number, creation state (`created` or `updated` for in-place), and checks outcome.
  - `queue/merge_train.py` was about 94 KB and included split/bisection logic (`_split_failed_batches`).
  - Draft PR description edits made by Mergify itself are filtered out of the event stream.
- **Plugin system:** actions and signals were Python entry points (`mergify_actions`: `queue`, `merge`, `rebase`, …).

**Today:**
- The self-hosted docs describe a **single container** (port 5000, behind a TLS proxy) plus **PostgreSQL ≥ 15**, which now holds the persistent state including merge queue state, and **Redis ≥ 7.4** with AOF, for queued events, cache and outage recovery.
- Git operations use temporary clones on disk (about 10 GB of scratch space).
- It supports GitHub.com and GHES ≥ 3.14, and is GitHub-only.

---

## 8. Takeaways for OpenMerge
- The 2022 Apache-2.0 snapshot is a legitimate reference for:
  - per-org sharded Redis stream ingestion
  - the train/car model
  - the draft-PR build via the merges API
  - condition parsing
- What it lacks and you'd design fresh: modes and scopes, dynamic batching, automatic timeouts, Direct Merge, git-notes metadata, and `merge-batch`.
- Compatibility surface worth matching:
  - the `.mergify.yml` lookup order and key names
  - the `mergify/merge-queue/` prefix, so users' existing two-step CI `if:` filters keep working
  - the `pull_request`-on-draft-PR trigger model, which works with any CI that reports to GitHub
  - the condition grammar

---

## Sources
- https://docs.mergify.com/llms.txt
- https://docs.mergify.com/configuration/file-format (.md)
- https://docs.mergify.com/configuration/conditions
- https://docs.mergify.com/configuration/data-types
- https://docs.mergify.com/configuration/sharing
- https://docs.mergify.com/merge-queue
- https://docs.mergify.com/merge-queue/rules
- https://docs.mergify.com/merge-queue/lifecycle
- https://docs.mergify.com/merge-queue/queue-modes
- https://docs.mergify.com/merge-queue/performance
- https://docs.mergify.com/merge-queue/batches
- https://docs.mergify.com/merge-queue/two-step
- https://docs.mergify.com/merge-queue/merge-strategies
- https://docs.mergify.com/merge-queue/priority
- https://docs.mergify.com/merge-queue/scopes
- https://docs.mergify.com/merge-queue/monorepo
- https://docs.mergify.com/merge-queue/direct-merge
- https://docs.mergify.com/merge-queue/stacks
- https://docs.mergify.com/merge-queue/pause
- https://docs.mergify.com/merge-queue/github-rulesets
- https://docs.mergify.com/merge-protections/freeze
- https://docs.mergify.com/merge-protections/auto-merge
- https://docs.mergify.com/commands
- https://docs.mergify.com/commands/queue
- https://docs.mergify.com/commands/restrictions
- https://docs.mergify.com/workflow/actions/queue
- https://docs.mergify.com/security
- https://docs.mergify.com/integrations/github
- https://docs.mergify.com/integrations/ci-status-checks
- https://docs.mergify.com/migrate/github-merge-queue
- https://docs.mergify.com/enterprise/requirements
- https://docs.mergify.com/enterprise/manual-installation-legacy
- https://docs.mergify.com/openapi.json
- Changelog entries under https://docs.mergify.com/changelog/:
  - 2023-03-23-add-partition-rules
  - 2024-07-26-queue-action-options-deprecation
  - 2024-08-26-priority-rules-is-now-at-the-top-level-of-the-mergify-config
  - 2024-09-23-add-max-parallel-checks
  - 2024-11-21-merge-queue-freeze-api-is-deprecated
  - 2025-06-02-max-parallel-checks-is-now-5-by-default
  - 2025-06-30-remove-deprecated-implicit-priority-between-queue-rules
  - 2025-07-28-partition-rules-are-now-deprecated
  - 2025-09-30-allow-inplace-checks-has-been-deprecated
  - 2026-02-18-the-requeue-command-is-deprecated
  - 2026-04-13-parallel-scopes
  - 2026-04-21-unified-mergify-merge-queue-check-run
  - 2026-04-21-check-interruption-disabled-by-default-for-priority-rules
  - 2026-05-05-unqueue-command-alias-removed
  - 2026-05-06-conditions-in-queue-rules-is-deprecated
  - 2026-05-06-routing-conditions-in-queue-rules-is-deprecated
  - 2026-06-02-isolated-merge-queue-mode
  - 2026-08-25-clearer-reason-when-the-merge-queue-cant-create-the-draft-pull-request
  - 2026-09-29-the-merge-queue-no-longer-rebases-fork-pull-requests-on-behalf-of-an-account
  - 2026-10-13-batch-pull-request-description-updated
- https://github.com/Mergifyio/mergify (formerly mergify-engine); code snapshot at commit fe6e586630; removal commit 3f0cf5476c
- https://github.com/Mergifyio (org repo listing)
- https://articles.mergify.com/handling-300k-github-events-per-day/
- https://articles.mergify.com/announcing-our-monorepo-feature-partition-rules/
- https://doc.mergify.io/integrations/rush (historical partition_rules usage)
