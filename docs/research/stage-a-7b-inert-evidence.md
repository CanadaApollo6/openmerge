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

Final code commit and required job results will be recorded after execution.
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
All **88** wire fixtures match origin/main recursively. Sorted relative path +
NUL + bytes SHA-256: `98c0d2fa8fd5aaf643e8eafafeeb6391c1b3f120984c702f2e091e6ea3c13f58`.
The independent receipt/wire comparisons exited **0**.

## Saved journal identity

The saved export contains 2,158 events and one queue. Event SHA-256:
`9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`.
Queue SHA-256:
`418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`.
Strict replay compares each revision, timer boundary and next action with no
new State fields expected and snapshot-byte equality required.
