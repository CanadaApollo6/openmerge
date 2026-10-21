# ADR 0008 PR 9b-ii-2d evidence

Trial class: **Hold**. Base: local main `d01baeaca2568ba80bbf39aa2c44931277da5904`.
This delivers the complete 2d row and #133's three carry-overs in one PR.
Public activation remains rejected; production targets still supply neither
`AuthorityRequired` nor the private lifecycle resolver. No deployment,
organization acceptance, queueing or merging is claimed.

## Requirement and carry-over map

| Requirement | Implementation and proof |
| --- | --- |
| Every start checks current sources and exact authority digest under the existing lock | `internal/serve/stage_a_gate.go:315` performs fresh admission after successful bounded check/review evaluation, before commit. `internal/serve/authority_admission.go:18` requires both opt-ins, current OpenMerge config, a typed interpreted read and stable binding/non-authority sources. `internal/store/landing_start.go:94` requires current verified authority, then `internal/queue/landing_checks.go:292` compares the exact pin/config/batch/policy digests and all source revisions under the existing queue lock. No I/O runs inside that lock. |
| Depth-1/4 speculative starts without approvals; admission read/quota failures | `TestAuthorityAdmissionNoApprovalsChains`, `internal/serve/authority_admission_test.go:19`, creates every speculative batch before landing the chain, requires one fresh authority read per start, checks atomic evidence/start adjacency, rejects missing resolvers and permission/quota failures, and checks recovery/backoff deadlines. Existing all-method depth-1/4 check tests run with an explicit test admission reader. |
| Source raised/repaired between read and lock; no partial admission | `TestAuthorityAdmissionLockedSourcesAndSettlement`, `internal/store/authority_admission_test.go:16`, injects authority raise/repair and digest changes immediately before the queue lock at depths 1/4. Only the competing event commits; eligibility/decision/check/start inserts roll back. `TestAuthorityAdmissionIndependentSourceRepairRaces` at line 186 covers rulesets, classic protection and permissions separately. |
| Admitted starts settle after drift; batching/fence unchanged | The same store test admits first, then raises a finding and changes the digest; it checks that fence resolution remains planned and records settlement through the original claim's emitter. Existing real-git settlement coverage remains in `internal/serve/write_restriction_test.go:29`. Planner, batching and fence implementations are unchanged. |
| Generated-remedy seams cannot bypass admission | There is no runtime remedy generator in this baseline; `GeneratedRemedyDecision` is the retained exemption-record contract. The common `LandingStarted` boundary is protected for both `Emit` and raw `Append`/`Record` (`internal/store/store.go:334`). The store race matrix's `remedy-bypass` cases at depths 1/4 append a valid remedy record and reject both direct emission and an atomic raw decision/start delivery. This adds no remedy generation or changes to its record semantics. |
| Rollback removes only authority admission requirements | Existing mode-specific fold clears only `write_restriction`. `TestAuthorityAdmissionRollbackPreservesOtherRequirements`, `internal/store/authority_admission_test.go:138`, accepts ordinary approval-gated starts without authority checks after rollback, but retains rulesets, classic protection, permissions, operator holds and approval decisions. `TestAuthorityReadErrorSourceIsolationAndRepair`, `internal/queue/authority_read_error_test.go:12`, also checks rollback with a pending read-error block and an independent permission finding. Existing 2b/2c lifecycle, warnings/history and rollback tests pass. |
| Same-digest governing rebinds | `internal/serve/landing_checks.go:18` records the current authority pin's governing commit; `ConfigApplied` intentionally retains the first commit for its digest. `TestAuthorityAdmissionSameDigestGoverningRebind`, `internal/store/authority_admission_test.go:230`, rejects evidence for that retained old commit and accepts evidence for the current governing pin. |
| #133 observer availability/cadence | `internal/serve/write_restriction.go:30` records sanitized read failures in an optional additive `WriteRestrictionObserved.read_error`, exclusively under the authority source. `observeWorkerAuthority` at line 138 uses RulesInterval cadence, capped exponential backoff, RulesRequest quota deadlines, binding/rollback resets and a reader-only timeout. Failed reads remain nonfatal after their block commits; successful observation/verification ages and definite findings remain intact. Admission uses the same recovery schedule. `internal/serve/write_restriction_worker_test.go:48` proves command ingestion, visible block and repair; lines 101/133/209 prove timeout persistence, retry/quota cadence and partial findings. The queue source-isolation test proves stale repair rejection and snapshot restart. |
| #133 GitHub projection CI assertions | `internal/serve/authority_github_compatibility_test.go:23` compares exact checked-in new goldens for no-approvals and approvals-configured GitHub queues: reviewer check, all brief audiences, change response, status/API/health and author summary. `cmd/openmerge/authority_github_compatibility_test.go:16` exercises the actual human/JSON CLI against both API goldens. CI runs both normally; the existing verifier export still compares legacy bytes across checkouts. No existing fixture is edited. |
| #133 brief nits and complete-seam A2 | `internal/status/brief_policy.go:151` selects the proper parser without discarding a public-load result. `internal/serve/status_brief.go:37` independently requires worker opt-in for a pin-bearing authority brief. `TestAuthorityBarrierPinBearingBriefAndAdmission`, `internal/serve/authority_admission_test.go:157`, rejects pin-bearing non-opt-in and resolver-less paths. |
| #132 schema collision | `internal/serve/serve_test.go:66` adds an atomic suffix to time-based scratch schema names. `TestSynctestDatabaseSchemasUnique`, `internal/serve/synctest_test.go:32`, opens two fixtures at the same virtual instant. This fixes an actual focused-run collision; **Closes #132**. New bubbled tests remain serial and create/clean fixtures inside their bubble. |

## Complete-seam A2

`TestAuthorityBarrierJournalCannotInstallRuntimeAuthority`
(`internal/serve/authority_barrier_test.go:49`) replays draft, pin and raw
activation histories, parses a candidate, supplies a valid database-bound receipt
and resolver without explicit opt-in, and exercises lifecycle, observer,
owner/status policy, push-option and fresh-admission paths together. No reader,
authority hook, owner policy or authority event is enabled. Public loader/schema
and CLI rejection remains covered. The executor additionally rejects a missing
private resolver at both depths; the pin-bearing brief path has its own guard.
`cmd/openmerge/installations.go:118` still sets neither runtime opt-in nor resolver.

Candidates, receipts and journal data are never activation credentials. A
handcrafted pin retains existing fail-closed effects; this is not a claim that
arbitrary authority-bearing journals equal pin-free GitHub journals, or that
journal/database write access is an authenticated security boundary.

## Verification

Verified implementation: `e5dd4c74fa4298dc8c160f454bf7b86a4aa133d3`.
The clean-tree driver returned **PASS**. Subsequent changes are evidence only.

- `make check`: **exit 0**, formatting, zero lint issues, complete short race
  suite and build. Focused authority/lifecycle/barrier race suite: **exit 0**.
- Strict frozen live-journal replay: **2,159/2,159 identical** actions, structural
  states, snapshot hashes and checkpoints; **8,676** matching boundary probes.
  Gates 2/4 and all **22** mandatory self-tests pass, with no differing fields.
- Depth-1/4 contracts: **90/90 byte-identical**. SHA-256:
  `88e727ebe7ea5b570f352c5c661c2cd1642c1b5120f3d3ed086993f4dd00362a`.
- Seeded simulator: both exits **0**, **146/146 normalized blocks byte-identical**.
  SHA-256: `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Existing wire fixtures: **104/104 unchanged**, including **90** registry fixtures.
  Manifest SHA-256: `d9fc673fbd7ec5410cf1491778157508533ca5c804932caaecb39204f2ae2776`.
  All other pre-existing fixture files are also unchanged; only four new GitHub
  projection/CLI goldens are added.
- GitHub status/API/health/author bytes: **identical**, SHA-256
  `d60ce02d80dea505dfb3643041b28aa7d25a012964a3bd7c0e93321cbe998c89`.
  Actual CLI bytes: **identical**, SHA-256
  `4bea7d07d00c094c8824751d3b5db07932de48be34f6db2d98bc772139e248cf`.
- Isolated local serve race/short suite, identical flags: **233.547s before**,
  **248.872s after** (**+15.325s**). The complete `make check` serve package took
  **283.287s** with other packages running. These are local measurements, not
  predictions of GitHub runner time. New bubbled tests add no real-time waits.
- Test Postgres namespace lists before and after **match exactly**: **96**
  pre-existing scratch schemas retained, no new scratch schemas left behind.
- Verified binary SHA-256:
  `a44711342370df6ca4b63784b805c6850037692c5faad34243b471b8fd209ea2`.

Retained [summary and exact commands](adr-0008-9b-ii-2d-artifacts/summary.json),
[make check](adr-0008-9b-ii-2d-artifacts/make-check.log),
[focused race run](adr-0008-9b-ii-2d-artifacts/focused.log),
[source hashes](adr-0008-9b-ii-2d-artifacts/source-hashes.json),
[serve timings](adr-0008-9b-ii-2d-artifacts/serve-timing.json),
[schema cleanup](adr-0008-9b-ii-2d-artifacts/schema-cleanup.json) and
[artifact manifest](adr-0008-9b-ii-2d-artifacts/manifest.json) are compact receipts.
Raw streams remain in `/home/riels/.cache/adr0008-2d-verification/final-r3`;
the [cache manifest](adr-0008-9b-ii-2d-artifacts/cache-manifest.json) records their hashes.

Reproduce from clean checkouts:

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable'
python3 scripts/verify-adr0008-9b-ii-2d.py \
  --baseline /home/riels/.cache/adr0008-2d-main-d01baea \
  --out /home/riels/.cache/adr0008-2d-verification-rerun
```

Every heavy Go command uses the shared flock, `GOFLAGS=-p=2` and 4 GiB limit,
raised to 6 GiB for replay, with both Go 1.27.3 GOROOT/PATH exported. Only shared
test Postgres localhost:54329 and the frozen offline export are used. No live
DB/status API, service units, live `openmerge/*` refs, application GitHub API or
Smart-Data-Ohio organization is accessed. Git push and PR creation are the
explicitly requested publication operations. No Makefile/CI/Dockerfile/test-flag
changes are included.


## Reviewer flags

- `read_error` is additive to the existing observation and authority snapshot;
  there are no new event types, SQL migrations, indexes or tables. Ordinary
  GitHub event/projection bytes and existing fixtures remain unchanged.
- New recurring observations run on cadence. Successful landing starts acquire
  an additional fresh interpreted read; failures schedule the same backoff/quota
  recovery used by reconciliation and keep admission closed.
- Real forge acquisition, all generated-object push guards, first-fence ordering,
  repository-wide propagation signal, ExpectedRefs drift, public-barrier removal
  and real-git activation acceptance remain **9c**. Existing local-git tests are
  regression coverage, not that acceptance. The old-worker drain/backup/attested
  binary and compatible-reader requirements remain before emission deployment.
- Two review rounds identified and closed missing-resolver admission, raw-start
  bypass, direct rebind regression and admission retry scheduling. Initial checks
  also caught synthetic fixture bindings and the schema collision; final results
  below supersede those development runs. The first clean driver stopped at one
  lint name before tests ran. The next full check found two older historical
  raw-start fixtures still ordering their authority pin before the seeded start;
  their historical start now precedes activation, preserving audit expectations.
  Both corrections passed targeted regression checks before the final clean run.
