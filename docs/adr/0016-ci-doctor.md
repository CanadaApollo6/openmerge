# 0016. Report where each repository's queue CI time goes

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

[ADR 0015](0015-ci-efficiency-engine.md) saves CI work by changing what the
queue runs. Much of a repository's CI time is lost inside the workflow
instead: jobs that start with a cold cache, one CPU-bound job that runs on a
single runner, setup repeated in every job, and retries of flaky jobs instead
of fixes. OpenMerge cannot change a user's workflow files. It can see where
the time goes, because it already records every queue CI run.

OpenMerge's own CI shows the pattern. An audit of 60 runs on 2026-11-02 found
that the required `check` job took a median of 1424s, of which `make test`
took 1292s. The test packages need 4002s of CPU time in total, so the job was
CPU-bound on one 4-vCPU runner. Queue branches (`openmerge/queue/**`) could
not read Go caches saved by pull requests, because GitHub scopes caches by
branch and only main's caches are readable everywhere. PR #217 sharded the
tests across four runners, seeded the cache from main only, and added an
aggregate `check` job that fails when any shard is skipped. Total CI time
fell from about 23.7 minutes to about 13.7. `sim-exits` (13m43s) is now the
longest job. None of these findings needed private knowledge. All of them can
be computed from data that GitHub returns for every Actions job.

Most of OpenMerge's future users run GitHub Actions. A report that finds the
same problems in their workflows saves their CI time without trusting a cache,
skipping a test, or changing a guarantee.

## Decision

### Scope

Add a read-only **CI doctor** report:
`openmerge report ci-doctor --installation <id> --repository owner/name --since <RFC3339>`.
It shares the flags, journal reader, and JSON contract versioning of
`openmerge report savings`. Later, the status API and the hosted dashboard
([ADR 0012](0012-hosted-service-architecture.md)) can show the same report.

CI doctor is a **T0 diagnostic** in ADR 0015's terms:

- It never changes a plan, a schedule, a cancellation, a retry, a check
  result, or a landing decision.
- No other component reads its output.
- It never writes to the forge or to the journal.

### Inputs

1. **The journal.** It identifies queue CI runs: the tested SHA, the queue
   ref, the check identities, retries, and the outcome.
2. **The GitHub Actions jobs API** for those workflow run IDs only: jobs,
   steps, `created_at`, `started_at`, `completed_at`, conclusion, runner
   labels, and `needs`. This needs **`actions: read`**. The App already
   requests `actions: write` for failed-job reruns (`internal/forge/github/setup.go`),
   and that grant includes read.

CI doctor does not read job logs or artifacts. Logs can contain secrets, and
step timings give enough evidence for every finding below. Runs that are not
queue CI runs are out of scope. The one exception is the repository's own
default-branch runs, used as the warm baseline for finding 2.

### Findings

Each finding has a stable code, the evidence that triggered it, the minutes
at stake, and a fix written for the workflow author.

| Code | What it detects | Evidence | Suggested fix |
|---|---|---|---|
| `time-split` | How queue CI time divides into runner wait, setup, and run | The time from OpenMerge pushing the queue test ref to the job's `created_at` is dispatch delay. Job `created_at` to `started_at` is wait. Steps before the first step that is not setup (checkout, `setup-*`, cache restore, dependency install) are setup. The remaining steps are run. | None. This is context for the other findings. |
| `cold-setup` | Queue jobs whose setup is much slower than the same job on the default branch | Median setup on queue runs is more than 2× the default-branch median for the same job name, over at least 10 runs | Save caches from the default branch only, so queue branches can restore them, and key the caches on the lockfiles |
| `long-pole` | A job that sets the run's duration while the other jobs sit idle | The job is on the critical path in at least 80% of runs, and the median idle time of its sibling jobs is more than half of its own run time | Shard the job, give it a larger runner, or split its slowest step |
| `serial-step` | One step that is most of a long job | One step is more than 70% of a job that takes more than 10 minutes | Split or parallelize the step. If it is a test suite, shard it by package or by historical duration. |
| `checkout-cost` | Every job in a run fetches the same commit, and the fetch is a large part of setup | Summed checkout-step time per run is more than 10% of the run's runner time, or one checkout takes more than 60s | Fetch only what the job needs: `fetch-depth: 1` or a blobless partial clone (`filter: blob:none`), and sparse checkout for jobs that read one directory |
| `masked-skip` | A required aggregate check that passes while a job it depends on was skipped or cancelled | The check run's conclusion is success, and a job it `needs` in the same workflow run concluded `skipped` or `cancelled` | Make the aggregate job fail unless every `needs.*.result` is `success` |
| `retry-habit` | A check that is rerun often instead of fixed | Same-tree reruns, from the journal's retry records, exceed 5% of a check's runs over the period, with the minutes they cost | Fix or quarantine the flaky test. Use ADR 0015's targeted reruns, not whole-workflow retries. |
| `duplicate-pr-run` | Pull request CI that is still running when the same composition is in the queue | Pull request and queue runs overlap in time for the same head SHA | Cancel superseded pull request runs with a `concurrency` group, and never cancel queue runs |

The thresholds above are the initial values. Each threshold is a named
constant, and the report prints the values it used. Evaluation can change
them.

### Accuracy rules

- **Advice, not proof.** CI doctor reports what step timings show. It says
  "setup is 3.1× slower on queue runs than on main", not "your cache is
  cold". A finding is never passing or failing evidence.
- **Fail silent on gaps.** A run with missing or ambiguous data produces no
  finding. Examples are deleted runs, expired retention, missing permission,
  and renamed jobs. The report counts and reports the gaps, and it never
  extrapolates over them.
- **Minimum sample.** A finding needs at least 10 qualifying runs in the
  period. Below that minimum, the report prints the data and no finding.
- **Minutes at stake** are the measured excess over the stated comparison
  (the default-branch median, or the sibling idle time), never a projected
  saving.

### Privacy and tenancy

The report covers one repository at a time, and only a caller who can already
read that repository's queue status can see it. It inherits ADR 0011
noninterference: no signal from another repository changes a finding. CI
doctor stores nothing outside the existing journal. It fetches Actions data
on demand, and the hosted service caches the data only inside the
repository's tenant partition.

### Rollout

| Slice | Exit evidence | Deploy class |
|---|---|---|
| This ADR and the index | Docs checks | Inert |
| Report from recorded fixtures | Golden reports for each finding from recorded jobs-API responses; gap and minimum-sample cases | Inert (CLI only, the server does not run it) |
| Live Actions reader | The fake forge returns jobs; a missing `actions: read` produces a labelled gap, not an error; rate-limit budget test | Inert |
| Status API and dashboard surface | Authorization test per ADR 0012 tenancy | Hold |

OpenMerge's own repository is the first evaluation set. The report on queue
runs from before PR #217 must find `cold-setup`, `long-pole`, and
`serial-step`, and the report on runs after it must find none of them.
Every finding on design-partner repositories must cite the workflow run IDs
behind it, so the partner can check it.

### Owner decision (2026-11-03)

Riel St. Amand (`CanadaApollo6`) accepted this ADR on 2026-11-03. Acceptance
changes no live service: each slice keeps the deploy class in the rollout
table, and the Hold slice still needs its own approval.

### Evaluation correction (2026-11-03)

Two statements above were wrong, and the decision does not depend on either.
Before PR #217 the workflow had no Go build cache at all (`actions/setup-go`
ran with `cache: false`, and no job used `actions/cache`). Queue and main runs
were therefore equally cold: recorded `check` setup was a median of 26 seconds
on both. The Context's claim that queue branches could not read caches saved by
pull requests described a problem that did not exist yet: #217 added the first
Go cache and saved it from main so that it would not arise. The evaluation's
expectation that pre-#217 runs produce `cold-setup` is therefore withdrawn.
Pre-#217 runs must still produce `long-pole` and `serial-step`. The first
recorded report ([PR #222](https://github.com/CanadaApollo6/openmerge/pull/222))
keeps the `cold-setup` threshold unchanged rather than lowering it to match the
expectation.

## Consequences

Users get specific, checkable advice on their own workflows, which saves CI
time that OpenMerge's queue changes cannot reach. The report gives design
partners a measured case for fixes they can make themselves before they pay
for executors ([ADR 0013](0013-ci-cost-executors.md)).

We now owe:

- A jobs-API reader with pagination and a rate budget.
- A setup-step classifier. It is a heuristic, so it needs fixtures for common
  setup actions and must label each step it cannot classify.
- Thresholds that are documented and versioned, together with the JSON
  contract.

The setup classifier can misclassify steps. The report shows its step
classification so that the user can see why a finding was triggered. The
report covers only GitHub Actions. Other CI systems get `time-split` only,
from check run timestamps, until a driver supplies job data.

## Alternatives considered

- **Reading logs** for cache-hit lines and CPU metrics gives more precise
  evidence. It loses because logs can contain secrets, and reading them would
  widen the data OpenMerge handles.
- **Automatic workflow fixes** (opening PRs that change `.github/workflows`)
  save the user effort but change their CI contract without review. Keep the
  report advisory. A later ADR can propose fix PRs as an opt-in.
- **Feeding findings into scheduling** (for example, ordering shards by
  `long-pole` data) belongs to ADR 0015's fail-fast ordering, under its
  evidence contract. CI doctor stays read-only so that it cannot affect
  correctness.
- **Serving queue test refs from OpenMerge's own Git cache** (as OpenAI's
  Pando does for its monorepo, reusing stored packfiles across the many
  jobs that fetch one commit) would cut fetch cost at the source. It loses
  for now because CI runners fetch from the forge, which OpenMerge does not
  run. `checkout-cost` gets most of the saving with a workflow change.
- **Using a third-party CI analytics product** works for one repository but
  cannot connect a job to the queue composition it tested. OpenMerge already
  holds that link.
