# ADR 0008 PR 9b-ii-2c evidence

Trial class: **Hold**. Base: local main `0c63b8dd12ff4a9b01e8e2c9df4d44edbea3b17e`
(includes #125, #127–#131). This delivers the entire **2c worker policy and
projections** row in one PR. No public activation, deployment, queueing, merging
or organization acceptance is authorized. The [plan](adr-0008-9-plan.md) retains
2d admission and 9c complete activation ownership.

## Requirement and carry-over map

| 2c requirement / carry-over | Implementation and proof |
| --- | --- |
| Runtime owners without approvals; ordinary changes have no invented floor | `internal/serve/reconcile.go:271` installs Stage A only for approvals or the explicit authority seam plus OpenMerge candidate. `internal/serve/stage_a.go:244` includes `RequirePolicyOwners` in the semantic digest/policy. Existing classification, fresh eligibility and worker decision plumbing are reused. `TestAuthorityWorkerFirstCycleAndObservationCadence` (`internal/serve/authority_worker_test.go:48`) asserts nil approvals, a recorded brief, zero ordinary obligations and an enabled decision/check gate. |
| Policy changes require eligible exact-revision governing owners; none cannot waive | `internal/serve/stage_a.go:542` continues acquiring classification at `w.configTip`, with governing rather than proposed CODEOWNERS. `TestAuthorityWorkerGoverningOwnersAndExactRevision` (test line 147) replaces CODEOWNERS with the author and edits config, rejects missing approval, accepts the governing owner and rejects the previous head's approval. `TestAuthorityWorkerNoneCannotWaivePolicyOwners` (line 330) tests a matching user `none`, exact owner approval, and fresh write-grant revocation. |
| Atomic decisions and per-check evidence live without approvals | `internal/store/store.go:385` requires the existing decision transaction for every enabled policy. `internal/serve/worker.go:145` supplies `stageAGate`; `internal/serve/stage_a_gate.go:240` selects exact-check recording on the explicit authority path. `TestAuthorityWorkerAtomicDecisionAndCheckEvidence` (line 264) uses the installed no-approvals policy, generated fake commits, exact run ID/attempt, rejects a direct start, and asserts decision/check/start records. It drives the executor directly to exclude the future 9c push gate. |
| First-cycle observations and status/CLI/API/health/author projections | `internal/serve/reconcile.go:315` calls `observeWorkerAuthority`; `internal/serve/write_restriction.go:61` binds reads to the current durable pin through PR 4's existing interpreter/fold. `internal/status/brief_policy.go:151` reconstructs historical authority briefs from the recorded pin and local objects; `internal/status/status.go:174` tracks same-digest authority rebind pairs. `internal/status/check.go:46` renders enabled mandatory-owner briefs. The first-cycle test drives `s.cycle`, checks durable observation, actual API/health output, CLI authority text, author queue instructions, no author verification warning, and a reconstructed HTTP brief after rebind. |
| Rollback stops new observations and new-move warning eligibility; delayed-origin warnings/history/unrelated blocks survive | `internal/serve/write_restriction.go:62` requires current OpenMerge configuration as well as both opt-ins. PR 5's existing `internal/store/outside_push_ledger.go:131` derives warning eligibility at each originating BaseMoved. `TestAuthorityWorkerRollbackRetainsDelayedWarnings` (test line 194) drives entry/active/rollback/post-rollback moves through configuration and reconciliation, delays all audits until after rollback, checks exactly the two authority-origin warnings, then acknowledgement/history and independent permission/operator blocks. |
| #130: unadopted cadence and same-binding draftless rebind failure | `internal/serve/authority_activation.go:57` reads for draft adoption only; line 173 short-circuits unchanged adopted or unadopted pins independently of reconciliation observations. The first-cycle test asserts one observation on entry, zero seeing reads over three unchanged configuration cycles, no invocation of a failing reader during draftless rebind, and exactly one more read on the next reconciliation. Adoption receipt/slow-read tests explicitly request drafts and retain their existing gates. |
| #130: ExpectedRefs/VerificationInput drift | Explicitly assigned to **9c** in `docs/research/adr-0008-9-plan.md:173`: refreeze/bind interpretation inputs and invalidate stale adoption before complete push activation. This PR preserves persisted adoption input and its comparison; it does not claim to solve drift. |
| #130: evidence wording | `docs/research/adr-0008-9b-ii-2b-evidence.md:161` now limits the historical zero-read assertion to adopted-pin configuration cycles. Line 19 describes 2b's cost and 2c's separation. |

## A2 argument

`cmd/openmerge/installations.go:118` continues constructing targets without
`AuthorityRequired`, private lifecycle resolver or authority callbacks. The
public schema/config loader still reject OpenMerge activation. Runtime candidate
parsing requires both opt-ins; owner installation additionally requires the
successfully loaded OpenMerge candidate. The recurring observer independently
checks both opt-ins, loaded mode and config validity. A receipt or journal
pin cannot install these worker policies or call their readers.

`TestAuthorityBarrierJournalCannotInstallRuntimeAuthority`
(`internal/serve/authority_barrier_test.go:49`, extended assertions at line 114)
loads draft/pin/raw-activation histories, parses a candidate, supplies a valid
receipt and resolver without opt-in, invokes the new observer, and proves no
owner policy, observation request, status policy or authority events are
installed. Existing flag-only/resolver-only, public-loader/schema/CLI agreement,
and depth-1/4 pin-only evidence-gate tests pass. Historical journal evidence
remains renderable and pre-existing fail-closed protections remain; A2 is not a
claim that arbitrary authority-bearing journals equal pin-free GitHub journals.

`TestAuthorityGithubProjectionBytes` (`internal/serve/authority_github_compatibility_test.go:15`)
is run unchanged in both trees through an overlay. The driver compares full
GitHub queue/status API, health API and author-summary bytes, plus the actual
`cmd/openmerge.printStatus` CLI output. No output normalization or field
allowlist is used. Strict replay, simulator and depth contracts separately
cover ordinary and legacy-approval behavior.

## Reviewer flags

- The private resolver supplies acquisition to the internal seam. Real governing
  acquisition/validation, complete push guards, first-fence ordering,
  propagation-complete signaling and public barrier removal remain **9c**.
- Findings/digest checks under the landing/remedy lock at depths 1/4, plus
  rollback removing authority-specific admission requirements, remain **2d**.
  Existing admitted-start settlement and batching/fence behavior are unchanged.
- Each successful authority reconciliation makes one runtime observation,
  including unadopted pins. Adopting a new draft additionally makes its required
  complete seeing read. Config-only refreshes do not poll protection. Read errors
  retain PR 4's failure/partial-evidence semantics; status makes no forge reads.
- New serve tests are serial and use `testSynctest` with fixtures inside the
  bubble. No parallel bubbled tests or #132 fix/closure is included.

## Verification

Verified implementation: `07f308a624589693f95c538bc70a02d604ffcacc` against `0c63b8dd12ff4a9b01e8e2c9df4d44edbea3b17e`.
The clean-tree driver returned **PASS**; the following commit retains evidence
only. [Summary and exact commands](adr-0008-9b-ii-2c-artifacts/summary.json),
[source hashes](adr-0008-9b-ii-2c-artifacts/source-hashes.json),
[artifact manifest](adr-0008-9b-ii-2c-artifacts/manifest.json),
[raw-cache hashes](adr-0008-9b-ii-2c-artifacts/cache-hashes.json),
[serve timings](adr-0008-9b-ii-2c-artifacts/serve-timing.json) and
[scratch namespace comparison](adr-0008-9b-ii-2c-artifacts/schema-cleanup.json)
are retained. Full logs/streams remain in `/home/riels/.cache/adr0008-2c-verification`; no large dumps are committed.

- `make check`: **exit 0** (format, zero lint issues, complete short race suite,
  build). Focused authority worker/lifecycle/barrier/store/queue/config race
  suite: **exit 0**.
- Strict frozen live-journal replay: **2,159/2,159 identical** actions, structural
  states, snapshot hashes and checkpoints; **8,676** matching boundary probes.
  Gates 2/4 and all **22** mandatory self-tests pass; no differing fields.
- Depth-1/4 contracts: **90/90 byte-identical**. SHA-256:
  `88e727ebe7ea5b570f352c5c661c2cd1642c1b5120f3d3ed086993f4dd00362a`.
- Seeded simulator: both exits **0**, **146/146 normalized blocks byte-identical**.
  SHA-256: `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
- Fixtures: **104/104 unchanged**, including **90** registry fixtures; no new
  event types, fixtures or wire fields. Manifest SHA-256:
  `d9fc673fbd7ec5410cf1491778157508533ca5c804932caaecb39204f2ae2776`.
- GitHub status/API/health/author bytes: **identical**, SHA-256
  `d60ce02d80dea505dfb3643041b28aa7d25a012964a3bd7c0e93321cbe998c89`.
  Actual CLI output: **identical**, SHA-256
  `4bea7d07d00c094c8824751d3b5db07932de48be34f6db2d98bc772139e248cf`.
- Isolated local serve race/short suite with identical flags: **228.444s before**,
  **224.380s after**. The complete `make check` serve package took **261.928s**
  while other packages ran concurrently. These are local measurements, not a
  prediction of GitHub runner time. New tests add no real-time waits or parallel
  bubbles. Existing lifecycle fixtures now expose optional identity/review
  readers and explicitly request drafts for adoption-I/O failure tests.
- Verified implementation binary SHA-256:
  `f493de223afcbc04204aff4438496a5e6c63e7baf2ad144c97c36934caf7060f`.
- Test Postgres namespace lists before and after the complete run **match exactly**;
  no additional scratch schemas remain and no pre-existing objects were dropped.

Reproduce from clean checkouts:

```sh
export GOROOT=/home/riels/.local/share/mise/installs/go/1.27.3
export PATH="$GOROOT/bin:$PATH"
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable'
python3 scripts/verify-adr0008-9b-ii-2c.py \
  --baseline /home/riels/.cache/adr0008-2c-main-0c63b8d \
  --out /home/riels/.cache/adr0008-2c-verification-rerun
```

Every heavy Go command uses the shared flock, `GOFLAGS=-p=2`, and 4 GiB limit
(6 GiB for replay), with both Go 1.27.3 GOROOT/PATH exported. Only shared test
Postgres localhost:54329 and the frozen offline journal export are used. Scratch
objects are cleaned by fixture cleanup; namespace comparison is retained.
No live DB/status API, systemd unit, remote `openmerge/*` ref, application GitHub
API or Smart-Data-Ohio organization is accessed. Git push/PR creation are the
requested publication operations. No SQL migration/index/new table,
Makefile/CI/Dockerfile/test-flag, wire event/field or existing fixture changes.
