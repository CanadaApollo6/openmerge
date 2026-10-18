# Stage A 4a: classification and replayable evidence

Baseline: `3f77a88daf1cb99961c22b4daebaeb58f9087bb0` (origin/main). Branch: `stage-a/4a-classification`.
Frozen round-1 implementation: `9fe04db` (after clean rebase from `7ed45fd`).
Worktree: `/home/riels/openmerge-stage-a-4a`.
Baseline checkout: `/home/riels/.cache/om-sa-4a-main`.
The implementation commit and evidence-only validation commit identify the
reviewed source in git; no deployment is performed by this PR.

## Dormant boundary

ADR 0007 class: **Inert**. No production path emits `change_classified`; runtime
integration waits for 11b. No config keys, actions, planner/executor/landing-gate
behavior, workflows or queue configuration change. Public loaders still reject
classification/approval keys. The optional owner/attribute interface remains
unimplemented until 4b, with unknown generated attributes and unresolved owners.
No live DB, systemd unit or `openmerge/*` ref was accessed. Only main and this PR branch were fetched;
only this PR branch is pushed. Tests use the supplied shared test Postgres.

**Reviewed replay exception for PR #65:** the only new `queue.State` field is
private `classifications`. It stays **nil/zero at every legacy replay checkpoint**
for queues without approvals: only applying `ChangeClassified` allocates it, and
there is no production emitter. Add this field to the live-journal replay tool's
reviewed exceptions list when that tool checks the merged, undeployed batch.
Do not enable compaction. Snapshot encoding advances to v4 because state shape
changed; v2 and v3 decoding remain supported. Existing wire-v1 bytes stay fixed.

## Evidence and follow-on contract

Classifier version:
`classification-v1/git-evidence-v7-bounded-omissions/governing-v1/hunks-v1/facets-v2`.
It folds in forge v7 inventory/rename/ancestry limits, governing selection and
absence proofs, bounded hunk semantics, and the classifier's mapping/facet rules.
A golden pins the version. Every-file docs/generated and paths remain unknown
on incomplete inventories; any-file touches retains positive observed hits.
Policy paths include all four config candidates, forge CODEOWNERS locations,
all .gitattributes files, workflow definitions and typed sensitive patterns,
on both rename sides and deletions. Forge PR labels are never inputs.

The wire-v1 event carries acquisition fingerprint, pinned endpoints and governing
commit, governing object IDs/checked absences, typed inventory/hunk coverage,
file inventory, facts, label-bound reasons and durable generation. The bounded
hunk evidence lives **in `ChangeClassified.Evidence.Classification.Files[].Hunks`**.
Omitted-range coordinates and summary counts are copied, never expanded. Inventory
completeness and hunk completeness stay independent. PR 7a owns the full record
size budget. The historical registry fixture and its two variants remain byte-identical;
a new facets-v2 variant pins current output.

The projection is keyed by change/revision. Apply and reads detach mutable
contents. Generation advances on semantic refresh and equal-diff base rewrites;
stale generations and head rebinding within a revision are rejected. Config
blob/governing commit provenance can refresh at the same generation only while
classification meaning stays unchanged. Acquisition fingerprints include
provenance; they are not brief digests. Q7 invalidation uses semantic policy,
owner/attribute content and classifier version, excluding config provenance.
Q2 forward predecessor landings preserve the pinned base and receipt generation.
No landing-gate re-diff is introduced.

`classification.Deriver` is 4b's seam for generated attributes and both independent
shared owner projections, including owned: facts. `BoundPRRevision` derives a
missing legacy ID from an explicitly supplied canonical `PRChangeID` result;
`BoundBatchRevision` retains the tested head/revision and rejects unrecorded zero.
The governing default-branch commit is explicit, including for release queues.
No owners or attributes are parsed here. `docs-codeowners-owner-required` tests
the policy-path half and skips positive ownership acceptance with a 4b pointer.

## Validation

Validation commands use `GOTMPDIR=~/.cache/om-sa-4a-tmp`,
`GOCACHE=~/.cache/go-build`, `GOMEMLIMIT=6GiB` and
`OPENMERGE_TEST_DATABASE_URL=postgres://postgres@localhost:54329/postgres?sslmode=disable`.
Heavy checks run sequentially. No database is initialized under /tmp.

No exported live journal
or live effective configuration was read, so the live deployment gates remain
operator work. Offline fixture replay and tracked config digest comparison are
reported separately and do not claim live-journal coverage.


### Original pre-review results (d15bc55 baseline; superseded below)

- `make check > ~/.cache/om-sa-4a.log 2>&1; echo exit=$?`: **exit=0**.
  Formatting, lint (zero issues), the complete short race suite using shared
  test Postgres, and production build all pass.
- Candidate `make test-sim-exits`: **exit=0** (`~/.cache/om-sa-4a-sim.log`).
  Fresh origin/main baseline: **exit=0** (`~/.cache/om-sa-4a-main-sim.log`).
  All **146 normalized result blocks** match: comparison **exit=0**,
  SHA-256 `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
  Normalization removes test-runner timing/framing and sorts unchanged logged
  result blocks; it retains all reported simulator metrics and scenario inputs.
  Helper/output: `~/.cache/om-sa-4a-normalize.py` and
  `~/.cache/om-sa-4a-sim-normalized.json`.
- `python3 scripts/check-phase2-compatibility.py /home/riels/.cache/om-sa-4a-main`:
  **exit=0** (`~/.cache/om-sa-4a-compatibility.log`).
  **17 legacy exit records** match, SHA-256
  `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`;
  **1,055 depth-1 retry envelopes/default-digest records** match, SHA-256
  `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`.
- All **75 existing wire-v1 fixtures** and all **108 existing JSON testdata
  fixtures** match origin/main byte-for-byte. The three added classification
  fixtures do not replace any existing fixture.
- Offline fixture replay uses the exact helper documented in
  [2a's evidence note](stage-a-2a-inert-evidence.md#reproduce-the-fixture-replay).
  Candidate replay **exit=0**, baseline replay **exit=0**, `cmp` **exit=0**.
  All **66 event checkpoints**, serialized actions/keys and tracked config
  digest match. Output has 67 lines including the digest header; SHA-256
  `b989daef369fff9b42f0f0542329ab4d6b2854ae1d55665e637ba25e28e7cb26`.
  Tracked config digest:
  `e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`.
  Outputs: `~/.cache/om-sa-4a-{main-replay,replay}.txt`.
- Negative/component coverage includes `truncated-all-docs`,
  `classifier-version-only-refresh`, unchanged-head rewinds, all file uncertainty
  kinds, policy deletion/rename paths, default-branch provenance, generated
  attribute uncertainty, independent hunk coverage, verbatim omission summaries,
  stale generations, immutable Apply/reads, snapshot-plus-tail, old snapshots,
  nested missing/null/unknown fields, value validation, and provenance refresh
  across nil-versus-empty wire collections.

The first development `make check` exited **2** because the new event initially
allowed nullable wire collections; its log is `~/.cache/om-sa-4a-initial.log`.
After boundary normalization the intermediate run exited **0**; final frozen
source adds the replay-representation regression and also exits **0**. The
remaining ownership-positive test is an explicit 4b skip, not a claimed pass.
Final status ledger: `~/.cache/om-sa-4a-statuses.log`; sequential command runner:
`~/.cache/om-sa-4a-validation.sh`. No live deployment gates are claimed.

## Fix round 1

Both round-1 blockers and all should-fixes/nits are addressed. The rebase onto
`3f77a88` was clean; main changed tooling/docs only since `d15bc55`.

- Wire decode, Apply and snapshot restore share recursive value checks for every
  coverage/completeness enum, recorded Truth (including owned: tokens), label and
  fact grammar, SHA-1/SHA-256 IDs or explicit unavailable/absent values, paths,
  Git modes, omission reason/count/range combinations and range overflow.
  Negative tests exercise wire AND snapshot rejection, plus inert Apply.
- Explicit version dispatch retains historical facets-v1 derivations, including
  its old single-sided rename flag. Current facets-v2 checks path-derived rename
  flags, Paths/Files agreement and aggregate/per-file hunk completeness. Existing
  fixtures remain byte-identical, with a separate current-version variant.
- Renamed now means unequal nonempty old/new paths; RenamePaired stays exact-only.
  Single-sided unpaired evidence adds unknown. A stub Deriver composition test
  supplies side-complete CodeOwners/PolicyOwners for renames, deletions, policy
  changes and unpaired additions; Evaluate accepts coherent approved evidence
  and blocks missing approval without owner_evidence_incoherent.
- Docs remains path-based on unsafe files; generated-pattern matches survive,
  while unsafe attribute evidence stays unknown. Definite every-file failures
  win over uncertainty only on complete inventories. Empty diffs have docs and
  generated unknown, policy false and unknown true. Workflow policy matching
  covers only direct *.yml/*.yaml definitions. Policy rename reasons retain the
  policy-side path; docs/generated explanations use readable prose.
- The matrix now covers generated all/false/rename/unsafe cases, truncated
  inventory without policy hits, separate hunk-only and governing-only failures,
  empty diffs, mismatched revision IDs, real governing probe absence/unknown
  markers, provenance refresh after snapshot restore, and rejected same-generation
  owner/attribute changes after replay. The named 4b parsing test remains a skip.
- 4b MUST bump the classifier version again when deriving owners/attributes,
  set unknown itself for unresolved owners, and record explicit False for every
  governing owner token absent from the change; missing owned: facts are unknown.

No State/wire shape changed in this round. The private projection remains nil
without classification events. No production emitter or parser was added.
11a still owns whole-record resource budgets and acquired-evidence derivation
cross-checks. Live DB, systemd units and openmerge/* refs remain untouched.

Round-1 validation uses the environment above plus GOMAXPROCS=2 and GOFLAGS=-p=1.
Heavy jobs run sequentially. Logs and actual final statuses are recorded below.

- Final `make check > ~/.cache/om-pr66-fix1.log 2>&1; echo exit=$?`:
  **exit=0** (fmt, zero lint issues, full short race suite with shared test
  Postgres, production build). The first round-1 attempt exited **2** on two
  exported-version comments; corrected before this final run. Initial log:
  `~/.cache/om-pr66-fix1-initial.log`.
- `make test-sim-exits`: **exit=0**, `~/.cache/om-pr66-fix1-sim.log`.
  Comparison to the archived d15bc55 baseline: **exit=0**, all 146 normalized
  blocks match, SHA-256
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
  Simulator source is byte-identical between d15bc55 and current origin/main;
  the baseline full simulator was not rerun. Helper and output:
  `~/.cache/om-pr66-fix1-normalize.py` and `om-pr66-fix1-sim-normalized.json`.
- `python3 scripts/check-phase2-compatibility.py /home/riels/.cache/om-sa-4a-main`:
  **exit=0**, `~/.cache/om-pr66-fix1-compat.log`. The baseline checkout is detached
  at origin/main **3f77a88**. All 17 exit records, 1,055 retry/default-digest records
  and 75 existing main wire fixtures match; hashes remain those above.
- Final focused classification/queue/changepolicy suites: **exit=0**,
  `~/.cache/om-pr66-focused-final.log`. Early focused builds exited **1** while
  migrating the shared label validator and removing an unused import.
- Byte comparison against origin/main: **exit=0**, all **108** existing JSON
  testdata fixtures match. Comparison against reviewed 7ed45fd: **exit=0**, all
  **78** existing PR wire fixtures match. This round adds one facets-v2 variant.
- Earlier offline fixture replay results above are pre-review evidence; no
  live-journal or deployment coverage is claimed or rerun in this fix round.

The final diff is approximately 2,000 changed lines against origin/main, below
3,000. Status ledger: `~/.cache/om-pr66-fix1-statuses.log`. All checks ran
sequentially with cache-backed Go temporary/build directories and GOMEMLIMIT=6GiB.
