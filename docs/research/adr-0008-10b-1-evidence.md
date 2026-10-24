# ADR 0008 PR 10b-1 execution evidence

Inert by behavior, transitively **Hold**: no deploy before period B drains. The 10b-1b follow-up adds historical abandonment, failed checks, retry dispatch history and unsuccessful attempts. 10b-2 is delivered in #153; PR 11 remains deferred. Normal Go testing uses OPENMERGE_TEST_DATABASE_URL with owned scratch resources and the standard local/CI routing guard. OPENMERGE_10B_OUT, when set, is a caller-supplied path that must not exist; its parent must already exist. The test uses os.Mkdir to create the directory with mode 0700 and refuses reuse. Restore is expected to skip in CI: pg clients 16.15 cannot dump service 18.6; restore is proven locally only.
Assertions cover retained event/queue prefixes from first adoption through transitions; ordered pins openmerge,github,openmerge; two landings and three decisions; stable verification/receipt/retirement counts; eleven typed pre-window dependencies; exact CLI digest/stdout and failure diagnostics; actual dependency deletion; effective PUBLIC-inclusive read privileges and denied-write SQLSTATE 42501; confirmed Git SHAs/parents; restored bytes and inventory. No unsuccessful-build read-back claim. PG15+ schema CREATE revoke is normally a default no-op. Owned DBs are dropped before roles, with zero leftovers asserted.
Restart now latches the NEW server's production cycle-finished log after health publication, then checks its discovered retired/not-ready/not-watched queue and no journal/action growth. It never samples InCycle or NextDeadline. Other waits inspect durable state/API results; all use a three-minute deadline and 100ms polling, with 250ms lifecycle-server polling; genesis bootstrap polls at 10ms.
Replay commands (export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3 and prepend $GOROOT/bin to PATH; set OPENMERGE_TEST_DATABASE_URL=postgresql://postgres@localhost:54329/postgres?sslmode=disable and TMPDIR=$HOME/.cache/om-10b-build): `flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB golangci-lint run ./cmd/openmerge/... ./internal/config/...`; `flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB GOMAXPROCS=2 go test -race -short -count=10 -timeout=30m -v ./cmd/openmerge -run '^TestRetentionLifecycle$'`.
2026-10-24 UTC, base 0742db6: lint exit 0 (zero issues); ten race runs under GOMAXPROCS=2 exit 0, durations 58.87/58.57/57.93/58.30/58.58/58.01/58.15/58.02/58.44/58.38s (min/median/mean/max 57.93/58.34/58.325/58.87s), all local restore legs passed. G-E exit 1 with `missing lifecycle start 5`; production retired-Watched mutant exit 1 with `timed out retired server discovery and cycle`; both restored byte-for-byte. Source SHA-256 `0ad5bac1211115ec8648b2c248a5d2feb2786e25a074f90f34fa52f8ce79a37e`; stress log `4b890c6232730c879440bd1e186f620abe74768178dc7aff55a23e5c2945070e`; mutant results `e7b31bb95dbe049457c6bd618babd018abaf319c3f6d80ee8f7fac31a55aaaf3`. Final race suite exit 0: cmd/config/store/audit 120.246/1.613/70.725/22.008s; command with the same env/wrapper: `go test -race -short -count=1 -timeout=15m ./cmd/openmerge ./internal/config ./internal/store ./internal/audit`. Package log SHA-256 `08b0dbb96939a52eec3f2b33823d01588d129a15710f6d3b698f7be5bdc6bb5a`. `git diff --check` clean.
Artifacts/logs are private outputs owned/read by riels (uid 1000), under /home/riels/.cache/om-10b-build/flake-*.log; matching local PostgreSQL/clients 18.6. Optional summary.json records CLI receipts and manifest digests. These outputs are test evidence, not independent custody.

## PR 10b-1b historical-family acceptance

The fixture enables one check retry. Through the real lifecycle server, store,
planner and executor it produces `checks_observed`, `check_rerun_requested`,
`check_rerun_dispatch_progress`, `check_rerun_dispatched`, and
`checks_failure_confirmed`. The first batch exhausts its retry and is dequeued;
a new queue command starts a second batch, whose rerun succeeds. A test Git
adapter rejects its first atomic landing push with `ErrLeaseRejected`, leaving
the private base untouched. Production fence recovery emits `landing_abandoned`
and rebuilds before the eventual successful landing. These are unsuccessful
attempts, with durable start/settlement and failure/dequeue relationships checked.
No known event family is appended directly. The GitHub-mode landing keeps
legacy checks enabled and asserts one `checks_completed` event, alongside
the earlier retry-aware history.

Forge observations and CI completion remain fake. The fake forge lacks the
workflow progress callback, so the test driver implements the provider protocol:
intent callback, fake rerun, accepted callback, then a deterministic newer
failed/passing check observation. Journal events come from the production
executor's callback, not from the fixture. The production Git implementation
still constructs and reads the private test refs and fence.

The full-history export checks every row of each of the six original historical event kinds and `landing_checks_recorded`
against the original JSONB payload hash. Its seven-entry assertion matrix refuses
a missing family assertion. Exhausted and abandoned attempt relationships are
checked separately. The narrow export starts at the first landing decision:
retry observations, request, dispatch progress and dispatch result must
appear as pre-window dependencies, alongside the original seven dependency
kinds. The narrow export and each narrow replay, including restore, must contain
both scope and dependency rows. Their recursive references must resolve to
retained sources. Independent SQL over the journal pins exact event IDs,
batches, attempts, tested SHAs, retry generations and check run identities;
exported references must match those expectations. Production replay also
recomputes the exact closure. Both manifests replay byte-for-byte across
openmerge → github → openmerge, retirement, restart and restored backup. Full
journal-prefix comparisons continue to protect the original JSONB rows.

Unknown historical kinds have no production emitter: `queue.DecodeEvent`
rejects unknown types, and a fabricated event would make this lifecycle queue
unreadable on restart. This optional family is therefore not injected into the
production-path lifecycle acceptance; there is no direct-append fallback or
unknown-kind coverage claim in this follow-up.

The cycle logger preserves its latch through `WithAttrs` and `WithGroup`, with
a focused derived-logger test. Server-start failures use `t.Error`, so the
independent two-landings check still runs. Output-directory reuse fails before
any output-file writes.

Initial candidate, 2026-10-24 UTC, base `a17e1d5` fetched from `origin/main`: repository-wide
`golangci-lint run` exits 0 with zero issues. Ten race-enabled lifecycle runs
under `GOMAXPROCS=2` pass, including all ten local restore legs. The test and an
owned Python CPU contender share affinity `0,1`; the contender repeatedly
computes `sum(i*i for i in range(100000))`. Its observed CPU use is 64–78%,
and it is terminated and reaped after the foreground test command. The PID
is absent afterward. Durations are 123.85, 131.77, 129.75, 138.08, 134.47,
135.74, 143.72, 138.47, 136.46, and 141.60 seconds, with median 136.10 seconds.
The command uses the GOROOT, PATH, TMPDIR, database URL and lock wrapper above:
`taskset -c 0,1 flock /home/riels/.cache/openmerge-heavy.lock env GOFLAGS=-p=2 GOMEMLIMIT=4GiB GOMAXPROCS=2 go test -race -short -v -count=10 -timeout=30m ./cmd/openmerge -run '^TestRetention(CycleLogDerivation|Lifecycle)$'`.

An ordinary-load local race timing pair uses the same lock, toolchain and
scratch PostgreSQL, with restore enabled on both sides. The original
`origin/main` acceptance takes 57.93 seconds; the extended acceptance takes
71.42 seconds, a +13.49-second estimate for the added CI work. This is a
single local pair, not a CI measurement or a statistically established
regression. The contention timings measure stability, not CI cost.

All 12 mutation checks exit 1 with the expected diagnostic. Removing each of
the six family entries from the retention assertion matrix fails with
`historical retention assertion matrix incomplete`. Omitting each family's
rows from the loaded export selection fails with that family's
`historical family ... missing event` diagnostic. The mutations change only
the acceptance test, never production code or the source journal. Source
restoration is checked against SHA-256
`7a16ac39d26c4f7b5f4403564dbed4508cd6260f8725c5ea67e8112266752356`.
The foreground runner and per-mutant logs are in
`/home/riels/.cache/om-10b-build/historical-mutations.py` and
`historical-mutant-*.log`, with results in `historical-mutations.json`.

After preserving the legacy GitHub check-result leg, the final fixture passes
another ten race-enabled runs with `GOMAXPROCS=2`. Three owned Python CPU
contenders and the test share affinity `0,1,2,3`. The test command is the
initial stress command above with that affinity; the foreground runner is
`/home/riels/.cache/om-10b-build/historical-stress-final.py`.
Durations are 128.92, 135.02, 137.84, 135.96, 138.50, 139.08, 139.40,
145.57, 139.17, and 135.68 seconds, with median 138.17 seconds. All ten local
restore legs pass, with zero race reports. All three contenders are terminated,
waited for, and confirmed absent from `/proc` afterward. Final stress output
and process results are `historical-stress-final.log` and
`historical-stress-final-process.json` under the same cache directory.

Final verification exits 0: `go test -race -short -v -count=1 -timeout=15m ./cmd/openmerge ./internal/config ./internal/store ./internal/audit`, with the standard environment and lock wrapper. Package durations are 134.204, 1.598, 70.422, and 21.612 seconds respectively. Repository-wide lint again reports zero issues. `git diff --check` is clean.
The standalone final run preserves its real inventories, private Git remote,
exports, restored replays and CLI summary in the fresh owned mode-0700 directory
`/home/riels/.cache/om-10b-build/historical-final-artifacts`. Independent read-back
confirms the inventory prefixes, six historical families, one legacy check-result
event, and byte equality of all five full-history replays.

Final log SHA-256 values under `/home/riels/.cache/om-10b-build`:

- `historical-stress-final.log`: `c1a63c72b5a10cfac20afb03fe0c9b4a58d065bf372cf8b98c25c8cc5e9065fd`

- `historical-mutations.json`: `7977216e571697c5bf10943b9e947f99509340f3619d6f21d8ad7d5fa788464a`

- `historical-timing-final.log`: `edac55baa9f13637aa872fe92a52516c0c31616650d6a7bcfdbcb9ba0a2c8388`

- `historical-lint-final.log`: `e92606b0bf483111dff0a120c315ea165821348f31365020e2468a0059095c47`

- `historical-packages-final.log`: `915575b8682370c26c1d05e265a9bae21dd54a3ff599adc940adc8ad22a2d075`

## Round 1 review corrections

The original full-history walk was vacuous because every selected row was a
scope root. The walk now runs on the narrow `before` export and every narrow
source, reader, transition, restart and restored replay. It rejects all-scope
input, dangling references and unreachable dependencies. Focused graph cases
exercise recursive cycles, dangling edges, orphans and missing roots.

An independent journal query identifies each ordinary or abandoned settlement,
its latest preceding start, and the decision/check records for that exact
batch and attempt. Assertions pin decision → checks, decision → start,
decision → settlement, start → decision, start → settlement and settlement →
start, including the abandoned attempt's owner identity. The abandoned check
record must reference the exact pre-window observation, request, latest
progress and dispatch rows, with journal-derived generation and run identity.
Expected edge IDs and identities are not derived from the exporter's reference list.

`landing_checks_recorded` joins the retained-family matrix. `checks_completed`
and `checks_failure_confirmed` have scope-retention claims only. No remedy
decision is produced, and remedy coverage remains deferred. Design-table
markers now distinguish those limits. Payload hashes remain defence in depth. Export's own integrity checks run
first; this evidence does not attribute payload-mutant failures to the local
hash assertion.
Event-count keys are sorted and all mismatches use `t.Error`, so every wrong
count is reported deterministically. The caller-supplied output path requires
an existing parent directory.

The four requested production resolver mutations each exit 1 at an independent
assertion on the first narrow export, rather than at Export:

- Skip all abandonment reference/dependency lookups: `missing independent closure edge landing_decision_recorded -> landing_abandoned`.

- Remove abandoned-settlement handling: `missing independent closure edge landing_abandoned -> landing_started`.

- Drop the decision-to-landing-check lookup: `missing independent closure edge landing_decision_recorded -> landing_checks_recorded`.

- Drop the decision-to-start lookup: `missing independent closure edge landing_decision_recorded -> landing_started`.

Disabling the reachability walk also fails the focused graph test with
`valid=false error=<nil>`. Every temporary production/test mutation is restored
byte-for-byte; production source is absent from the final diff. The foreground
runner is `/home/riels/.cache/om-10b-build/r1-production-mutants.py`, with
results in `r1-production-mutants.json` and diagnostics in `r1-mutant-*.log`.

Round 1's seven-family matrix passes all fourteen assertion-removal and
selection-row-omission mutations. A fresh local timing pair restores exactly
the c54bf24 acceptance file for the baseline, runs only `TestRetentionLifecycle`,
then restores the corrected file byte-for-byte. The baseline takes 72.32 seconds
and this revision takes 72.09 seconds, a -0.23-second observed delta. One local
pair does not establish a speedup or a CI timing result. Both runs pass local
restore. The corrected standalone run retains artifacts in the fresh owned
`/home/riels/.cache/om-10b-build/r1-final-artifacts` directory.

Round 1 stress passes ten race-enabled lifecycle runs with `GOMAXPROCS=2`
and three owned CPU contenders sharing affinity `0,1,2,3`. All ten local restore
legs and ten graph-test runs pass, with zero race reports. Durations are
127.70, 136.34, 135.17, 143.79, 137.38, 138.68, 145.98, 138.03, 138.61, 141.10 seconds; median 138.32 seconds.
All three contenders are terminated, waited for and confirmed absent from
`/proc`. The foreground runner is `/home/riels/.cache/om-10b-build/r1-verify.py`,
with contention in `r1-stress.py` and per-family mutations in `r1-family-mutants.py`.
Repository-wide `golangci-lint run` exits 0 with zero issues.

Final Round 1 race verification exits 0 with the same lock/environment wrapper:
`go test -race -short -v -count=1 -timeout=15m ./cmd/openmerge ./internal/config ./internal/store ./internal/audit`.
Package durations are 136.668, 1.598, 70.491 and 21.669 seconds respectively.
`git diff --check` is clean. Final test source SHA-256 values are
`4e184cf6eef75d480adc61bece10bbc2df91b0d0de7748d9713287477f92151e`
for `retention_acceptance_test.go` and
`25db49e88bb3736f6d4047a0f9d623c5e51a2ad52f7da772e9020a886420c0d4`
for `retention_closure_test.go`.

Round 1 log/result SHA-256 values under `/home/riels/.cache/om-10b-build`:

- `r1-production-mutants.json`: `b9c492d419d9d6a44a0e563e0a0035930cff675db1bbd448d9264a31d961f8b1`

- `r1-family-mutants.json`: `58f9f7bb9c88fb6153615ea595fbfe4249368b4f5dde55472bcad3e222a75661`

- `r1-stress.log`: `8a7f8242573cc62cf7330432b44164ecccdcf92754d8029719232435ee08feb0`

- `r1-packages.log`: `0e06ea3ad80781bf89d628a3b3a4aa953e582ef9c4169451bf72d2e0fe136373`
