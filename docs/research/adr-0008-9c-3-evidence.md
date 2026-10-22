# ADR 0008 PR 9c-3 evidence

Classification: **Hold**. The complete 9c-3 row is delivered in one PR. This
implementation may merge to main; deployment remains prohibited until dogfood
period B drains. Owner-executed organization acceptance remains PR 11.

Baseline: pinned main `5fcfd115f4125cd848931e6f9cbfca82672a7bef` (#141).
Implementation tested: `18b1a90` (full SHA in the verification receipt). Main
advanced independently with #142 (`75c4029`), touching two files outside this
PR. The requested pinned baseline is retained for the comparisons below.
Prerequisite evidence: [9c-1](adr-0008-9c-1-evidence.md),
[2a](adr-0008-9c-2a-evidence.md), [2b](adr-0008-9c-2b-evidence.md),
[2c](adr-0008-9c-2c-evidence.md), [2d](adr-0008-9c-2d-evidence.md).

## Public boundary

The public schema, loader and config-validation CLI accept OpenMerge authority
with a dedicated team. Import's permanent activation barrier is retired; it
retains non-waivable governing-evidence failures and reports operational
prerequisites as guidance. Runtime activation requires the reviewed verification
plan, authentic proposed event IDs and the database-bound emission receipt.
`serve --authority-plan` / `--emission-receipt` install `AuthorityRequired` and
the private lifecycle resolver through the production constructors. Workers without
a reviewed runtime plan remain opted out. Public opted-in GitHub targets retain the
original loader bytes and exact diagnostic line numbers, with no receipt or
protection reads; historical private seams remain unchanged.

Pins can propagate before completion. Every public authority push guard then
requires `AuthorityCompletion.RequireComplete` from fresh discovery and the
actual `Store.RepositoryAuthorityCompletion` snapshot. All registered partitions
remain in the proof, including other installations, paused, retired and excluded
queues. Per-queue adoption does not authorize the switch. The completion reader
has a separate Git cache/session so a guard cannot re-enter the active push's
Git lock. Completion is point-in-time and is re-read rather than cached.

The actual public `setup rulesets --verify` path and production resolution call
`EmissionReceipt.ValidateForBinary`; missing, invalid and expired drain/backup
receipts refuse. Before an authority-boundary event exists, the live global
`max(events.id)` must equal the tested `BackupWatermark`. Verification checks
before seeing reads and in its final preparation gate. Public lifecycle writes
also validate the receipt and recheck the live default-branch name/tip immediately
before emission, followed by a pure receipt-window recheck. Receipt expiry while
a store transaction waits for locks or commits remains a documented limitation.

## Carry-over resolutions

- **#140 live backup boundary:** `Store.CheckAuthorityEmissionBackup`, direct
  store tests, and the actual public setup command refuse an unbacked journal
  advance before any boundary event. Compatible subsequent emissions retain the
  original pre-emission backup after the boundary exists.
- **#138 typed base advance:** `forge.BaseMovedError` is produced only after a
  successful live ref read, object acquisition and Git's clean negative ancestry
  result for a different tip. It is checked before the first public guard and
  again before publishing generated objects. Ancestry execution/read failures
  retain their error path. `TestAuthorityAcceptanceBaseAdvanceBeforeBuild` now
  requires that exact type and observed SHA, unchanged remote refs and no push or
  success event, then drives public observation/replanning to a successful build.
  The GitHub path takes none of these authority-only checks.
- **#138 ExpectedRefs:** `TestPublicAuthorityExpectedRefsRecovery` keeps a newly
  selected branch throughout rollback, fresh proposed verification and public
  re-enable. Main adoption alone still refuses completion; the new queue must
  adopt its own fresh proposal with current inputs. A subsequent discovery error
  refuses again, proving the success was not cached.
- **Activation operations:** the operating guide and ADR document one installation,
  no lanes, frozen default branch/selected inventory during the switch, complete
  inherited/repository/classic protection visibility, stopped-reader inventory,
  tested backup, v7/read_error strict-decoder boundaries and rollback/restore with
  reconciliation. Mutable actor identity failures retry on RulesInterval cadence.
- **A1 fixtures:** new activation variants replace the historical PR 1 role-capable
  future schema and update full schema composition without editing old fixtures.
  `repository_admin` remains historical wire only and is rejected publicly.
- **Nits:** the downgrade baseline comment names `addec7d^`; double blank lines
  in the operating guide are removed; complete-protection-visibility requirements
  are explicit.

## Consult-proof mutations

Spies count receipt loads, seeing reads, complete branch discovery and the actual
repository store method. The store spy delegates to `Store` and never replaces
its all-partition snapshot with an adoption receipt. The public setup command
spies zero forge calls on receipt and live-watermark refusal.

Each scratch-source overlay must exit 1 with the named behavioral test failure;
a compiler error or unrelated failure is rejected by the verifier. Production
sources and fixtures are never edited by a mutation.

| Removed/bypassed behavior | Named test killed the mutant (exit 1) |
| --- | --- |
| `public-reader-error-class` | `TestPublicAuthorityReaderPreservesSanitizedErrorClasses` |
| `public-github-loader` | `TestPublicAuthorityGithubConfigurationBytes` |
| `setup-live-backup-call` | `TestPublicAuthoritySetupVerifyRequiresLiveBackupWatermark` |
| `typed-base-move` | `TestAuthorityAcceptanceBaseAdvanceBeforeBuild` |
| `final-governing-recheck` | `TestPublicAuthorityActivationRechecksGoverningTip` |
| `require-complete` | `TestPublicAuthorityActivationConsultsCompletion` |
| `repository-source` | `TestPublicAuthorityActivationConsultsCompletion` |
| `setup-verify-receipt` | `TestPublicAuthoritySetupVerifyConsultsReceipt` |
| `public-runtime-receipt` | `TestPublicAuthorityActivationConsultsReceipt` |
| `pre-switch-verification` | `TestPublicAuthorityActivationRequiresVerification` |
| `live-backup-watermark` | `TestAuthorityEmissionLiveBackupWatermark` |
| `production-constructor` | `TestPublicAuthorityProductionConstructor` |
| `production-push-completion` | `TestPublicAuthorityActivationConsultsCompletion` |

## Verification

The clean-tree verifier returned **PASS** on `18b1a90981c43bcb76558f88f4a395189a3d3ff6`.

- `make check`: **0**, zero lint issues, complete short race suite and build.
  Focused authority/acceptance/completion/Stage A exact-output tests: **0**.
  Serve inside `make check`: **291.800 s**.
- Strict replay: **2,159/2,159** actions, structural states, snapshot hashes and
  boundary checkpoints identical; **8,676** probes; gates 2/4 and self-tests pass,
  with no non-allowlisted differences.
- Simulator: both exits **0**, **146/146** normalized blocks identical.
  Depth-1/4 contracts: **90/90** identical. GitHub projections (status/API/health/
  author), actual CLI bytes and Stage A exact-output regressions pass unchanged.
- The pinned 9c-2d mixed GitHub OID/permission/event export remains
  `cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee`.
  Legacy real-Git/fake object/ref/command probes also pass.
- Existing wire fixtures **105/105 unchanged** (90 registry fixtures); every
  existing testdata fixture is unchanged. Only new config/schema variants are added.
- **13/13** bypass mutations killed by their named tests. No compile failure is
  accepted as a mutation kill.
- All new/changed tests pass **20 iterations**, `GOMAXPROCS=1 -race -cpu=1`, with
  **eight owned CPU burners**, all reaped. Serve public group **141.038 s**;
  real-Git public base recovery **198.816 s**. Other packages at 20 iterations:
  config **2.366 s**, changepolicy **1.775 s**, store **3.041 s**, CLI **5.052 s**,
  gitops **10.911 s**. The verifier drops its unique scratch database in `finally`.

CI-like `GOMAXPROCS=2 go test -race -short -count=1 -p=1 -timeout=15m`:

| Package | Pinned main 5fcfd11 (s) | Implementation 18b1a90 (s) | Delta (s) |
| --- | ---: | ---: | ---: |
| `internal/serve` | 261.697 | 276.816 | +15.119 |
| `internal/queue` | 163.170 | 162.023 | -1.147 |
| `internal/gitops` | 93.391 | 96.222 | +2.831 |
| `cmd/openmerge` | 64.415 | 63.815 | -0.600 |

These are single local measurements, not runner forecasts. The baseline is the
completed, exit-0 pinned-main timing run retained from round 2; its exact log hash
is in the receipt. The final implementation is freshly measured. All measured
one-pass package times remain below 900 seconds; no CI limit or flag changes.
Top-level new public serve tests sum to **4.34 s** in the focused race run;
new public CLI tests sum to **0.04 s**. This is observed test time, not an
estimate that attributes the entire package delta to those tests.

Full logs/manifests: `~/.cache/openmerge-9c3-evidence/final-r3/`.
The documentation-only follow-up changes no runtime, test or verifier source
from the tested implementation commit.


## Reproduction and limits

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
# Set OPENMERGE_TEST_DATABASE_URL to the shared test Postgres localhost:54329.
python3 scripts/verify-adr0008-9c-3.py \
  --baseline "$HOME/.cache/openmerge-9c3-baseline" \
  --out "$HOME/.cache/openmerge-9c3-rerun"
```

The baseline must be clean `5fcfd11`. The verifier uses the shared
`adr0008_scratch_db.scratch_url` allowlist before any subprocess, creates a unique
allowed scratch database, and drops it in `finally`. Four entrypoints are covered
by 120 connection-free hostile DSN/environment refusals, asserting no subprocess.
Go 1.27.3 GOROOT and PATH are both pinned; every Go command uses the heavy flock,
GOFLAGS=-p=2 and 4 GiB memory (6 GiB for replay). CI-like timing uses GOMAXPROCS=2,
-race -short -count=1 -p=1. Reported package times exclude lock waiting.

All new pool fixtures are created and closed inside synctest bubbles. Serve uses
`testSynctest`; the command test prewarms process-wide signal delivery before its
bubble. Synchronization uses callbacks and explicit virtual cache expiry, with
no sleeps or wall-clock deadline ordering. Added short tests are small fake/DB
cases; the real-Git base-advance case extends the existing acceptance test.

No migrations, Makefile/CI/test flags/Dockerfile, shared GitHub formatters, GitHub Stage A
retry behavior or existing fixture bytes change. No live OpenMerge database, service
unit, live refs/status API or Smart-Data-Ohio writes are involved. Runtime tests
and simulator make no real forge API calls; publication is the requested branch
and Hold PR only.
Local real-Git fixtures and mocked forge transports are implementation evidence,
not organization release acceptance. No deployment, merge, queue or GitHub comment
is performed by this task.

The safe public reader wrapper also preserves quota deadlines and cancellation/
timeout identity through propagation, with no forge error text exposure. Its
public regression and restoration-of-generic-error mutation both pass.

Earlier development runs are excluded from acceptance. The initial full check
found the schema-composition fixture mismatch; its Go packages otherwise passed
and its lint was clean. The final run covers the new immutable schema variant and
the exact public GitHub loader regression. Round 2 was deliberately stopped
after its successful timing/equality phases to preserve sanitized error types;
round 3 reran every gate on the final source, reusing only the unchanged baseline
timing. A development signal-initialization abort left one owned test schema;
its exact queue/event ownership was verified and it was removed. Compact receipt: [verification/source-hash receipt](adr-0008-9c-3-artifacts/verification.json), SHA-256 `97f428c23680c700bceeba14e9e06416252946f1321ee2e0c4b7ee7bd66d76aa`.
