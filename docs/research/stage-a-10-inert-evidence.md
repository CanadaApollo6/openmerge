# Stage A 10: build-time change and classification trailers

Baseline: `c2d9b3a` (origin/main, includes Stage A PRs 1 and 4a/4b).
Branch: `stage-a/10-trailers`; worktree: `/home/riels/openmerge-stage-a-10`.
The implementation commit and final verification results are recorded below.

## Inert boundary

ADR 0007 class: **Inert for queues without approvals**. This PR touches the
build path, which requires the ADR 0009 materiality check. All production queues
still use the legacy path: production callers supply no change metadata and do
not install the dormant executor loader. Public config continues to reject
`approvals`; activation belongs to PR 11b. No config, events or action wire
formats change. Existing fixtures and effective config digest must stay
byte-identical. No landing, fence CAS, gate, planner or check-observation behavior
changes. Metadata is fixed when constructing the test ref, before CI. Landing
has no API to add receipts, classifications or edited intent to tested commits.
Changed build inputs require new work.

The internal input contains a change ID, revision and classification labels.
Labels come from recorded positive classification facts, including PR 4's
canonical `owned:user:`, `owned:team:` and `owned:email:` spellings. Labels are
sorted lexically and comma-separated; empty is the literal `none`, never a label.
The spelling is `OpenMerge-Change: <change-id>@<revision>` followed by
`OpenMerge-Classification: <change-id>@<revision> <labels>`.

Squash/merge place the pair in each generated commit's single trailer block;
rebase places it on the last replayed commit of each member. The rebase path
preserves original authors/dates, DCO, coauthors and authoritative replay
provenance. Activated builds remove reserved `OpenMerge-*` input lines before
generating their own metadata. The top generated commit alone ends with the
sole authoritative `OpenMerge-Batch` line accepted by `forge.ParseBatchMessage`.
Fast-forward preserves original contributor objects, including signature
headers, and puts adjacent pairs on the unique tested marker in pinned member
order, followed by the single batch trailer. Its parent and tree still match
the landed head; the fence records the tested marker separately.

## Checks

Heavy jobs run sequentially with `GOFLAGS=-p=2`, `GOMAXPROCS=2`,
`GOMEMLIMIT=4GiB`, `TMPDIR`/`GOTMPDIR=~/.cache/om-sa-10/tmp`,
`GOCACHE=~/.cache/go-build`. Database tests use only
`OPENMERGE_TEST_DATABASE_URL=postgres://postgres@localhost:54329/postgres?sslmode=disable`.
No initdb, live DB access, systemd changes, live `openmerge/*` ref operations,
or real GitHub API calls in code/tests occur. Only main is fetched; only this
PR branch is pushed. Test repos and refs are isolated local fixtures.

Real-git adversarial tests exercise every method, two multi-commit changes,
multiline messages/bodies, reserved-prefix injection, retained attribution,
rebase author dates/provenance, opaque signature-header preservation, marker
parent/tree, and tested/landed identities. Shared fake/real forge contracts
check per-member placement and metadata validation. Approval state arriving
while CI runs cannot call the build metadata loader again or amend a tested
object. No receipt event is introduced ahead of PR 11.

This evidence is not a deployment record. The operator must rerun the exported
journal/config and commit-ID checks against the actual deployed source and
candidate release, establish release eligibility, obtain the pre-metrics
maintainer materiality verdict, and log any deployment. No deployment is made
by this PR.

The serial command environment above applies to these checks:

- `make test-sim-exits` in the clean baseline: **exit=0**,
  `~/.cache/om-sa-10-main-sim.log`; candidate: **exit=0**,
  `~/.cache/om-sa-10-sim.log`.
- `python3 scripts/check-phase2-compatibility.py /home/riels/openmerge-stage-a-10-main`:
  **exit=0**, `~/.cache/om-sa-10-compatibility.log`. All 17 legacy exit
  records match (SHA-256
  `bc7c730e66e11a6b3ec3e2253e81061d49358d592cb673e634fac89bf41b26c6`);
  all 1,055 retry/default-digest records match (SHA-256
  `07ed2ecf13debd5d75c8424b222edfd740470c00f5fc2b4ae00183fdd9ce7b37`);
  all 76 existing registry wire fixtures are byte-identical.
- `python3 scripts/check-stage-a-4b-inert.py /home/riels/openmerge-stage-a-10-main
  ~/.cache/om-sa-10-main-sim.log ~/.cache/om-sa-10-sim.log`:
  **exit=0**, `~/.cache/om-sa-10-inert.log`. All 113 existing JSON fixtures
  match. At every one of 66 fixture replay checkpoints, next actions/keys match
  and classifications stay nil; with the digest header, 67 records match
  (SHA-256 `94e97da35ba05d72dcc0c5c1b326b49783b7f994f6060bf1b98dc402d60c2d6f`).
  The tracked config digest remains
  `e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`.
  All 146 full simulator result blocks match after removing only runner
  framing/timing (SHA-256
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`).
- Full `go test ./internal/gitops ./internal/forge/... -count=1`:
  **exit=0**, `~/.cache/om-sa-10-contract.log`; real/fake shared contracts
  and adversarial real-git builds pass.

The first development `make check` exited **2** at formatting while the executor
tests were still being integrated; no lint/tests/build ran in that invocation
(`~/.cache/om-sa-10-initial-fmt.log`). The completed suite's status and frozen
source commit are recorded in the final verification below.

## Final verification on the frozen implementation

Frozen implementation: `49a8e1ed237cb8fd90d5011d946e1cb46e465c30`.
Baseline: `c2d9b3aa17113b7e208ceac1be53aa42fe86dce8`.
The subsequent evidence-only commit changes this document, not build inputs.

`make check > ~/.cache/om-sa-10.log 2>&1; echo exit=$?`: **exit=0**.
Formatting, zero lint issues, the full short race suite (including shared test
Postgres), and production build pass. The final absent-key guard is additionally
covered by the focused executor/store race run on the frozen source.

### legacy-commit-id-per-method

```bash
python3 scripts/check-legacy-commit-ids.py \
  --old-source /home/riels/openmerge-stage-a-10-main \
  --new-source /home/riels/openmerge-stage-a-10 \
  > ~/.cache/om-sa-10-commit-ids.log 2>&1; echo exit=$?
```

**exit=0**, `verdict=PASS methods=4`. Both checkouts were clean and unchanged
throughout the comparison. The same injected harness compiles into separate
old/new test binaries and invokes real local Git builds and fenced landings.
Every row uses multiple members, including a stacked fast-forward. Author and
committer identities/dates and nonce bytes are identical; the nonce replacement
exists only in the test process (`cryptocustomrand=1`), with no production hook.
The actual old/new IDs are equal in each column below:

```text
method        tested                                    landed
squash        5b31cb231477553b8e6138220b93672106b61aab  5b31cb231477553b8e6138220b93672106b61aab
merge         ebeaacca34b1fce74be9b0609486363cc41d952b  ebeaacca34b1fce74be9b0609486363cc41d952b
rebase        f159c344cb975f1027ae6951bae31ae7090267df  f159c344cb975f1027ae6951bae31ae7090267df
fast-forward  285c6cffec4b123979e78032c2e25a54e0dadb49  d9a55295992cc3db0048391fb4beb84e323d1636
```

Harness SHA-256:
`ce8c4a95927ff48a9b8a94699bfda4e84aa8f97ecaa6c30316ea97fc13c5cdc3`.
Compiled old test-binary SHA-256:
`4c4911c7c8401a1be62e9d280a364c435b06febc329317f59ebc6ff7577bbd19`.
Compiled candidate test-binary SHA-256:
`57e266d82b578ff9334bf8c056bc8ec2aa0bac64fd870b6b8a78e9d8daa1ba18`.
The log also identifies both full source-tree hashes and retained binaries in
`~/.cache/openmerge-legacy-commit-ids/compare-7w4nz9_t`.

This is commit-ID evidence, not a comparison of message substrings. No-approvals
builds are byte-identical to current main for **all four methods**. Fast-forward
continues to test the marker and land its pinned parent; other methods land
the exact tested object. No landing/fence/gate code was modified.

### Saved exported-journal replay

```bash
python3 scripts/check-live-journal-replay.py \
  --baseline /home/riels/openmerge-stage-a-10-main \
  --candidate /home/riels/openmerge-stage-a-10 \
  --events ~/.local/share/openmerge/exports/events-20261018T051000Z.jsonl \
  --queues ~/.local/share/openmerge/exports/queues-20261018T051000Z.jsonl \
  --strict-state --out ~/.cache/om-sa-10-replay.json \
  > ~/.cache/om-sa-10-replay.log 2>&1; echo exit=$?
```

**exit=0**, verdict and gates 2/4 **PASS**. This reads an existing export only;
it does not connect to the live DB. Watermark: event 2158, 2,158 event rows,
one queue, last recorded at `2026-10-18T01:08:55.427607-04:00`.
Event export SHA-256:
`9c8f78f3e967c7cb030948cfc9871cc67a45da06f80b12f27d1c55d09ba8accf`.
Queue export SHA-256:
`418f025a0e3c8792321b6d71d297f17fff1adf48cb616c8f4fcb774163696886`.

Actions, structural state, strict snapshot hashes, and deadline/boundary
observations match at **2,159/2,159** checkpoints, with no first divergence,
no non-allowlisted fields, and deterministic derived projections. Mandatory
synthetic preflight self-tests exit 0. Baseline, candidate and repeated-candidate
streams each contain 2,160 records with SHA-256
`73357abab6aaf010a040b10894ec5a448c3fcf1799d4f646207ea4c7047a3839`.
The saved effective config digest and recomputed digest both remain
`e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`;
no new `ConfigApplied` would be emitted. Config bytes SHA-256:
`3efb43bbc05f5f0b03c005331f79064829fa018e9f6446224682e00dd357fead`.
No new event types, config fields, action fields or legacy fixture bytes change.

The historical export is not a fresh view of the live server. Before deploying,
repeat these gates with a fresh operator export, the actual deployed source and
the candidate release. This PR is behavior-neutral for queues without approvals;
it does not waive release eligibility or the maintainer's materiality verdict.
The aggregate diff remains under 1,700 changed lines, below the ~3,000-line
review ceiling; no split is needed.

Final frozen-source follow-up checks: `go test -race -short -count=1
./internal/executor ./internal/store`: **exit=0**
(`~/.cache/om-sa-10-final-focused.log`). This includes the final absent-key guard
and confirms executor/store test imports remain acyclic. Final `make fmt lint`: **exit=0**, zero issues;
results are retained in `~/.cache/om-sa-10-final-lint.log`.
