# Stage A 7a inert evidence — fix round 1

Validated code: `bab40308b82ffe98d1cd45bda70b452e9d138ac0`, branch
`stage-a/7a-receipts`, against origin/main
`3e1536093441594ec6b1abd723426e29b8465ad5`. Main did not move, so no rebase
was needed. Fetches named only main and the 7a branch by explicit refspec.
The following commit changes only this note. Worktree: `$HOME/openmerge-stage-a-7a`.

This remains **inert**: no production producer/command, endpoint, config key,
planner/executor/landing change or permission change. No live DB, service unit
or `openmerge/*` ref was touched. Replay read a pre-existing exported journal;
tests used shared test Postgres. No real GitHub API calls were added to code/tests.
Legacy State.receipts remains nil; snapshot v6 adds its null field and upgrades
v2–v5. Only that exact nil-map structural difference is allowlisted.

## Review fixes

- Receipt-backed sources are pinned at 4,096-source saturation. Acceptance
  requires a matching approval-kind stance source; failed admission preserves
  the prior state. Reachability property seeds and fuzz exercise saturation,
  new receipts, native noise, barriers, duplicates and rejected receipts, with
  encode/decode/byte-identical re-encoding after each generated transition.
- Receipt creation must be strictly after the durable revision boundary plus
  PR 6's 2-second uncertainty and 1-second resolution allowance. Rejections use
  `receipt_before_revision`; BoundaryAt/Basis are checked against revision data.
- Build errors on stale Current, invalid approval configuration or missing
  opted-in requirements. It computes identity before receipt-required progress.
- `State.RecordedBrief` and receipt preparation read recorded classification,
  accountability and intent themselves. Fresh owner membership affects gate
  progress separately; recorded owner changes alter the digest. Tests pin both.
- Evaluator expansion origins/rule indexes and unresolved causes are typed.
  Unknown owners differ from unknown writer eligibility; inventory codes apply
  only to affected obligations, and met/group rows retain their own status.
- Classification, receipt, approval and stance admission share the 48 MiB
  reserve and check the full 64 MiB snapshot bound. Errors name the admitted
  change, including the three-large-plus-smaller admission cases.
- Bare receipts construct explicit Supplied from recorded Current plus the
  supplied digest. Mismatch is `receipt_digest_mismatch`, with no issued ledger.
  Parsing trims surrounding whitespace and requires exactly 64 lowercase hex
  characters. Supplied strings are UTF-8 and at most 1,024 bytes each. Missing
  evidence returns an error: 11a defers rather than recording a rejection.
  Rejections are capped at 64 per actor/change. First outcomes remain pinned.
- Canonical v2 replaces prose Origin with typed cause/rule index, deduplicates
  requirements and uses slice cloning instead of JSON cloning. Fact placeholders
  are removed; Truth's numeric/string interpretation is documented for 7b.
  Stance/provenance presentation and structured TestLinks remain for 7b.
- ADR 0007 now has a dated hunk-cap clarification; v7 rejects `hunk_count_limit`.
  Forge/classification boundaries fail closed on invalid UTF-8 paths.

Protocol details: `internal/brief/README.md` and `internal/queue/receipts.md`.
V1 byte/hash fixtures are preserved exactly as `testdata/v1-*`.
V2 vectors (independently reproduced with Python canonical serialization,
reversed input object order and intent hashing):

- Complete: `a2c579bfd5aa5a9911e0d69bfed2a0ca9dd6036e504db184ab1a811240ec1227`.
- Incomplete: `7eacfa5f593d2db45045a4f07ff5befc39646dbc4195905d2897e3860ea36fca`.
- Empty intent: `af362a32e2cfb3a5d0f7e8d065e28d4ed0ef2f702c5955776fb4d6232eeb2f0f`.
- Complex: `64c05eff2a713bc8261aeb148c1f0a488ce5a024fd699df4643653ecd5767e50`.
  This pins unsorted multi-file renames, owners/requirements, deduplication,
  nil/empty equivalence and `<`, `&`, U+2028 and control-character escaping.

## Carry-overs and checks

#64's v8 4,096-hunk/4 MiB JSON-escaped text caps and omission summaries remain;
retained evidence is validated without truncating it again. #69's eight bounded
owner diagnostics plus omitted count and hashing exclusions remain. #71's older
dismissal/later-native ordering fix and intent binding remain. #66 retains only
the latest classified revision/generation and enforces admission byte budgets.

All heavy jobs ran serially, after checking
`pgrep -af 'go test|\.test |check-live-journal'` for other agents' running jobs.
Environment:

```sh
export GOFLAGS=-p=2 GOMEMLIMIT=4GiB GOMAXPROCS=2
export GOTMPDIR=$HOME/.cache/om-pr72-tmp GOCACHE=$HOME/.cache/go-build TMPDIR=$HOME/.cache
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
make check > ~/.cache/om-pr72-fix1.log 2>&1; echo exit=$?
make test-sim-exits > ~/.cache/om-pr72-fix1-sim.log 2>&1; echo exit=$?
python3 scripts/check-phase2-compatibility.py "$HOME/openmerge-stage-a-7a-main"
python3 scripts/check-live-journal-replay.py \
  --baseline "$HOME/openmerge-stage-a-7a-main" --candidate "$HOME/openmerge-stage-a-7a" \
  --events "$HOME/.cache/om-pr72-fix1-input/events.jsonl" \
  --queues "$HOME/.cache/om-pr72-fix1-input/queues.jsonl" \
  --out "$HOME/.cache/om-pr72-fix1-deployment-pair.json"
```

Actual exits: **make check 0; sim-exits 0; compatibility 0; deployment replay 0**.
The first check attempt exited 2 on three revive lint issues, all corrected.
Final check had zero lint issues and passing race/short tests/build; sim-exits
completed in 43.795 seconds. Additional final targeted tests: 0.
Independent leaf/encoding checks, original receipt repros and both dismissal
oracles: final exit 0, 7,500 sets/45,000 runs per oracle (seeds 100 and 710300),
zero divergences. Saturation fuzz result: exit 0 (90-second budget;
4 completed executions, including the 3 baseline seeds).
Logs: `~/.cache/om-pr72-{fix1,fix1-sim,fix1-compat,fix1-replay,final-oracles,fuzz}.log`.

Compatibility: 17 legacy exit records and 1,055 retry/default-digest records
remain byte-identical, with SHA-256 respectively
`bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6` and
`07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
All **84** tracked baseline wire files (including variants) are byte-identical;
sorted path + NUL + bytes SHA-256:
`73371fde38dce0aa7325ae4177c346c2bd8fc1b64972efd7963624cfc029d878`.
Final PR: **2,806 changed lines**, below the 3,000-line review ceiling.

## Deployment pair

The pre-existing `20261018T051000Z` export contains **2,158 events, one queue**.
Read-only copies live under `~/.cache/om-pr72-fix1-input/`. Event SHA-256:
`9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`;
queue SHA-256: `418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`.
Config SHA-256: `3efb43bbc05f5f0b03c005331f79064829fa018e9f6446224682e00dd357fead`.

**Gates 2/4 PASS**: actions, typed structural state and boundary checkpoints
**2,159/2,159** identical; **8,676** timer probes; derived determinism PASS;
no first difference or non-allowlisted fields. Mandatory preflight: **20 tests
PASS**, synthetic **7/7** checkpoints and **15** boundary probes. Snapshot bytes
are **0/2,159** identical, expected for v5 → v6/null receipts; structural state
and next actions agree. The effective-config gate passed. This supersedes the
previous synthetic-only deployment-pair evidence; no deployment was performed.
