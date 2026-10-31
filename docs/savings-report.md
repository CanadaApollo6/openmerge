# CI savings report

`openmerge report savings` answers one question for a single queue and time
window: how many CI runs, CI minutes and how much waiting did OpenMerge save,
compared with testing every PR on its own? It is the report to hand a design
partner after a trial period.

The command is **read-only**. It opens the same restricted journal reader as
`tools/dogfood`: no migrations, no connection pings, and every query runs in a
`REPEATABLE READ, READ ONLY` transaction with a bounded deadline. It never
contacts GitHub or any other forge. Every number comes from the queue's recorded
journal, replayed through the production `queue.Apply` fold.

```sh
openmerge report savings \
  --database-url "$OPENMERGE_DATABASE_URL" \
  --installation 12345 --repository acme/api \
  --since 2026-10-01T00:00:00Z --until 2026-11-01T00:00:00Z \
  [--base main] [--lane LANE] [--full-suite-minutes 18] [--json]
```

| Flag | Meaning |
| --- | --- |
| `--database-url` | Connection string; defaults to `OPENMERGE_DATABASE_URL`. A SELECT-only role is enough. |
| `--installation`, `--repository`, `--base`, `--lane` | The exact journal queue key. Spelling and case must match the journal. |
| `--since` | Inclusive window start (RFC 3339). Required. |
| `--until` | Exclusive window end (RFC 3339). Defaults to now; a future value is rejected. |
| `--full-suite-minutes` | Minutes for one full CI run on this repository. When omitted, the observed median is used (see [CI minutes](#ci-minutes)). |
| `--timeout` | Journal read deadline (default `2m`). |
| `--json` | Emit the JSON document instead of the table. |

Exit codes: `0` success, `2` usage, `1` read or computation failure. Flag errors
never echo arguments, and read errors redact the connection string.

## What is counted

**One CI run** is one full execution of the repository's queue CI (every
workflow that runs on `openmerge/queue/**`) on one tested commit. Executed runs
and baseline runs use this same unit.

**Executed runs** are every queue test ref OpenMerge pushed, plus every CI
rerun the forge accepted:

- A pushed test ref counts **whatever happened to it afterwards**: it landed,
  failed, was split by bisection, was invalidated because speculation guessed
  wrong, or was still running at `--until`. Invalidated speculative runs cost
  runner minutes, so they are always counted, even when cancellation stopped them
  early.
- Runs are read from the push records themselves, not from the batches still
  in the queue. A builder that pushes after its batch was dequeued or
  invalidated still started CI, so that late push counts too. Repeated
  confirmations of the same ref (same batch and tested commit) count once. A
  rerun counts once when any record shows the forge accepted it, including an
  acceptance recorded after the batch left the queue. When the journal is
  ambiguous the report counts more executed runs, never fewer.
- A batch whose test ref was never pushed (a build failure or merge conflict)
  ran no CI and is **not** counted. A rerun request that the forge did not
  confirm (unsupported, or an unknown dispatch outcome) is not counted either;
  unconfirmed reruns are reported in `reruns_unconfirmed`.
- A run belongs to the window when its test ref was pushed in
  `[since, until)`, **or** when it tested a PR that landed in the window. The
  second rule carries in work pushed before `--since` (for example, a failed
  parent batch whose bisection halves landed inside the window), so a window
  boundary never hides the cost of a landing it reports.

**The baseline** is one full CI run per PR landed in the window, plus the same
reruns OpenMerge dispatched. It models testing each PR alone before it merges,
with the same flaky reruns. Reruns appear on both sides, so they never create
savings by themselves.

The baseline is deliberately a **lower bound** on the cost without batching. It
gives no credit for the runs a one-at-a-time queue would also spend on PRs that
failed or were dequeued, or on retesting PRs after the base branch moved.

```text
executed  = test refs pushed + accepted reruns
baseline  = PRs landed in window + accepted reruns
saved     = baseline - executed          (may be negative; never clamped)
saved %   = saved / baseline              (0 when the baseline is 0)
```

Landings count by the time the `Landed` event was recorded. A PR whose batch is
still testing at `--until` is not counted as landed, but its run is counted as
executed and as `open`. Events recorded at or after `--until` never affect the
report, so rerunning a closed window later gives the same result.

## CI minutes

Minutes are `runs x per-run minutes`, applied to executed, baseline and saved
runs alike. The `source` field says where per-run minutes came from:

- `full-suite-minutes`: the operator's `--full-suite-minutes`. Use the billed
  duration of one full CI run (the sum of job minutes, if you pay per job
  minute). This always takes precedence.
- `observed`: the median, over runs that reached a terminal CI result, of the
  journal wall-clock from the test-ref push to the first terminal result. This
  includes runner queueing and polling delay and is **not** billed job minutes:
  parallel jobs bill more minutes than their wall-clock. Prefer
  `--full-suite-minutes` for a cost conversation. Wall-clock can be off in
  either direction: runner wait inflates it, and parallel jobs bill more than
  it. In this mode the text report labels every minutes row
  `wall-clock, not billed` and prints a `NOTE:` line under the table.
- `unavailable`: no run completed in the window and no flag was given; minutes
  are not estimated.

Every executed run is charged a full run, including cancelled and failed ones.
This overstates OpenMerge's cost and understates its savings.

## Time to merge and failure isolation

- **Enqueue to landed** is the nearest-rank p50 and p90, over PRs landed in the
  window, of the time from the admission's enqueue to the recorded landing.
- **Bisections** counts failed multi-PR batches that were split into halves in
  the window. **Bisection runs** counts the executed runs that tested a half.
  Both are the price of isolating a failure without blaming the whole batch.
- **Dequeued** counts PRs removed from the queue in the window, by stable
  dequeue reason (`checks-failed`, `conflict`, `requested`, and so on).

## Reading it with a design partner

```text
PRs landed             14
Batches landed         6          mean 2.33 PRs; p50 1; p90 8; max 8
CI runs executed       13         12 test refs + 1 rerun(s); 1 carried in from before --since
  test ref outcomes               landed 6; invalidated 1; discarded 4; open 1
Baseline CI runs       15         14 landed PRs + 1 rerun(s)
CI runs saved          2          lower bound; 13.3% of baseline; 0.93 runs per landed PR
CI minutes per run     12.5       source: full-suite-minutes (operator-supplied minutes for one full CI run)
CI minutes saved       25.0
Enqueue to landed      p50 1m10s  p90 2m20s (14 PRs)
Bisections             2          4 bisection runs
Dequeued               2          checks-failed 1; requested 1
```

This excerpt is from the fixture in
`internal/queuemetrics/testdata/savings-flag.txt`, not from a real installation.

1. Start with **CI runs saved** and **runs per landed PR**. Below 1.0 means
   batching tested several PRs per run; above 1.0 means failures, bisection or
   invalidated speculation cost more than batching saved in this window.
2. Check **test ref outcomes**. Many `invalidated` or `discarded` runs point at
   flaky CI or frequently failing PRs, which smaller batches or a lower
   speculation depth may reduce.
3. Convert to money with **CI minutes saved** only when `source` is
   `full-suite-minutes`. Say "at least" when you quote the saving: the
   baseline is a lower bound.
4. Read **Enqueue to landed** next to the savings. Batching should save runs
   without making PRs wait much longer.
5. Read every `WARNING` line. They name carried-in runs, runs still open at
   `--until`, unconfirmed reruns and landings that replay rejected (run the
   landing audit before quoting such a window).

## JSON

`--json` emits one object (`version: 1`) with the window, both definitions,
`prs_landed`, `batches`, `ci_runs`, `ci_minutes`, `enqueue_to_landed`,
`failure_isolation` and `warnings`. The shape is pinned by
`internal/queuemetrics/testdata/savings-*.json`. Times are RFC 3339 UTC,
latencies are seconds, and minutes are floating point.

## Relationship to tools/dogfood

`tools/dogfood report` measures the ADR 0009 experiment cohort, using GitHub
Actions attempts. The savings report reuses the same journal replay primitives
(`internal/queuemetrics`) but counts runs from the journal alone, so it needs no
GitHub token. During the ADR 0009 trial, do not run either report's latency or
runs-per-merge measurements against the trial queue before period B, as the
[pre-registered plan](research/phase2-dogfood.md) requires.
