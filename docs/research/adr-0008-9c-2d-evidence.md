# ADR 0008 PR 9c-2d evidence

Trial class: **Hold**. Baseline: local main `d9ec975` (#137/#138/#140 and
#131/#139 landed). Go implementation: `3302b0c33b1c02ba3458d923adddcb39b53e54cd`;
verifier grouping: `cadd432`. Go sources are byte-identical across these commits.
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
| #82 production boundary | `HistoryBoundary` (`serve`) | Cold real Git reads at 10,000 and 10,001 commits; exact-limit selection versus lookahead refusal; actual executor durably records oversized composition and plans dequeue before a push; complete remote refs unchanged. |
| Long base history | merge case of `ColdCacheAllMethods` (`serve`) | 10,001 old ancestors excluded from the new-ref creation inventory; small real build/land still succeeds. |
| Rejected intermediate/marker/fence | `IntegratedObjectRefusal` (`serve`), `RejectedObjectsRemoteRefs` (`gitops`) | Actual typed message-content refusal, complete `ls-remote --refs` equality, no success events; cold gitops executor probe additionally spies zero push dispatches and reads base/fence directly. |
| Mixed GitHub compatibility | `MixedRealGit`, `MixedGithubBytes` (`serve`) | Adopted authority main and ordinary release share one server/repo/bare origin; all four generated OIDs equal legacy builds and no authority journal bleed. Separate main-built export compares all-method OIDs/commit text, complete build/permission/command event bytes, exact replies and permission read counts. |
| `import --accept` governing waiver | `ImportGoverningEvidenceCannotBeAccepted` (`cmd/openmerge`) | Actual CLI receives immutable real-origin config/CODEOWNERS objects over a local transport; uncovered owners yield a non-waivable governing item; accepting each governing/activation item refuses with unchanged draft and real remote refs. Valid owners still meet the public activation barrier. |
| #138 item 2 carry-over | `BaseAdvanceBeforeBuild` (`serve`) | Synchronous base advance after fresh trust/config and immutable preparation; current generic boundary-proof error, failed-build event and identical real remote refs. Typed handling and public recovery remain 9c-3. |

## Verification

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
SHA-256 `e821c0a43009943709e4c672cbbb13efd8db17dda7a6153c7ef3883f6bfe681e`.

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
  exact-limit reader/selection is tested; replaying 10,000 commits through the
  rebase builder remains the inherited non-short regression. Every new case,
  including the actual production boundary, runs in `make test`; no new
  acceptance case is skipped in CI. Existing package-private seams keep the
  suite in serve rather than exporting production API for a new package.
- Branch tests use `testSynctest`; other new cases use synchronous Git/DB calls
  or explicit callbacks. No sleeps or wall-clock deadline ordering. Serial
  compatibility probes inject fixed nonce bytes after DB allocation and restore
  the reader, matching the existing legacy-probe pattern.
- Fresh renamed-trunk verification and public recovery, ExpectedRefs recovery,
  typed base-advance handling and public activation stay 9c-3. Retired historical
  queues conservatively keep completion false; this slice does not erase them.
  Owner-executed organization release acceptance remains PR 11.
- Only publication is the requested branch and Hold PR. No live DB/status API,
  units, live refs, application GitHub API or Smart-Data-Ohio access.
