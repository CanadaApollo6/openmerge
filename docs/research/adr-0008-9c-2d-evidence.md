# ADR 0008 PR 9c-2d evidence

Trial class: **Hold**. Baseline: local main `d9ec975` (#137/#138/#140 and
#131/#139 landed). Go implementation: `3302b0c33b1c02ba3458d923adddcb39b53e54cd`;
verifier grouping: `cadd432`. Those original receipts cover the pre-review sources;
round-1 changes and their verification are recorded below.
This slice adds acceptance tests and an offline verifier only. **No new production
bug found or fixed; no production code changed.** Public rejection and production opt-out
remain; no deployment, organization acceptance, queueing or merging is claimed.

## Acceptance matrix

All new tests have the `TestAuthorityAcceptance` prefix and run under `-short`.
They consume the existing 2a evidence, 2b push/lifecycle and 2c completion contracts;
no ownership or activation boundary moves.

| Scenario | Test suffix (package) | Actual read-back / comparison |
| --- | --- | --- |
| Default rename/deletion, both modes | `DefaultBranchTransitions` (`serve`) | Real default deletion/restoration, same-SHA rename and old/current-default deletion; retained fence; initial complete signal, exact stale-main/unadopted-trunk digest rows; denied fence creation preserves complete remote refs. GitHub history has no authority events. |
| Empty-cache fast-forward/rebase/merge/squash land | `ColdCacheAllMethods` (`serve`) | Private lifecycle adoption; delete cache before build and before land; real tested ref/trailers, remote base/fence, exact persisted decision identity/classification/eligibility and check run/attempt/source/result/config/policy digests, common attempt ID and atomic checks/start adjacency; full journal and snapshot settlement replay. |
| #84 acquisition | `ColdCacheAllMethods` (`serve`) | Fresh Git client with no repository objects before landing must reacquire the advertised tested commit; fast-forward lands the contributor head, other methods land the tested SHA. |
| #82 production boundary | `HistoryBoundary` (`serve`) | Literal 10,000/10,001 cold real Git fixtures and expectations, with an independent assertion that the production limit is 10,000; exact-limit selection versus lookahead refusal; actual executor durably records oversized composition and plans dequeue before a push; complete remote refs unchanged. |
| Long base history | merge case of `ColdCacheAllMethods` (`serve`) | 10,001 old ancestors excluded from the new-ref creation inventory; small real build/land still succeeds. |
| Rejected intermediate/marker/fence | `IntegratedObjectRefusal` (`serve`), `RejectedObjectsRemoteRefs` (`gitops`) | Actual typed message-content refusal, complete `ls-remote --refs` equality, no success events; cold gitops executor probe additionally spies zero push dispatches and reads base/fence directly. |
| Mixed GitHub compatibility | `MixedRealGit`, `MixedGithubBytes` (`serve`) | Adopted authority main and ordinary release share one server/repo/bare origin; all four generated OIDs equal legacy builds and no authority journal bleed. Main d9ec975-built SHA-256 is asserted in-tree for the all-method OID/commit text, complete build/permission/command event, exact reply and permission-read export; alone/mixed bytes must also match. |
| `import --accept` governing waiver | `ImportGoverningEvidenceCannotBeAccepted` (`cmd/openmerge`) | Actual CLI receives immutable real-origin config/CODEOWNERS objects over a local transport; uncovered owners yield a non-waivable governing item; accepting each governing/activation item refuses with unchanged draft. The CLI sees only the mocked HTTP transport, so this test makes no remote-ref preservation claim. Valid owners still meet the public activation barrier. |
| #138 item 2 carry-over | `BaseAdvanceBeforeBuild` (`serve`) | Synchronous base advance after fresh trust/config and immutable preparation; either the current generic boundary-proof error or typed lease refusal; identical real remote refs, no success/landing event and zero Git push dispatches. Refusal/recovery journal shape may change in 9c-3. |

## Round-1 review fixes

Starting head: `bf702b3`. This round remains test-only; existing fixtures and all
production, Makefile, CI, test flags and Dockerfile bytes are unchanged.

- **Astra P2:** source fixtures, counts, selection and durable refusal expectations
  pin literal 10,000/10,001. A separate assertion pins the production constant to
  10,000. Scratch-source overlays mutate only `MaxFirstParentCommits` to 9,999:
  the original test survives (exit 0), while the revised test fails at the
  independent assertion (exit 1, `production first-parent limit = 9999, want 10000`).
  The offline verifier now repeats this mutation and checks that exact failure.
- **Exact-limit worker cost:** a scratch test overlay changes the queued PR and
  advertised source ref to the exact 10,000-commit head, runs the actual
  `Server.execute` build, and asserts a nonempty tested SHA and matching remote
  test ref. With `GOMAXPROCS=2 -p=1 -short -race -count=1`, it passes in **217.36 s**
  (**218.375 s** package time), far above the approximately 10 s budget. It is
  therefore excluded from the short suite. The committed exact-limit case tests
  real-Git composition reading/selection; its in-limit executor/builder coverage
  relies on inherited `TestStageARebaseCompositionBeforeCI/at-limit` **without
  `-short`**. Under `-short`, that inherited test reduces the limit to 4. The
  committed 10,001-commit case still runs the real worker/executor and asserts
  durable refusal, planned dequeue and unchanged remote refs.
- **9c-3 carry-over:** base advance accepts either the present boundary-proof
  error or `errors.Is(err, forge.ErrLeaseRejected)`, without requiring a particular
  failure/recovery event. Remote refs, absence of success/landing events and zero
  Git push dispatches remain strict. The dispatch spy is installed before client
  construction and observes the fixture's initial fence push before being reset.
- **CLI claim:** removed the vacuous remote-ref cleanup assertion (and its
  `t.Fatal`). This transport-only CLI test asserts immutable governing reads,
  non-waivable barriers and unchanged output drafts.
- **Main-built golden:** both main `d9ec975` (test overlay) and this branch assert
  export SHA-256 `cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee`.
  CI now pins that hash in-tree in addition to comparing alone/mixed exports.

Round-1 re-verification:

- `make check`: **0**, zero lint issues; complete short race suite and build.
  Serve inside this invocation: **281.119 s**.
- Every acceptance test passes under `-race -short -count=5`: serve **180.575 s**,
  gitops **3.385 s**, CLI **1.304 s**. One further pass with `GOMAXPROCS=1 -race`:
  serve **37.946 s**, gitops **1.484 s**, CLI **1.075 s**.
- `python3 scripts/test_adr0008_scratch_db.py`: **0** (90 refused entrypoint
  combinations, zero subprocesses, plus destination-rebuilding assertions).
- CI-like isolated serve (`GOMAXPROCS=2 -p=1 -short -race -count=1`):
  **264.493 s** on `bf702b3` → **266.537 s** after round 1, **+2.044 s**.
  Both invocations use Go 1.27.3, the heavy lock, `GOFLAGS=-p=2` and 4 GiB memory.
  This is a single local before/after measurement; the 900 s budget is unchanged.
- The verifier's new mutation helper also passes its direct integration probe,
  requiring the mutant's independent assertion failure rather than any nonzero exit.

Round-1 logs, scratch overlays and command receipts are in
`~/.cache/openmerge-141-r1-evidence/`. Package timings exclude lock waiting;
runner wall-clock receipts explicitly include it. The compact receipt below
retains original gates and records current source hashes separately. Full
GitHub-mode projection/replay/legacy proofs were not repeated: no shared
production paths changed. The required main-built export was regenerated.

## Original verification (before round 1)

- `make check`: **0**, zero lint issues, complete short race suite/build.
  Focused acceptance/completion/barrier and Stage A exact-output race tests: **0**.
- Strict replay: **2,159/2,159 identical** actions, structural states, snapshot
  hashes and boundary checkpoints; **8,676** probes; gates/self-tests pass with
  no non-allowlisted differences.
- Simulator: both exits **0**, **146/146 normalized blocks byte-identical**.
  Depth-1/4 contracts **90/90 identical**. GitHub status/API/health/author and
  actual CLI bytes, plus new mixed OID/permission/event exports: **identical**.
  Raw legacy real-Git and fake probes: **24/24 each identical**, retaining the
  prior 3a object/ref/command hashes. Existing wire fixtures **105/105 unchanged**.
- CI-like isolated `GOMAXPROCS=2 go test -p=1 -race -short -count=1 -timeout=15m`:
  serve **229.118 → 265.655 s (+36.537)**; gitops **92.847 → 93.167 s**;
  queue **163.717 → 162.474 s**; cmd/openmerge **64.169 → 64.469 s**.
  These are single local measurements, not runner forecasts. Inside `make check`:
  serve **281.776 s**, gitops **94.416 s**, queue **163.329 s**, CLI **65.366 s**.
  Every measured one-pass CI package remains well below 900 seconds; no CI flags changed.
- **20/20 iterations of every new test pass**, `GOMAXPROCS=1 -race -cpu=1`,
  **eight owned CPU burners**. Bounded serve groups: landing/history **594.102 s**
  and transitions/guards/mixed **403.325 s**. The original combined run completed
  gitops **10.585 s** and CLI **2.525 s** at 20 iterations. All burners reaped;
  unique scratch databases dropped. Go sources match the CI/equality receipt.
- Scratch routing tests: **90** refused entrypoint combinations across three
  drivers, asserting zero subprocesses, plus destination-rebuilding assertions
  (`python3 scripts/test_adr0008_scratch_db.py`: **0**).

Raw logs and manifests: `~/.cache/openmerge-9c2d-evidence/final-r2` and
`stress-bounded/`;
initial comparable timing: `initial-timing-before/`. The first verifier pass
stopped at one unused test-adapter parameter reported by lint; it was corrected
before the clean-tree CI/equality run. The first combined 20× serve stress
invocation reached its 900 s package timeout without an assertion failure or
race report; the final verifier splits serve into landing/history and
transitions/guards/mixed groups, retaining 20 iterations, the same 900 s cap and
eight burners for each. CI commands and Go tests are unchanged. Focused development fixture corrections
are excluded from acceptance. Compact receipt: [verification and source-hash receipt](adr-0008-9c-2d-artifacts/verification.json),
The receipt retains original gate results separately from round-1 verification and current source hashes.
Current receipt SHA-256 `621980ae7dc1de99452fa9e113e976e251621a5c59b1d6825cc89151aa02a6be`.

Reproduce with both Go 1.27.3 `GOROOT` and its `bin` first in `PATH`, and the
shared test Postgres DSN in `OPENMERGE_TEST_DATABASE_URL`:

```sh
python3 scripts/verify-adr0008-9c-2d.py \
  --baseline "$HOME/.cache/openmerge-9c2d-baseline" \
  --out "$HOME/.cache/openmerge-9c2d-rerun"
```

The baseline must be clean `d9ec975`. Every Go command holds the shared heavy
flock with `GOFLAGS=-p=2` and 4 GiB memory (6 GiB for strict replay).

## Limits and reviewer flags

- No Go production file, SQL migration/index/table, capability, wire field/event,
  existing fixture, Makefile, CI, Dockerfile or frozen test flag changed.
- Fixtures use owned local bare remotes and the shared test Postgres only. New
  database creation uses `adr0008_scratch_db.scratch_url`: routing query parameters
  and all `PG*` overrides fail before subprocess execution; the destination is
  rebuilt as localhost:54329 with a unique existing-allowlist scratch name and
  dropped in `finally`. Schema-only fixtures clean up their own schemas.
- Fast-import constructs both 10,000/10,001 source cases in one process. The
  exact-limit reader/selection is tested; the in-limit executor/rebase builder
  remains covered by inherited `TestStageARebaseCompositionBeforeCI/at-limit`
  without `-short` (its short variant reduces the limit to 4). Every new case,
  including the actual production boundary, runs in `make test`; no new
  acceptance case is skipped in CI. Existing package-private seams keep the
  suite in serve rather than exporting production API for a new package.
- All serve acceptance fixtures are created and closed inside `testSynctest`
  bubbles; method/kind subtests enter their own bubble. Git/DB calls and explicit
  callbacks provide synchronization. No sleeps or wall-clock deadline ordering. Serial
  compatibility probes inject fixed nonce bytes after DB allocation and restore
  the reader, matching the existing legacy-probe pattern.
- Fresh renamed-trunk verification and public recovery, ExpectedRefs recovery,
  typed base-advance handling and public activation stay 9c-3. Retired historical
  queues conservatively keep completion false; this slice does not erase them.
  Owner-executed organization release acceptance remains PR 11.
- Only publication is the requested branch and Hold PR. No live DB/status API,
  units, live refs, application GitHub API or Smart-Data-Ohio access.
