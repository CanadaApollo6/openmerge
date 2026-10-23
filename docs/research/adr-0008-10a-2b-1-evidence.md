# ADR 0008 PR 10a-2b-1 evidence

Classification: **Inert by its own behaviour; depends on Hold code; deploy only
after period B**. Base: main `4b1fe46`. Production changes stay in
`internal/audit/evidence.go`. Complete-export fixtures and v1 remain
**PROVISIONAL**, with no regeneration or freeze in this step.

## Fold agreement and fail-closed history

Audit streams only `config_applied` and `policy_authority_applied` events before
the decision, in event-ID order within the existing repeatable-read snapshot.
It applies them through `queue.Apply`, keeping one projected state and the
accepted pin's retained event ID. Config transitions preserve the durable pin
as the queue fold does. Payload reads retain the existing 4 MiB cap and errors
are sanitized to `errSource`. No queue, event, wire, formatter, schema, migration,
index, CLI dispatch, Makefile, CI, test-flag or Dockerfile change is included.

The prefix differential compares this projection's `AuthorityPin()` with the
full queue fold after every event, including unrelated events, config changes,
same-digest rebinds, duplicate pins, rollback, Validate-rejected pins and pins
whose effective digest disagrees with the current config. Separate prefixes
start with an ignored pin before config and a clean config-first history.
Every rejected/ignored pin poisons the prefix, even after a valid replacement;
export fails closed rather than silently removing evidence requiredness.
Database-prefix assertions exercise the actual streaming query too. Invalid
payloads may fail at decode before reaching the fold, also closed.

This query scans the config/pin subset for each decision; it does not scan all
check observations or retain the queue journal in memory. Very long config/pin
histories can increase export work. Step 10a-2b-2 retains the realistic growth
measurement and final independent complete-export acceptance work.

## Inapplicable status and normalized attempt ties

A governing github rollback pin retains its source with `inapplicable` status.
`not_recorded` remains the explicit absence representation with no source.
Shape validation restricts inapplicable references to the governing authority
edge. Closure follows the retained source even outside the requested time
range. Replay re-resolves it against saved retained history; fully rehashed
substitutions of recorded/absent status or another real rollback source fail.
Valid rollback replay remains byte-exact. Fully rehashed omissions still need
the future 10b external manifest anchor.

The dead `sameGeneration` helper and its helper-only test are removed.
Generation selection remains enforced by SQL and its existing cross-generation
tests. The new cross-event tie is run 5 attempt 1 (failure), then run 5 attempt 0
(success): SQL normalizes 0 to 1 before ordering and selects the later event.
The reference describes attempt 1. SQL selection and Go normalization within
the chosen event are tested separately; the new mutant removes normalization
from the SQL tuple itself.

## Verification

Measured on committed implementation `8f34017cc92841735f541894264fde458fee75fb` against
`4b1fe46313bf15a0d02c2c3cdb889b0ff5e356ce`. The final commit changes documentation and the mutation target table only;
production code and Go tests are byte-identical to this measured head.
All long commands ran in the foreground. Go used exported GOROOT/PATH for
1.27.3, the heavy lock, `GOFLAGS=-p=2`, 4 GiB memory (6 GiB for strict replay),
and only the guarded test database on `localhost:54329`. Temporary build and
test files stayed under the cache directory.

- `make check`: **exit 0**, 574.6 s including lock time.
- Full non-short audit race suite: **exit 0**, 419.9 s.
- Strict replay: **2,159/2,159 identical** actions, structural states, snapshot
  hashes and boundary checkpoints. 8,676 boundary probes;
  Gate 2 / Gate 4 PASS, self-test exit 0, no non-allowlisted fields.
- Simulator: **146 identical** normalized blocks. Contracts: **90/90 identical**.
- GitHub projection and actual CLI bytes, Stage A exact-output assertions,
  and MixedGithubBytes remain identical to main. Mixed hash:
  `cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee`.
- **211 fixtures outside `internal/audit/testdata` unchanged**.
  Complete-export fixture payloads inside audit testdata are unchanged too;
  only their README names the two-step sequence. No fixture regeneration.
- Existing landing-caller tests and source DB URL guard pass.
- Stress: **19 tests ×20 = 380 top-level passes**,
  `GOMAXPROCS=1 -race -count=20`, non-short, in
  158.4 s. Eight owned CPU burners
  ran throughout; minimum measured CPU consumption was
  15,720 ticks. All eight were terminated and reaped.
  No data race. Stress includes all new tests, changed rollback/absence tests,
  changed approval-fixture tests and existing cross-generation selection tests.

### Honest mutation results

**79 targets: 78 assertion kills, 1 equivalent,
0 unexpected survivors, 0 build/setup failures,
0 infrastructure failures.** Every counted kill compiled and
reached a semantic test failure. Equivalents never count as assertion kills.
The historical filename `scripts/verify-adr0008-10a-2a.py` now targets this step.
Dead generation and github source-erasure targets are removed. Generation SQL
mutations remain, independently of Go normalization within a chosen event.

| New target | Outcome | Failing assertion |
| --- | --- | --- |
| `pin-skip-config-fold` | assertion kill | TestGoverningPinPrefixAgreement |
| `pin-ignore-rejected-decode` | assertion kill | TestGoverningPinPrefixAgreement |
| `pin-ignore-digest` | assertion kill | TestGoverningPinPrefixAgreement |
| `pin-hide-rejection` | assertion kill | TestGoverningPinPrefixAgreement |
| `rollback-status-recorded` | assertion kill | TestGoverningPinRollbackReplaySubstitutions |
| `rollback-status-absent` | assertion kill | TestGoverningPinRollbackReplaySubstitutions |
| `inapplicable-shape` | assertion kill | TestGoverningPinInapplicableReferenceShape |
| `sql-zero-attempt-not-normalized` | assertion kill | TestCheckAttemptNormalizedCrossEventTie |
| `closure-valid-resolution-call` | equivalent | none; declared equivalent |

Removing closure's defensive `validResolution` call is the declared equivalent:
the resolver constructs matching references and dependency sets together.
`TestValidResolutionRequiresExactReferenceAndDependencySets` still tests the
validator directly. The equivalent is run and reported, not silently omitted.
All per-mutant failing test names and logs are retained in the artifact table.

### CI-like package timings versus main

Both sides ran `GOMAXPROCS=2 go test -race -short -count=1 -p=1 -timeout=15m ./...`
with exit 0. Go-reported package seconds exclude compilation and lock waiting.
These are descriptive single runs, not benchmark estimates or performance gates.

| Package | Main (s) | Step 1 (s) | Delta (s) |
| --- | ---: | ---: | ---: |
| `cmd/openmerge` | 68.912 | 68.310 | -0.602 |
| `internal/ascii` | 1.007 | 1.006 | -0.001 |
| `internal/audit` | 9.730 | 11.146 | +1.416 |
| `internal/authority` | 1.097 | 1.106 | +0.009 |
| `internal/brief` | 3.364 | 3.352 | -0.012 |
| `internal/changepolicy` | 1.748 | 1.763 | +0.015 |
| `internal/classification` | 2.228 | 2.261 | +0.033 |
| `internal/config` | 1.670 | 1.664 | -0.006 |
| `internal/config/condition` | 1.021 | 1.020 | -0.001 |
| `internal/diagnostic` | 1.017 | 1.019 | +0.002 |
| `internal/executor` | 1.155 | 1.157 | +0.002 |
| `internal/forge` | 8.322 | 8.100 | -0.222 |
| `internal/forge/fake` | 1.465 | 1.454 | -0.011 |
| `internal/forge/github` | 75.201 | 67.698 | -7.503 |
| `internal/gitops` | 103.564 | 103.897 | +0.333 |
| `internal/queue` | 185.119 | 183.643 | -1.476 |
| `internal/serve` | 335.077 | 334.269 | -0.808 |
| `internal/sim` | 37.566 | 37.841 | +0.275 |
| `internal/sim/capacity` | 1.009 | 1.010 | +0.001 |
| `internal/status` | 12.003 | 12.092 | +0.089 |
| `internal/store` | 75.314 | 75.266 | -0.048 |
| `scripts` | 1.082 | 1.078 | -0.004 |
| `tools/dogfood` | 1.643 | 1.658 | +0.015 |

The 23-package total is 930.314 s → 921.810 s (-8.504 s).
Audit's delta includes the added database-prefix and replay assertions.

Artifacts: `/home/riels/.cache/openmerge-10b1/acceptance-final` (`summary.json`,
`mutation-results.json`, per-command stdout/stderr and commands, replay JSON,
normalized simulator output, package timings and SHA-256 manifest).
Manifest SHA-256:
`82d000a09a4fafbce80a7f8059e9dd379f413c6111a0959429f72a501972ea7f`.
The authoritative final mutation artifact is `/home/riels/.cache/openmerge-10b1/mutations-final`,
manifest SHA-256 `00abdeffb6728401c3d94a9539aa18cacc99719318d7301de1323f55794f4dd0`.
It records verifier SHA-256 `b72de6b598a57e678ae7f39a40177bc494401ad467d8dd13d37abfbc2bc18880`.
The full acceptance artifact's original table reported 79 assertion failures / one
equivalent. Final review removed two helper-only targets (`pin-skip-validation`
and `pin-skip-queue-fold`) from behavioural kill claims: production decode already
validates pins, and removing the pin projection update affects only the direct
projection invariant test. The final table adds `pin-ignore-rejected-decode`,
which skips the actual retained-history decode failure and is killed by the
prefix/export assertions. All final targets were rerun; the 78 / one count above
supersedes the original table. Regression, stress and timing code was unchanged.
An initial acceptance attempt was stopped during mutations to strengthen source
substitution coverage; no result from that stopped run is counted above.
