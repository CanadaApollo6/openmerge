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
