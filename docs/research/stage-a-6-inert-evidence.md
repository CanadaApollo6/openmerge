# Stage A 6 inert component evidence

Baseline: `c2d9b3aa17113b7e208ceac1be53aa42fe86dce8` (`origin/main` at branch creation).
Implementation: `afd14dc449c2aff38c89602a17cc7b2af2d436f6`.
Dedicated worktree: `$HOME/openmerge-stage-a-6`, branch `stage-a/6-stances`.
Detached baseline: `$HOME/.cache/om-sa-6-baseline`. Go: `go1.27.2 linux/amd64`.
The subsequent evidence commit changes only this note.

This is merge/component evidence, not authorization to deploy. No live database,
journal, service unit, App permission, configuration or `openmerge/*` ref was
read or changed. Git fetched only main. Tests use the shared test Postgres at
`localhost:54329`, with per-test isolation, and no real GitHub API calls.
A deployment still requires the operator's exported live-journal shadow replay
and live effective-config comparison under ADR 0007's trial gates.

## Scope and inert boundary

Four additive wire-v1 event types record accountability, canonical intent,
review stances and eligibility. There are exactly four new registry fixtures;
eight extra variants live outside the event/action glob. Snapshot v5 retains
first-observation barriers and source-to-revision attribution and decodes old
versions 2–4. Compaction is not enabled. No actions, loader/schema keys, planner,
executor, landing gate or existing condition wiring change.

`Apply` folds these observations into a copy-on-write side projection without
advancing planner revisions. New facts and revision boundaries change the
per-change approval fingerprint without changing CI keys, timers or
classification. Tests compare next plans, immutable prior state, snapshot-plus-tail
and journal replay. The new private `queue.State.approvalFacts` field remains
**nil** for queues without these observations. At the next deployment,
`scripts/check-live-journal-replay.py` needs a reviewed nil-only state exception
for `approvalFacts` (alongside any still-pending exceptions for earlier Stage A fields).
This PR does not relax that script's live replay checks.

```sh
rg -n 'ApprovalFacts|approvalFacts|ReviewStanceObserved|ChangeIntentObserved|ChangeAccountabilityObserved|ApprovalEligibilityObserved|EvaluateApprovals|NativeStance' \
  internal/serve internal/executor cmd
```

The search returns no matches (rg exit 1). All new observation creation,
conversion and evaluation calls are component functions or tests; none is a
production caller. Public activation and fresh external reads remain 11a/11b.

All **80** baseline wire files, including pre-existing variants, are byte-identical.
SHA-256 over sorted baseline paths followed by NUL and file bytes:
`f32728989e704e31cae160f82d12113cc3007988a73b628ab5dd1d598fb8eb54`.
No existing fixture/digest golden changes. The implementation and contract notes
add 1,703 lines and remove 6; the final PR count also includes this evidence note
and remains below the ADR's 3,000-line ceiling.

## Fold and fresh eligibility evidence

`internal/queue/approvals.md` specifies source namespaces, conservative
head-epoch attribution, pinned first-observation bounds and recovery. Native
reviews never acquire intent/brief bindings. A read ID witnesses blocker
re-observation before clearing; equality or unavailable comparable bounds fail
closed. Polls/restart never renew the fallback barrier. Old sources retain their
attribution through H1 → H2 → H1; ambiguous first discoveries are historical.
Receipt ordering tests model shared stance types; receipt command authentication
and wire acceptance belong to 7a.

`EvaluateApprovals` immediately replaces both owner projections and known flags
using `classification.ResolveOwnerEligibility` over stored governing evidence
with a fresh resolver built from the current observation's recorded owner-token
pools. Owner membership changes therefore also change the approval fingerprint.
Stored classification-time `Fresh=true` pools and stored
eligibility never authorize a decision. Tests cover removed owners, revoked
writers, canonical `GitHubUserID` spellings, unavailable membership, exclusions,
alias deduplication and eligible change-request blockers. A classification-time
membership-only unknown resolves successfully at decision time without changing
the durable classification. The contract note explains why a rule explicitly
matching the historical `unknown` facet remains conservative until a normal new
classification generation refreshes that semantic evidence.

## Verification commands and results

Checks run serially, with Go temporary/cache files under `$HOME/.cache`:

```sh
export GOFLAGS=-p=2 GOMAXPROCS=2 GOMEMLIMIT=4GiB
export GOTMPDIR=$HOME/.cache GOCACHE=$HOME/.cache/go-build TMPDIR=$HOME/.cache
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
make check > ~/.cache/om-sa-6.log 2>&1; echo exit=$?
make test-sim-exits > ~/.cache/om-sa-6-sim.log 2>&1; echo exit=$?
python3 scripts/check-phase2-compatibility.py "$HOME/.cache/om-sa-6-baseline" \
  > ~/.cache/om-sa-6-compat.log 2>&1; echo exit=$?
python3 scripts/check-stage-a-identity-compatibility.py "$HOME/.cache/om-sa-6-baseline" \
  > ~/.cache/om-sa-6-replay.log 2>&1; echo exit=$?
```

The initial `make check` exited **2** on eight lint findings (export comments and
one test variable shadowing `copy`). They were corrected before the implementation
commit. Its preserved log is `~/.cache/om-sa-6-initial-lint.log`.
The focused `go test ./internal/queue` run exited **0**.

The final `make check` exits **0** (format, lint, race-enabled short suite, build).
The first successful full check preceded the source-clock regression test;
`~/.cache/om-sa-6-first-pass.log` preserves it. The final log above validates the
amended implementation, including recorded owner pools, source-clock ordering
and the late lower-ID blocker regression tests. Intermediate successful checks
are also preserved as `om-sa-6-before-owner-pools.log` and
`om-sa-6-before-late-blocker.log`; those are not the final validation log.

`check-phase2-compatibility.py` exits **0** against the detached `origin/main`
baseline. Its unchanged harness reports:

- **17** legacy exit records byte-identical, SHA-256
  `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`.
- **1,055** depth-1 retry envelope/default-digest records byte-identical, SHA-256
  `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
- **76** existing registry wire fixtures byte-identical. The independent
  all-files comparison above also includes the four old variants (80 total).

The final `make test-sim-exits` exits **0** (47.465 s in the retained log).
`check-stage-a-identity-compatibility.py` exits **0**, comparing deterministic
synthetic journals and next actions at **every** revision:

- Depth 1: **682** events; journal SHA-256
  `75b4bcfbcf8ef89be731a6109b0b6051639c1476b530a3f346a0ff82716f577e`.
- Depth 4: **1,071** events; journal SHA-256
  `ab5384c42a8038d3223aad2c84832b372b8de7f3bd51de21671756d5cec69875`.
- **1,753** byte-identical event/action checkpoints; combined SHA-256
  `59e8f223916c504b10a20ead4d300f861f776f9f39dd91248f3294981dc2a6de`.

The unchanged harness confirms no `change_*` emission. Its full journal-byte and
checkpoint comparison also detects any other added event. This is synthetic
merge evidence; it is not a replay of the operator's live exported journal.

## Reproducible binary identities

In each checkout, with the corresponding output path:

```sh
CGO_ENABLED=0 go build -trimpath -buildvcs=false \
  -ldflags '-X main.version=stage-a-6-evidence' \
  -o "$HOME/.cache/om-sa-6-new" ./cmd/openmerge
# Baseline output: $HOME/.cache/om-sa-6-old
sha256sum "$HOME/.cache/om-sa-6-old" "$HOME/.cache/om-sa-6-new"
```

Both builds exit **0**. The fixed version/path-independent builds hash to:

- Baseline: `0568ce802761b5c577d1dd4303c543f1113f6e3f68a8ec0859bf11e404f2bd1e`.
- Candidate: `a9df64c7ab3fbd046da4e4f3d8055f1d5b36a9424d3c93b612ffcecc3738d7d7`.

The binaries differ because the new codec/projection code is linked. Inertness
is supported by call-site isolation, nil legacy state, unchanged configuration
and wire evidence, simulator exits and differential next-action replay, rather
than binary identity. Live deployment gates remain outstanding as stated above.
