# CI doctor report

`openmerge report ci-doctor` reports where one queue's CI time goes and suggests
workflow changes from recorded Actions job and step timings. It implements the
recorded-data slice of [ADR 0016](adr/0016-ci-doctor.md), deploy class **Inert**.
It is an ADR 0015 T0 diagnostic. No planner, server, retry, cancellation, check
result, or landing decision reads this report.

The command reads the journal with the same restricted, bounded reader as
[report savings](savings-report.md). It performs no migrations, writes, GitHub
requests, log reads, or artifact reads. The Actions reader is a later slice.

```sh
openmerge report ci-doctor \
  --installation 12345 --repository acme/api \
  --since 2026-10-01T00:00:00Z --until 2026-11-01T00:00:00Z \
  --actions-file actions.json \
  [--database-url "$OPENMERGE_DATABASE_URL"] \
  [--base main] [--lane LANE] [--timeout 2m] [--json]
```

`--installation`, `--repository`, `--base`, and `--lane` select the exact journal
queue key. `--since` is inclusive and required. `--until` is exclusive, defaults
to now, and cannot be in the future. `--actions-file` is required. The file is
limited to 64 MiB. `--full-suite-minutes` belongs to savings and is rejected here.
Exit codes are `0` for success, `2` for usage, and `1` for read or computation
failure. Flag errors never echo arguments. Database errors redact credentials.

## Inputs and identity

The journal is replayed through `queue.Apply`. Test-ref push records identify the
tested SHA and queue branch, including pushes that completed after invalidation.
A run must match both. When the journal records identified workflow runs, the
Actions run ID must match those observations too. Repeated push confirmations
count once. Ambiguous identities become gaps.

This report selects pushes **recorded** in `[since, until)`. Unlike savings,
there is no landing-based carry-in. Only journal events recorded before `until`
are replayed. Jobs must have completed before `until`. Default-branch baselines
must have been created within the window. The input file is part of the evidence;
keep it when saving a report so the same closed window can be reproduced.

The JSON input has `version: 1`, `repository`, `default_branch`, and `runs`.
`Run`, `Job`, `Step`, and `Graph` in
[`internal/cidoctor/types.go`](../internal/cidoctor/types.go) define the fields.
The trimmed [OpenMerge recording](../internal/cidoctor/testdata/openmerge-after.actions.json)
is a complete example. Times are RFC 3339, durations are seconds, and minutes
are floating point.

Each run records the workflow run ID, workflow ID and path, attempt, event,
head SHA and branch, creation time, and every job from that attempt. Jobs record
their ID, display name, attempt, creation/start/completion times, conclusion,
runner labels, and numbered steps with names, conclusions, and timestamps.
`jobs_complete: true` attests that all pages were recorded. Missing pagination
or job identities suppress the run. No job names or runner identities are guessed.

Queue runs are the subject of the report. Default-branch runs are the warm
comparison. Same-SHA, same-workflow `pull_request` runs are comparison evidence
for `duplicate-pr-run` only. Unrelated runs cannot supply queue findings.

The jobs API does **not** include `needs` or required-check policy. The optional
`graph` records both at the run's head SHA. `head_sha` must match the run,
`source` identifies the workflow and policy source, `needs` maps jobs-API display
names to expanded dependency names, and `required` lists required check names.
Matrix dependencies include every matrix member. No dependency graph is inferred
from start times. Missing graph, policy, or referenced jobs produces a
`dependency-graph` gap and no `masked-skip` advice. The fixture recorder reads the
pinned workflow and `.openmerge.yml` from local Git history and accepts only the
known independent-job and post-#217 aggregate shapes.

## Reading findings and observations

Every report prints the thresholds used. The JSON contract is
`CIDoctorVersion = 1`. `assessments` retains qualifying and triggering counts,
workflow run IDs, triggering run IDs, comparison run IDs, and measured medians.
`findings` adds stable codes, measured minutes at stake, the comparison definition,
and suggested fix text. `jobs` shows the underlying time splits and step
classifications. Text and JSON use the same result.

Each finding needs **10 distinct qualifying workflow run IDs** per subject.
Attempts do not increase that sample. A baseline also needs ten distinct
matching default-branch runs. Below the minimum, measurements remain visible
without advice. Successful baseline jobs must have a complete attempt. Subjects distinguish
workflow ID/path, job display name, and runner labels. Renamed jobs cannot borrow the old job's baseline.

| Code | Trigger | Measured minutes at stake |
| --- | --- | --- |
| `time-split` | Ten runs with known push time and complete job timing | Zero; context only |
| `cold-setup` | Queue median setup >2× matching default-branch median | Sum of positive setup excess over that baseline |
| `long-pole` | Last-finishing job in ≥80% of runs; median sibling idle >50% of its median duration | Sum of median sibling finish-to-job-finish idle time on last-finishing runs |
| `serial-step` | Step >70% of a job longer than 600 seconds | Step seconds above 70% of that job duration in triggering runs |
| `checkout-cost` | All jobs have checkout and their sum >10% of runner time, or one checkout >60 seconds | Larger of summed checkout excess over 10% and largest checkout excess over 60 seconds |
| `masked-skip` | Required aggregate succeeds with a skipped/cancelled recorded dependency | Zero; no time saving follows from this correctness hazard |
| `retry-habit` | Journal-confirmed same-SHA reruns exceed 5% of distinct check runs | Actual runner time of those rerun jobs |
| `duplicate-pr-run` | Same-SHA PR job runner intervals overlap queue CI | Observed PR runner seconds from queue workflow creation through completion |

Named exported constants are `MinimumRuns`, `ColdSetupRatio`, `LongPoleFraction`,
`SiblingIdleFraction`, `SerialStepFraction`, `LongJobSeconds`, `CheckoutFraction`,
`CheckoutSeconds`, and `RetryFraction`. Except the minimum and ≥80% frequency,
threshold comparisons are strict. Medians use the existing nearest-rank helper.
The last-finisher rule is an observed proxy for the long pole, not a claim to
reconstruct the full workflow critical path.

Dispatch is push-to-job-created. Wait is job-created-to-started; for dependent
jobs it can include dependency wait. Setup is job-started-to-first-non-setup-step;
run is the remainder through job completion. Dispatch/wait are per job and must
not be added as workflow elapsed time. Runner minutes sum job-started-to-completed
intervals, excluding wait; they are observed time, not rounded billed minutes.

The setup classifier uses step names, a heuristic. It recognizes checkout,
`actions/setup-*`, cache restore, dependency install, runner initialization, and
container initialization. Post actions are cleanup. Every other step is labelled
`unknown` and ends the setup prefix. Later setup actions remain labelled setup
but belong to the run phase. The report prints the class, phase, and duration of
every step, including unknown and skipped steps. Review that classification
before interpreting `cold-setup` as evidence about caches.

Minutes at stake are comparisons of recorded durations, never promised savings.
Findings overlap and must not be added together. A slow setup ratio describes
timing, not proof of a cold cache. A report with no findings is not passing CI
evidence. Multiple queue comparisons can overlap the same PR job interval.

## Gaps

`gap_count` counts distinct `(workflow run ID, code, detail)` gaps, not missing
runs. Gaps name missing Actions data, incomplete pagination, ambiguous IDs,
unfinished or inconsistent timestamps, unavailable push times, missing warm
baselines, missing dependency graphs, and missing PR comparison data.

Incomplete job timing suppresses timing advice for that workflow attempt. The
report can still diagnose a masked skip from complete identity, conclusion, and
dependency metadata. Skipped jobs do not need invented execution timestamps.
Unknown step names remain visible classifications rather than fabricated setup.
No value is extrapolated over missing runs. A missing recorded PR run does not
establish that no PR run existed.

## OpenMerge evaluation

The fixtures were recorded on 2026-11-03 with `gh api` against
`CanadaApollo6/openmerge`. They contain the most recent 23 queue runs in the
recorded listing (20 before and three at/after the workflow change), plus 16
main runs split into 13 before and three after. The boundary is commit
`fa1096387f88dbd3c43223d99512b8f83ce86bff` at `2026-11-02T23:57:18Z`, the
workflow change from PR #217. The queue run that landed that commit already used
the sharded workflow and belongs to the after group.

Refresh these pinned run IDs from the repository root with:

```sh
python3 scripts/record-ci-doctor-fixtures.py
OPENMERGE_UPDATE_REPORTING_GOLDENS=1 mise exec -- go test ./internal/cidoctor
```

The script reads only run/jobs metadata and the workflow/policy at each head
SHA. It strips URLs, actors, runner IDs, repository objects, logs, and artifacts.
The rule goldens use synthetic metadata to exercise each finding and threshold;
they are labelled synthetic in the test builder. They are not real-run samples.

`TestRealOpenMergeRecordings` creates an **identity-only journal projection**
from recorded queue branch names and tested SHAs. No production journal was
available in this checkout. Push times and retry records are unknown, so the
projection leaves push time zero and produces dispatch gaps. It establishes
Actions timing behavior, not a production journal-to-Actions integration result.
The CLI integration test separately reads a real test Postgres journal.

The full outputs are
[before](../internal/cidoctor/testdata/openmerge-before.txt) and
[after](../internal/cidoctor/testdata/openmerge-after.txt). Before the change,
`check` triggers `long-pole` and `Run make test` triggers `serial-step`.
**The ADR's expected pre-#217 `cold-setup` finding is not supported by this
recording.** The median setup for `check` is 26 seconds on both queue and main,
a 1.0× ratio. `make test` is an unknown run step, not setup, so its compilation
cost cannot honestly be reclassified as setup to force that result.

The three post-#217 queue workflow run IDs are `41549291483`, `41688299610`,
and `41691904891`. There are **fewer than ten** post-change queue runs, and only
three post-change main runs. The report emits no findings for that group.
This is insufficient evidence to claim that sharding eliminated all three
problems. It preserves the ADR's thresholds and exposes the evaluation gap.
