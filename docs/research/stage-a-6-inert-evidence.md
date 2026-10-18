# Stage A 6 inert component evidence — fix round 2

Baseline/current main: `dd03aa3e1eccbd2a7a2f81f60f45c155f7ea43df` (PR #70).
Implementation: `cf2b7ce2bad85af92d5191f570c1c8fcdec646fb`.
The following commit changes only this evidence note.
Candidate: `$HOME/openmerge-stage-a-6`, branch `stage-a/6-stances`.
Baseline checkout: `$HOME/.cache/om-sa-6-baseline`, clean and detached at main.
Explicit main/branch refspec fetches confirmed main had not moved; no rebase needed.

This remains **Inert**, with no production emitter, action, public config key,
condition, planner/executor change or landing gate. No live journal/DB, service
unit or `openmerge/*` ref was operated on. Tests use only the shared test Postgres
and fake forge servers; code/tests make no real GitHub API calls. No processes
were killed. Round 1's default fetch created a remote-tracking queue ref; this
round used only the explicit main and stage-a/6-stances refspecs and left it alone.
This synthetic evidence does not authorize deployment: an operator still needs
an exported-live-journal shadow replay and effective-config comparison.

## Changes and coverage

1. Late-original joins promote the dismissal's attribution basis and stance
   revision together, retaining its original action time/bound. Astra's H2
   dismissal observed during H1, followed by H2 and late original, now survives
   snapshot round trips. Encoding validates approval attribution and derived
   caches before returning bytes; decoding independently rejects cache/source
   contradictions, including an older Latest approval hiding a retained request.
2. Prior-head history lives only in `approvalFacts`, initialized by approval
   observations. Legacy and revision-only queues retain nil facts and unchanged
   `RevisionRecord` shape/values. Missing early history stays conservative.
3. Dismissals clear only native requests with sequence at most the dismissed
   review ID, with strict time proof. They cannot erase a later native request
   or a command request, or revoke a command approval. Native approvals retain
   Q4 time/barrier clearance requirements. All six permutations of approval
   103 / request 120 / dismissal 103 run for timeline, review-dismissal and
   fallback times, both native and command requests, restarting after every step.
4. On reused/unknown-history heads, ambiguous matching-head requests remain on
   the current revision; ambiguous approvals stay historical. Tests include
   pre-boundary and 0/2/3-second submission offsets.
5. Revision changes prune older, positively attributed native approvals and
   recover Saturated when capacity permits. Historical requests, withdrawals,
   ambiguous sources and all command pins stay retained. At capacity, incoming
   barriers replace positive noise; Saturated remains fail-closed across polls
   and restart. All-barrier overflow requires admission rejection. Tests cover
   one legitimate approval plus another reviewer's positive noise, an arriving
   legitimate blocker, restart, revision recovery and a later blocker.
6. Source-derived Latest/Blockers/Frontiers are recomputed for validation on
   both encode/decode. Fingerprints use the validated effective indices; a forged
   v5 Latest cannot authorize a change contrary to retained evidence.

The contract documents that decision `ReliedOn` contains all effective current
stances; counted subsets are `Evaluation.Requirements[].ReliedOn`. `Apply`
silently drops unbound observations, so 11a must call typed boundary preflight.
11a must filter freshly proven ineligible/non-writer reviewers before allocating
pins, block on unknown eligibility, and reacquire complete history on newly
eligible reviewers. No production ingestion is introduced here.

11a also owns queue-level admission: count limits alone are insufficient.
Encoding returns a typed per-change admission error above **8 MiB/change** or
**48 MiB total approval facts**, and checks the actual **64 MiB** whole snapshot
limit. 11a must roll back the rejected change and retain the previous recoverable
queue; the remaining 16 MiB is reserved for legacy state. No existing blocker
is evicted to admit positive noise. Resource tests cover both individual and
collective byte limits. Roughly 4.6 MiB/saturated change means about ten such
changes fit the approval reserve, not fourteen under the whole-codec cap.

The full permutation sweep retains **19 sets**, 3–5 sources, **11,688 schedules**,
**0 divergences**, read regrouping, duplicate polls and every snapshot split.
Every reachable prefix additionally encodes/decodes and checks identical bytes
and approval facts. Full sweep: exit **0**, 17.023 s. Under `-short`, six
permutations per complete set retain every semantic fixture and every split;
the full sweep runs otherwise. Final 60-second reachable-state fuzz: **3,869
executions**, **0 failures**, exit **0** (61.230 s), one worker. It mixes revision
changes/head reuse, late originals, both channels and dismissal time sources,
checking every prefix's round trip. An earlier run passed 3,987 executions.
These are snapshot properties, not new independent-oracle claims.

## Verification and replay gate

All heavy jobs ran serially with:

```sh
export GOFLAGS=-p=2 GOMAXPROCS=2 GOMEMLIMIT=4GiB
export GOTMPDIR=$HOME/.cache GOCACHE=$HOME/.cache/go-build TMPDIR=$HOME/.cache
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
make check > ~/.cache/om-pr71-fix2.log 2>&1; echo exit=$?
make test-sim-exits > ~/.cache/om-pr71-fix2-sim.log 2>&1; echo exit=$?
python3 scripts/check-phase2-compatibility.py "$HOME/.cache/om-sa-6-baseline"
python3 scripts/check-stage-a-identity-compatibility.py "$HOME/.cache/om-sa-6-baseline"
OM_REPLAY_PREFLIGHT_REQUEST='{"baseline":"/home/riels/.cache/om-sa-6-baseline","candidate":"/home/riels/openmerge-stage-a-6"}' \
 OM_REPLAY_PREFLIGHT_RESULT="$HOME/.cache/om-pr71-fix2-selftest-report.json" \
 python3 scripts/test-live-journal-replay.py
python3 scripts/check-live-journal-replay.py \
 --baseline "$HOME/.cache/om-sa-6-baseline" --candidate "$HOME/openmerge-stage-a-6" \
 --events "$HOME/.cache/om-pr71-fix2-journal/events.jsonl" \
 --queues "$HOME/.cache/om-pr71-fix2-journal/queues.jsonl" \
 --out "$HOME/.cache/om-pr71-fix2-live-replay-report.json"
go test ./internal/queue -run '^$' -fuzz '^FuzzApprovalReachableSnapshot$' -fuzztime=60s -parallel=1
```

Actual final exits: **check 0; sim-exits 0; compatibility 0; differential replay
0; deployment-pair self-test 0; deployment-pair replay 0; fuzz 0; benchmark 0**.
Logs use `~/.cache/om-pr71-fix2-{compat,replay,selftest,live-replay,fuzz-final,benchmark}.log`.
`check` includes lint (0 issues), race/short tests and build. Simulator: 43.972 s.
Compatibility: 17 legacy exits and 1,055 retry/default-digest records unchanged.
Differential replay: 682 + 1,071 events, **1,753 identical event/action checkpoints**;
combined SHA-256 `59e8f223916c504b10a20ead4d300f861f776f9f39dd91248f3294981dc2a6de`.
All **80** baseline wire files are byte-identical; sorted path + NUL + bytes hash:
`f32728989e704e31cae160f82d12113cc3007988a73b628ab5dd1d598fb8eb54`.
No approval production call-site matches in `internal/serve internal/executor cmd`.

Deployment-pair self-test: **19 tests PASS**, synthetic pair **7/7** checkpoints,
15 boundary probes. CLI: **gates 2/4 PASS**, **10/10** structural/action/boundary
checkpoints, **27** timer probes, derived determinism PASS, no unreviewed fields
or first divergence. Its nine-event synthetic journal includes three
`pr_observed` events (including a head change), enqueue/batch/test/check events,
and main's ConfigApplied digest with its actual 1–4 window bounds. Digest:
`e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`.
Export SHA-256: events `838a9655db51af90e0e63b93ad7c68be8b868d6c5dc1dbc13004638f0c3305ae`;
queues `b34e493c77be1a9ba76d78b27a40db4a767a4027254279f4327d97756b454617`.

Exactly these comparator exceptions exist for deployment:

- **New in this pair:** absent-to-nil `approvalFacts` only, with the exact map
  type/nil shape checked. Empty/populated/wrong-type facts fail the self-test.
- Pre-existing `outsidePushes` absent-to-zero exception for older baselines;
  current main already has it, so shared contents must match exactly.
- Pre-existing absent/empty-baseline `ChangeRevisions`/`BatchRevisions` derivation
  exemption, with strict record schemas and deterministic candidate replay.
  A populated baseline must match exactly. This pair has populated revisions;
  there is **no PriorHeads or other revision-record exception**.

Raw snapshot hashes differ because codec v5 adds ApprovalFacts null; structural
comparison deliberately excludes codec bytes/version, not legacy state fields.
No other new field, nonzero approval data or effective-config exception is allowed.

Initial focused tests exited **1** while expectations were updated for the new
lineage/pruning semantics. The first deployment self-test/replay exited **1**
from a malformed map key in the new comparator fixture; fixed, with failed logs
preserved as `om-pr71-fix2-initial-*`. Two intermediate CLI runs exited **1**
with gate 2 PASS but gate 4 FAIL because the synthetic journal omitted first
ConfigApplied and then its speculation bounds; preserved as
`om-pr71-fix2-without-{config,bounds}-*`. The final journal includes the exact
current-main effective configuration; the comparator was not weakened.

## Replay cost

Go 1.27.2 linux/amd64, Intel i7-13620H, GOMAXPROCS=2, shared host; observed
load 1.73/1.32/1.26. Non-race command, exit **0**:

```sh
go test ./internal/queue -run '^$' -bench '^BenchmarkApprovalReplay$' -benchtime=1x -count=5
```

- **1,500 sources:** median **0.495565 s**, range **0.483449–0.510086 s**;
  median cumulative allocation **1,073,137,992 bytes** per complete replay.
- **4,095 sources:** median **3.376131 s**, range **3.357139–3.456695 s**;
  median cumulative allocation **8,065,299,968 bytes** per complete replay.

Each timed region starts empty, applies every mixed approve/request/unapprove
source and verifies exact source count, no saturation and the final blocker.
These are ten actual successful replays, not cached/no-op work. Allocation is
cumulative, not resident heap. Map-copy volume remains quadratic; no matched
baseline speedup, CPU bottleneck or production ingestion latency is claimed.
