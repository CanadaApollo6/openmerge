# Stage A 8 verification evidence

Trial deployment class: **Hold**. Merge is OK; **do not deploy until ADR 0009's
dogfood period B drains**. Passing the legacy gates does not waive this hold.
No public approval configuration or executor ingestion is enabled here; 11a
consumes the dormant outcome contract and installs the read-only probe reader.

## PR #73 fix round 1 (2026-10-18)

Runtime/test commit: `5329161b091c1bd142848ca186b1cbb4bc3cb828`.
Explicit-refspec fetches before editing and after `make check` both found
`origin/main` at `3e1536093441594ec6b1abd723426e29b8465ad5`; no rebase was needed.
The reviewed starting head was `5e9cd53b9c003604664a1f296fe64b9d7e8cc216`
(Astra and Sonnet approved that head; these fixes are subsequent changes).

`ApprovalGateRejected` now encodes/decodes composition through `wireBatchPR`
(`pr`, `head_sha`). Regeneration changed only this PR's new registry fixture
and multi-member variant. All pre-existing fixtures remain byte-identical.
The event-key test covers every registered event fixture recursively, rejecting
new keys outside lowercase snake_case. Exact legacy paths in the existing
classification, review and accountability payloads remain allowlisted because
changing those nested keys would violate the existing-fixture constraint;
the golden tests continue to freeze their bytes.

Intent and accountability observations now use the same stale-basis guard as
eligibility. The guard includes recorded intent/accountability times, so delayed
older reads cannot regress text or accountability and spuriously wake a wait.
Tests cover no-op behavior before a wait, after snapshot recovery, and while
waiting, plus a fresh changed read that does wake the wait.

Heavy jobs waited for other agents' actual `go test`, `*.test` and replay
processes to finish, then ran serially with `GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`,
`GOTMPDIR=~/.cache/om-pr73-fix1-tmp`, `GOCACHE=~/.cache/om-pr73-fix1-go`, and
the supplied shared test Postgres URL. No process belonging to another task
was killed. No live DB, systemd unit or `openmerge/*` ref was touched; code and
tests make no real GitHub API calls.

Fix-round command results:

- `UPDATE_APPROVAL_FIXTURES=1 go test ./internal/queue -run '^(TestApproval|TestWire)' -count=1`:
  **0** (`~/.cache/om-pr73-fix1-focused.log`).
- `make check > ~/.cache/om-pr73-fix1.log 2>&1; echo exit=$?`: **0**;
  lint reported zero issues, all race/short packages passed, build version `5329161`.
- `python3 scripts/check-legacy-plan-action-depths-1-4.py ~/openmerge-stage-a-8-main`:
  **0** (`~/.cache/om-pr73-fix1-differential.log`).
- `python3 scripts/check-phase2-compatibility.py ~/openmerge-stage-a-8-main`:
  **0** (`~/.cache/om-pr73-fix1-compatibility.log`).

The differential included the complete required full simulator corpus (Team/Slow
profiles and both cancellation capabilities), with no plan/action/wake exceptions:

- Short: 227,662 comparisons;
  `71f1ae2108d98685b675aa5a158834bbd96595ce3c9e5f3ede6cb9c575aec96a`.
- Full: 5,615,998 comparisons;
  `05c71f7394b7a59e84b6e86cab4c219d39d3e60c6c82ebd336d3c3157b3dc4ed`.
- Fixture journals: 180 comparisons;
  `9e43aeadfec234518f232eb63373be1a5196110ac0907d21afbe12cf4a1c0e88`.

Compatibility confirmed all 80 existing wire fixtures byte-identical, 17 legacy
exit records (`bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`)
and 1,055 retry/default-digest records
(`07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`).
No separate `make test-sim-exits` or live-journal replay was rerun this round;
the required full simulator corpus was exercised in the differential above.

11a obligations are recorded in `internal/queue/approvals.md`: isolate failing
probes (log and continue / avoid blocking other PRs); same-input probes journal
nothing and do not advance persisted freshness; `not(approved-reviews-by = X)`
with X present is classified as an approval wait; optionally narrow per-event
refresh work to approval-input events. The probe-isolation change remains an
11a obligation, not a production acquisition change in this round.

## Original verification before this fix round

The results below are historical evidence for the reviewed starting head. The
live deployment-pair replay was not rerun for this fix round; current validation
is listed above. Trial class remains **Hold**.

Baseline: current `origin/main`, `3e1536093441594ec6b1abd723426e29b8465ad5`.
Runtime Go code: `eeb459e6a88145eb92e72b987382ab8aaf04fab9`.
Clean replay candidate: `75fa3223daeea383282b2797adc1b566ca9dea6a` (only a Python
negative-test fixture correction after the runtime-code commit). Subsequent
changes expand the offline differential corpus and record this evidence; the Go
code and live effective configuration remain identical to the replay candidate.

The dedicated worktrees are `~/openmerge-stage-a-8` and the detached baseline
`~/openmerge-stage-a-8-main`. Heavy jobs were launched serially by this task.
Tests used the supplied test Postgres with isolated schemas; live DB, service
units, live refs and deployment were untouched. Exports were read in place and
were never copied into the repository.

Commands ran with `GOFLAGS=-p=2`, `GOMEMLIMIT=4GiB`, and `GOTMPDIR`/`GOCACHE`
under `~/.cache/om-sa-8-{tmp,go}`. Replay used `GOMEMLIMIT=6GiB` and Python
scratch files under `~/.cache`. The DB test environment was
`OPENMERGE_TEST_DATABASE_URL=postgres://postgres@localhost:54329/postgres?sslmode=disable`.

Final command results:

- `make check > ~/.cache/om-sa-8.log 2>&1; echo exit=$?`: **0**, including lint,
  full short/race suite and build. The build recorded version `eeb459e`.
- `make test-sim-exits`: **0**; required Team/Slow profiles and thresholds kept.
- `go test -race ./internal/queue ./internal/serve -run '^TestApproval' -count=1`:
  **0**, after the final gate-read freshness fence (queue 4.037s, serve 1.197s).
- `python3 scripts/check-phase2-compatibility.py ~/openmerge-stage-a-8-main`:
  **0**; all 80 existing wire fixtures, 17 legacy exit records and 1,055 retry/
  default-digest records byte-identical.
- `python3 scripts/check-legacy-plan-action-depths-1-4.py ~/openmerge-stage-a-8-main`:
  **0**. The same baseline-selected short and complete required full simulator
  corpus ran in both checkouts, including all required Team/Slow seeds and both
  cancellation capabilities. Only the optional adaptive sizing flag workload is
  excluded. Every observed state is planned at depths 1 and 4; ordered wire
  action bytes/keys, first next action, nil plans and next wakes match exactly.
  Short corpus: 227,662 comparisons, digest
  `71f1ae2108d98685b675aa5a158834bbd96595ce3c9e5f3ede6cb9c575aec96a`.
  Full corpus: 5,615,998 comparisons, digest
  `05c71f7394b7a59e84b6e86cab4c219d39d3e60c6c82ebd336d3c3157b3dc4ed`.
  Fixture comparisons: 180, digest
  `9e43aeadfec234518f232eb63373be1a5196110ac0907d21afbe12cf4a1c0e88`.
  Fixtures include the coherent legacy-block journal; registry fixtures are
  explicitly independent event journals. No plan/action/wake exceptions.

The live deployment-pair command was:

```sh
python3 scripts/check-live-journal-replay.py \
  --baseline /home/riels/openmerge-stage-a-8-main \
  --candidate /home/riels/openmerge-stage-a-8 \
  --events /home/riels/.local/share/openmerge/exports/events-20261018T051000Z.jsonl \
  --queues /home/riels/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl \
  --out /home/riels/.cache/om-sa-8-live-replay.json
```

Exit **0**, verdict/Gate 2/Gate 4/self-test **PASS**. All 2,159 checkpoints from
2,158 events have identical actions, snapshot hashes, legacy state and structural
state. All 8,676 boundary probes match; no cap was hit. Candidate repeat is
deterministic. The mandatory synthetic deployment pair matches all seven
checkpoints and 15 boundary probes; all 20 preflight tests pass.

Effective config digest remains
`e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`;
both binaries report `would_emit_config_applied=false`. No new event producer
runs during replay.

Required new structural exception: only `approvalWaits` absent in baseline to a
strictly nil, zero `map[queue.ChangeID]queue.ApprovalWait` in candidate. Populated,
non-nil empty and wrongly typed values fail the gate. The tool's inherited
`approvalFacts` and `outsidePushes` exceptions remain; neither is newly needed
against this main. No action, boundary or snapshot-hash exception is required.

Input hashes:

- events export: `9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`
- queues export: `418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`
- replay report: `e1bef1472a56518ce910807b952c4ea160ed0e49944c971122d708ce0d683150`

Earlier lint-only check attempts exited 2 and were corrected. The initial replay
exited 1 because the new nil-exception negative test used a malformed reflection
map key; the real and synthetic deployment pairs already matched. That test
fixture was corrected, tested directly (exit 0), committed, and the entire
replay rerun to the final passing result above.
