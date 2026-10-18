# Stage A 1: change identity and revision projection evidence

Implementation source: `2b7ee4c2bfbe9209fa436e2ea68bdcde4c6dc04a`.
Clean baseline: `df3af44ffede645c7fb64a37d83d6bae334a6aa7` (`origin/main`).
Approved plan: `f55a31b`, ADR 0007 Stage A PR 1.
Toolchain: mise Go 1.27.2 and golangci-lint 2.14.0.
Built candidate CLI SHA-256 (embedded version `2b7ee4c`):
`414a55c645f4fedc923ab17c0fc7fbe8341326bf58b2128a14a8ef07b05cec77`.

Trial classification: **Inert**, with production emission dormant until PR 11b.
This PR does not deploy a binary or activate approval configuration. The
per-deployment exported-live-journal/config shadow replay remains a deployment
gate; the comparisons here use reproducible fake-forge journals and never read
or write the live database. No systemd units or live refs were changed.

## Required checks

Run from the implementation worktree with mise on PATH:

```sh
mise exec -- bash
OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' make check > /tmp/om-sa-1.log 2>&1; echo exit=$?
make test-sim-exits
python3 scripts/check-phase2-compatibility.py /tmp/om-sa-1-baseline
python3 scripts/check-stage-a-identity-compatibility.py /tmp/om-sa-1-baseline
go test ./internal/config -run '^TestLegacyDefaultPolicyDigest$' -count=1 -v
```

`/tmp/om-sa-1-baseline` is a clean detached worktree at the baseline SHA above.
Postgres tests use isolated test schemas in the supplied local test database.

- `make check`: final exit **0**, including formatting, lint, full short race
  suite with Postgres, and the CLI build. Log: `/tmp/om-sa-1.log`.
- `make test-sim-exits`: exit **0**.
- `check-phase2-compatibility.py`: exit **0**.
- `check-stage-a-identity-compatibility.py`: exit **0**.
- Default digest test: exit **0**.
- Focused queue/store race tests, including real Postgres races: exit **0**.

The first `make check` attempt exited **2** at its formatting gate. After
formatting `internal/store/store.go`, the first full run exited **2** on
`TestBackstopReplansInterruptedPeerEnqueue` (peer not interrupted) and
`TestBackstopRecomputesPeerPartialBatchDeadline` (elapsed planner deadline).
Both tests then passed unchanged with `-race` on the candidate and on clean
main (exit **0** each). The failed full log is
`/tmp/om-sa-1-check-failed.log`; the complete check was rerun.
The rebase from `1383c29` to `df3af44` adds only the approved ADR plan and
roadmap/dogfood documentation, with no Go source changes.
A post-rebase compatibility-script attempt exited **1** because its Go test
process ended with `signal: killed`. Its log is
`/tmp/om-sa-1-compatibility-killed.log`. A fresh retry exited **0** and reproduced
the same 17/1,055 record hashes and all 73 unchanged fixtures.

## Differential results

All **73** pre-existing wire fixtures are byte-identical. Exactly one registry
fixture is added, `event_change_revision_observed.json`; all additional vectors
are table tests. There are no new action types or config fields.

The compatibility script reports:

```text
legacy exits: 17 records byte-identical; sha256=bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6
depth-1 retry envelopes and default digest: 1055 records byte-identical; sha256=07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37
all 73 existing wire fixtures byte-identical; exit=0
```

The effective default policy digest remains
`75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa`.
`queue.Config` and its serialization are untouched.

The extra replay compares the event payload and complete next-action list,
including action keys, at **every** revision, using the same fake-forge inputs
at depths 1 and 4: seed 42, 40 PRs, one-hour arrivals, batch size 4, one-minute
batch wait, 20% broken rate, 10% flakes, one failed-check retry and 50 runners.
It reports:

```text
synthetic journal depth=1 events=682 sha256=75b4bcfbcf8ef89be731a6109b0b6051639c1476b530a3f346a0ff82716f577e
synthetic journal depth=4 events=1071 sha256=ab5384c42a8038d3223aad2c84832b372b8de7f3bd51de21671756d5cec69875
every-revision events/actions: 1753 checkpoints byte-identical; sha256=59e8f223916c504b10a20ead4d300f861f776f9f39dd91248f3294981dc2a6de
no change_* events emitted; exit=0
```

These no-`approvals` queues emit no new event types or extra `ConfigApplied`.
The new event appears only in explicit component tests and its wire fixture;
production has no emitter for it.

## Projection guarantees and boundaries

- Identity hashes length-prefixed canonical installation/repository/base/lane
  and PR fields under `openmerge/change/pr/v1`. The stored repository spelling
  is authoritative; case aliases reuse the canonical key before derivation.
- Legacy `PRObserved` events establish revision boundaries during full replay;
  H1, H2, H1 is three revisions. Re-enqueue, duplicate heads and closure retain
  those boundaries. Historical records gain no invented review or attribution.
- The dormant event validates required/unknown fields and values. Append checks
  the canonical change ID and predecessor under the queue lock. Stale deliveries
  roll back all their inserts; concurrent updates have exactly one winner.
- Immutable side state pins each batch member's revision. Later heads cannot
  rebind an unsettled landing. Existing `HeadChanged` removal and conflict
  behavior remain in force; conflict grace is excluded from Stage A.
- Snapshot version 3 retains current revision boundaries and bounded active
  batch bindings. Tests prove snapshot-plus-tail equals full replay and next
  plans. Version 2 still decodes; absent historical boundaries remain explicitly
  unrecorded because version 2 never stored their sequence. A full journal replay
  recovers them. Snapshot persistence, compaction and retention stay disabled.
