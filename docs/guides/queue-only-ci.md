# Queue-only full CI

Run a light suite on every pull request and the full required suite only on
the commit the queue actually tests. Each PR push then costs light-suite
minutes, and the full suite runs once per queue build instead of once per push.
This is the queue-only full CI lever in
[ADR 0013](../adr/0013-ci-cost-executors.md#apply-the-savings-stack-in-order-of-leverage).

This guide is documentation only. It changes no OpenMerge behavior and adds no
configuration keys: everything below works with the existing `merge_when`,
`enter_when` and [queue CI provenance](../operating.md#queue-ci-provenance)
rules. OpenMerge's own repository keeps running `check` and `sim-exits` on both
PRs and queue pushes until the ADR 0009 period-B drain is recorded.

## The model

```text
PR push ──▶ pull_request event ──▶ pr-light   (advisory, or enter_when admission)
                                        │
                     @openmerge queue   ▼
queue build ──▶ push openmerge/queue/<base>/<batch> ──▶ queue-full (required)
                                        │
                                        ▼
               merge_when: check-success = queue-full ──▶ App lands the tested commit
```

| Check | Trigger | Runs on | Role |
|---|---|---|---|
| `pr-light` | `pull_request` | The PR head | Fast feedback. Advisory, or required at admission through `enter_when`. Never required by the ruleset or `merge_when`. |
| `queue-full` | `push` to `openmerge/queue/**` | The queue's tested SHA | The full suite. The only check the ruleset and `merge_when` require. |

The names are examples. Use any stable names, but keep the light and full names
distinct. Never rename a light job to satisfy a full-suite requirement.

## Which check names the ruleset may require

**Only queue-tested names.** In forge-authority mode (the default, or
`policy_authority: github`), the branch ruleset may require only check names
that run on pushes to `openmerge/queue/**`, such as `queue-full`. For a matrix,
every required member must run on the exact tested commit; the aggregator job
below reduces that to one name.

**Never require `pr-light` in the ruleset or in `merge_when`.** Every merge
method lands a commit that `pr-light` never ran on. Merge, squash and rebase
compose a new commit; fast-forward tests an empty marker commit above the head.
`pr-light` ran on the PR head, a different SHA. OpenMerge reads `merge_when`
checks on the tested SHA and binds them to the queue push suite's exact
`head_branch`, so a PR-side `pr-light` result never counts. A required
`pr-light` therefore either stays missing until `check_timeout` dequeues the
batch, or, in the one-workflow layout, appears as skipped. OpenMerge treats
skipped as failure. Either way nothing lands.

To make `pr-light` a gate, require it at admission. `enter_when` reads the PR
head, which is exactly where `pr-light` runs:

```yaml
version: 1
queues:
  - name: default
    enter_when:
      - "-draft"
      - check-success = pr-light
    merge_when:
      - check-success = queue-full
```

To keep it purely advisory, leave it out of policy entirely:

```yaml
version: 1
queues:
  - name: default
    merge_when:
      - check-success = queue-full
```

In OpenMerge-authority mode (`policy_authority: openmerge`), the same
`merge_when` carries the full-suite requirement, imported under ADR 0008's
[protection import rules](../operating.md#auditing-protection-before-a-policy-authority-switch).
Document each check's workflow, job, trigger and policy name. The import report
counts a check only when a producing job runs on queue pushes. It reports
path filters, reusable-workflow callers and job `if:` conditions other than
absent or literal `true` as unknown. Unknown producers block landing until
you resolve them.

## Why the PR stays mergeable only through the queue

The PR head never receives a `queue-full` result, because the full workflow
does not run on `pull_request`. With the two-workflow layout, GitHub shows the
required `queue-full` check as *Expected* on the PR forever, so the merge button
stays blocked for everyone without a bypass. That includes administrators,
unless you also give them a bypass.

The OpenMerge App has an **always** Integration bypass on the ruleset. It
pushes the tested commit to the base after `merge_when` passes on that
commit. Manual merges are blocked, and the only path to the base is the commit
the queue tested.

The one-workflow layout weakens this. A job skipped by `if:` still produces a
check run on the PR head, and GitHub
[reports a skipped job as "Success"](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-jobs-with-conditions):
"It will not prevent a pull request from merging, even if it is a required
check." The PR then shows `queue-full` as passed (skipped), and the merge
button opens. OpenMerge itself still refuses to count the skip, so queue
landings stay correct; only the manual-merge block is lost. Use the
two-workflow layout in forge-authority mode, or block direct base updates
another way. ADR 0008's OpenMerge-authority write restrictions are one example.

## Workflow examples

All examples live in [`docs/examples/queue-only-ci/`](../examples/queue-only-ci/)
and pass `actionlint`. Copy them into your repository's `.github/workflows/`.
Replace the `make` targets with your own commands.

### (b) Two workflow files (recommended)

[`two-workflows/pr-light.yml`](../examples/queue-only-ci/two-workflows/pr-light.yml):

```yaml
name: pr-light

on:
  pull_request:

permissions:
  contents: read

concurrency:
  group: pr-light-${{ github.event.pull_request.number }}
  cancel-in-progress: true

jobs:
  pr-light:
    name: pr-light
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - run: make lint test-unit
```

[`two-workflows/queue-full.yml`](../examples/queue-only-ci/two-workflows/queue-full.yml)
shards the suite across a matrix and exposes one stable required name:

```yaml
name: queue-full

on:
  push:
    branches: ["openmerge/queue/**"]

permissions:
  contents: read

jobs:
  test:
    name: test (${{ matrix.shard }})
    runs-on: ubuntu-latest
    timeout-minutes: 45
    strategy:
      fail-fast: false
      matrix:
        shard: [1, 2, 3]
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - run: make test-full SHARD=${{ matrix.shard }} SHARDS=3

  queue-full:
    name: queue-full
    needs: [test]
    if: ${{ always() }}
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - name: Require every shard to succeed
        env:
          RESULT: ${{ needs.test.result }}
        run: |
          echo "test matrix result: $RESULT"
          test "$RESULT" = success
```

`needs.test.result` is `success` only when every shard succeeded, so a
failed, cancelled or skipped shard fails `queue-full`. `if: always()` keeps the
aggregator from being skipped when a shard fails, because a skipped required
check would read as success on GitHub.

### (a) One workflow file with job-level `if:`

[`single-workflow/ci.yml`](../examples/queue-only-ci/single-workflow/ci.yml).
Read [the skipped-job caveat](#why-the-pr-stays-mergeable-only-through-the-queue)
first.

```yaml
name: CI

on:
  pull_request:
  push:
    branches: ["openmerge/queue/**"]

permissions:
  contents: read

concurrency:
  group: ci-${{ github.event_name == 'pull_request' && format('pr-{0}', github.event.pull_request.number) || github.sha }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}

jobs:
  pr-light:
    name: pr-light
    if: ${{ github.event_name == 'pull_request' }}
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - run: make lint test-unit

  queue-full:
    name: queue-full
    if: ${{ github.event_name == 'push' && startsWith(github.ref, 'refs/heads/openmerge/queue/') }}
    runs-on: ubuntu-latest
    timeout-minutes: 45
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - run: make test-full
```

Adding `main` to the push branches re-runs the full suite after landing. That
gives post-landing evidence at the cost of one extra full run per landing;
omit it if you don't need it.

## Matching `.openmerge.yml`

[`docs/examples/queue-only-ci/.openmerge.yml`](../examples/queue-only-ci/.openmerge.yml)
goes at your repository root, on the default branch. Policy is read from there,
so a change takes effect after it lands.

```yaml
version: 1
queues:
  - name: default
    enter_when:
      - "-draft"
      - check-success = pr-light
    merge_when:
      - check-success = queue-full
    merge_method: squash
    check_timeout: 1h
    batch:
      max_size: 4
      max_wait: 2m
```

Check it locally with `openmerge config validate .openmerge.yml`. The loader
rejects a `merge_when` that could pass with missing or failed checks, but it
cannot know which workflow produces a name. Getting the producer right is
your job.

## Matching ruleset

Create one branch ruleset on the default branch (**Settings → Rules →
Rulesets**):

| Setting | Value |
|---|---|
| Enforcement | Active |
| Target | Default branch |
| Bypass list | The OpenMerge App, mode **Always** |
| Require status checks to pass | `queue-full` only, source **GitHub Actions** |
| Require branches to be up to date | Off. OpenMerge's own verification mode governs freshness. |
| Block force pushes, restrict deletions | On |

Never add `pr-light`, or any job that runs only on `pull_request`, to the
required list. Optional PR rules, such as required reviews, stay as they are.
The App's bypass covers them for queue landings.

The same ruleset as a REST payload is in
[`ruleset.json`](../examples/queue-only-ci/ruleset.json). Replace `123456` with
your App's numeric ID. `15368` is GitHub Actions' integration ID, which pins
`queue-full` to that producer.

```bash
gh api -X POST repos/OWNER/REPO/rulesets --input docs/examples/queue-only-ci/ruleset.json
```

Then run `openmerge setup rulesets` (see the [README](../../README.md)) to
audit the bypass and create the `openmerge-fence` ruleset. `--apply` adds the
App's always bypass to existing default-branch rulesets if you skipped it.
Personal-account repositories can reject an Integration bypass actor. If yours
does, follow the README's guidance rather than substituting a broader bypass.
Classic branch protection with required status checks is rejected at runtime;
use a ruleset.

## Worked example: minutes saved

Let:

- `N` = PRs landed per day
- `P` = CI-triggering pushes per PR (initial push plus fix-ups)
- `F` = full-suite minutes per run (sum across matrix jobs)
- `L` = light-suite minutes per run
- `Q` = queue builds per day, roughly `N / B × r`, where `B` is the average
  batch size and `r` counts extra builds from retries, bisection and
  invalidated speculation (`r ≥ 1`)

| Setup | Minutes per day |
|---|---|
| Full CI on every PR push, no queue | `N × P × F` |
| Full CI on PR pushes **and** queue builds | `N × P × F + Q × F` |
| Queue-only full CI | `N × P × L + Q × F` |

Savings against full-CI-on-every-push without a queue:

```text
saved = N × P × (F − L) − Q × F
```

Against a setup that already runs the full suite on queue builds, the queue
term cancels: `saved = N × P × (F − L)`.

Illustrative numbers. These are hypothetical, not OpenMerge measurements.
Take N = 40, P = 3, F = 20, L = 3, B = 4, r = 1.5:

```text
Q      = 40 / 4 × 1.5          = 15 queue builds/day
before = 40 × 3 × 20           = 2,400 min/day
after  = 40 × 3 × 3 + 15 × 20  =   660 min/day
saved  = 2,400 − 660           = 1,740 min/day (72.5%)
```

Over 22 working days that is 38,280 minutes. At GitHub's $0.006/min list
price for 2-core Linux ([runner pricing](https://docs.github.com/en/billing/reference/actions-runner-pricing),
as recorded in ADR 0013) that is about $230/month.

The lever needs repeated pushes or batching to pay off. With `P = 1`, `B = 1`
and `r = 1`, "after" is `N × (L + F)`, which costs more than `N × F`. Measure
your own `P`, `B` and `r` before promising savings, and count failed,
retried and cancelled queue builds in `r`.

## Pitfalls

- **A flaky full suite now hits the queue only.** PR authors no longer see
  full-suite flakes, but each flake fails a queue build, triggers bisection
  and raises `r`. Fix flakes or enable one same-commit retry with
  `checks.retry_failed: 1` (see [queue CI provenance](../operating.md#queue-ci-provenance)).
  Expect some failures to show up first in the queue. Keep `pr-light` broad
  enough to catch the common ones.
- **Path filters.** A workflow skipped by `paths:`, `paths-ignore:` or
  branch filtering produces no check run. A required `queue-full` then never
  arrives, and the batch waits until `check_timeout`. Never put path filters on
  the queue workflow. To skip work, filter inside the job and still report a
  real result. Path filters on `pr-light` are fine while it is not required.
- **Required checks that never run.** A required name nothing produces on
  queue pushes looks like slow CI, not an error. Common causes: a typo, a
  renamed job, a matrix member name, or a workflow that triggers only on
  `pull_request`. If runs exist on the tested SHA but are not on the queue
  push suite, `why` and the PR check report
  [`unbound-check-provenance`](../operating.md#remedy-for-unbound-check-provenance).
  Before you require a name, confirm it appears on an `openmerge/queue/...` run.
- **Matrix jobs.** Each member reports its own check, such as `test (1)`, and
  adding a shard changes the set. Require one aggregator, like `queue-full`
  above, with `if: always()` and an explicit result test, not individual
  members. A plain `needs:` job is skipped when a member fails, and GitHub
  counts a skipped required check as passing.
- **Reusable workflows.** A job that calls a reusable workflow reports checks
  named `<caller job> / <called job>`. The required name must be that full
  string. The ADR 0008 import report treats reusable callers as unknown
  producers, so in OpenMerge-authority mode verify them by hand.
- **`concurrency` on queue refs.** `cancel-in-progress` must never cancel one
  queue build because another started. A group keyed on `github.workflow`,
  the base branch, or anything shared across batches cancels speculative
  builds for other batches. Their required check then fails or never completes.
  Give queue runs no concurrency group, or one unique per SHA, and enable
  cancellation only for `pull_request` runs. OpenMerge cancels invalidated
  speculative builds itself, so you don't need workflow-level cancellation.
  A cancelled run never counts as success.
- **Skipped jobs pass on GitHub.** This is the one-workflow layout's flaw from
  above. Any required job that can be skipped, through `if:` or a failed
  `needs:` dependency, can open the merge button. OpenMerge counts skipped as
  failure, so this weakens only manual-merge protection.
- **Secrets and trust.** A branch prefix is not a security boundary. Don't
  grant extra secrets or permissions just because a ref starts with
  `openmerge/`; see ADR 0013.

## Migrating from full CI on every PR

Apply these in order. Each step lands through the queue.

1. **Inventory.** List the check names your ruleset requires and the names in
   `merge_when`. Run `openmerge setup rulesets` without `--apply` to audit the
   bypass. Confirm the current full workflow already runs on
   `openmerge/queue/**` pushes; OpenMerge needs that today.
2. **Add the new workflows alongside the old one.** Land `queue-full.yml`, and
   `pr-light.yml` if the light suite is new, while the existing full workflow
   still runs on PRs. Nothing is required yet.
3. **Verify queue coverage.** For the next few queue builds, confirm that
   `queue-full` appears on the tested SHA from the `openmerge/queue/...` push,
   and that neither the PR check nor `why` reports `unbound-check-provenance`.
4. **Switch `merge_when`.** Land `merge_when: [check-success = queue-full]`,
   plus `enter_when` with `pr-light` if wanted. The PR that makes this change
   is tested under the old policy; the new policy applies after it lands.
5. **Switch the ruleset.** Add `queue-full` to the required status checks and
   remove the old full-suite names. Keep the App's always bypass, and never
   add `pr-light`.
6. **Drop the full suite from PRs.** Remove the `pull_request` trigger from
   the old full workflow, or delete it once `queue-full` covers everything. This
   is the step that saves minutes.
7. **Watch.** Track queue failures, flake retries and time-to-land for a
   week or two, and compare minutes against the formula above. To roll back,
   re-add the `pull_request` trigger. Policy can stay on `queue-full`, because
   the full suite still runs on queue builds.
