# Stage A 2b: merge evidence for the dormant readers

This is merge evidence for ADR 0007's Inert-class governing-file and hunk
capabilities, not a deployment approval. No live database, service units, App
permissions, production git cache or remote `openmerge/*` refs were accessed or
changed. Database tests use only the supplied shared test Postgres. Transport
tests use temporary local repositories and authenticated localhost HTTP git.

## Inputs and scope

- Baseline main: `18d585526d1a77f180a2ccb471d12b7631ef515d` (2a, #56).
- Implementation: `b1595ee2f7cbd15b8bbb9914489284fd699c0299`. The later evidence
  commit changes only this note; the PR records the final pushed head.
- Worktree: `/tmp/om-sa-2b`; branch: `stage-a/2b-governing-hunks`.
- Baseline comparison checkout: `/home/riels/.cache/om-sa-2b-baseline`.
- Toolchain: Go 1.27.2, linux/amd64.
- Semantics: `git-evidence-v5-single-span`; the bounds golden pins inventory,
  ancestry, hunk and governing budgets. `internal/forge/diff.md` documents the
  required version bump for behavior changes even when bounds remain constant.

The implementation diff is confined to new/extended forge and gitops reader,
contract, test and documentation files: 1,060 added / 30 removed lines before
this evidence note. No changes to Forge's existing interface, changepolicy,
queue, planner, executor, config, store, serve, simulator, workflows or tracked
queue configuration. Existing fixtures and digest vectors are unchanged.
There are no new config keys, wire/event/action types, runtime records or
production consumers. The GitHub governing factory constructs another dormant
capability over the existing authenticated installation cache.

## Evidence contract

`RevisionDiff` retains base/head/merge-base and immutable file objects/modes.
`DiffFile` now carries hunk coverage and omitted spans independently of the
inventory and aggregate hunks. One UTF-8 replacement hunk per ordinary text file
excludes common prefix/suffix lines; internal unchanged lines are replaced too.
This is linear work over the already inspected blobs, not a minimal edit script
or another git patch fetch. Both rename sides and deletion content remain
identified. Binary, invalid UTF-8, symlink, gitlink and mode-change omissions are
explicit. Limit omissions retain known old/new ranges and typed reasons.

`GoverningFiles` separately pins the governing commit, records every checked
config candidate, CODEOWNERS precedence/selection and earlier absences, and
sorted/deduplicated ancestor attributes for both rename sides and deletions.
Every present readable file supplies mode, object ID and content; only complete
absent probes prove absence. Git-backed reads provide modes, bounded raw bytes
and tree-proven absence that `Driver.ReadFile`'s bytes-only contents API cannot
provide together. All new commands use the existing hardened diffRead session;
history is rechecked before completeness. No transport leaves forge/gitops.

PR 4a converts these independent forge DTOs into changepolicy types: endpoints,
governing commit and blob provenance into `ClassificationInputs`; octal modes,
paths, objects and uncertainty flags into `FileEvidence`; hunk bodies/ranges and
omissions into `Hunk`/`HunkRange`; per-file coverage into `HunkEvidence.Complete`;
and aggregate coverage into `Classification.HunksComplete`. 4a/4b own parsing
and classification. Forge does not import changepolicy.

Bounds and rationale:

- Preserve 2a's 1,000 files, 2 MiB inventory/inspected blobs, 100,000 divergence
  commits and 30-second shared ancestry deadline.
- 512 hunks total, one per file, 256 KiB body text. This keeps brief content
  bounded even for a complete 1,000-file inventory and avoids quadratic line
  matching. A hunk that will not fit is omitted whole, never silently cut.
- 256 governing probes, counting absence, and 1 MiB content. This bounds tree
  operations as well as retained policy text, allowing ordinary nested repos
  and failing closed on deep/large ones. Changed-path input is at most 2,000
  paths / 2 MiB; ancestor probe paths separately fit in 2 MiB, preventing long
  shared prefixes from amplifying metadata without a limit.

Limits return nil-error incomplete observations. Only invalid object IDs,
missing objects and read failures have diagnostic non-nil errors; caller
cancellation remains read_failure. Complete inventory is not complete hunk or
governing evidence.

## Differential inertness results

1. All **74** tracked wire-v1 fixtures are byte-identical. Hash sorted relative
   paths, NUL, then bytes: both trees yield
   `1c52cf31d0a2239756b7b179f16d6736ddc5177d7bb146ff5e61034180914fe3`.
2. The offline fixture replay helper in
   [2a's evidence note](stage-a-2a-inert-evidence.md#reproduce-the-fixture-replay)
   produces identical serialized next actions and keys at **66** checkpoints
   (depths 1 and 4). Both runs exit **0**; `cmp` exits **0**. Output SHA-256:
   `b989daef369fff9b42f0f0542329ab4d6b2854ae1d55665e637ba25e28e7cb26`.
   This is fixture evidence, not an exported deployment journal.
3. `make test-sim-exits` exits **0** in both trees with unchanged seeds and
   sources. All **146** normalized logged result blocks match. Normalization
   groups each indented `file.go:line:` message with its continuation lines,
   removes Go harness status/timing lines, sorts blocks, and serializes with
   Python `json.dumps(blocks, indent=2) + "\n"`. SHA-256:
   `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
4. The tracked config and queue/config source trees are byte-identical; fixture
   replay reports the same effective tracked config digest:
   `e58c893b8b3af7685dc07a086824b3bc1fc044456083d193f9566f8387cb58fc`.
5. `go list -deps ./cmd/openmerge` output is identical (`cmp`: **0**).
   Reproducible command below builds both trees successfully (**0**):

   ```sh
   CGO_ENABLED=0 go build -trimpath -buildvcs=false \
     -ldflags '-X main.version=stage-a-inert-comparison' \
     -o <output> ./cmd/openmerge
   ```

   Baseline binary SHA-256:
   `98d6c45d7cbda45b646e12547308324be3a143c2e9bc249878ee32eb83cef2e5`.
   Implementation binary SHA-256:
   `dbab9d4c8c4d2bc7145bce996235da503aa85c83a2283ef6e52bf64c0335fdc0`.
   The binaries differ. `go tool nm` confirms the new governing/hunk methods
   link into the binary (including fake/simulator embedding wrappers). A source
   call-site search below finds only forgetest and the dormant GitHub factory's
   construction call; no production consumer calls these capabilities.

   ```sh
   rg -n '\.(RevisionDiff|GoverningFiles|RevisionDiffReader|GoverningFilesReader)\(' \
     --glob '*.go' --glob '!**/*_test.go'
   git diff 18d5855 -- internal/queue internal/config internal/serve \
     internal/executor internal/store internal/sim .github .openmerge.yml
   ```

   The protected-source diff is empty. No planner/executor/gate or check
   observation behavior changes. PR 10's composition-ID gate is not applicable.

ADR 0007 deployment gates 2 and 4 still require the operator's actual exported
journal and live config comparison at deploy time. Neither was accessed here;
this PR does not claim those deployment gates passed or update the dogfood log.

## Validation

The initial full `make check` exits **0** (`~/.cache/om-sa-2b-initial-check.log`),
including serve (319.760 seconds). This run began before the final ancestor-path
budget refinement and contract assertions; it is retained as initial evidence.

On frozen implementation `b1595ee`, the focused race suite exits **0**:

```sh
go test -race ./internal/forge/... ./internal/gitops \
  > ~/.cache/om-sa-2b-race-final.log 2>&1; echo exit=$?
```

The final frozen-tree full check exits **0** (`~/.cache/om-sa-2b.log`):
formatting, lint (zero issues), all short race tests and build pass.
The command uses only the supplied shared test database and puts compiler
scratch space under the home cache rather than initializing Postgres in /tmp:

```sh
OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable' \
GOTMPDIR=/home/riels/.cache/om-sa-2b-tmp \
make check > ~/.cache/om-sa-2b.log 2>&1; echo exit=$?
```

The new simulator run was repeated on frozen `b1595ee` and again exits **0**,
with all 146 blocks identical to baseline. Fixture replay was also repeated on
the frozen commit with the same output/hash and exit **0**.

Both baseline/new simulator runs exit **0**; logs:
`~/.cache/om-sa-2b-baseline-sim.log` and `~/.cache/om-sa-2b-sim.log`.
Replay outputs are `~/.cache/om-sa-2b-baseline-replay.txt` and
`~/.cache/om-sa-2b-replay.txt`; normalized simulator blocks are
`~/.cache/om-sa-2b-sim-normalized.json`.
