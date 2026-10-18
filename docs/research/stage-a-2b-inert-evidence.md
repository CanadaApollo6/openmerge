# Stage A 2b: fix-round 2 inert evidence

Baseline: `e0e282499ee287a2a104ce6bc661bbfa95da5556` (origin/main).
Implementation: `56456931b2b2a476ad7f6a8893dd9fc57b6721c3`.
The subsequent evidence-only commit records validation of this frozen source;
the PR fix-round section identifies the final pushed head.
Worktree: `/home/riels/om-pr64-fix2`; branch: `stage-a/2b-governing-hunks`.
Baseline comparison checkout: `/home/riels/.cache/om-pr64-main`.

This remains an ADR 0007 Inert-class reader change. There are no production
consumers, configuration/workflow changes, event/action additions or changes to
queue, planner, executor, store, serve or simulator sources. The source diff
against the baseline in those directories, config and `.openmerge.yml` is empty.
Only optional reader implementations, their DTOs, documentation and tests change.
No live OpenMerge DB, systemd units or live `openmerge/*` refs were accessed.
Transport tests use local repositories and localhost HTTP; application tests make
no real GitHub API calls. Repository operations are limited to fetching main and
the PR branch, pushing the PR branch, and the explicitly requested PR body edit.

## Evidence semantics

Semantics: `git-evidence-v7-bounded-omissions`. The v6 multi-region minimal Myers
patches, three context lines, 1,000-file limit, 2 MiB retained UTF-8 body text,
200 emitted body lines per file and later-small-hunk retention are preserved.
Both readers still operate on the same inspected immutable raw blobs.

Detailed omissions are capped at **32 per file and 256 across files**, plus
**one reserved summary per file**. Absolute omission-entry bounds are therefore
**33 per file and 1,256 across the 1,000-file collection**. A summary has reason
`omitted_range_limit`, a `Count` of collapsed omissions and enclosing old/new
spans. Enclosing spans may include retained or unchanged lines; unknown ranges
remain zero. Coverage remains incomplete and retains the underlying reasons.
PR 4a/7a must copy the summary count and ranges verbatim into `HunkRange`;
the changepolicy DTO documents the same producer contract.

The complete compact-JSON hunk-evidence subrecord is bounded by **39,528,448
bytes (<40 MiB)**: worst-case escaped text (6 × 2 MiB), at most 200,000 retained
hunks × 128 metadata bytes, 1,256 omission entries × 256 bytes, and 1,000
coverage/container records × 1,024 bytes. Inventory and classification/owner
facts are separate envelope components with their own bounds. PR 7a's existing
text/file/line limits are preserved. See `internal/forge/diff.md` for accounting
and encoding assumptions. A synthetic JSON test over-approximating all metadata
and using worst-case text escaping serializes **29,672,353 bytes**.

The bounds golden preserves all historical entries and adds v7, including the
omission budgets, 8 KiB stderr budget and the single `DiffHunkAlgorithmFlags`
constant used by the command. Supported hosts use Git 2.39 <= version < 3 and
must pass behavioral vectors; this run uses **Git 2.56.0**.

The cache parent is created with mode 0700 when missing, including a nonexistent
`XDG_CACHE_HOME`. Temporary plaintext blobs are 0600 in a private 0700 directory
inside that cache root. Normal returns remove them; SIGKILL can leave bounded
files behind for operator cleanup. A fresh Git environment and discovery ceiling
still isolate attributes/config. Stderr has its own bounded buffer, cannot
corrupt patch parsing, and fails closed on overflow.

Post-inventory hunk failures preserve completed inventory in both fake and
gitops. History mutation still invalidates the ancestry proof and inventory.
CODEOWNERS advances only past complete absent probes: present, unreadable or
unknown-presence locations stop fallback. Later config/attribute diagnostics
continue within their independent budget. The governing commit remains the
separately selected default-branch policy commit, including for release queues.

## Regression and differential results

- Muse's adversarial probe uses 160,000 five-byte lines, changing every eighth
  line (20,000 separated regions), repeated across ten files sharing a blob pair.
  Each file retains 25 hunks. Exact range counts/spans, both omission budgets,
  summary counts and incomplete coverage are asserted. The first eight files
  retain 32 detailed omissions each; later files have only a summary.
- Existing behavioral vectors retain the 300/70,000 equal-line repros, CRLF,
  missing-newline, insertion/deletion, 1,000-file, long-line, exact byte/line,
  later-retained-hunk, deadline and config-isolation coverage. New tests cover
  nonexistent cache roots, host stderr, stderr overflow, direct gitlink mode,
  unknown CODEOWNERS presence and independent reader coverage on hunk failure.
- All **108 tracked JSON testdata fixtures** match baseline byte-for-byte,
  including the previously measured 75 wire-v1 fixtures.
- Current offline fixture replay exits **0** and `cmp` against main exits **0**;
  the tracked config digest and all **66** serialized checkpoints match.
  Output SHA-256: `b989daef369fff9b42f0f0542329ab4d6b2854ae1d55665e637ba25e28e7cb26`.
  The helper is documented in
  [2a's evidence note](stage-a-2a-inert-evidence.md#reproduce-the-fixture-replay).
- Candidate `make test-sim-exits` exits **0**. Comparison against the recorded
  run on the same main baseline exits **0**: all **146** normalized result blocks
  match, SHA-256 `a7e088668bc1c6b2159a043aa40f2aa49487625bbb3eeb418cb3c8df39751ca7`.
  The normalization helper/output are `~/.cache/om-pr64-fix2-normalize.py` and
  `~/.cache/om-pr64-fix2-sim-normalized.json`.
- Candidate `go list -deps ./cmd/openmerge` exits **0**; dependency sets match
  the recorded main baseline. Binary identity is not claimed: dormant reader
  methods link into the binary.

No live deployment journal/config gate is claimed. ADR deployment gates remain
operator work. Baseline simulator/replay logs from fix round 1 were reused
because origin/main is unchanged; the candidate comparisons were rerun.

## Validation

Final frozen-implementation `make check` exits **0**
(`~/.cache/om-pr64-fix2.log`): formatting, lint (zero issues), the complete short
race suite including shared-Postgres integrations, and production build pass.

```sh
export OPENMERGE_TEST_DATABASE_URL='postgres://postgres@localhost:54329/postgres?sslmode=disable'
export GOTMPDIR=/home/riels/.cache/om-sa-2b-tmp
export GOCACHE=/home/riels/.cache/go-build
make check > ~/.cache/om-pr64-fix2.log 2>&1; echo exit=$?
```

The first focused run exited **1** (fixture helper return type and nonunique
probe-line assumptions); the corrected focused run exited **0**
(`~/.cache/om-pr64-fix2-focused-rerun.log`). The JSON envelope and unknown-presence
regressions exit **0** (`~/.cache/om-pr64-fix2-envelope.log`).
The validation-in-progress full `make check` exited **0**
(`~/.cache/om-pr64-fix2-initial.log`). A separate final check runs after freezing
the implementation, including the last stderr and governing regressions.
All database tests use only the supplied shared test Postgres. No database was
initialized under `/tmp`. `GOTMPDIR` and `GOCACHE` are under `~/.cache`.
