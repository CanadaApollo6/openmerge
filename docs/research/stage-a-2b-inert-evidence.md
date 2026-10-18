# Stage A 2b: fix-round 1 inert evidence

Baseline: `e0e2824` (latest origin/main, including #53 and #63).
Implementation: `d56a37f0a06c20ad517a0099578826aeb38c1d31`.
The subsequent evidence-only commit records validation of this frozen source;
the PR fix-round section identifies the final pushed head.
Worktree: `/tmp/om-sa-2b`; branch: `stage-a/2b-governing-hunks`.
Baseline comparison checkout: `/home/riels/.cache/om-pr64-main`.

This remains an ADR 0007 Inert-class reader change. There are no production
consumers, configuration/workflow changes, event/action additions or changes to
queue, planner, executor, store, serve or simulator sources. The source diff
against the baseline in those directories and `.openmerge.yml` is empty.
A call-site search finds only shared test contracts and the dormant GitHub
reader factory constructing another optional reader. No live OpenMerge DB,
service units or remote `openmerge/*` refs were accessed. Transport tests use
local repositories and localhost HTTP; application tests make no real GitHub
API calls. GitHub operations are limited to Git fetch/push and the requested
PR metadata maintenance.

## Evidence design

Semantics: `git-evidence-v6-unified-hunks`. After bounded inventory inspection,
both readers compute patches from those same immutable raw blobs. A private
cache directory with neutral filenames and a repository-discovery ceiling
isolates Git from repository attributes and local config. A fresh environment
sets `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`,
`GIT_ATTR_NOSYSTEM=1` and `LC_ALL=C`; inherited Git overrides are discarded.

The command is:

```sh
git -c diff.algorithm=myers -c diff.indentHeuristic=false \
  -c core.attributesFile=/dev/null diff --no-index --no-ext-diff \
  --no-textconv --no-color --no-renames --diff-algorithm=myers \
  --no-indent-heuristic --minimal --inter-hunk-context=0 --unified=3 \
  -- old new
```

Three context lines match ordinary review presentation. Minimal Myers avoids
shortcuts that can discard repeated equal interior blocks. Unified headers
become ranges; bodies retain raw CRLF and missing-newline annotations.

Retention matches PR 7a: at most 1,000 files, 2 MiB UTF-8 hunk text total,
200 emitted body lines per file (including context and newline annotations),
and deterministic path order. No separate hunk-count limit remains. Hunks that
do not fit are omitted whole; every omitted hunk retains exact old/new ranges
and a typed `hunk_line_limit` or `hunk_byte_limit`. Later small hunks can fit.
Special/binary/invalid-UTF-8 omissions retain their existing typed reasons.

Computation has independent bounds: 2 MiB combined input per file, a 2-second
process deadline, a 30-second collection deadline, and 16,781,312 bytes of
patch output. Exhaustion is incomplete `hunk_work_limit`, with unknown zero
ranges where computation did not produce an inventory. Caller cancellation and
read errors remain diagnostic `read_failure`. Hardened diffRead still performs
all object acquisition and rechecks history after hunk construction.

The bounds golden pins every limit. Exact behavioral golden vectors cover
replacement, insertion, deletion, interior context, missing final newlines,
CRLF changes in both directions, and the reviewers' 300/70,000 equal-line
repros. Retention tests cover all 1,000 files, a 300,001-byte line, exact byte
and line boundaries, shared byte exhaustion, and retaining a later hunk after
an oversized earlier one. An injected busy Git process proves deadline failure
is typed incomplete; inherited configuration injection cannot alter output.

Governing reads continue later probes after incomplete results within budget.
Non-UTF-8 governing content is incomplete `invalid_utf8` without usable content.
A shared fake/gitops contract covers invalid config, an implicit directory at
`.github/CODEOWNERS` (present `040000`, `special_mode`, no fallback), and later
attribute evidence. The governing commit is the default-branch policy commit
per ADR Classification, including release queues; 4a/serve selects it separately
from the diff endpoints and merge base. Policy parsing remains future work.

## Differential results on the frozen implementation

- All **75** wire-v1 JSON fixtures match. Hash sorted relative paths, NUL,
  then bytes: `5170dd1b062194217547096bc0fb2e760b6f0723659e89723d14c5aa76619f3d`.
- Offline fixture replay uses the helper in
  [2a's evidence note](stage-a-2a-inert-evidence.md#reproduce-the-fixture-replay).
  Baseline and implementation both exit **0**; `cmp` exits **0**. All **66**
  serialized checkpoints and the tracked config digest match. Output SHA-256:
  `b989daef369fff9b42f0f0542329ab4d6b2854ae1d55665e637ba25e28e7cb26`.
- Baseline and implementation `make test-sim-exits` both exit **0**. All **146**
  normalized result blocks match, SHA-256:
  `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
  Normalization groups indented `file.go:line:` messages and continuations,
  removes harness timing/status lines, sorts blocks, and writes indented JSON.
- `go list -deps ./cmd/openmerge` exits **0** in both checkouts. Raw-order `cmp`
  exits **1**; the sorted dependency sets are identical (no additions/removals).
  Reader methods link into the binary; binary identity is not claimed.

Logs and replay outputs are under `~/.cache/om-pr64-{main,fix1}-*`;
normalized simulator blocks are `~/.cache/om-pr64-fix1-sim-normalized.json`.
No live deployment journal/config comparison is claimed. ADR deployment gates
remain operator work.

## Validation

Final frozen-source `make check` exits **0** (`~/.cache/om-pr64-fix1.log`):
formatting, lint (zero issues), all short race tests, and build pass.

```sh
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
export GOTMPDIR=/home/riels/.cache/om-sa-2b-tmp
export GOCACHE=/home/riels/.cache/go-build
make check > ~/.cache/om-pr64-fix1.log 2>&1; echo exit=$?
```

The initial check exited **2** at lint (unchecked temporary-directory cleanup),
fixed before the final implementation commit. Two superseded checks were
stopped with exit **143** after the final frozen-source check started; their
logs are `~/.cache/om-pr64-fix1-{intermediate,final}.log`. Its log is
`~/.cache/om-pr64-fix1-initial.log`. Focused reader tests exit **0**
(`~/.cache/om64-focused3.log`); the final forge/fake race tests exit **0**
(`~/.cache/om64-boundary-final.log`). The full check includes the complete short
race suite, lint, formatting and build. All database tests use only the supplied
shared Postgres; no database was initialized under `/tmp`.
