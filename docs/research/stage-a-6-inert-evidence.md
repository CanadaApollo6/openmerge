# Stage A 6 inert component evidence — fix round 1

Base: `dd03aa3e1eccbd2a7a2f81f60f45c155f7ea43df` (`origin/main`, PR #70).
Implementation: `7e6e117c8c43a1d12b87f806ec2bbe4af0b8481e`.
Worktree: `$HOME/openmerge-stage-a-6`, branch `stage-a/6-stances`.
Baseline checkout: `$HOME/.cache/om-sa-6-baseline`, detached at the base above.
Go: `go1.27.2 linux/amd64`; host CPU: Intel i7-13620H.
The following evidence commit changes only this note.

This is component/merge evidence, not deployment authorization. The live DB,
service units and live branch refs were not operated on. Tests use only the
shared test Postgres at localhost:54329 and local fake forge servers. No real
GitHub API calls occur in code/tests. An initial default-refspec fetch created
`origin/openmerge/queue/main/b44`, a remote-tracking ref; it did not write a
local or remote `openmerge/*` branch. Subsequent fetches explicitly selected
`refs/heads/main:refs/remotes/origin/main`. This records the fetch-scope exception
rather than claiming all fetches were main-only.

## Design and review findings

The effective stance and blocker indices are recomputed for the changed user
from the attributed source set and pinned first-observation barriers. Positives
are reconsidered when new evidence arrives. ReadID and arrival order no longer
control recovery. Requests win equal/unknown/ambiguous ordering; fallback
withdrawals cannot hide later requests. Approvals must clear all relevant
barriers with strict time and applicable namespace sequence proof. Requests
cleared by withdrawals are resolved with sorted prefix maxima, avoiding a
pairwise scan. The all-source pins remain bounded to 4096 per change; copies
clone only the changed maps/payload instead of JSON-round-tripping the entire
change twice per Apply. Capacity exhaustion clears the partial decision and
fails closed for that change. Another change's budget/stance stays independent.
The 11a eligibility ingestion filter and explicit oversized-change rejection
are required activation gates, documented in `internal/queue/approvals.md`;
arbitrary pin eviction would permit historical source rebinding.

Dismissal submission time/commit now remain independent attribution evidence;
dismissal action time still comes exclusively from PR 3b's dismissal DTO.
An unseen old review on a reused head cannot acquire a fresh dismissal's epoch.
Known original attribution joins in either discovery order. Novel heads do not
need time proof against local first-poll time. Reused heads require submission
after the boundary plus Q4's three-second precision/clock allowance. Prior-head
sets survive snapshots and legacy-first boundary enrichment. Old snapshots
without history stay conservative through future revisions until full replay.
Any revision change clears current request/withdrawal indices.

Accountability records and evaluation compare policy digests, including agent
mapping changes. Current intent must match recorded revision/head/digest.
Fingerprints exclude historical/non-effective sources and include recorded
policy and ConfigApplied digests. `PrepareApprovalDecision` carries exact fresh
eligibility, accountability, intent, relied-on stances, receipt identity,
evaluation and snapshot; Stage A 9 must atomically journal that exact eligibility.
Typed boundary preflight and 7a/8/9/11a receipt/ordering gates are documented.
Fresh owner pools, both owner known flags, PR 3b time mapping and Q4 first bounds
remain covered by the existing component tests.

## Inertness and coverage

No production emitter, action, config/schema key, condition, planner/executor
or landing gate is added. Legacy `approvalFacts` remains nil. Snapshot v5 reads
v2–v4 without enabling compaction. Production call-site search:

```sh
rg -n 'approvalFacts|ReviewStanceObserved|EvaluateApprovals|NativeStance|PrepareApprovalDecision' \
  internal/serve internal/executor cmd
```

Result: no matches (exit 1). No live journal is read. Deployment still needs the
operator's exported-live-journal shadow replay and effective-config comparison.
The deploy-time script's future nil-only approvalFacts exception is documented,
not enabled by this PR.

All **80** baseline wire files are byte-identical. Sorted path + NUL + bytes
SHA-256: `f32728989e704e31cae160f82d12113cc3007988a73b628ab5dd1d598fb8eb54`.
New inert observation goldens additionally pin the new original-submission and
policy-digest fields and the shared exported-name JSON fields. The null-field
mutation test skips only the explicit absent Receipt/null, not every unchanged
mutation. The complete PR remains below the 3000 changed-line review ceiling.

`TestStanceSetPermutationsAndEverySnapshotSplit` covers **19 sets**, sizes
**3, 4 and 5**: seven semantic regression sets and twelve seeded generated sets.
All permutations, every snapshot split (including both ends), three read groups
and duplicate polls produce **11,688** identical effective stance/blocker/frontier
and fingerprint results: **0 divergences**. Sets include both channels, exact
and fallback dismissals, approvals, requests, unapprove, equal and missing times.
Additional tests pin the reported two-eligible-approver safety repro, unseen old
review dismissals on H1→H2→H1, original-known/unknown cases, novel-head polling,
clock allowance, incomplete historical snapshots, per-change capacity isolation,
policy/intent binding and exact evaluated eligibility in the decision bundle.

## Verification

All workloads run serially with the live server left running. Environment:

```sh
export GOFLAGS=-p=2 GOMAXPROCS=2 GOMEMLIMIT=4GiB
export GOTMPDIR=$HOME/.cache GOCACHE=$HOME/.cache/go-build TMPDIR=$HOME/.cache
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
make check > ~/.cache/om-pr71-fix1.log 2>&1; echo exit=$?
make test-sim-exits > ~/.cache/om-pr71-fix1-sim.log 2>&1; echo exit=$?
python3 scripts/check-phase2-compatibility.py "$HOME/.cache/om-sa-6-baseline" \
  > ~/.cache/om-pr71-fix1-compat.log 2>&1; echo exit=$?
python3 scripts/check-stage-a-identity-compatibility.py "$HOME/.cache/om-sa-6-baseline" \
  > ~/.cache/om-pr71-fix1-replay.log 2>&1; echo exit=$?
```

Final results against `dd03aa3`: **make check 0; test-sim-exits 0;
compatibility 0; differential replay 0**. Simulator exits took 44.035 s.
Compatibility preserves 17 legacy exit records and 1055 retry/default-digest
records; its registry comparison preserves all 76 pre-existing registry fixtures.
The independent all-files comparison above includes four additional variants.
Replay preserves depth-1's 682 events and depth-4's 1071 events and all **1753**
event/action checkpoints. Combined checkpoint SHA-256:
`59e8f223916c504b10a20ead4d300f861f776f9f39dd91248f3294981dc2a6de`.
No change_* events are emitted by the synthetic legacy workloads.

The first two check attempts exited **2**, on a slice-comparison type error and
then a builtin-shadowing lint error. Preserved logs:
`om-pr71-fix1-initial.log`, `om-pr71-fix1-lint.log`. Both were fixed.
The complete pre-rebase check exited **0** (`om-pr71-fix1-pre-rebase.log`).
A post-rebase run was intentionally stopped (exit **2**) to close the missing
snapshot-history edge (`om-pr71-fix1-history-interrupted.log`). Only test
processes started by this task were terminated. The final results above apply
to the implementation named at the top.

## Replay cost

```sh
go test ./internal/queue -run '^$' -bench '^BenchmarkApprovalReplay$' \
  -benchtime=1x -count=5 > ~/.cache/om-pr71-fix1-benchmark.log 2>&1
```

Exit **0**, five complete replays at each size, non-race, GOMAXPROCS=2:

- **1500** mixed sources: median **0.475976 s**, range **0.473035–0.482726 s**.
- **4095** mixed sources: median **3.407444 s**, range **3.384777–3.466484 s**.

Each timed region starts with an empty projection and actually applies every
source, checks the exact retained count, absence of saturation and the final
request-for-changes blocker. It measures full component replay for one user,
including validation, immutable map copies and recomputation, not a no-op or
cached lookup. No I/O/gate acquisition latency is measured; the component is
inert and has no production ingestion path. The published results exclude
profiling and the earlier single-run exploratory measurements.

Cumulative allocation medians are 1.073 GB and 8.065 GB **per entire replay**,
not resident heap sizes. The full replay still has quadratic map-copy volume;
it no longer serializes the entire growing change twice per event. Allocation
counters establish the remaining copy cost, but the CPU limiter is unconfirmed:
the separate 4095-source CPU-profile attempt was killed before execution
(`go test` exit **1**, empty profile; pprof exit **2**). A smaller diagnostic
retry at 512 MiB was also killed at startup (exit **137**; pprof exit **2**).
Those failed diagnostics are not timing samples and no CPU-profile conclusion
or before/after speedup is claimed. Logs: `om-pr71-fix1-profile-run.log`,
`om-pr71-fix1-small-profile-run.log`. The live server and unrelated processes
were left alone.

The host was shared, with 16 logical CPUs and pre-run load averages
0.97/1.56/1.72 (`om-pr71-fix1-benchmark-host.log`). These are local candidate
measurements, not a comparison to a matched baseline. Both benchmark sizes
assert successful work and valid final output; all ten measured replays passed.
