# Stage A 7b inert evidence

Fix round 1 baseline: `origin/main`,
`2d68d6ffad209ac60e63b40f7ed022f78d8e6851` (unchanged after explicit fetch of
main and `stage-a/7b-brief-views` only). Reviewed head: `e574d4a`.
Worktrees: `$HOME/openmerge-sa-7b` and `$HOME/openmerge-sa-7b-main`.

This delta remains **Inert**: public display DTOs, renderers, an authenticated
read-only status route and CLI. The historical policy provider and local Git
object reader add no State fields, events, actions, planner/executor/landing
changes, public config keys, or check-observation changes. Existing executable
queue sources and persisted fixtures remain unchanged. No deployment was made;
the Stage A 8 deployment Hold is not waived.

No live database, systemd unit, live status API, or live `openmerge/*` ref was
accessed. Tests use only the shared test Postgres on localhost:54329. Code and
tests make no real GitHub API calls. Replay uses the saved export identified
below; it does not export the live database. Temp/cache directories are under
`$HOME/.cache`, including Go temp/cache. Only this task's processes are managed.

## Fix round 1

- The brief route passes the handler's registered sanitizer into status
  projection. HTTP regressions cover API token, webhook secret and DB DSN
  diagnostics (including the password contained in that DSN).
- A Source policy provider loads the config commit pinned by `ConfigApplied`
  when the selected classification was recorded. `gitops.LocalConfig` reads
  existing immutable commit/tree/blob objects only: no remote resolution,
  fetch, lazy hydration, mutable-ref lookup, cache creation, ref update or GC.
  Tests prove a remote-only commit is unavailable and that a cached commit
  remains readable with the remote resolver disabled.
- Candidate approvals are parsed by the internal candidate loader; legacy
  configs use the unchanged public loader. Recomputed policy identity must
  match the classification's recorded `PolicyDigest`. The ConfigApplied digest
  pins effective configuration separately from its semantic policy component.
  Typed mismatches produce
  `policy_digest_mismatch`; missing commits/evidence produce `policy_unavailable`.
  Requirements come from `State.RecordedBrief(policy, recordedBasis)`; HTTP
  digest equality is checked independently against `brief.Digest` of that fact.
  A later config event cannot replace the classification's policy. Known legacy
  queues expose `approvals_required: false`, empty obligations and a full digest.
  Nil obligations always withhold the digest.
- Separate display budgets protect tests, obligations and provenance from hunks.
  Short enums, IDs, SHA values and reason/action codes have per-field bounds
  without consuming a shared content budget. The reviewers' 32-file, 884-byte
  path, eight 2,000-byte hunk case preserves machine fields and safety warnings.
- Per-change recovery includes exact-head trust, eligibility, recorded-base
  `rebase_onto`, named/linked failing `fix_check`, and `wait_for_approval` actions.
  Queue recovery reuses `OperatorClearable` and `BlockRecovery`: a writer uses
  `@openmerge refresh` for clearable sources; other sources recover automatically.
  All seven block sources are tested together so their actions cannot collapse.
- Known-empty stances/tests use arrays. HTTP coverage includes withdrawals,
  requests for changes, receipt bindings, saturation, new revisions, revision-0
  sources, escaping, truncation and registered-secret redaction.
- Truth values are strings; JSON labels intent as `claimed_intent`. Reviewer
  text puts safety warnings first and includes progress. Hunks are escaped
  multiline blocks. Revision-0 sources say "not counted: ambiguous attribution".
  Q4 recovery uses the fold's upper-bound-first rule. Saturation advice says
  retained barriers and command pins can keep the change saturated. Empty
  warning details are omitted. `change brief --help` exits 0 before an ID.

Remaining activation: 11a must record ingestion/accountability/eligibility and
static identity resolution, including team identities; unavailable recorded
resolution never triggers a fresh forge query here. 11b composes candidate keys
with the public config schema and the full effective legacy-plus-approval policy.
The public loader still rejects candidate keys. 12a owns operator readiness and
policy-gate wiring; this display does not claim a fresh readiness verdict or
turn recorded requirements into current authorization.

## Validation

Before **each** heavy job, the task's load gate waits for other `go test`,
`*.test`, compatibility and replay processes to finish, including PR 9's active
implementer. Jobs run serially with:

```sh
export GOFLAGS=-p=2 GOMEMLIMIT=4GiB GOMAXPROCS=2
export GOTMPDIR=$HOME/.cache/om-pr74-tmp GOCACHE=$HOME/.cache/go-build TMPDIR=$HOME/.cache
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
make check > ~/.cache/om-pr74-fix1.log 2>&1; echo exit=$?
make test-sim-exits > ~/.cache/om-pr74-fix1-sim.log 2>&1; echo exit=$?
python3 scripts/check-phase2-compatibility.py "$HOME/openmerge-sa-7b-main"
python3 scripts/check-live-journal-replay.py \
  --baseline "$HOME/openmerge-sa-7b-main" --candidate "$HOME/openmerge-sa-7b" \
  --events "$HOME/.cache/om-pr72-fix1-input/events.jsonl" \
  --queues "$HOME/.cache/om-pr72-fix1-input/queues.jsonl" \
  --strict-state --out "$HOME/.cache/om-pr74-fix1-deployment-pair.json"
```

Validated executable commit: `1caa10dda8f9c48b923d74373a3356a535e28db8`.
The final follow-up commit changes only this evidence note.

- `make check`: **exit 0**, zero lint issues, all race/short tests passed and
  binary built as `1caa10d`. Queue 277.620s; serve 141.649s; status 16.366s.
- `make test-sim-exits`: **exit 0**, 44.515s.
- Compatibility vs origin/main: **exit 0**. All 17 legacy exits and 1,055
  retry/default-digest records match. Stream hashes remain
  `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`
  and `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
- Strict deployment-pair replay: **exit 0**, gates 2/4 and verdict **PASS**.
  All 2,159 action/structural-state/snapshot checkpoints match; 8,676 boundary
  probes; derived determinism PASS; no non-allowlisted fields or divergence.
  Mandatory preflight exited 0; synthetic comparison matched 7/7 checkpoints
  and 15 probes. Baseline/candidate/repeat stream SHA-256:
  `2f6e9bb374e85f5a18d95cde65ef187d9bc130f27ddb972a8f80f404c2576aeb`.
  Effective config digest remains
  `e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`;
  `would_emit_config_applied` is false. Report:
  `$HOME/.cache/om-pr74-fix1-deployment-pair.json`.

Final logs: `om-pr74-fix1{,-sim,-compat,-replay}.log` under `$HOME/.cache`.
The final main/branch-only fetch still returned main `2d68d6f`; no rebase was
necessary. Earlier complete validation on `3c53a3f` also exited 0 for all four
jobs (`om-pr74-fix1-pre-final*.log`), before tightening historical-check advice.
An intermediate check was stopped after the new historical-failure regression
failed (**exit 143**, `om-pr74-fix1-regression-interrupted.log`); the first edit
had missed the executable condition. The next complete check exited **2**
(`om-pr74-fix1-fixture-failure.log`) because an older terminal-check fixture
lacked its now-required blocking context. After correcting that fixture, full
brief/status race tests exited **0** (`om-pr74-brief-status-final.log`) before
all four final jobs above. No other agent's process was stopped.
Final focused integration `om-pr74-focused7.log`: **exit 0**, all five packages
passed. `om-pr74-focused4.log` and the all-block-source/trust/rebase regression
run `om-pr74-focused5.log` also exited **0**.
Earlier focused attempts: **exit 1** for an old nil/empty-obligation fixture,
then **exit 1** for new legacy config fixtures missing required `merge_when`;
both corrected. `om-pr74-focused3.log`: **exit 0**. A waiting shell for focused6
was cancelled (**exit 143**, before Go started) because PR 9's broad process
matcher mistook its command arguments for an active test. A job-key wrapper
removed that wait-loop deadlock. No failures are reported as
successes. Original e574d4a evidence had make check, simulator exits,
compatibility and replay **exit 0**; those results do not substitute for this run.

All five v2 receipt vectors remain unchanged, as do v1 vectors and
`internal/queue/testdata/brief-facts.json`. Presentation is excluded from
canonical serialization; a rendering change cannot invalidate a receipt.
All **88** wire fixtures match origin/main recursively. Sorted repo-relative path +
NUL + bytes SHA-256: `ac1d5fa4f4bee6c65c28a52439fe868c5a8cc0635c9279f1bb73fbdb8dc8f9f9`.
The independent receipt/wire comparisons exited **0**.

## Saved journal identity

The saved export contains 2,158 events and one queue. Event SHA-256:
`9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`.
Queue SHA-256:
`418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`.
Strict replay compares each revision, timer boundary and next action with no
new State fields expected and snapshot-byte equality required.

## Fix round 2

Round 2 supersedes the policy availability claims above. The policy-only
candidate schema cannot reconstruct the full effective queue digest. Matching
its semantic component is insufficient proof of ConfigApplied.Digest, so it now
withholds requirements and the brief digest with `policy_unavailable`. Semantic
mismatches still produce `policy_digest_mismatch`. Legacy journals have no
semantic approval component and also report unavailable; unresolved group
identities do not masquerade as mismatches. Full effective config composition
remains 11b work. The loader never fetches or substitutes current policy.

- `TestAstraR2EffectiveDigestMismatch` preserves Astra's exact zero-digest
  HTTP reproduction for candidate and legacy journals.
  `TestBriefHTTPHistoricalPolicyRequiresEffectiveDigest`,
  `TestBriefHTTPPolicyUnavailableAndMismatch`, and
  `TestBriefUnresolvableGroupIsUnavailable` cover availability, semantic mismatch,
  historical commit selection, and missing identity recovery.
- `TestAstraR2SupersededCheckHTTP` retains failed lint attempt 1, successful
  attempt 2, and failing unit tests; only the current unit failure gets repair
  advice. Both display warnings and operator check recovery exclude rerun rows.
- `TestLocalConfigBusyCacheReturnsWithoutWaiting` holds the shared cache lock
  and verifies immediate unavailability within a 200 ms context deadline.
  Local reads use TryLock and check cancellation before acquisition.
- `TestBriefHTTPFinalDisplaySanitizesIntentHunksAndPaths` covers registered
  opaque secrets in title/body, hunks and paths for all three audiences.
  Every final DTO string passes through the handler sanitizer after projection;
  `display_redacted` identifies display changes. The sanitizer also normalizes
  whitespace and applies its diagnostic length bound.
  `TestBriefDisplaySanitizationPreservesRecordedDigest` checks a full digest
  before/after display sanitization; `TestAstraR2HandlerDoesNotMutateJournal`
  checks journal immutability for all audiences.
- `TestViewKnownEmptyStancesRenderNoneRecorded` covers known-empty text.
  README/API now describe pinned loading, fail-closed policy identity, Truth
  strings, claimed_intent, structured recovery, and separate display budgets.

Only brief/status display, immutable local reads, documentation and tests change;
no queue/store/planner/executor behavior or persisted fixtures change.

Validated executable commit: `31ce6ec52d21925353e921a459e49afe54b51493`.
The final commit adds only these validation results. Final explicit main/PR-branch
fetch left main at `2d68d6ffad209ac60e63b40f7ed022f78d8e6851`; no rebase
was necessary. The clean baseline checkout remains `$HOME/openmerge-sa-7b-main`.

All heavy jobs ran serially after the process load gate, with GOFLAGS=-p=2,
GOMEMLIMIT=4GiB (6GiB for replay), GOMAXPROCS=2, shared test Postgres only,
and Go/temp/cache directories under `$HOME/.cache`.

- `make check`: **exit 0**, zero lint issues, all race/short tests passed;
  queue 276.564s, serve 147.108s, status 16.533s. Binary built as `31ce6ec`.
- `make test-sim-exits`: **exit 0**, 44.306s.
- Compatibility vs origin/main: **exit 0**; 17 legacy exits and 1,055
  retry/default-digest records match. Stream hashes remain
  `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`
  and `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
  Its 83 wire-v1 fixtures match; the separate recursive comparison of all 88
  wire fixtures and existing canonical brief vectors also **exited 0**.
- Strict deployment-pair replay: **exit 0**, gates 2/4 and verdict **PASS**.
  All 2,159 action/structural-state/snapshot checkpoints match, with 8,676
  boundary probes and derived determinism PASS. Mandatory 21-test preflight
  **exited 0**. Effective config digest remains
  `e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`.
  Saved input event/queue identity is unchanged from the section above.
  Report: `$HOME/.cache/om-pr74-fix2-deployment-pair.json`.

Final gate logs: `om-pr74-fix2-{check,sim,compat,replay}.log` under
`$HOME/.cache`; orchestration log `om-pr74-fix2-gates.log` **exited 0**.
Focused logs, in execution order:
`om-pr74-fix2-focused.log` **exit 1** (new redaction fixtures had invalid
hunk ranges/path inventory, so classification was rejected),
`om-pr74-fix2-focused-final.log` **exit 0**,
`om-pr74-fix2-focused-complete.log` **exit 1** (new full-digest test changed
intent text without updating its intent digest), and
`om-pr74-fix2-focused-verified.log` **exit 0** after correcting the fixture.
No job was interrupted or any other agent's process killed.

## Fix round 3

This round supersedes round 2's blanket policy unavailability and final reflective
sanitizer. Main advanced through PR #75 to
`15aa69929e89970fb49381493398814b8793567a`; the complete PR was rebased onto it.
The baseline checkout is detached at that commit.

- Legacy pinned local config is loaded with the public loader, resolved for the
  queue base branch using `ResolveBranch`, and compared using the current
  `Queue.Digest`. Explicit DeleteHeadBranch is respected; absent overrides accept
  either deletion setting. Matching legacy requirements are known-empty `[]`,
  approvals_required is false, the full brief digest is emitted, and the response
  says “approvals not required by the recorded policy”. Old-binary planner JSON
  differences and zero/forged pins fail with policy_digest_mismatch. Missing
  objects or load/resolve failures remain policy_unavailable.
  Tests: `TestR3LegacyEffectiveDigest`,
  `TestBriefHTTPHistoricalPolicyRequiresEffectiveDigest`,
  `TestLocalConfigNeverHydratesMissingObjects` (now moves the remote main ref),
  and `TestBriefPolicyUsesOnlyPinnedLocalReader`.
- Explicit 11b obligation: define, reconstruct and verify the full recorded-policy
  digest composition, including effective legacy queue settings and
  WithPolicyDigest, before candidate approval requirements or a digest may be
  returned. Candidates remain policy_unavailable with a distinct 11b reason and
  implement_11b_policy_composition action, never restore_recorded_policy advice.
  `TestR3PolicyRecovery` covers candidate, missing-object and transient recovery;
  policy-only unavailability never recommends record_evidence.
- Complete display inputs are redacted before bounding. Both 336 ampersands and
  334 less-than characters followed by the registered opaque secret, plus the
  hunk variant, are covered by `TestR3TruncatedSecret` and
  `TestR3SanitizeBeforeBoundsAndPreserveHunks`. The shared multiline sanitizer
  preserves tabs, indentation and newlines. Its unbounded redaction path allows
  briefs to apply their own 2,048-byte escaped cap; the forge diagnostic wrapper
  retains its 1,024-rune-per-line cap. display_redacted reports redaction only,
  independently of bounds or whitespace. Tests:
  `TestSanitizeTextPreservesDiffAndReportsOnlyRedaction` and
  `TestBriefDisplaySanitizationPreservesRecordedDigest`.
- Explicit display sanitation excludes digests, IDs, SHAs, action/reason codes and
  enums. Tests: `TestR3MachineDigest` and
  `TestR3MachineFieldsPreservedDuringProjection`.
- Worker selection reads its immutable key before acquiring a worker mutex.
  Registry and selected-worker acquisition use TryRLock/TryLock with context
  checks; no unrelated worker lock is acquired. Cache/worker/registry contention
  returns policy_busy with retry advice. Tests:
  `TestR3BriefPolicyBusyUnrelatedWorker`,
  `TestR3BriefPolicyBusyTargetAndRegistry`,
  `TestLocalConfigBusyCacheReturnsWithoutWaiting`, `TestR3PolicyRecovery`.
- Only the current batch and non-superseded producer attempt yield fix_check
  actions. Historic runs remain visible, and current timeout evidence is retained.
  Tests: `TestR3InvalidatedBatch`, `TestR3ManualReruns`,
  `TestAstraR2SupersededCheckHTTP`.

The changes remain inert. No live DB, status API, systemd, openmerge/* refs,
GitHub calls in code/tests, public candidate-key activation, or deployment.
The Stage A 8 deployment Hold remains in force.

Round-3 validation uses the same saved journal export and shared test Postgres
as above. Every heavy job waits for other agents' Go tests/test binaries/replay,
then runs serially with GOFLAGS=-p=2, GOMEMLIMIT=4GiB (6GiB for replay),
GOMAXPROCS=2, and Go/temp/cache paths under ~/.cache. The load-gated runner is
`~/.cache/om74-heavy.py`.

Validated executable commit: `d8f26ebbc4db67c4c9c918e1eb8834f48f87bda7`.

- Focused runs: **exit 0**, then **exit 1** for two newly added fixture mistakes
  (changed intent without updating its digest; assuming a fixture SHA had at
  least 16 characters), then **exit 0** after correction on the rebased code.
  Failed log: `om-pr74-fix3-focused-fixtures.log`; final:
  `om-pr74-fix3-focused.log`. All five selected packages passed.
- Initial `make check`: **exit 2** at lint only, for missing exported-method
  documentation on BusyError.Temporary; corrected with a comment.
  Log: `om-pr74-fix3-check-lint.log`. An earlier gofmt attempt **exited 2** for
  an extra parenthesis in the new test assertion, corrected before running Go.
- Final `make check`: **exit 0**, zero lint issues, all race/short tests passed,
  binary built at d8f26eb. Queue 279.102s; serve 146.982s; status 16.844s.
  Log: `om-pr74-fix3-check.log`.
- `make test-sim-exits`: **exit 0**, 44.230s.
  Log: `om-pr74-fix3-sim.log`.
- Independent comparison against rebased main: **exit 0** for all 90 wire
  fixtures and all 16 canonical vector files (five v2 and three v1 pairs).
  Saved event/queue hashes remain exactly those in Saved journal identity above.
- Compatibility vs rebased origin/main: **exit 0**; 17 legacy exits and 1,055
  retry/default-digest records match with the same hashes as earlier rounds.
  Its 84 wire-v1 fixture comparison also passed. Log:
  `om-pr74-fix3-compat.log`.
- First replay invocation: **exit 2** before replay because this evidence note
  was uncommitted; no probe ran. Log: `om-pr74-fix3-replay-dirty.log`.
  Documentation was committed before retrying.
- Final strict deployment-pair replay: **exit 0**, gates 2/4 and verdict **PASS**.
  All 2,159 action/structural-state/snapshot checkpoints match, with 8,676
  boundary probes and derived determinism PASS. Mandatory 22-test preflight
  **exited 0**. Candidate checkout was clean at
  `11bb6a04c9c7aa1dcf972c5c7dade02c738c01ca` (only evidence changed after the
  executable commit); baseline was clean at `15aa699`. Effective config digest
  remains `e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`.
  Report: `~/.cache/om-pr74-fix3-deployment-pair.json`;
  log: `om-pr74-fix3-replay.log`.

The final explicit fetch of main and the PR branch only still returned main
`15aa69929e89970fb49381493398814b8793567a`; no second rebase was necessary.
The final follow-up commit changes only this evidence note. All PR commits were
checked for the required Riel author and exact final sign-off/co-author trailers
(**exit 0**). No job was interrupted and no other agent's process was killed.
