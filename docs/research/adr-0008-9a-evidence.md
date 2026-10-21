# ADR 0008 PR 9a evidence

Trial class: **Hold**. Base: `7207a72`. The [split plan](adr-0008-9-plan.md)
assigns every PR 9 requirement and review carry-over to 9a, 9b or 9c. This slice
emits no authority events and enables no runtime authority switch. Deployment
remains held until period B drains; organization acceptance remains PR 11.

## Implemented boundary

The public schema and loader reserve both root authority keys together. Omitted
and explicit `github` configurations normalize identically. `openmerge` returns
an activation-unavailable error; `break_glass` is rejected publicly. The same
13-document corpus exercises schema, loader and CLI validation. Candidate setup
and proposed verification retain their separate parser, now requiring a dedicated
team for new declarations. Historical repository-role wire variants and every
existing fixture remain unchanged; setup HTTP tests now use a dedicated team
and retain rotation/restore assertions.

Candidate `ValidateGoverning` checks exact selector compilation and exactly one
queue serving the resolved default branch. It validates complete immutable
probes at the requested governing commit, one governing config, selected
CODEOWNERS precedence, and fresh resolved effective owners for every supported
config/CODEOWNERS location. Absent locations also need ownership because adding
a higher-precedence file can replace policy. The parsed candidate must equal
the governing config; proposed/head content cannot substitute. Actor resolution
and protection observations remain separate prerequisites in 9c.

`changepolicy.Policy.RequirePolicyOwners` enables the existing classifier,
brief, fresh-owner eligibility and landing-decision contracts independently of
`Approvals`. It is not wired from public configuration in this slice. With nil
approvals there is no ordinary default/rule obligation. Policy changes still
require eligible exact-revision governing owners; user `none` rules cannot waive
that obligation. The policy digest includes classifier/governing content and a
separate authority-owner domain, preserving every legacy digest vector. The
queue, executor gate and store reuse the existing immutable decision transaction;
failed final authorization rolls eligibility/decision/start back together.

## Verification

Verified implementation: `c8dbebb23f1e898ce8c8d00bf54fa678433f8f54`. Only
evidence documents/receipts change after these checks. Baseline:
`7207a72a699185058d2f2afa2cd61738b0fbcd75`. Verification completed on
2026-10-21 UTC. [Commands and results](adr-0008-9a-artifacts/summary.json),
[hash manifest](adr-0008-9a-artifacts/manifest.json),
[full check](adr-0008-9a-artifacts/make-check.log),
[focused check](adr-0008-9a-artifacts/focused.log), and
[simulator normalization](adr-0008-9a-artifacts/sim-normalized.json) are retained.
Raw replay streams, full boundary tables and simulator logs remain under
`/home/riels/.cache/adr0008-9a-verification`, with paths/hashes in the manifest.

- `make check`: **exit 0**, formatting, zero lint issues, complete race suite and build.
- Focused governing/brief/setup/queue/atomic-store tests: **exit 0**.
- Strict offline replay of the retained 2,158-event, one-queue export: **PASS**.
  All **2,159/2,159** actions, full structural states, snapshot hashes and boundary
  checkpoints agree. All **8,676** boundary probes agree. Gates 2/4 pass; all
  **22** mandatory synthetic self-tests pass. No allowlisted differences.
- Depth-1/4 approval and legacy-block contracts: **90/90** byte-identical records,
  including plan/snapshot hashes and sizes.
- Existing wire fixtures: **104/104** unchanged (90 registry fixtures), no added events.
- Seeded simulator: baseline and candidate **exit 0**, all **146** normalized
  result blocks byte-identical. SHA-256:
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Binary SHA-256:
  `23bc7ff22b31b89c169018343252571c124cf53c00272c59b2aa42d5545b7e62`.

Initial runs exposed a missing positive CI term in a new governing test fixture
and two older test guards assuming the pre-publication schema/role actor. The
fixtures/guards were corrected without changing stored golden fixtures; the
superseded full run was stopped after those failures. Final checks above run
the committed corrected source. Shared-lock waits delayed execution; no lock
bypass or unrelated process termination was used.

All Go commands export Go 1.27.3 GOROOT and PATH,
use the shared flock with `GOFLAGS=-p=2`, and use 4 GiB memory (6 GiB for replay).
Database tests use isolated schemas in the shared test Postgres on localhost:54329.

No live database, status API, service units, organization settings or remote
OpenMerge refs are accessed. No real GitHub API calls occur in code/tests or
simulator; HTTP tests use fakes. No SQL migrations/indexes, Makefile/CI/Dockerfile
or test-flag changes are included. 9b owns lifecycle/adoption/receipt hardening,
#120 and complete lock-time admission wiring. 9c owns actual governing acquisition,
import rejection, every push guard, first-fence bootstrap and real-git activation.
The complete first-emission drain/backup/v7 downgrade procedure remains required.
